package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/config"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

type sendOptions struct {
	org, token, output, file, key, subagent, clientRequestID string
	steer, watch                                             bool
	timeout                                                  time.Duration
}

func newSend() *cobra.Command {
	o := &sendOptions{}
	cmd := &cobra.Command{
		Use:   "send <session-id | --key <client-request-id>> [<message> | -f <file> | -]",
		Short: "Send a message to a running agent session",
		Long: `Send a message to an agent session, by its ID or by the --client-request-id it was launched with (--key).

The message is a follow-up. A follow-up from a person reaches the agent at its next step when that person's own
message started the current turn; any other follow-up waits for the current turn to finish. --steer reaches the agent
mid-turn. --subagent sends it to one of the session's subagents instead of the main agent.

Every send is delivered, including the same message sent again by a rerun or by another matrix leg. To make a repeat
replay instead, pass the same --client-request-id both times. Nothing in a job's environment tells one matrix leg from
another, so build the key from workflow expressions.`,
		Example: `  depot agent send das_0123 "Also check the migration"
  git log -1 --format=%B | depot agent send --key pr-12 --steer -
  depot agent send --key pr-12 "tests passed" --client-request-id "tests-${{ github.run_id }}-${{ strategy.job-index }}"`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSend(cmd, o, args)
		},
	}
	cmd.Flags().StringVarP(&o.file, "file", "f", "", "Read the message from this file (- for stdin)")
	cmd.Flags().BoolVar(&o.steer, "steer", false, "Reach the agent mid-turn instead of as a follow-up")
	cmd.Flags().StringVar(&o.key, "key", "", "Find the session by the --client-request-id it was launched with, instead of its ID")
	cmd.Flags().StringVar(&o.subagent, "subagent", "", "Send to this subagent (its conversation ID in the session view) instead of the main agent")
	cmd.Flags().StringVar(&o.clientRequestID, "client-request-id", "", "Key that makes the send safe to repeat: a second send with it in the same session delivers nothing (default: random)")
	cmd.Flags().StringVar(&o.org, "org", "", "Organization ID (defaults to DEPOT_ORG_ID or the current organization)")
	cmd.Flags().StringVar(&o.token, "token", "", "Depot API token (defaults to DEPOT_TOKEN, which a Depot CI job already has)")
	cmd.Flags().StringVarP(&o.output, "output", "o", "text", "Output format: text or json")
	addWatchFlags(cmd, &o.watch, &o.timeout)
	return cmd
}

func runSend(cmd *cobra.Command, o *sendOptions, args []string) error {
	if o.output != "text" && o.output != "json" {
		return fmt.Errorf("unsupported output format %q", o.output)
	}
	sessionID := ""
	if o.key == "" {
		if len(args) == 0 {
			return fmt.Errorf("pass a session ID or --key")
		}
		sessionID, args = args[0], args[1:]
	} else if len(args) == 2 {
		return fmt.Errorf("pass either a session ID or --key, not both")
	}
	message, err := readMessage(cmd, args, o.file)
	if err != nil {
		return err
	}

	if o.org == "" {
		o.org = os.Getenv("DEPOT_ORG_ID")
	}
	if o.org == "" {
		o.org = config.GetCurrentOrganization()
	}
	if o.key != "" {
		if o.org == "" {
			return fmt.Errorf("--key needs the organization ID; pass --org or DEPOT_ORG_ID")
		}
		if sessionID, err = sessionIDForKey(o.org, o.key); err != nil {
			return err
		}
	}

	mode := "followup"
	if o.steer {
		mode = "steer"
	}
	if o.subagent != "" && o.watch {
		return fmt.Errorf("--watch follows the main agent's reply and cannot be combined with --subagent")
	}
	req := &agentv1.SendInputRequest{SessionId: sessionID, Content: message}
	if o.subagent != "" {
		target, err := proto.Marshal(&agentv1.DepotAgentSubagentsInput{ConversationId: o.subagent, FollowUp: !o.steer})
		if err != nil {
			return err
		}
		req.Contributions = map[string][]byte{"subagents": target}
	}
	clientRequestID := o.clientRequestID
	if clientRequestID == "" {
		clientRequestID = randomRequestID()
	}
	if o.subagent != "" {
		mode = "subagent"
	}
	req.Mode, req.ClientRequestId = &mode, &clientRequestID

	c, err := turnCaller(cmd, o.token, o.org)
	if err != nil {
		return err
	}
	var base *int64
	if o.watch {
		base = newestSeq(cmd.Context(), c, sessionID)
	}
	resp, err := c.client.SendInput(cmd.Context(), authed(c, req))
	if err != nil {
		if o.key != "" && connect.CodeOf(err) == connect.CodeNotFound {
			return fmt.Errorf("%w\nNo session was launched with --client-request-id %q in organization %s", err, o.key, o.org)
		}
		return c.refused(err, "SendInput")
	}

	// Only a replayed send returns a cancelled input: its session was archived before the agent took it.
	const cancelled = "the input was cancelled before the agent took it"
	status := resp.Msg.GetInput().GetStatus()
	if status == "cancelled" && !o.watch {
		if o.output == "json" {
			if err := printJSON(cmd.OutOrStdout(), resp.Msg); err != nil {
				return err
			}
		}
		return fmt.Errorf("%s was not delivered: %s", resp.Msg.GetInput().GetInputId(), cancelled)
	}
	if o.output == "json" && !o.watch {
		return printJSON(cmd.OutOrStdout(), resp.Msg)
	}
	if o.output == "text" {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Sent %s to session %s\n", resp.Msg.GetInput().GetInputId(), sessionID)
		if u := sessionURL(o.org, sessionID); err == nil && u != "" {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), u)
		}
		if err != nil {
			return err
		}
	}
	if !o.watch {
		return nil
	}
	// A repeated client request ID returns the input of the first send. Once the session has taken that one,
	// what was read just now is not from before it, so the watch finds its own base.
	if status != "" && status != "pending" {
		base = nil
	}
	wait := watchOptions{afterInput: resp.Msg.GetInput().GetInputId(), untilIdle: true, timeout: o.timeout, base: base, did: "got the message"}
	if status == "cancelled" {
		return report(cmd, c, o.output, sessionID, wait, watchResult{SessionID: sessionID, InputID: wait.afterInput, Status: "archived", Outcome: outcomeFailed, Error: cancelled})
	}
	return watchAndReport(cmd, c, o.output, sessionID, wait)
}

func readMessage(cmd *cobra.Command, args []string, file string) (string, error) {
	if (len(args) == 1) == (file != "") {
		return "", fmt.Errorf("pass the message as an argument, with -f <file>, or as - to read stdin")
	}
	var data []byte
	var err error
	switch {
	case file == "" && args[0] != "-":
		data = []byte(args[0])
	case file == "" || file == "-":
		data, err = io.ReadAll(cmd.InOrStdin())
	default:
		data, err = os.ReadFile(file)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("the message must not be empty")
	}
	return string(data), nil
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
