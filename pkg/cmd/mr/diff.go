package mr

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

type diffFileJSON struct {
	Path        string  `json:"path"`
	Kind        string  `json:"kind"`
	OldOid      string  `json:"oldOid"`
	NewOid      string  `json:"newOid"`
	OldMode     string  `json:"oldMode"`
	NewMode     string  `json:"newMode"`
	ContentKind string  `json:"contentKind"`
	Additions   *uint32 `json:"additions"`
	Deletions   *uint32 `json:"deletions"`
	Patch       string  `json:"patch,omitempty"`
}
type diffJSON struct {
	MergeRequestID string         `json:"mergeRequestId"`
	RepositoryID   string         `json:"repositoryId"`
	HeadOid        string         `json:"headOid"`
	BaseOid        string         `json:"baseOid"`
	AnchorBaseOid  string         `json:"anchorBaseOid,omitempty"`
	Files          []diffFileJSON `json:"files"`
	NextPageToken  string         `json:"nextPageToken"`
}

func mode(o *code.TreeObject) string {
	switch o.GetMode() {
	case code.TreeEntryMode_TREE_ENTRY_MODE_REGULAR_FILE:
		return "100644"
	case code.TreeEntryMode_TREE_ENTRY_MODE_EXECUTABLE_FILE:
		return "100755"
	case code.TreeEntryMode_TREE_ENTRY_MODE_SYMLINK:
		return "120000"
	case code.TreeEntryMode_TREE_ENTRY_MODE_GITLINK:
		return "160000"
	default:
		return ""
	}
}
func quotePath(path string) string {
	if strings.IndexFunc(path, func(r rune) bool { return r < 32 || r == 127 || r == '"' || r == '\\' }) < 0 {
		return path
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, v := range []byte(path) {
		switch v {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(v)
		default:
			if v < 32 || v >= 127 {
				fmt.Fprintf(&b, "\\%03o", v)
			} else {
				b.WriteByte(v)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
func diffFile(f *code.DiffCommitsResponse_FileDiff, patch bool) (diffFileJSON, error) {
	v := diffFileJSON{Path: string(f.Path), Kind: strings.ToLower(strings.TrimPrefix(f.Kind.String(), "TREE_CHANGE_KIND_")), OldOid: hexOID(f.OldObject.GetOid()), NewOid: hexOID(f.NewObject.GetOid()), OldMode: mode(f.OldObject), NewMode: mode(f.NewObject)}
	if !utf8.Valid(f.Path) || len(f.Path) == 0 {
		return v, fmt.Errorf("diff contains a path that cannot be represented as UTF-8")
	}
	var b strings.Builder
	oldPath, newPath := quotePath("a/"+v.Path), quotePath("b/"+v.Path)
	fmt.Fprintf(&b, "diff --git %s %s\n", oldPath, newPath)
	if f.OldObject == nil {
		fmt.Fprintf(&b, "new file mode %s\n", v.NewMode)
		oldPath = "/dev/null"
	} else if f.NewObject == nil {
		fmt.Fprintf(&b, "deleted file mode %s\n", v.OldMode)
		newPath = "/dev/null"
	} else if v.OldMode != v.NewMode {
		fmt.Fprintf(&b, "old mode %s\nnew mode %s\n", v.OldMode, v.NewMode)
	}
	oldOid, newOid := v.OldOid, v.NewOid
	if oldOid == "" {
		oldOid = strings.Repeat("0", len(newOid))
	}
	if newOid == "" {
		newOid = strings.Repeat("0", len(oldOid))
	}
	fmt.Fprintf(&b, "index %s..%s\n", oldOid, newOid)
	switch content := f.Content.GetKind().(type) {
	case *code.DiffCommitsResponse_FileDiffContent_Text_:
		v.ContentKind = "text"
		v.Additions = &f.Additions
		v.Deletions = &f.Deletions
		if patch && len(content.Text.Hunks) > 0 {
			fmt.Fprintf(&b, "--- %s\n+++ %s\n", oldPath, newPath)
			for _, h := range content.Text.Hunks {
				if !utf8.Valid(h.Section) || strings.ContainsAny(string(h.Section), "\r\n") {
					return v, fmt.Errorf("invalid diff hunk section")
				}
				fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
				if len(h.Section) > 0 {
					b.WriteByte(' ')
					b.Write(h.Section)
				}
				b.WriteByte('\n')
				for _, l := range h.Lines {
					if !utf8.Valid(l.Text) {
						return v, fmt.Errorf("diff for %q contains non-UTF-8 text; read it with depot mr file", v.Path)
					}
					switch l.Kind {
					case code.DiffCommitsResponse_DIFF_LINE_KIND_ADDED:
						b.WriteByte('+')
					case code.DiffCommitsResponse_DIFF_LINE_KIND_DELETED:
						b.WriteByte('-')
					case code.DiffCommitsResponse_DIFF_LINE_KIND_CONTEXT:
						b.WriteByte(' ')
					default:
						return v, fmt.Errorf("API returned an unknown diff line kind")
					}
					b.Write(l.Text)
					b.WriteByte('\n')
					if !l.EndsWithNewline {
						b.WriteString("\\ No newline at end of file\n")
					}
				}
			}
		}
	case *code.DiffCommitsResponse_FileDiffContent_Binary_:
		v.ContentKind = "binary"
		fmt.Fprintf(&b, "Binary files %s and %s differ\n", oldPath, newPath)
	case *code.DiffCommitsResponse_FileDiffContent_TooLarge_:
		v.ContentKind = "too_large"
	case nil:
		if v.OldMode != "160000" && v.NewMode != "160000" {
			return v, fmt.Errorf("API omitted diff contents for %q", v.Path)
		}
		v.ContentKind = "gitlink"
		if (v.OldMode != "" && v.OldMode != "160000") || (v.NewMode != "" && v.NewMode != "160000") {
			fmt.Fprintf(&b, "Binary files %s and %s differ\n", oldPath, newPath)
			break
		}
		// Gitlinks name commits rather than blobs and cannot be read with GetBlob.
		fmt.Fprintf(&b, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", oldPath, newPath, boolInt(v.OldOid != ""), boolInt(v.OldOid != ""), boolInt(v.NewOid != ""), boolInt(v.NewOid != ""))
		if v.OldOid != "" {
			fmt.Fprintf(&b, "-Subproject commit %s\n", v.OldOid)
		}
		if v.NewOid != "" {
			fmt.Fprintf(&b, "+Subproject commit %s\n", v.NewOid)
		}
	default:
		return v, fmt.Errorf("API returned an unknown diff content kind")
	}
	v.Patch = b.String()
	return v, nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func readDiffPage(cmd *cobra.Command, o *options, c comparison, f comparisonFlags, path string, context uint32, namesOnly, patch bool) (diffJSON, error) {
	page := diffJSON{MergeRequestID: c.mr.Id, RepositoryID: c.mr.RepositoryId, Files: []diffFileJSON{}}
	if f.commit == "" {
		page.AnchorBaseOid = c.mr.BaseOid
	}
	if namesOnly {
		res, err := api.NewCodeClient().CompareCommits(cmd.Context(), request(o, &code.CompareCommitsRequest{RepositoryId: c.mr.RepositoryId, HeadOid: c.head, Base: c.base, PageSize: f.limit, PageToken: f.pageToken}))
		if err != nil {
			return page, err
		}
		if hexOID(res.Msg.HeadOid) != hexOID(c.head) || (c.base.GetMergeBaseWithOid() != nil && hexOID(res.Msg.BaseOid) == "") {
			return page, fmt.Errorf("API returned a different diff head")
		}
		page.HeadOid = hexOID(res.Msg.HeadOid)
		page.BaseOid = hexOID(res.Msg.BaseOid)
		page.NextPageToken = res.Msg.NextPageToken
		for _, v := range res.Msg.Changes {
			if !utf8.Valid(v.Path) {
				return page, fmt.Errorf("diff contains a path that cannot be represented as UTF-8")
			}
			name := string(v.Path)
			if path != "" && name != path && !strings.HasPrefix(name, path+"/") {
				continue
			}
			page.Files = append(page.Files, diffFileJSON{Path: name, Kind: strings.ToLower(strings.TrimPrefix(v.Kind.String(), "TREE_CHANGE_KIND_")), OldOid: hexOID(v.OldObject.GetOid()), NewOid: hexOID(v.NewObject.GetOid()), OldMode: mode(v.OldObject), NewMode: mode(v.NewObject), ContentKind: "unread"})
		}
		return page, nil
	}
	req := &code.DiffCommitsRequest{RepositoryId: c.mr.RepositoryId, HeadOid: c.head, Base: c.base, PageSize: f.limit, PageToken: f.pageToken, ContextLines: &context}
	if path != "" {
		req.Paths = [][]byte{[]byte(path)}
	}
	res, err := api.NewCodeClient().DiffCommits(cmd.Context(), request(o, req))
	if err != nil {
		return page, err
	}
	if hexOID(res.Msg.HeadOid) != hexOID(c.head) || (c.base.GetMergeBaseWithOid() != nil && hexOID(res.Msg.BaseOid) == "") {
		return page, fmt.Errorf("API returned a different diff head")
	}
	page.HeadOid = hexOID(res.Msg.HeadOid)
	page.BaseOid = hexOID(res.Msg.BaseOid)
	page.NextPageToken = res.Msg.NextPageToken
	for _, file := range res.Msg.Files {
		v, err := diffFile(file, patch)
		if err != nil {
			return page, err
		}
		page.Files = append(page.Files, v)
	}
	return page, nil
}
func newDiff(o *options) *cobra.Command {
	var f comparisonFlags
	var nameOnly, stat bool
	var path string
	var context uint32
	cmd := &cobra.Command{Use: "diff [branch]", Short: "Show the changes in a merge request", Long: "Show a unified diff against the merge base of the recorded MR head and base.\nMerged and closed MRs retain their historical comparison. --name-only lists paths.\nText output follows all pages. --json returns one page, including per-file status,\nresolved revisions and nextPageToken; pass it back with --page-token.\nBinary files have no line counts; oversized files are explicitly marked too_large.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if context > 100 {
			return fmt.Errorf("--context must be at most 100")
		}
		if cmd.Flags().Changed("path") && path == "" {
			return fmt.Errorf("--path must not be empty")
		}
		if stat {
			context = 0
		}
		c, err := f.resolve(cmd, o, args)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		incomplete := false
		for {
			page, err := readDiffPage(cmd, o, c, f, path, context, nameOnly, !stat)
			if err != nil {
				return err
			}
			if nameOnly || stat {
				for i := range page.Files {
					page.Files[i].Patch = ""
				}
			}
			if o.json {
				return writeCodeJSON(cmd, page)
			}
			for _, file := range page.Files {
				s := file.Patch
				if stat {
					counts := "-\t-"
					if file.Additions != nil && file.Deletions != nil {
						counts = fmt.Sprintf("%d\t%d", *file.Additions, *file.Deletions)
					}
					s = fmt.Sprintf("%s\t%s (%s)\n", counts, quotePath(file.Path), file.ContentKind)
				}
				if nameOnly {
					s = quotePath(file.Path) + "\n"
				} else if !stat && file.ContentKind == "too_large" {
					incomplete = true
					fmt.Fprintf(cmd.ErrOrStderr(), "Diff omitted for %s: file is too large\n", quotePath(file.Path))
				}
				if strings.ContainsAny(s, "\x1b\u009b") {
					return fmt.Errorf("diff contains terminal escape sequences; use --json for escaped output")
				}
				if _, err = fmt.Fprint(cmd.OutOrStdout(), s); err != nil {
					return err
				}
			}
			if page.NextPageToken == "" {
				if incomplete {
					return fmt.Errorf("diff is incomplete: oversized files were omitted; use --json to inspect their status")
				}
				return nil
			}
			if err = nextPage(seen, page.NextPageToken); err != nil {
				return err
			}
			f.pageToken = page.NextPageToken
		}
	}}
	f.register(cmd, true, true)
	cmd.Flags().BoolVar(&stat, "stat", false, "Show file line counts and content status without patches")
	cmd.Flags().BoolVar(&nameOnly, "name-only", false, "Show only changed file names")
	cmd.Flags().StringVar(&path, "path", "", "Restrict to a repository-relative file or directory")
	cmd.Flags().Uint32Var(&context, "context", 3, "Unchanged context lines (0–100)")
	cmd.MarkFlagsMutuallyExclusive("stat", "name-only")
	return cmd
}
