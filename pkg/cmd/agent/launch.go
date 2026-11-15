package agent

import (
	"fmt"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
)

type launchOptions struct {
	org, token, output, message, clientRequestID string
	sources                                      []string
	watch                                        bool
	timeout                                      time.Duration
}

func newLaunch() *cobra.Command {
	o := &launchOptions{}
	cmd := &cobra.Command{
		Use:   "launch <agent[@ref]> --source <kind>:<owner/repo>[@ref] --message <text>",
		Short: "Start a session of a published agent",
		Long: `Start a session of a published agent on one or more repositories, and print the session URL.

In a Depot CI job, no flags beyond the agent, sources and message are needed: the job's DEPOT_TOKEN launches as the
organization. Every launch starts a new session unless --client-request-id is passed: a launch that repeats a key returns
the session the first one started. Nothing in a job's environment tells one matrix leg from another, so build the key
from workflow expressions, one per leg and per attempt you want a session for.`,
		Example: `  depot agent launch public-agent-code-review --source github:acme/app@refs/pull/12/head --message "Review this pull request"
  depot agent launch public-agent-code-review --source github:acme/app --message "Review" --client-request-id "review-${{ github.run_id }}-${{ strategy.job-index }}"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLaunch(cmd, o, args[0])
		},
	}
	cmd.Flags().StringArrayVar(&o.sources, "source", nil, "Repository the agent works on, as <kind>:<owner/repo>[@ref] (repeatable)")
	cmd.Flags().StringVar(&o.message, "message", "", "First message of the session")
	cmd.Flags().StringVar(&o.clientRequestID, "client-request-id", "", "Key that makes the launch safe to repeat: a second launch with it returns the first session")
	cmd.Flags().StringVar(&o.org, "org", "", "Organization ID (defaults to DEPOT_ORG_ID or the current organization)")
	cmd.Flags().StringVar(&o.token, "token", "", "Depot API token (defaults to DEPOT_TOKEN, which a Depot CI job already has)")
	cmd.Flags().StringVarP(&o.output, "output", "o", "text", "Output format: text or json")
	addWatchFlags(cmd, &o.watch, &o.timeout)
	_ = cmd.MarkFlagRequired("source")
	_ = cmd.MarkFlagRequired("message")
	return cmd
}

func runLaunch(cmd *cobra.Command, o *launchOptions, agent string) error {
	if o.output != "text" && o.output != "json" {
		return fmt.Errorf("unsupported output format %q", o.output)
	}
	if strings.TrimSpace(o.message) == "" {
		return fmt.Errorf("--message must not be empty")
	}
	req := &agentv1.LaunchRequest{Agent: agent, Message: o.message}
	for _, value := range o.sources {
		source, err := parseSource(value)
		if err != nil {
			return err
		}
		req.Sources = append(req.Sources, source)
	}
	if key := o.clientRequestID; key != "" {
		req.ClientRequestId = &key
	}

	if o.org == "" {
		o.org = os.Getenv("DEPOT_ORG_ID")
	}
	if o.org == "" {
		o.org = config.GetCurrentOrganization()
	}
	token, err := helpers.ResolveAgentAuth(cmd.Context(), o.token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token; pass --token or DEPOT_TOKEN, or run `depot login`")
	}

	resp, err := api.NewDepotAgentLaunchClient().Launch(cmd.Context(), api.WithAuthenticationAndOrg(connect.NewRequest(req), token, o.org))
	if err != nil {
		if code := connect.CodeOf(err); strings.HasPrefix(token, "depot_cijob_") && (code == connect.CodePermissionDenied || code == connect.CodeUnauthenticated) {
			return fmt.Errorf("%w\nThis API does not accept the CI job token for launches; pass an organization or user token from a secret with --token", err)
		}
		return err
	}

	sessionID := resp.Msg.GetSession().GetSessionId()
	switch u := resp.Msg.GetUrl(); {
	case o.output == "json" && !o.watch:
		return printJSON(cmd.OutOrStdout(), resp.Msg)
	case o.output == "json":
	case u != "":
		_, err = fmt.Fprintln(cmd.OutOrStdout(), u)
	case o.org == "":
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Launched session %s (pass --org to print its URL)\n", sessionID)
	default:
		_, err = fmt.Fprintln(cmd.OutOrStdout(), sessionURL(o.org, sessionID))
	}
	if err != nil || !o.watch {
		return err
	}
	return watchAndReport(cmd, newCaller(token, o.org), o.output, sessionID, watchOptions{afterInput: resp.Msg.GetInput().GetInputId(), untilIdle: true, timeout: o.timeout, url: resp.Msg.GetUrl(), base: beforeFirst()})
}

func parseSource(value string) (*agentv1.DepotAgentLaunchSource, error) {
	kind, rest, ok := strings.Cut(value, ":")
	repository, ref, hasRef := strings.Cut(rest, "@")
	owner, name, _ := strings.Cut(repository, "/")
	if !ok || kind == "" || owner == "" || name == "" || (hasRef && ref == "") {
		return nil, fmt.Errorf("source %q must be <kind>:<owner/repo>[@ref], for example depot-code:acme/app@main", value)
	}
	source := &agentv1.DepotAgentLaunchSource{Kind: kind, Repository: repository}
	if hasRef {
		source.Ref = &ref
	}
	return source, nil
}
