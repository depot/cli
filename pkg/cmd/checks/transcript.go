package checks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/depot/cli/pkg/api"
	checksv1 "github.com/depot/cli/pkg/proto/depot/checks/v1"
	"github.com/spf13/cobra"
)

func newTranscript(o *options) *cobra.Command {
	var filename string
	cmd := &cobra.Command{
		Use: "transcript <check-id | attempt-id>", Short: "Display or download an archived check transcript", Args: cobra.ExactArgs(1),
		Long: "Display the selected attempt's archived transcript. Archives are available after execution, subject to retention.\nUse --output jsonl for the exact original archive or --output-file to save the selected format.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.authenticate(cmd); err != nil {
				return err
			}
			attempt, err := selectAttempt(cmd.Context(), o, args[0])
			if err != nil {
				return err
			}
			resp, err := api.NewCheckClient().GetCheckTranscript(cmd.Context(), request(o, &checksv1.GetCheckTranscriptRequest{CheckId: attempt.CheckId, AttemptId: attempt.Id}))
			if err != nil {
				return err
			}
			meta := resp.Msg
			if meta.Status != "archived" {
				return fmt.Errorf("transcript %s: %s", meta.Status, meta.Reason)
			}
			u, err := url.Parse(meta.DownloadUrl)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				return fmt.Errorf("invalid transcript download location")
			}
			digest, err := hex.DecodeString(meta.Sha256)
			if err != nil || len(digest) != sha256.Size || meta.Bytes == 0 || meta.Bytes >= uint64(1<<63-1) {
				return fmt.Errorf("invalid transcript archive metadata")
			}
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, meta.DownloadUrl, nil)
			if err != nil {
				return fmt.Errorf("invalid transcript download request")
			}
			client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			download, err := client.Do(req)
			// Transport errors include the signed URL. Keep it out of terminal output and telemetry.
			if err != nil {
				return fmt.Errorf("transcript download failed; request the transcript again for a fresh download")
			}
			defer download.Body.Close()
			if download.StatusCode != http.StatusOK {
				return fmt.Errorf("transcript download failed (HTTP %d); request the transcript again for a fresh download", download.StatusCode)
			}
			archive, err := os.CreateTemp("", "depot-check-transcript-*")
			if err != nil {
				return err
			}
			defer os.Remove(archive.Name())
			defer archive.Close()
			hash := sha256.New()
			n, err := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(download.Body, int64(meta.Bytes)+1))
			if err != nil {
				return fmt.Errorf("could not read transcript archive")
			}
			if uint64(n) != meta.Bytes || hex.EncodeToString(hash.Sum(nil)) != meta.Sha256 {
				return fmt.Errorf("transcript archive size or SHA-256 does not match its metadata")
			}
			if _, err := archive.Seek(0, io.SeekStart); err != nil {
				return err
			}
			write := func(w io.Writer) error {
				if o.output == "jsonl" {
					_, err := io.Copy(w, archive)
					return err
				}
				return renderTranscript(w, archive)
			}
			if filename == "" || filename == "-" {
				return write(cmd.OutOrStdout())
			}
			staged, err := os.CreateTemp(filepath.Dir(filename), ".depot-transcript-*")
			if err != nil {
				return err
			}
			defer os.Remove(staged.Name())
			defer staged.Close()
			if err := write(staged); err != nil {
				return err
			}
			if err := staged.Close(); err != nil {
				return err
			}
			return os.Rename(staged.Name(), filename)
		},
	}
	cmd.Flags().StringVar(&filename, "output-file", "", "Save to a file instead of stdout (- for stdout)")
	return cmd
}

func renderTranscript(w io.Writer, r io.Reader) error {
	decoder := json.NewDecoder(r)
	var rawHeader json.RawMessage
	var header struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
	}
	if err := decoder.Decode(&rawHeader); err != nil {
		return fmt.Errorf("invalid transcript session; use --output jsonl for the original archive")
	}
	if err := json.Unmarshal(rawHeader, &header); err != nil || header.Type != "session" || header.Version != 3 {
		return fmt.Errorf("unsupported transcript session; use --output jsonl for the original archive")
	}
	prettyHeader, err := json.MarshalIndent(rawHeader, "", "  ")
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s\n", prettyHeader); err != nil {
		return err
	}
	for {
		var entry struct {
			Type     string  `json:"type"`
			ID       string  `json:"id"`
			ParentID *string `json:"parentId"`
			Message  struct {
				Role     string          `json:"role"`
				Content  json.RawMessage `json:"content"`
				ToolName string          `json:"toolName"`
				IsError  bool            `json:"isError"`
			} `json:"message"`
		}
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid transcript record; use --output jsonl for the original archive")
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("invalid transcript record")
		}
		if entry.Type != "message" {
			pretty, err := json.MarshalIndent(raw, "", "  ")
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "\n%s\n", pretty); err != nil {
				return err
			}
			continue
		}
		parent := "root"
		if entry.ParentID != nil {
			parent = *entry.ParentID
		}
		if _, err := fmt.Fprintf(w, "\n%s [%s; parent %s] %s\n", entry.Message.Role, entry.ID, parent, entry.Message.ToolName); err != nil {
			return err
		}
		if entry.Message.IsError {
			if _, err := fmt.Fprintln(w, "Tool error:"); err != nil {
				return err
			}
		}
		var text string
		if json.Unmarshal(entry.Message.Content, &text) == nil {
			if _, err := fmt.Fprintln(w, text); err != nil {
				return err
			}
			continue
		}
		var blocks []json.RawMessage
		if err := json.Unmarshal(entry.Message.Content, &blocks); err != nil {
			return fmt.Errorf("unsupported transcript content; use --output jsonl for the original archive")
		}
		for _, rawBlock := range blocks {
			var block struct{ Type, Text, Thinking string }
			if err := json.Unmarshal(rawBlock, &block); err != nil {
				return fmt.Errorf("invalid transcript content")
			}
			switch block.Type {
			case "text":
				text = block.Text
			case "thinking":
				text = "Thinking:\n" + block.Thinking
			default:
				pretty, err := json.MarshalIndent(rawBlock, "", "  ")
				if err != nil {
					return err
				}
				text = string(pretty)
			}
			if _, err := fmt.Fprintln(w, text); err != nil {
				return err
			}
		}
	}
}
