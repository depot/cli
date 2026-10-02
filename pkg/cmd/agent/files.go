package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/docker/go-units"
)

// fileInputs names what to attach: paths to files or directories, and optionally stdin under a file name.
type fileInputs struct {
	paths     []string
	stdinName string
	stdin     io.Reader
}

func (in fileInputs) empty() bool { return len(in.paths) == 0 && in.stdinName == "" }

// source is one file to attach. data holds its bytes once read, so the upload sends exactly what was hashed.
type source struct {
	label      string
	attachment *agentv1.DepotAgentAttachment
	data       []byte
}

// uploadAttachments uploads the files in, which go out in one message with pending,
// and returns the new attachments. A set over the server's limits is refused before anything is read in full or uploaded.
// Every URL Prepare returns is PUT, since a stored copy can expire before the message is sent.
func uploadAttachments(ctx context.Context, s *session, pending []*agentv1.DepotAgentAttachment, in fileInputs, notices io.Writer) ([]*agentv1.DepotAgentAttachment, error) {
	if in.empty() {
		return nil, nil
	}
	limits, err := prepareUploads(ctx, s, nil)
	if err != nil {
		return nil, err
	}
	sources, err := collectSources(in, pending, limits)
	if err != nil {
		return nil, err
	}
	if err := checkLimits(pending, sources, limits); err != nil {
		return nil, err
	}

	taken := map[string]bool{}
	for _, a := range pending {
		taken[a.GetName()] = true
	}
	attachments := make([]*agentv1.DepotAgentAttachment, 0, len(sources))
	bySHA := make(map[string]source, len(sources))
	var total int64
	for i := range sources {
		if err := readSource(&sources[i], limits.GetMaxAttachmentBytes()); err != nil {
			return nil, err
		}
		a := sources[i].attachment
		a.Name = uniqueName(a.GetName(), taken)
		attachments = append(attachments, a)
		bySHA[a.GetSha256()] = sources[i]
		total += a.GetSizeBytes()
	}
	if len(sources) == 1 {
		fmt.Fprintf(notices, "(attaching %s, %s)\n", attachments[0].GetName(), units.HumanSize(float64(total)))
	} else {
		fmt.Fprintf(notices, "(attaching %d files, %s)\n", len(sources), units.HumanSize(float64(total)))
	}

	resp, err := prepareUploads(ctx, s, attachments)
	if err != nil {
		return nil, err
	}
	for _, upload := range resp.GetUploads() {
		src, ok := bySHA[upload.GetSha256()]
		if !ok {
			return nil, fmt.Errorf("prepare attachments: upload offered for unknown file %s", upload.GetSha256())
		}
		if err := putSource(ctx, src, upload); err != nil {
			return nil, err
		}
	}
	return attachments, nil
}

func prepareUploads(ctx context.Context, s *session, attachments []*agentv1.DepotAgentAttachment) (*agentv1.PrepareAttachmentUploadsResponse, error) {
	resp, err := withRetries(ctx, func() (*connect.Response[agentv1.PrepareAttachmentUploadsResponse], error) {
		return s.client.PrepareAttachmentUploads(ctx, authed(s, &agentv1.PrepareAttachmentUploadsRequest{Attachments: attachments}))
	})
	if err != nil {
		return nil, fmt.Errorf("prepare attachments: %w", err)
	}
	return resp.Msg, nil
}

// collectSources expands directories and reads stdin, sizing every file from its metadata.
func collectSources(in fileInputs, pending []*agentv1.DepotAgentAttachment, limits *agentv1.PrepareAttachmentUploadsResponse) ([]source, error) {
	var sources []source
	for _, path := range in.paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("attach %s: %w", path, err)
		}
		switch {
		case info.IsDir():
			files, err := dirSources(path)
			if err != nil {
				return nil, err
			}
			if len(files) == 0 {
				return nil, fmt.Errorf("attach %s: no files to send", path)
			}
			sources = append(sources, files...)
		case info.Mode().IsRegular():
			sources = append(sources, fileSource(path, filepath.Base(path), info.Size()))
		default:
			return nil, fmt.Errorf("attach %s: not a regular file", path)
		}
	}
	if in.stdinName != "" {
		max := stdinLimit(pending, sources, limits)
		r := in.stdin
		if max >= 0 {
			r = io.LimitReader(r, max+1)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("attach stdin: %w", err)
		}
		if max >= 0 && int64(len(data)) > max {
			return nil, fmt.Errorf("stdin is over the %s limit for attachments", units.HumanSize(float64(max)))
		}
		sources = append(sources, source{
			label:      "stdin",
			attachment: &agentv1.DepotAgentAttachment{Name: filepath.Base(in.stdinName), SizeBytes: int64(len(data))},
			data:       data,
		})
	}
	return sources, nil
}

// stdinLimit is the most stdin may hold: the per-file limit, or less if the message's other files leave less of the total.
// It is -1 when the server sets neither limit.
func stdinLimit(pending []*agentv1.DepotAgentAttachment, sources []source, limits *agentv1.PrepareAttachmentUploadsResponse) int64 {
	limit := limits.GetMaxAttachmentBytes()
	if limit <= 0 {
		limit = -1
	}
	if total := limits.GetMaxTotalBytes(); total > 0 {
		for _, a := range pending {
			total -= a.GetSizeBytes()
		}
		for _, src := range sources {
			total -= src.attachment.GetSizeBytes()
		}
		total = max(total, 0)
		if limit < 0 || total < limit {
			limit = total
		}
	}
	return limit
}

func fileSource(path, name string, size int64) source {
	return source{label: path, attachment: &agentv1.DepotAgentAttachment{Name: name, SizeBytes: size}}
}

// dirSources lists a directory's files, named by their path under the directory's parent with "/" flattened to "_".
// Inside a git work tree it sends what git would track, so .gitignore applies, and fails if git does;
// elsewhere it skips .git.
func dirSources(dir string) ([]source, error) {
	inTree, err := inWorkTree(dir)
	if err != nil {
		return nil, fmt.Errorf("attach %s: %w", dir, err)
	}
	var rels []string
	if inTree {
		rels, err = gitFiles(dir)
	} else {
		rels, err = walkFiles(dir)
	}
	if err != nil {
		return nil, fmt.Errorf("attach %s: %w", dir, err)
	}
	base := filepath.Base(filepath.Clean(dir))
	var sources []source
	for _, rel := range rels {
		path := filepath.Join(dir, rel)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			// Tracked by git but deleted from the work tree.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("attach %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		name := strings.ReplaceAll(filepath.ToSlash(filepath.Join(base, rel)), "/", "_")
		sources = append(sources, fileSource(path, name, info.Size()))
	}
	return sources, nil
}

// inWorkTree reports whether dir or a parent holds .git, without asking git, so a broken git cannot hide the answer.
func inWorkTree(dir string) (bool, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	for {
		if _, err := os.Lstat(filepath.Join(abs, ".git")); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return false, nil
		}
		abs = parent
	}
}

func gitFiles(dir string) ([]string, error) {
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, fmt.Errorf("list files with git, to respect .gitignore: %w", err)
	}
	var rels []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" {
			rels = append(rels, filepath.FromSlash(rel))
		}
	}
	return rels, nil
}

func walkFiles(dir string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			rels = append(rels, rel)
		}
		return nil
	})
	return rels, err
}

func checkLimits(pending []*agentv1.DepotAgentAttachment, sources []source, limits *agentv1.PrepareAttachmentUploadsResponse) error {
	if max := int(limits.GetMaxAttachments()); max > 0 && len(pending)+len(sources) > max {
		return fmt.Errorf("%d files is over the limit of %d per message", len(pending)+len(sources), max)
	}
	var total int64
	for _, a := range pending {
		total += a.GetSizeBytes()
	}
	for _, src := range sources {
		size := src.attachment.GetSizeBytes()
		if max := limits.GetMaxAttachmentBytes(); max > 0 && size > max {
			return fmt.Errorf("%s is %s, over the %s limit for attachments", src.label, units.HumanSize(float64(size)), units.HumanSize(float64(max)))
		}
		total += size
	}
	if max := limits.GetMaxTotalBytes(); max > 0 && total > max {
		return fmt.Errorf("attachments total %s, over the %s limit per message", units.HumanSize(float64(total)), units.HumanSize(float64(max)))
	}
	return nil
}

// readSource reads the source once, up to limit bytes when limit is set, and sets its hash, size, and media type.
func readSource(src *source, limit int64) error {
	if src.data == nil {
		f, err := os.Open(src.label)
		if err != nil {
			return fmt.Errorf("attach %s: %w", src.label, err)
		}
		defer f.Close()
		var r io.Reader = f
		if limit > 0 {
			r = io.LimitReader(f, limit+1)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("attach %s: %w", src.label, err)
		}
		if limit > 0 && int64(len(data)) > limit {
			return fmt.Errorf("%s grew over the %s limit for attachments", src.label, units.HumanSize(float64(limit)))
		}
		src.data = data
	}
	sum := sha256.Sum256(src.data)
	a := src.attachment
	a.Sha256 = hex.EncodeToString(sum[:])
	a.SizeBytes = int64(len(src.data))
	a.MediaType = mediaType(a.GetName(), src.data[:min(len(src.data), 512)])
	return nil
}

// mediaType guesses from the name, then from the first bytes, so piped images still go to the model as images.
func mediaType(name string, head []byte) string {
	t := mime.TypeByExtension(filepath.Ext(name))
	if t == "" {
		t = http.DetectContentType(head)
	}
	if parsed, _, err := mime.ParseMediaType(t); err == nil {
		return parsed
	}
	return t
}

// maxNameBytes is the API's limit on an attachment name.
const maxNameBytes = 255

// uniqueName returns name, or name with a "~N" suffix before its extension if taken already holds it,
// within maxNameBytes.
func uniqueName(name string, taken map[string]bool) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	candidate := fitName(stem, ext)
	for i := 2; taken[candidate]; i++ {
		candidate = fitName(stem, fmt.Sprintf("~%d%s", i, ext))
	}
	taken[candidate] = true
	return candidate
}

// fitName drops the start of stem until stem+suffix fits, keeping the extension and the deepest path parts.
func fitName(stem, suffix string) string {
	for len(stem)+len(suffix) > maxNameBytes && stem != "" {
		_, size := utf8.DecodeRuneInString(stem)
		stem = stem[size:]
	}
	return stem + suffix
}

func putSource(ctx context.Context, src source, upload *agentv1.DepotAgentAttachmentUpload) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, upload.GetUrl(), bytes.NewReader(src.data))
	if err != nil {
		return fmt.Errorf("upload %s: %w", src.label, err)
	}
	req.ContentLength = src.attachment.GetSizeBytes()
	for k, v := range upload.GetHeaders() {
		if strings.EqualFold(k, "content-length") {
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload %s: %w", src.label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("upload %s: %s: %s", src.label, resp.Status, msg)
	}
	return nil
}
