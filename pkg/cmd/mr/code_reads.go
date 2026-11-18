package mr

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	gitv1 "github.com/depot/cli/pkg/proto/depot/git/v1"
	"github.com/spf13/cobra"
)

type comparisonFlags struct {
	target
	commit, pageToken string
	limit             uint32
}

func (f *comparisonFlags) register(cmd *cobra.Command, commit, paging bool) {
	f.target.register(cmd)
	if commit {
		cmd.Flags().StringVar(&f.commit, "commit", "", "Show one commit against its first parent instead of the MR comparison")
	}
	if paging {
		cmd.Flags().StringVar(&f.pageToken, "page-token", "", "Continue a JSON page (requires --json)")
		cmd.Flags().Uint32Var(&f.limit, "limit", 100, "Files or commits per API page (1–100)")
	}
}

func parseOID(s string) (*gitv1.ObjectId, error) {
	b, err := hex.DecodeString(s)
	if err != nil || (len(b) != 20 && len(b) != 32) {
		return nil, fmt.Errorf("invalid full commit object ID %q", s)
	}
	if len(b) == 20 {
		return &gitv1.ObjectId{Value: &gitv1.ObjectId_Sha1{Sha1: b}}, nil
	}
	return &gitv1.ObjectId{Value: &gitv1.ObjectId_Sha256{Sha256: b}}, nil
}
func hexOID(oid *gitv1.ObjectId) string {
	if oid == nil {
		return ""
	}
	if b := oid.GetSha1(); len(b) > 0 {
		return hex.EncodeToString(b)
	}
	return hex.EncodeToString(oid.GetSha256())
}

type comparison struct {
	mr   *code.MergeRequest
	head *gitv1.ObjectId
	base *code.CommitBase
}

func (f *comparisonFlags) resolve(cmd *cobra.Command, o *options, args []string) (comparison, error) {
	var c comparison
	name, err := f.target.check(cmd, args)
	if err != nil {
		return c, err
	}
	if cmd.Flags().Lookup("limit") != nil && (f.limit == 0 || f.limit > 100) {
		return c, fmt.Errorf("--limit must be between 1 and 100")
	}
	if cmd.Flags().Changed("page-token") && (!o.json || f.pageToken == "") {
		return c, fmt.Errorf("--page-token requires --json and a nonempty token")
	}
	if cmd.Flags().Changed("commit") {
		if _, err = parseOID(f.commit); err != nil {
			return c, err
		}
	}
	if err = o.authenticateFor(cmd, name); err != nil {
		return c, err
	}
	var found *code.GetMergeRequestResponse
	if name != "" {
		found, err = o.get(cmd, name)
	} else {
		found, err = o.fetch(cmd, &code.GetMergeRequestRequest{Lookup: &code.GetMergeRequestRequest_MergeRequestId{MergeRequestId: f.id}})
	}
	if err != nil {
		return c, err
	}
	c.mr = found.MergeRequest
	if (f.id != "" && c.mr.Id != f.id) || c.mr.RepositoryId == "" || (o.repo != "" && c.mr.RepositoryId != o.repo) {
		return c, fmt.Errorf("API returned a different merge request or repository")
	}
	if f.commit != "" {
		c.head, err = parseOID(f.commit)
		c.base = &code.CommitBase{Base: &code.CommitBase_FirstParent_{FirstParent: &code.CommitBase_FirstParent{}}}
	} else {
		c.head, err = parseOID(c.mr.HeadOid)
		if err != nil {
			return c, err
		}
		if c.mr.BaseOid == "" {
			return c, fmt.Errorf("merge request %s has no recorded base commit; its historical comparison is unavailable", c.mr.Id)
		}
		var base *gitv1.ObjectId
		base, err = parseOID(c.mr.BaseOid)
		c.base = &code.CommitBase{Base: &code.CommitBase_MergeBaseWithOid{MergeBaseWithOid: base}}
	}
	return c, err
}

func writeCodeJSON(cmd *cobra.Command, v any) error {
	e := json.NewEncoder(cmd.OutOrStdout())
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func nextPage(seen map[string]bool, token string) error {
	if token != "" {
		if seen[token] {
			return fmt.Errorf("API repeated a page token; results are incomplete")
		}
		seen[token] = true
	}
	return nil
}

type commitJSON struct {
	Oid           string   `json:"oid"`
	ParentOids    []string `json:"parentOids"`
	Message       string   `json:"message"`
	AuthorName    string   `json:"authorName"`
	AuthorEmail   string   `json:"authorEmail"`
	CommittedDate *string  `json:"committedDate"`
}
type commitsJSON struct {
	MergeRequestID string       `json:"mergeRequestId"`
	RepositoryID   string       `json:"repositoryId"`
	HeadOid        string       `json:"headOid"`
	BaseOid        string       `json:"baseOid"`
	Commits        []commitJSON `json:"commits"`
	NextPageToken  string       `json:"nextPageToken"`
}

func newCommits(o *options) *cobra.Command {
	var f comparisonFlags
	cmd := &cobra.Command{Use: "commits [branch]", Short: "List commits unique to a merge request", Long: "List commits reachable from the recorded MR head but not its recorded base.\nText output follows all pages. --json returns one page with nextPageToken; pass it back with --page-token.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := f.resolve(cmd, o, args)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for {
			res, err := api.NewCodeClient().ListCommits(cmd.Context(), request(o, &code.ListCommitsRequest{RepositoryId: c.mr.RepositoryId, HeadOid: c.head, ExcludeReachableFromOid: c.base.GetMergeBaseWithOid(), PageSize: f.limit, PageToken: f.pageToken}))
			if err != nil {
				return err
			}
			r := res.Msg
			if hexOID(r.HeadOid) != hexOID(c.head) || hexOID(r.ExcludeReachableFromOid) != c.mr.BaseOid {
				return fmt.Errorf("API returned a different commit comparison")
			}
			page := commitsJSON{MergeRequestID: c.mr.Id, RepositoryID: c.mr.RepositoryId, HeadOid: hexOID(r.HeadOid), BaseOid: hexOID(r.ExcludeReachableFromOid), Commits: []commitJSON{}, NextPageToken: r.NextPageToken}
			for _, v := range r.Commits {
				if o.json && (!utf8.Valid(v.Message) || !utf8.Valid(v.GetAuthor().GetName()) || !utf8.Valid(v.GetAuthor().GetEmail())) {
					return fmt.Errorf("commit %s contains non-UTF-8 metadata; JSON output is unavailable", hexOID(v.Oid))
				}
				item := commitJSON{Oid: hexOID(v.Oid), Message: string(v.Message), AuthorName: string(v.GetAuthor().GetName()), AuthorEmail: string(v.GetAuthor().GetEmail()), ParentOids: []string{}}
				for _, p := range v.ParentOids {
					item.ParentOids = append(item.ParentOids, hexOID(p))
				}
				if v.Committer != nil && v.Committer.TimestampSeconds != nil {
					date := time.Unix(*v.Committer.TimestampSeconds, 0).UTC()
					if date.Year() >= 0 && date.Year() <= 9999 {
						s := date.Format(time.RFC3339)
						item.CommittedDate = &s
					}
				}
				page.Commits = append(page.Commits, item)
			}
			if o.json {
				return writeCodeJSON(cmd, page)
			}
			for _, v := range page.Commits {
				if _, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", v.Oid, printable(strings.SplitN(v.Message, "\n", 2)[0])); err != nil {
					return err
				}
			}
			if r.NextPageToken == "" {
				return nil
			}
			if err = nextPage(seen, r.NextPageToken); err != nil {
				return err
			}
			f.pageToken = r.NextPageToken
		}
	}}
	f.register(cmd, false, true)
	return cmd
}
