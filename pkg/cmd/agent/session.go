package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// sessionFlags are the flags every command that works on a session takes.
type sessionFlags struct{ org, token, output string }

func (f *sessionFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.org, "org", "", "Organization ID (defaults to DEPOT_ORG_ID or the current organization)")
	cmd.Flags().StringVar(&f.token, "token", "", "Depot API token (defaults to DEPOT_TOKEN or your login)")
	cmd.Flags().StringVarP(&f.output, "output", "o", "text", "Output format: text or json")
}

const missingToken = "missing API token; pass --token or DEPOT_TOKEN, or run `depot login`"

func (f *sessionFlags) checkOutput() error {
	if f.output != "text" && f.output != "json" {
		return fmt.Errorf("unsupported output format %q", f.output)
	}
	return nil
}

// caller resolves both credentials. needOrg says the command also makes a call that does not take a turn's token.
func (f *sessionFlags) caller(cmd *cobra.Command, needOrg bool) (*caller, error) {
	if err := f.checkOutput(); err != nil {
		return nil, err
	}
	token, turn, err := helpers.ResolveAgentAuth(cmd.Context(), f.token, needOrg)
	if err != nil {
		return nil, err
	}
	if turn == "" || needOrg && token == "" {
		return nil, errors.New(missingToken)
	}
	c := newCaller(token, f.org)
	c.turn = turn
	return c, nil
}

// orgCaller is for a command none of whose calls takes a turn's token.
func (f *sessionFlags) orgCaller(cmd *cobra.Command) (*caller, error) {
	if err := f.checkOutput(); err != nil {
		return nil, err
	}
	token, err := helpers.ResolveOrgAuth(cmd.Context(), f.token)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, errors.New(missingToken)
	}
	return newCaller(token, f.org), nil
}

// caller holds what a session RPC is made with: token as every other command resolves it, and turn for asTurn.
type caller struct {
	client           agentv1connect.DepotAgentServiceClient
	token, turn, org string
}

func newCaller(token, org string) *caller {
	return &caller{client: api.NewDepotAgentClient(), token: token, turn: token, org: resolveOrg(org)}
}

func resolveOrg(org string) string {
	if org == "" {
		org = os.Getenv("DEPOT_ORG_ID")
	}
	if org == "" {
		org = config.GetCurrentOrganization()
	}
	return org
}

func authed[T any](c *caller, msg *T) *connect.Request[T] {
	return api.WithAuthenticationAndOrg(connect.NewRequest(msg), c.token, c.org)
}

// asTurn is authed for the RPCs the API lets a Dispatch turn's sender call: CreateSession, GetSession,
// GetSessionMessage and ListDepotAgentSessions.
func asTurn[T any](c *caller, msg *T) *connect.Request[T] {
	return api.WithAuthenticationAndOrg(connect.NewRequest(msg), c.turn, c.org)
}

func sessionURL(org, sessionID string) string {
	if org == "" {
		return ""
	}
	return fmt.Sprintf("https://depot.dev/orgs/%s/agent-runs/sessions/%s", url.PathEscape(org), url.PathEscape(sessionID))
}

func randomRequestID() string {
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	return "cli:" + hex.EncodeToString(random)
}

func printJSON(w io.Writer, msg proto.Message) error {
	out, err := protojson.MarshalOptions{Multiline: true}.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(out))
	return err
}

func newCreate() *cobra.Command {
	f := &sessionFlags{}
	var file, title, model, clientRequestID string
	var watch bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "create [<message> | -f <file> | -]",
		Short: "Start an agent session with a first message",
		Long: `Start a session of the default agent, with no published agent and no repository, and print its ID and URL.

Use ` + "`depot agent launch`" + ` to start a published agent on a repository.`,
		Example: `  depot agent create "Reply with the word ready" --watch`,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			message, err := readMessage(cmd, args, file)
			if err != nil {
				return err
			}
			c, err := f.caller(cmd, watch)
			if err != nil {
				return err
			}
			if clientRequestID == "" {
				clientRequestID = randomRequestID()
			}
			req := &agentv1.CreateSessionRequest{Title: title, Message: message, ClientRequestId: &clientRequestID, Model: parseModel(model)}
			resp, err := c.client.CreateSession(cmd.Context(), asTurn(c, req))
			if err != nil {
				return err
			}
			sessionID := resp.Msg.GetSession().GetSessionId()
			if watch && f.output == "json" {
				return watchAndReport(cmd, c, f.output, sessionID, watchOptions{afterInput: resp.Msg.GetInput().GetInputId(), untilIdle: true, timeout: timeout, base: beforeFirst()})
			}
			if f.output == "json" {
				return printJSON(cmd.OutOrStdout(), resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created session %s\n", sessionID)
			if u := sessionURL(c.org, sessionID); err == nil && u != "" {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), u)
			}
			if err != nil || !watch {
				return err
			}
			return watchAndReport(cmd, c, f.output, sessionID, watchOptions{afterInput: resp.Msg.GetInput().GetInputId(), untilIdle: true, timeout: timeout, base: beforeFirst()})
		},
	}
	f.add(cmd)
	cmd.Flags().StringVarP(&file, "file", "f", "", "Read the message from this file (- for stdin)")
	cmd.Flags().StringVar(&title, "title", "", "Session title (defaults to the first line of the message)")
	cmd.Flags().StringVar(&model, "model", "", "Model as <provider>/<model-id> or <model-id> (defaults to the server's choice)")
	cmd.Flags().StringVar(&clientRequestID, "client-request-id", "", "Key that makes the create safe to retry")
	addWatchFlags(cmd, &watch, &timeout)
	return cmd
}

func parseModel(s string) *agentv1.DepotAgentModel {
	if s == "" {
		return nil
	}
	if provider, id, ok := strings.Cut(s, "/"); ok {
		return &agentv1.DepotAgentModel{Provider: provider, ModelId: id}
	}
	return &agentv1.DepotAgentModel{ModelId: s}
}

func newList() *cobra.Command {
	f := &sessionFlags{}
	var limit int32
	var pageToken string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List agent sessions, most recently updated first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := f.caller(cmd, false)
			if err != nil {
				return err
			}
			req := &agentv1.ListDepotAgentSessionsRequest{PageSize: &limit}
			if pageToken != "" {
				req.PageToken = &pageToken
			}
			resp, err := c.client.ListDepotAgentSessions(cmd.Context(), asTurn(c, req))
			if err != nil {
				return err
			}
			if f.output == "json" {
				return printJSON(cmd.OutOrStdout(), resp.Msg)
			}
			if err := writeSessionTable(cmd.OutOrStdout(), resp.Msg.GetSessions()); err != nil {
				return err
			}
			if next := resp.Msg.GetNextPageToken(); next != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "More sessions: --page-token %s\n", next)
			}
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().Int32Var(&limit, "limit", 20, "Sessions to list, at most 100")
	cmd.Flags().StringVar(&pageToken, "page-token", "", "Page token from a previous list")
	return cmd
}

func writeSessionTable(w io.Writer, sessions []*agentv1.DepotAgentSession) error {
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(w, "No agent sessions found")
		return err
	}
	tw := tabwriter.NewWriter(&safeWriter{w: w}, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tUPDATED\tTITLE")
	for _, s := range sessions {
		updated := ""
		if s.GetUpdatedAt() != nil {
			updated = s.GetUpdatedAt().AsTime().Local().Format(time.DateTime)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", oneLine(s.GetSessionId()), oneLine(s.GetStatus()), updated, truncate(oneLine(s.GetTitle())))
	}
	return tw.Flush()
}

func newLogs() *cobra.Command {
	f := &sessionFlags{}
	cmd := &cobra.Command{
		Use:   "logs <session-id>",
		Short: "Print an agent session's transcript",
		Long: `Print the session's recent messages and tool calls, then its status.

The transcript is the API's bounded view of the session: old history is cut, and so are long tool results. A long
reply of the agent is read whole; -o json prints the view as the API returns it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.caller(cmd, false)
			if err != nil {
				return err
			}
			resp, err := c.client.GetSession(cmd.Context(), asTurn(c, &agentv1.GetSessionRequest{SessionId: args[0]}))
			if err != nil {
				return err
			}
			if f.output == "text" {
				r := NewRenderer(cmd.OutOrStdout()).Whole(func(messageID string) (string, error) {
					return wholeMessage(cmd.Context(), c, args[0], messageID)
				})
				_, err := r.Render(resp.Msg.GetSession(), resp.Msg.GetViewJson())
				return err
			}
			session, err := protojson.Marshal(resp.Msg.GetSession())
			if err != nil {
				return err
			}
			view := json.RawMessage("null")
			if v := resp.Msg.GetViewJson(); json.Valid([]byte(v)) {
				view = json.RawMessage(v)
			}
			out, err := json.MarshalIndent(map[string]any{"session": json.RawMessage(session), "viewSeq": resp.Msg.GetViewSeq(), "view": view}, "", "  ")
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return err
		},
	}
	f.add(cmd)
	return cmd
}

func newStop() *cobra.Command {
	f := &sessionFlags{}
	var archive bool
	cmd := &cobra.Command{
		Use:   "stop <session-id>",
		Short: "Interrupt an agent session's current turn, or archive the session",
		Long: `Interrupt the turn the session is running; the session stays open for more messages.

--archive ends the session instead: it stops for good and takes no more messages.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.orgCaller(cmd)
			if err != nil {
				return err
			}
			var msg proto.Message
			done := "Interrupted"
			if archive {
				resp, err := c.client.ArchiveSession(cmd.Context(), authed(c, &agentv1.ArchiveSessionRequest{SessionId: args[0]}))
				if err != nil {
					return err
				}
				msg, done = resp.Msg, "Archived"
			} else {
				resp, err := c.client.InterruptSession(cmd.Context(), authed(c, &agentv1.InterruptSessionRequest{SessionId: args[0]}))
				if err != nil {
					return err
				}
				msg = resp.Msg
			}
			if f.output == "json" {
				return printJSON(cmd.OutOrStdout(), msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s session %s\n", done, args[0])
			return err
		},
	}
	f.add(cmd)
	cmd.Flags().BoolVar(&archive, "archive", false, "End the session instead of interrupting its turn")
	return cmd
}
