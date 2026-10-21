package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type sendOptions struct {
	org, token, output, message, mode, key, subagent, clientRequestID string
}

func newSend() *cobra.Command {
	o := &sendOptions{}
	cmd := &cobra.Command{
		Use:   "send [session-id] --message <text>",
		Short: "Send a message to a running agent session",
		Long: `Send a message to an agent session, by its ID or by the --client-request-id it was launched with (--key).

By default the message waits for the agent's current turn to finish (--mode followup). --mode steer reaches the agent
mid-turn. --subagent sends it to one of the session's subagents instead of the main agent.`,
		Example: `  depot agent send das_0123 --message "Also check the migration"
  depot agent send --key ci:abc123 --message "Rebased; review the new head" --client-request-id push:def456`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSend(cmd, o, args)
		},
	}
	cmd.Flags().StringVar(&o.message, "message", "", "Message text")
	cmd.Flags().StringVar(&o.mode, "mode", "followup", "When the agent gets the message: followup or steer")
	cmd.Flags().StringVar(&o.key, "key", "", "Find the session by the --client-request-id it was launched with, instead of its ID")
	cmd.Flags().StringVar(&o.subagent, "subagent", "", "Send to this subagent (its conversation ID in the session view) instead of the main agent")
	cmd.Flags().StringVar(&o.clientRequestID, "client-request-id", "", "Key that makes the send safe to retry; unique per session")
	cmd.Flags().StringVar(&o.org, "org", "", "Organization ID (defaults to DEPOT_ORG_ID or the current organization)")
	cmd.Flags().StringVar(&o.token, "token", "", "Depot API token (defaults to DEPOT_TOKEN)")
	cmd.Flags().StringVarP(&o.output, "output", "o", "text", "Output format: text or json")
	_ = cmd.MarkFlagRequired("message")
	return cmd
}

func runSend(cmd *cobra.Command, o *sendOptions, args []string) error {
	if o.output != "text" && o.output != "json" {
		return fmt.Errorf("unsupported output format %q", o.output)
	}
	if strings.TrimSpace(o.message) == "" {
		return fmt.Errorf("--message must not be empty")
	}
	if o.mode != "followup" && o.mode != "steer" {
		return fmt.Errorf("--mode must be followup or steer, not %q", o.mode)
	}
	if (len(args) == 1) == (o.key != "") {
		return fmt.Errorf("pass either a session ID or --key")
	}

	if o.org == "" {
		o.org = os.Getenv("DEPOT_ORG_ID")
	}
	if o.org == "" {
		o.org = config.GetCurrentOrganization()
	}
	sessionID := ""
	if len(args) == 1 {
		sessionID = args[0]
	} else {
		if o.org == "" {
			return fmt.Errorf("--key needs the organization ID; pass --org or DEPOT_ORG_ID")
		}
		var err error
		if sessionID, err = sessionIDForKey(o.org, o.key); err != nil {
			return err
		}
	}

	mode := o.mode
	req := &agentv1.SendInputRequest{SessionId: sessionID, Content: o.message}
	if o.subagent != "" {
		target, err := proto.Marshal(&agentv1.DepotAgentSubagentsInput{ConversationId: o.subagent, FollowUp: o.mode == "followup"})
		if err != nil {
			return err
		}
		mode = "subagent"
		req.Contributions = map[string][]byte{"subagents": target}
	}
	req.Mode = &mode
	if o.clientRequestID != "" {
		req.ClientRequestId = &o.clientRequestID
	}

	token, err := helpers.ResolveOrgAuth(cmd.Context(), o.token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token; pass --token or DEPOT_TOKEN, or run `depot login`")
	}
	resp, err := api.NewDepotAgentClient().SendInput(cmd.Context(), api.WithAuthenticationAndOrg(connect.NewRequest(req), token, o.org))
	if err != nil {
		if o.key != "" && connect.CodeOf(err) == connect.CodeNotFound {
			return fmt.Errorf("%w\nNo session was launched with --client-request-id %q in organization %s", err, o.key, o.org)
		}
		return err
	}

	if o.output == "json" {
		out, err := protojson.MarshalOptions{Multiline: true}.Marshal(resp.Msg)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Sent %s to session %s\n", resp.Msg.GetInput().GetInputId(), sessionID)
	if err == nil && o.org != "" {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "https://depot.dev/orgs/%s/agent-runs/sessions/%s\n", url.PathEscape(o.org), url.PathEscape(sessionID))
	}
	return err
}

// The ID the API gives a session created with this client request ID: "das_" and the first 32 hex digits of the
// SHA-256 of the JavaScript JSON.stringify([orgID, key]), with the key trimmed as JavaScript's trim() does.
func sessionIDForKey(orgID, key string) (string, error) {
	key = strings.TrimFunc(key, func(r rune) bool { return r == '\ufeff' || (r != '\u0085' && unicode.IsSpace(r)) })
	if key == "" || !utf8.ValidString(key) || !utf8.ValidString(orgID) {
		return "", fmt.Errorf("--key must be non-empty UTF-8 text")
	}
	sum := sha256.Sum256([]byte("[" + jsonString(orgID) + "," + jsonString(key) + "]"))
	return "das_" + hex.EncodeToString(sum[:])[:32], nil
}

// Quotes as JSON.stringify does; encoding/json escapes <, >, &, U+2028 and U+2029, which would change the hash.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
