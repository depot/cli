package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
)

func (f *fakeAgentService) PrepareAttachmentUploads(_ context.Context, req *connect.Request[agentv1.PrepareAttachmentUploadsRequest]) (*connect.Response[agentv1.PrepareAttachmentUploadsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepares++
	resp := &agentv1.PrepareAttachmentUploadsResponse{MaxAttachmentBytes: f.maxAttachmentBytes}
	for _, a := range req.Msg.GetAttachments() {
		if !f.stored[a.GetSha256()] {
			resp.Uploads = append(resp.Uploads, &agentv1.DepotAgentAttachmentUpload{
				Sha256:  a.GetSha256(),
				Url:     f.uploadURL + "/" + a.GetSha256(),
				Headers: map[string]string{"x-amz-checksum-sha256": "sum-" + a.GetSha256()[:8]},
			})
		}
	}
	return connect.NewResponse(resp), nil
}

// fakeStore records the PUTs an upload makes, by path.
type fakeStore struct {
	mu   sync.Mutex
	puts map[string]string
	hdrs map[string]string
}

func startStore(t *testing.T) (*fakeStore, string) {
	t.Helper()
	st := &fakeStore{puts: map[string]string{}, hdrs: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		st.mu.Lock()
		st.puts[strings.TrimPrefix(r.URL.Path, "/")] = string(body)
		st.hdrs[strings.TrimPrefix(r.URL.Path, "/")] = r.Header.Get("x-amz-checksum-sha256")
		st.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return st, srv.URL
}

func writeFile(t *testing.T, name, content string) (path, sum string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(content))
	return path, hex.EncodeToString(h[:])
}

func TestUploadAttachmentsUploadsOnlyMissingFiles(t *testing.T) {
	store, url := startStore(t)
	newPath, newSum := writeFile(t, "new.png", "fresh bytes")
	oldPath, oldSum := writeFile(t, "old.log", "already stored")
	f := &fakeAgentService{stored: map[string]bool{oldSum: true}, uploadURL: url, maxAttachmentBytes: 1024}
	s := startFake(t, f)

	var notices strings.Builder
	got, err := uploadAttachments(context.Background(), s, []string{newPath, oldPath}, &notices)
	if err != nil {
		t.Fatalf("uploadAttachments: %v", err)
	}
	if len(store.puts) != 1 || store.puts[newSum] != "fresh bytes" {
		t.Fatalf("expected one PUT of the missing file, got %v", store.puts)
	}
	if store.hdrs[newSum] != "sum-"+newSum[:8] {
		t.Fatalf("PUT should send the server's headers, got %q", store.hdrs[newSum])
	}
	if len(got) != 2 || got[0].GetSha256() != newSum || got[0].GetName() != "new.png" || got[0].GetSizeBytes() != 11 ||
		got[0].GetMediaType() != "image/png" || got[1].GetSha256() != oldSum {
		t.Fatalf("attachments = %v", got)
	}
	if !strings.Contains(notices.String(), "attaching new.png, 11B") {
		t.Fatalf("expected the size shown, got %q", notices.String())
	}
}

func TestUploadAttachmentsRefusesAFileOverTheLimitBeforeUploading(t *testing.T) {
	store, url := startStore(t)
	small, _ := writeFile(t, "small.txt", "ok")
	big, _ := writeFile(t, "big.bin", strings.Repeat("x", 100))
	f := &fakeAgentService{uploadURL: url, maxAttachmentBytes: 50}
	s := startFake(t, f)

	_, err := uploadAttachments(context.Background(), s, []string{small, big}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "over the 50B limit") {
		t.Fatalf("expected an over-limit error, got %v", err)
	}
	if len(store.puts) != 0 {
		t.Fatalf("nothing should upload when one file is over the limit, got %v", store.puts)
	}
}

func TestUploadAttachmentsRejectsADirectory(t *testing.T) {
	s := startFake(t, &fakeAgentService{})
	_, err := uploadAttachments(context.Background(), s, []string{t.TempDir()}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected a not-a-file error, got %v", err)
	}
}

func TestAttachQueuesAFileForTheNextMessage(t *testing.T) {
	_, url := startStore(t)
	path, sum := writeFile(t, "a b.txt", "contents")
	other, otherSum := writeFile(t, "c.txt", "more")
	f := &fakeAgentService{streams: [][]*agentv1.WatchSessionResponse{{frame(running, viewOne)}}, uploadURL: url}
	s := startFake(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	in := strings.NewReader(`/file "` + path + `"` + "\nlook at this\n/file " + other + " and this\nplain\n/file\n/quit\n")
	var out syncBuffer
	if err := attachSession(ctx, s, "s1", in, &out); err != nil {
		t.Fatalf("attachSession: %v", err)
	}

	want := []struct {
		content string
		sums    []string
	}{
		{"look at this", []string{sum}},
		{"and this", []string{otherSum}},
		{"plain", nil},
	}
	if len(f.inputs) != len(want) {
		t.Fatalf("expected %d inputs, got %d: %v", len(want), len(f.inputs), f.inputs)
	}
	for i, w := range want {
		in := f.inputs[i]
		var sums []string
		for _, a := range in.GetAttachments() {
			sums = append(sums, a.GetSha256())
		}
		if in.GetContent() != w.content || strings.Join(sums, ",") != strings.Join(w.sums, ",") {
			t.Errorf("input %d = %q with %v, want %q with %v", i, in.GetContent(), sums, w.content, w.sums)
		}
	}
	for _, notice := range []string{"(a b.txt goes out with your next message)", "(usage: /file <path> [message])"} {
		if !strings.Contains(out.String(), notice) {
			t.Errorf("expected notice %q, got:\n%s", notice, out.String())
		}
	}
}

func TestAttachKeepsAFileWhoseUploadFailedOutOfTheMessage(t *testing.T) {
	f := &fakeAgentService{streams: [][]*agentv1.WatchSessionResponse{{frame(running, viewOne)}}}
	s := startFake(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	in := strings.NewReader("/file " + filepath.Join(t.TempDir(), "missing") + " hello\n/quit\n")
	var out syncBuffer
	if err := attachSession(ctx, s, "s1", in, &out); err != nil {
		t.Fatalf("attachSession: %v", err)
	}
	if len(f.inputs) != 0 {
		t.Fatalf("a message whose file failed should not send, got %v", f.inputs)
	}
	if !strings.Contains(out.String(), "no such file") {
		t.Fatalf("expected the failure shown, got:\n%s", out.String())
	}
}
