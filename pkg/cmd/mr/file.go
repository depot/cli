package mr

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

const maxFileBytes = 4 * 1024 * 1024

type fileJSON struct {
	MergeRequestID string `json:"mergeRequestId"`
	RepositoryID   string `json:"repositoryId"`
	HeadOid        string `json:"headOid"`
	BaseOid        string `json:"baseOid"`
	Path           string `json:"path"`
	OldContents    []byte `json:"oldContents"`
	NewContents    []byte `json:"newContents"`
}

func readBlob(cmd *cobra.Command, o *options, repo, oid, mode string) ([]byte, error) {
	contents := []byte{}
	if oid == "" {
		return contents, nil
	}
	if mode == "160000" {
		return nil, fmt.Errorf("gitlinks name commits, not file contents")
	}
	object, err := parseOID(oid)
	if err != nil {
		return nil, err
	}
	for {
		res, err := api.NewCodeClient().GetBlob(cmd.Context(), request(o, &code.GetBlobRequest{RepositoryId: repo, BlobOid: object, Offset: uint64(len(contents)), Length: 1024 * 1024}))
		if err != nil {
			return nil, err
		}
		if res.Msg.Size > maxFileBytes {
			return nil, fmt.Errorf("file contents exceed the 4 MiB read limit (%d bytes)", res.Msg.Size)
		}
		contents = append(contents, res.Msg.Content...)
		if uint64(len(contents)) == res.Msg.Size {
			return contents, nil
		}
		if uint64(len(contents)) > res.Msg.Size || len(res.Msg.Content) == 0 {
			return nil, fmt.Errorf("API returned incomplete file contents")
		}
	}
}
func newFile(o *options) *cobra.Command {
	var f comparisonFlags
	var path, side string
	cmd := &cobra.Command{Use: "file [branch] --path <path>", Short: "Read the contents of a changed file", Long: "Read one exact changed file from the recorded MR comparison (up to 4 MiB per side).\n--json returns both sides as base64, with resolved revisions. An absent side is empty.\nText output prints --side head (default) or base. Gitlinks cannot be read as blobs.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if path == "" {
			return fmt.Errorf("--path is required")
		}
		if side != "head" && side != "base" {
			return fmt.Errorf("--side must be head or base")
		}
		c, err := f.resolve(cmd, o, args)
		if err != nil {
			return err
		}
		f.limit = 100
		seen := map[string]bool{}
		for {
			page, err := readDiffPage(cmd, o, c, f, path, 0, true, false)
			if err != nil {
				return err
			}
			for _, file := range page.Files {
				if file.Path != path {
					continue
				}
				result := fileJSON{MergeRequestID: c.mr.Id, RepositoryID: c.mr.RepositoryId, HeadOid: page.HeadOid, BaseOid: page.BaseOid, Path: path}
				if o.json || side == "base" {
					result.OldContents, err = readBlob(cmd, o, c.mr.RepositoryId, file.OldOid, file.OldMode)
					if err != nil {
						return err
					}
				}
				if o.json || side == "head" {
					result.NewContents, err = readBlob(cmd, o, c.mr.RepositoryId, file.NewOid, file.NewMode)
					if err != nil {
						return err
					}
				}
				if o.json {
					return writeCodeJSON(cmd, result)
				}
				contents := result.NewContents
				if side == "base" {
					contents = result.OldContents
				}
				if bytes.ContainsAny(contents, "\x00\x1b\u009b") {
					return fmt.Errorf("file contains binary or terminal control bytes; use --json for base64 output")
				}
				_, err = cmd.OutOrStdout().Write(contents)
				return err
			}
			if page.NextPageToken == "" {
				return fmt.Errorf("%s is not an exact changed file in this comparison", strings.TrimSpace(path))
			}
			if err = nextPage(seen, page.NextPageToken); err != nil {
				return err
			}
			f.pageToken = page.NextPageToken
		}
	}}
	f.register(cmd, true, false)
	cmd.Flags().StringVar(&path, "path", "", "Exact repository-relative changed file path (required)")
	cmd.Flags().StringVar(&side, "side", "head", "Side to print in text mode: head or base")
	return cmd
}
