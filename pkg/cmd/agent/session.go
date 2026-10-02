package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func newCmdSessionCreate() *cobra.Command {
	var (
		auth   authFlags
		repo   string
		ref    string
		model  string
		title  string
		watch  bool
		output string
	)

	cmd := &cobra.Command{
		Use:   "create [flags] <message>",
		Short: "Start a new agent session with a first message",
		Example: `  # Start a session against a repository and stream it until it settles
  depot agent session create --repo https://github.com/org/repo --watch "fix the flaky test in pkg/foo"

  # Pick a model explicitly
  depot agent session create --model claude-opus-5-5 "summarize the README"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

			s, err := auth.resolve(ctx)
			if err != nil {
				return err
			}
			req := &agentv1.CreateSessionRequest{
				Title:           title,
				Message:         strings.Join(args, " "),
				ClientRequestId: ptr(uuid.NewString()),
			}
			if repo != "" {
				req.RepoUrl = ptr(repo)
			}
			if ref != "" {
				req.Ref = ptr(ref)
			}
			if model != "" {
				req.Model = parseModel(model)
			}

			resp, err := createSession(ctx, s, req)
			if err != nil {
				return err
			}
			if output == "json" {
				return writeProtoJSON(resp)
			}
			sessionID := resp.GetSession().GetSessionId()
			fmt.Printf("Created session %s\n", sessionID)
			if !watch {
				fmt.Printf("Watch it with: depot agent session watch %s\n", sessionID)
				return nil
			}
			return watchSession(ctx, s, sessionID, NewRenderer(os.Stdout), untilTurnDone)
		},
	}

	auth.register(cmd)
	cmd.Flags().StringVar(&repo, "repo", "", "Git repository URL to clone into the agent workspace")
	cmd.Flags().StringVar(&ref, "ref", "", "Git ref to check out (defaults to the repository's default branch)")
	cmd.Flags().StringVar(&model, "model", "", "Model as <provider>/<model-id> or <model-id> (defaults to the server's choice)")
	cmd.Flags().StringVar(&title, "title", "", "Session title")
	cmd.Flags().BoolVar(&watch, "watch", false, "Stream the session after creating it, until it settles")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	cmd.MarkFlagsMutuallyExclusive("watch", "output")
	return cmd
}

func newCmdSessionSend() *cobra.Command {
	var (
		auth  authFlags
		steer bool
	)

	cmd := &cobra.Command{
		Use:   "send [flags] <session-id> <message>",
		Short: "Send a message to a session",
		Long: `Send a message to a session.

By default the message is queued as a follow-up after the current turn.
With --steer it is delivered into the running turn instead.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := auth.resolve(ctx)
			if err != nil {
				return err
			}
			mode := modeFollowup
			if steer {
				mode = modeSteer
			}
			inputID, err := sendInput(ctx, s, args[0], strings.Join(args[1:], " "), mode)
			if err != nil {
				return err
			}
			fmt.Printf("Sent input %s\n", inputID)
			return nil
		},
	}

	auth.register(cmd)
	cmd.Flags().BoolVar(&steer, "steer", false, "Deliver the message into the running turn instead of queueing it")
	return cmd
}

func newCmdSessionInterrupt() *cobra.Command {
	var auth authFlags

	cmd := &cobra.Command{
		Use:   "interrupt <session-id>",
		Short: "Abort the session's running turn",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := auth.resolve(ctx)
			if err != nil {
				return err
			}
			resp, err := s.client.InterruptSession(ctx, authed(s, &agentv1.InterruptSessionRequest{SessionId: args[0]}))
			if err != nil {
				return fmt.Errorf("interrupt session %s: %w", args[0], err)
			}
			fmt.Printf("Interrupt queued as input %s\n", resp.Msg.GetInput().GetInputId())
			return nil
		},
	}

	auth.register(cmd)
	return cmd
}

func newCmdSessionList() *cobra.Command {
	var (
		auth      authFlags
		pageToken string
		output    string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List agent sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := auth.resolve(ctx)
			if err != nil {
				return err
			}
			req := &agentv1.ListDepotAgentSessionsRequest{}
			if pageToken != "" {
				req.PageToken = ptr(pageToken)
			}
			resp, err := s.client.ListDepotAgentSessions(ctx, authed(s, req))
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			if output == "json" {
				return writeProtoJSON(resp.Msg)
			}
			if err := writeSessionTable(os.Stdout, resp.Msg.GetSessions()); err != nil {
				return fmt.Errorf("write session table: %w", err)
			}
			if next := resp.Msg.GetNextPageToken(); next != "" {
				fmt.Fprintf(os.Stderr, "More sessions: depot agent session list --page-token %s\n", next)
			}
			return nil
		},
	}

	auth.register(cmd)
	cmd.Flags().StringVar(&pageToken, "page-token", "", "Page token from a previous list")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}

func newCmdSessionWatch() *cobra.Command {
	var (
		auth      authFlags
		untilIdle bool
	)

	cmd := &cobra.Command{
		Use:   "watch [flags] <session-id>",
		Short: "Stream a session's transcript",
		Long: `Stream a session's transcript: user messages, assistant text, and tool calls.

Runs until interrupted, or with --until-idle until the session is idle, waiting for input, or stopped.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			s, err := auth.resolve(ctx)
			if err != nil {
				return err
			}
			until := untilCancelled
			if untilIdle {
				until = untilSettled
			}
			return watchSession(ctx, s, args[0], NewRenderer(os.Stdout), until)
		},
	}

	auth.register(cmd)
	cmd.Flags().BoolVar(&untilIdle, "until-idle", false, "Exit once the session is idle, waiting for input, or stopped")
	return cmd
}

func newCmdSessionAttach() *cobra.Command {
	var auth authFlags

	cmd := &cobra.Command{
		Use:   "attach <session-id>",
		Short: "Watch a session and send each stdin line as a follow-up",
		Long: `Watch a session and send each line read from stdin as a follow-up message.

Lines starting with a slash are commands:
  /steer <message>   deliver the message into the running turn
  /interrupt         abort the running turn
  /quit              detach (the session keeps running)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			s, err := auth.resolve(ctx)
			if err != nil {
				return err
			}
			return attachSession(ctx, s, args[0], os.Stdin, os.Stdout)
		},
	}

	auth.register(cmd)
	return cmd
}

const requestAttempts = 3

// createSession retries transient failures with the request's one client_request_id,
// so a retry of a create that did land returns that session instead of a second one.
func createSession(ctx context.Context, s *session, req *agentv1.CreateSessionRequest) (*agentv1.CreateSessionResponse, error) {
	resp, err := withRetries(ctx, func() (*connect.Response[agentv1.CreateSessionResponse], error) {
		return s.client.CreateSession(ctx, authed(s, req))
	})
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return resp.Msg, nil
}

// withRetries calls do until it succeeds, fails for good, or runs out of attempts.
// do must resend the same request, so its client_request_id makes a retry of a call that landed return the original.
func withRetries[T any](ctx context.Context, do func() (T, error)) (T, error) {
	for attempt := 1; ; attempt++ {
		resp, err := do()
		if err == nil || attempt == requestAttempts || !retryable(err) {
			return resp, err
		}
		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
}

const (
	modeFollowup = "followup"
	modeSteer    = "steer"
)

func sendInput(ctx context.Context, s *session, sessionID, content, mode string) (string, error) {
	req := &agentv1.SendInputRequest{
		SessionId:       sessionID,
		Content:         content,
		Mode:            ptr(mode),
		ClientRequestId: ptr(uuid.NewString()),
	}
	resp, err := withRetries(ctx, func() (*connect.Response[agentv1.SendInputResponse], error) {
		return s.client.SendInput(ctx, authed(s, req))
	})
	if err != nil {
		return "", fmt.Errorf("send input to session %s: %w", sessionID, err)
	}
	return resp.Msg.GetInput().GetInputId(), nil
}

// watchUntil says when watchSession stops on its own.
type watchUntil int

const (
	// untilCancelled watches until ctx is cancelled.
	untilCancelled watchUntil = iota
	// untilSettled stops at the first settled status, including one already settled when the watch starts.
	untilSettled
	// untilTurnDone stops once the session settles after its first turn has started,
	// so a fresh session that is idle before its first input starts does not count.
	untilTurnDone
)

// watchSession streams a session through r,
// reconnecting when the server ends the stream,
// until ctx is cancelled or the until condition holds.
// A terminal status always ends a bounded watch.
func watchSession(ctx context.Context, s *session, sessionID string, r *Renderer, until watchUntil) error {
	w := watchState{}
	for {
		done, err := watchOnce(ctx, s, sessionID, r, until, &w)
		if done || ctx.Err() != nil {
			return nil
		}
		if err != nil && !retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

// watchState carries across reconnects,
// so a new stream resumes after the last view already rendered.
type watchState struct {
	started bool
	lastSeq uint64
}

func watchOnce(ctx context.Context, s *session, sessionID string, r *Renderer, until watchUntil, w *watchState) (bool, error) {
	req := &agentv1.WatchSessionRequest{SessionId: sessionID}
	if w.lastSeq > 0 {
		req.AfterSeq = ptr(w.lastSeq)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := s.client.WatchSession(streamCtx, authed(s, req))
	if err != nil {
		cancel()
		return false, fmt.Errorf("watch session %s: %w", sessionID, err)
	}
	// Close drains unread messages,
	// so a live stream must be cancelled first or Close blocks forever.
	defer func() {
		cancel()
		_ = stream.Close()
	}()

	for stream.Receive() {
		msg := stream.Msg()
		if err := r.Render(msg); err != nil {
			return false, err
		}
		w.lastSeq = max(w.lastSeq, msg.GetViewSeq())
		status := msg.GetSession().GetStatus()
		// A turn can finish between watches, so a message in the view also shows it started.
		if status == "running" || r.sawMessage {
			w.started = true
		}
		if until != untilCancelled && terminal(status) ||
			until == untilSettled && settled(status) ||
			until == untilTurnDone && w.started && settled(status) {
			return true, nil
		}
	}
	if err := stream.Err(); err != nil {
		return false, fmt.Errorf("watch session %s: %w", sessionID, err)
	}
	return false, nil
}

// retryable reports whether err is a transient RPC failure.
// Any other error, such as a view that fails to decode, would fail the same way again.
func retryable(err error) bool {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return false
	}
	switch connectErr.Code() {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeUnknown:
		return true
	}
	return false
}

// attachSession watches the session while forwarding stdin lines as inputs.
// It returns when ctx is cancelled, on /quit, or when the watch fails;
// stdin reaching EOF stops sending but keeps watching;
// failing to read it ends the attach, so a line is never dropped silently.
func attachSession(ctx context.Context, s *session, sessionID string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	watchErr := make(chan error, 1)
	go func() {
		watchErr <- watchSession(ctx, s, sessionID, NewRenderer(out), untilCancelled)
	}()

	lines := make(chan string)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			readErr <- err
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return <-watchErr
		case err := <-watchErr:
			return err
		case err := <-readErr:
			cancel()
			<-watchErr
			return fmt.Errorf("read stdin: %w", err)
		case line, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			cmd := parseAttachLine(line)
			switch cmd.kind {
			case attachNone:
				continue
			case attachQuit:
				cancel()
				return <-watchErr
			case attachUnknown:
				fmt.Fprintf(out, "(unknown command %q; try /steer, /interrupt, /quit)\n", cmd.content)
				continue
			case attachInterrupt:
				if _, err := s.client.InterruptSession(ctx, authed(s, &agentv1.InterruptSessionRequest{SessionId: sessionID})); err != nil {
					fmt.Fprintf(out, "(interrupt failed: %v)\n", err)
				}
				continue
			}
			if _, err := sendInput(ctx, s, sessionID, cmd.content, cmd.mode); err != nil {
				if errors.Is(err, context.Canceled) {
					return <-watchErr
				}
				fmt.Fprintf(out, "(send failed: %v)\n", err)
			}
		}
	}
}

type attachKind int

const (
	attachNone attachKind = iota
	attachInput
	attachInterrupt
	attachQuit
	attachUnknown
)

type attachCommand struct {
	kind    attachKind
	mode    string
	content string
}

func parseAttachLine(line string) attachCommand {
	line = strings.TrimSpace(line)
	if line == "" {
		return attachCommand{kind: attachNone}
	}
	if !strings.HasPrefix(line, "/") {
		return attachCommand{kind: attachInput, mode: modeFollowup, content: line}
	}
	name, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	switch name {
	case "/steer":
		if rest == "" {
			return attachCommand{kind: attachNone}
		}
		return attachCommand{kind: attachInput, mode: modeSteer, content: rest}
	case "/interrupt":
		return attachCommand{kind: attachInterrupt}
	case "/quit":
		return attachCommand{kind: attachQuit}
	}
	return attachCommand{kind: attachUnknown, content: name}
}

func parseModel(s string) *agentv1.DepotAgentModel {
	if provider, modelID, ok := strings.Cut(s, "/"); ok {
		return &agentv1.DepotAgentModel{Provider: provider, ModelId: modelID}
	}
	return &agentv1.DepotAgentModel{ModelId: s}
}

func writeSessionTable(w io.Writer, sessions []*agentv1.DepotAgentSession) error {
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(w, "No agent sessions found")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tTITLE\tREPO\tUPDATED")
	for _, s := range sessions {
		updated := ""
		if s.UpdatedAt != nil {
			updated = s.UpdatedAt.AsTime().Local().Format(time.DateTime)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.GetSessionId(), s.GetStatus(), truncate(oneLine(s.GetTitle())), s.GetRepoUrl(), updated)
	}
	return tw.Flush()
}

func ptr[T any](v T) *T {
	return &v
}
