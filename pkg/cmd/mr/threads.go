package mr

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// A review thread is the comment on lines of the diff that started it, then its replies, oldest first.
type thread struct {
	ID        string            `json:"id"`
	Path      string            `json:"path"`
	Side      string            `json:"side"`
	StartLine uint32            `json:"startLine"`
	EndLine   uint32            `json:"endLine"`
	Resolved  bool              `json:"resolved"`
	Outdated  bool              `json:"outdated"`
	Comments  []json.RawMessage `json:"comments"`
	comments  []*code.MergeRequestComment
}

type threadList struct {
	MergeRequestID string            `json:"mergeRequestId"`
	Threads        []*thread         `json:"threads"`
	Comments       []json.RawMessage `json:"comments"`
	conversation   []*code.MergeRequestComment
}

// outdated reports whether the merge request moved past the commits a thread's lines were read at. A merge
// request opened before its base commit was recorded has none to compare.
func outdated(a *code.MergeRequestCommentAnchor, mr *code.MergeRequest) bool {
	return a.HeadOid != mr.HeadOid || (mr.BaseOid != "" && a.BaseOid != mr.BaseOid)
}

// groupThreads splits comments into review threads and the conversation. A thread is outdated once the merge
// request's head or base moved past the commits its lines were read at.
func groupThreads(resp *code.GetMergeRequestResponse) *threadList {
	list := &threadList{MergeRequestID: resp.MergeRequest.Id, Threads: []*thread{}, Comments: []json.RawMessage{}}
	byID := map[string]*thread{}
	for _, c := range resp.Comments {
		if a := c.Anchor; a != nil {
			t := &thread{ID: c.Id, Path: a.Path, Side: label(a.Side, "SIDE_"), StartLine: a.StartLine, EndLine: a.EndLine,
				Resolved: c.ResolvedAt != nil,
				Outdated: outdated(a, resp.MergeRequest)}
			byID[c.Id] = t
			list.Threads = append(list.Threads, t)
		}
	}
	for _, c := range resp.Comments {
		id := c.InReplyToId
		if c.Anchor != nil {
			id = c.Id
		}
		if t := byID[id]; t != nil {
			t.comments = append(t.comments, c)
		} else {
			list.conversation = append(list.conversation, c)
		}
	}
	return list
}

func (l *threadList) writeJSON(out io.Writer) error {
	raw := func(comments []*code.MergeRequestComment) ([]json.RawMessage, error) {
		items := make([]json.RawMessage, 0, len(comments))
		for _, c := range comments {
			data, err := protojson.Marshal(c)
			if err != nil {
				return nil, err
			}
			items = append(items, data)
		}
		return items, nil
	}
	var err error
	for _, t := range l.Threads {
		if t.Comments, err = raw(t.comments); err != nil {
			return err
		}
	}
	if l.Comments, err = raw(l.conversation); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}

func (l *threadList) writeText(out io.Writer) error {
	var b strings.Builder
	for _, t := range l.Threads {
		state := "unresolved"
		if t.Resolved {
			state = "resolved"
		}
		if t.Outdated {
			state += ", outdated"
		}
		fmt.Fprintf(&b, "Thread %s  %s  %s\n", t.ID, t.where(), state)
		writeComments(&b, t.comments)
	}
	if len(l.conversation) > 0 {
		b.WriteString("Conversation\n")
		writeComments(&b, l.conversation)
	}
	if b.Len() == 0 {
		b.WriteString("No comments\n")
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func (t *thread) where() string {
	lines := fmt.Sprintf("%s:%d", printable(t.Path), t.StartLine)
	if t.EndLine > t.StartLine {
		lines += fmt.Sprintf("-%d", t.EndLine)
	}
	if t.Side == "old" {
		lines += " (before the change)"
	}
	return lines
}

func writeComments(b *strings.Builder, comments []*code.MergeRequestComment) {
	for _, c := range comments {
		fmt.Fprintf(b, "  %s  %s  %s\n", c.Id, actorName(c.Author), c.CreatedAt.AsTime().UTC().Format("2006-01-02 15:04 UTC"))
		body := printableText(c.Body)
		if c.DeletedAt != nil {
			body = "(deleted)"
		}
		for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
			fmt.Fprintf(b, "    %s\n", line)
		}
	}
}

func actorName(a *code.MergeRequestActor) string {
	if a == nil {
		return "unknown"
	}
	return label(a.Kind, "KIND_") + " " + printable(a.Id)
}

func newThreads(o *options) *cobra.Command {
	var t target
	var unresolved bool
	cmd := &cobra.Command{Use: "threads [branch]", Short: "List a merge request's review threads and comments", Args: cobra.MaximumNArgs(1),
		Long: "List the review threads on a merge request's diff, then its conversation comments.\nEach thread shows its file and lines, whether it is resolved, and whether the diff has moved on since (outdated).\nA thread's ID is the ID of its first comment; `depot mr reply`, `resolve` and `unresolve` take any comment ID of the thread.\nWith --json the output is {mergeRequestId, threads: [{id, path, side, startLine, endLine, resolved, outdated, comments}], comments}, where each comment is the API's comment.",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := t.check(cmd, args)
			if err != nil {
				return err
			}
			if err := o.authenticateFor(cmd, name); err != nil {
				return err
			}
			var resp *code.GetMergeRequestResponse
			if name != "" {
				resp, err = o.get(cmd, name)
			} else {
				resp, err = o.getByID(cmd, t.id)
			}
			if err != nil {
				return err
			}
			list := groupThreads(resp)
			if unresolved {
				open := list.Threads[:0]
				for _, t := range list.Threads {
					if !t.Resolved {
						open = append(open, t)
					}
				}
				list.Threads, list.conversation = open, nil
			}
			if o.json {
				return list.writeJSON(cmd.OutOrStdout())
			}
			return list.writeText(cmd.OutOrStdout())
		},
	}
	t.register(cmd)
	cmd.Flags().BoolVar(&unresolved, "unresolved", false, "Only review threads that are not resolved, without the conversation")
	return cmd
}

func (o *options) getByID(cmd *cobra.Command, id string) (*code.GetMergeRequestResponse, error) {
	return o.fetch(cmd, &code.GetMergeRequestRequest{Lookup: &code.GetMergeRequestRequest_MergeRequestId{MergeRequestId: id}})
}

// commentID validates the one argument of reply, resolve and unresolve without calling the API.
func commentID(args []string) (string, error) {
	id := strings.TrimSpace(args[0])
	if id == "" {
		return "", fmt.Errorf("comment ID must not be blank")
	}
	return id, nil
}

const threadsUnsupported = "this Depot API does not support review threads yet"

func newReply(o *options) *cobra.Command {
	var flags bodyFlags
	cmd := &cobra.Command{Use: "reply <comment ID> --body <text>", Short: "Reply in a review thread", Args: cobra.ExactArgs(1),
		Long: "Reply in the review thread that a comment belongs to. `depot mr threads` lists the IDs.\nA reply works on resolved and outdated threads and leaves the thread's resolved state as it is.\n--repo is not needed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := commentID(args)
			if err != nil {
				return err
			}
			body, _, err := flags.read(cmd)
			if err != nil {
				return err
			}
			if strings.TrimSpace(body) == "" {
				return fmt.Errorf("--body or --body-file is required and must not be blank")
			}
			if err := o.login(cmd); err != nil {
				return err
			}
			resp, err := api.NewMergeRequestClient().CreateMergeRequestComment(cmd.Context(), request(o, &code.CreateMergeRequestCommentRequest{InReplyToId: id, Body: body}))
			// An API without threads ignores the thread and asks for the merge request the reply leaves out.
			if connect.CodeOf(err) == connect.CodeInvalidArgument && strings.Contains(err.Error(), "merge_request_id") {
				return fmt.Errorf("%s: %w", threadsUnsupported, err)
			}
			if err != nil {
				return err
			}
			comment := resp.Msg.Comment
			if comment == nil || comment.Id == "" || comment.InReplyToId == "" {
				return fmt.Errorf("API returned no reply")
			}
			if o.json {
				return o.writeJSON(cmd, resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Replied in thread %s on %s: %s\n", comment.InReplyToId, comment.MergeRequestId, comment.Id)
			return err
		},
	}
	flags.register(cmd, "Reply text (required unless --body-file is set)")
	return cmd
}

func newResolve(o *options, resolve bool) *cobra.Command {
	use, short, done := "resolve", "Mark a review thread resolved", "Resolved"
	if !resolve {
		use, short, done = "unresolve", "Mark a resolved review thread unresolved again", "Unresolved"
	}
	return &cobra.Command{Use: use + " <comment ID>", Short: short, Args: cobra.ExactArgs(1),
		Long: short + ".\nTakes the ID of any comment in the thread; `depot mr threads` lists them.\nOnly threads on lines of the diff resolve, not conversation comments. Any member of the organization may do it.\n--repo is not needed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := commentID(args)
			if err != nil {
				return err
			}
			if err := o.login(cmd); err != nil {
				return err
			}
			client := api.NewMergeRequestClient()
			var root *code.MergeRequestComment
			var message proto.Message
			if resolve {
				resp, err := client.ResolveMergeRequestThread(cmd.Context(), request(o, &code.ResolveMergeRequestThreadRequest{CommentId: id}))
				if err != nil {
					return threadError(err)
				}
				root, message = resp.Msg.Comment, resp.Msg
			} else {
				resp, err := client.UnresolveMergeRequestThread(cmd.Context(), request(o, &code.UnresolveMergeRequestThreadRequest{CommentId: id}))
				if err != nil {
					return threadError(err)
				}
				root, message = resp.Msg.Comment, resp.Msg
			}
			if root == nil || root.Id == "" || (root.ResolvedAt != nil) != resolve {
				return fmt.Errorf("API returned no thread")
			}
			if o.json {
				return o.writeJSON(cmd, message)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s thread %s on %s\n", done, root.Id, root.MergeRequestId)
			return err
		},
	}
}

func threadError(err error) error {
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		return fmt.Errorf("%s: %w", threadsUnsupported, err)
	}
	return err
}
