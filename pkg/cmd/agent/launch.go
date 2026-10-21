package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

type launchOptions struct {
	org, token, output, message, clientRequestID string
	sources                                      []string
}

func newLaunch() *cobra.Command {
	o := &launchOptions{}
	cmd := &cobra.Command{
		Use:   "launch <agent[@ref]> --source <kind>:<owner/repo>[@ref] --message <text>",
		Short: "Start a session of a published agent",
		Long: `Start a session of a published agent on one or more repositories, and print the session URL.

In a CI job, the run ID is the default --client-request-id, so a rerun returns the session the first attempt started.`,
		Example: `  depot agent launch public-agent-code-review --source github:acme/app@refs/pull/12/head --message "Review this pull request"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLaunch(cmd, o, args[0])
		},
	}
	cmd.Flags().StringArrayVar(&o.sources, "source", nil, "Repository the agent works on, as <kind>:<owner/repo>[@ref] (repeatable)")
	cmd.Flags().StringVar(&o.message, "message", "", "First message of the session")
	cmd.Flags().StringVar(&o.clientRequestID, "client-request-id", "", "Key that makes the launch safe to retry (defaults to the CI run ID in CI)")
	cmd.Flags().StringVar(&o.org, "org", "", "Organization ID (defaults to DEPOT_ORG_ID or the current organization)")
	cmd.Flags().StringVar(&o.token, "token", "", "Depot API token (or DEPOT_TOKEN)")
	cmd.Flags().StringVarP(&o.output, "output", "o", "text", "Output format: text or json")
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
	} else if key := ciRequestID(agent, o.sources, o.message); key != "" {
		req.ClientRequestId = &key
	}

	if o.org == "" {
		o.org = os.Getenv("DEPOT_ORG_ID")
	}
	if o.org == "" {
		o.org = config.GetCurrentOrganization()
	}
	token, err := helpers.ResolveOrgAuth(cmd.Context(), o.token)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token; pass --token or DEPOT_TOKEN, or run `depot login`")
	}

	resp, err := api.NewDepotAgentLaunchClient().Launch(cmd.Context(), api.WithAuthenticationAndOrg(connect.NewRequest(req), token, o.org))
	if err != nil {
		if strings.HasPrefix(token, "depot_cijob_") && connect.CodeOf(err) == connect.CodePermissionDenied {
			return fmt.Errorf("%w\nThe CI job token cannot launch agents; pass an organization or user token from a secret with --token", err)
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
	sessionID := resp.Msg.GetSession().GetSessionId()
	if o.org == "" {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Launched session %s (pass --org to print its URL)\n", sessionID)
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "https://depot.dev/orgs/%s/agent-runs/sessions/%s\n", url.PathEscape(o.org), url.PathEscape(sessionID))
	return err
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

// The key covers what is launched, so two different launches in one job do not replay each other.
func ciRequestID(agent string, sources []string, message string) string {
	runID := os.Getenv("GITHUB_RUN_ID")
	if runID == "" {
		return ""
	}
	parts := append([]string{os.Getenv("GITHUB_REPOSITORY"), runID, os.Getenv("GITHUB_JOB"), agent, message}, sources...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "ci:" + hex.EncodeToString(sum[:])
}
