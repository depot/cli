package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	f.prepares = append(f.prepares, req.Msg)
	resp := &agentv1.PrepareAttachmentUploadsResponse{
		MaxAttachmentBytes: f.maxAttachmentBytes,
		MaxTotalBytes:      f.maxTotalBytes,
		MaxAttachments:     f.maxAttachments,
	}
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

// fakeStore records the PUTs an upload makes, by hash.
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
		sum := strings.TrimPrefix(r.URL.Path, "/")
		st.mu.Lock()
		defer st.mu.Unlock()
		st.puts[sum] = string(body)
		st.hdrs[sum] = r.Header.Get("x-amz-checksum-sha256")
	}))
	t.Cleanup(srv.Close)
	return st, srv.URL
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return sha(content)
}

func sha(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

func names(attachments []*agentv1.DepotAgentAttachment) []string {
	var out []string
	for _, a := range attachments {
		out = append(out, a.GetName())
	}
	return out
}

func TestUploadAttachmentsPutsOnlyTheURLsTheServerReturns(t *testing.T) {
	store, url := startStore(t)
	dir := t.TempDir()
	newSum := writeFile(t, filepath.Join(dir, "new.png"), "fresh bytes")
	oldSum := writeFile(t, filepath.Join(dir, "old.log"), "already stored")
	f := &fakeAgentService{stored: map[string]bool{oldSum: true}, uploadURL: url, maxAttachmentBytes: 1024}
	s := startFake(t, f)

	var notices strings.Builder
	in := fileInputs{paths: []string{filepath.Join(dir, "new.png"), filepath.Join(dir, "old.log")}}
	got, err := uploadAttachments(context.Background(), s, nil, in, &notices)
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
		got[0].GetMediaType() != "image/png" || got[1].GetSha256() != oldSum || got[1].GetMediaType() != "text/plain" {
		t.Fatalf("attachments = %v", got)
	}
	if !strings.Contains(notices.String(), "attaching 2 files, 25B") {
		t.Fatalf("expected the count and size shown, got %q", notices.String())
	}
}

func TestUploadAttachmentsRefusesAFileOverTheLimitBeforeReadingIt(t *testing.T) {
	store, url := startStore(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "small.txt"), "ok")
	writeFile(t, filepath.Join(dir, "big.bin"), strings.Repeat("x", 100))
	f := &fakeAgentService{uploadURL: url, maxAttachmentBytes: 50}
	s := startFake(t, f)

	in := fileInputs{paths: []string{filepath.Join(dir, "small.txt"), filepath.Join(dir, "big.bin")}}
	_, err := uploadAttachments(context.Background(), s, nil, in, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "over the 50B limit") {
		t.Fatalf("expected an over-limit error, got %v", err)
	}
	if len(store.puts) != 0 || len(f.prepares) != 1 || len(f.prepares[0].GetAttachments()) != 0 {
		t.Fatalf("only the limits should be fetched, got %d prepares and %v puts", len(f.prepares), store.puts)
	}
}

func TestUploadAttachmentsSendsADirectoryMinusIgnoredFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	_, url := startStore(t)
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	dir := filepath.Join(root, "proj")
	writeFile(t, filepath.Join(dir, ".gitignore"), "build/\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main")
	writeFile(t, filepath.Join(dir, "pkg", "util.go"), "package pkg")
	writeFile(t, filepath.Join(dir, "build", "out.bin"), "ignored")
	s := startFake(t, &fakeAgentService{uploadURL: url})

	got, err := uploadAttachments(context.Background(), s, nil, fileInputs{paths: []string{dir}}, io.Discard)
	if err != nil {
		t.Fatalf("uploadAttachments: %v", err)
	}
	gotNames := names(got)
	slices.Sort(gotNames)
	if want := []string{"proj_.gitignore", "proj_main.go", "proj_pkg_util.go"}; !slices.Equal(gotNames, want) {
		t.Fatalf("names = %v, want %v", gotNames, want)
	}
}

func TestUploadAttachmentsRefusesADirectoryOverTheFileCount(t *testing.T) {
	store, url := startStore(t)
	dir := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(dir, name), name)
	}
	s := startFake(t, &fakeAgentService{uploadURL: url, maxAttachments: 2})

	_, err := uploadAttachments(context.Background(), s, nil, fileInputs{paths: []string{dir}}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "3 files is over the limit of 2") {
		t.Fatalf("expected a file-count error, got %v", err)
	}
	if len(store.puts) != 0 {
		t.Fatalf("nothing should upload, got %v", store.puts)
	}
}

func TestUploadAttachmentsRefusesAnEmptyDirectory(t *testing.T) {
	s := startFake(t, &fakeAgentService{})
	_, err := uploadAttachments(context.Background(), s, nil, fileInputs{paths: []string{t.TempDir()}}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no files to send") {
		t.Fatalf("expected an empty-directory error, got %v", err)
	}
}

func TestUploadAttachmentsSendsPipedInputAsAnImage(t *testing.T) {
	store, url := startStore(t)
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 16)
	s := startFake(t, &fakeAgentService{uploadURL: url, maxAttachmentBytes: 1024})

	in := fileInputs{stdinName: "screenshot", stdin: strings.NewReader(png)}
	got, err := uploadAttachments(context.Background(), s, nil, in, io.Discard)
	if err != nil {
		t.Fatalf("uploadAttachments: %v", err)
	}
	if len(got) != 1 || got[0].GetName() != "screenshot" || got[0].GetMediaType() != "image/png" || store.puts[sha(png)] != png {
		t.Fatalf("attachments = %v, puts = %d", got, len(store.puts))
	}
}

func TestUploadAttachmentsRefusesPipedInputOverTheLimit(t *testing.T) {
	s := startFake(t, &fakeAgentService{maxAttachmentBytes: 4})
	in := fileInputs{stdinName: "out.log", stdin: bytes.NewReader(make([]byte, 5))}
	_, err := uploadAttachments(context.Background(), s, nil, in, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "stdin is over the 4B limit") {
		t.Fatalf("expected an over-limit error, got %v", err)
	}
}

func TestUploadAttachmentsKeepsNamesUniqueInAMessage(t *testing.T) {
	_, url := startStore(t)
	one, two := filepath.Join(t.TempDir(), "x.txt"), filepath.Join(t.TempDir(), "x.txt")
	writeFile(t, one, "one")
	writeFile(t, two, "two")
	f := &fakeAgentService{uploadURL: url}
	s := startFake(t, f)
	pending := []*agentv1.DepotAgentAttachment{{Name: "x.txt", Sha256: sha("zero"), SizeBytes: 4}}

	got, err := uploadAttachments(context.Background(), s, pending, fileInputs{paths: []string{one, two}}, io.Discard)
	if err != nil {
		t.Fatalf("uploadAttachments: %v", err)
	}
	if want := []string{"x~2.txt", "x~3.txt"}; !slices.Equal(names(got), want) {
		t.Fatalf("names = %v, want %v", names(got), want)
	}
	if last := f.prepares[len(f.prepares)-1]; !slices.Equal(names(last.GetAttachments()), names(got)) {
		t.Fatalf("only the new files should be prepared, got %v", names(last.GetAttachments()))
	}
}

func TestAttachQueuesAFileForTheNextMessage(t *testing.T) {
	_, url := startStore(t)
	dir := t.TempDir()
	spaced := filepath.Join(dir, "a b.txt")
	sum := writeFile(t, spaced, "contents")
	other := filepath.Join(dir, "c.txt")
	otherSum := writeFile(t, other, "more")
	f := &fakeAgentService{streams: [][]*agentv1.WatchSessionResponse{{frame(running, viewOne)}}, uploadURL: url}
	s := startFake(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	in := strings.NewReader(`/file "` + spaced + `"` + "\nlook at this\n/file " + other + " and this\nplain\n/file\n/quit\n")
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
		var sums []string
		for _, a := range f.inputs[i].GetAttachments() {
			sums = append(sums, a.GetSha256())
		}
		if f.inputs[i].GetContent() != w.content || !slices.Equal(sums, w.sums) {
			t.Errorf("input %d = %q with %v, want %q with %v", i, f.inputs[i].GetContent(), sums, w.content, w.sums)
		}
	}
	for _, notice := range []string{"(" + spaced + " goes out with your next message)", "(usage: /file <path> [message])"} {
		if !strings.Contains(out.String(), notice) {
			t.Errorf("expected notice %q, got:\n%s", notice, out.String())
		}
	}
}

func TestAttachCountsQueuedFilesAgainstTheLimit(t *testing.T) {
	_, url := startStore(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a"), "a")
	writeFile(t, filepath.Join(dir, "b"), "b")
	f := &fakeAgentService{streams: [][]*agentv1.WatchSessionResponse{{frame(running, viewOne)}}, uploadURL: url, maxAttachments: 1}
	s := startFake(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	in := strings.NewReader("/file " + filepath.Join(dir, "a") + "\n/file " + filepath.Join(dir, "b") + "\nsend\n/quit\n")
	var out syncBuffer
	if err := attachSession(ctx, s, "s1", in, &out); err != nil {
		t.Fatalf("attachSession: %v", err)
	}
	if len(f.inputs) != 1 || !slices.Equal(names(f.inputs[0].GetAttachments()), []string{"a"}) {
		t.Fatalf("expected one message carrying only the first file, got %v", f.inputs)
	}
	if !strings.Contains(out.String(), "2 files is over the limit of 1") {
		t.Fatalf("expected the limit shown, got:\n%s", out.String())
	}
}

func TestAttachDoesNotSendAMessageWhoseFileFailed(t *testing.T) {
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
