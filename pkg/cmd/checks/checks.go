package checks

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	checksv1 "github.com/depot/cli/pkg/proto/depot/checks/v1"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type options struct {
	org, token, output string
}

func NewCmdChecks() *cobra.Command {
	o := &options{}
	cmd := &cobra.Command{
		Use: "checks", Short: "Manage Agent Checks and review agents",
		Long: "Manage Agent Checks and review agents. Run and retry queue an execution and return its IDs immediately.\nSuccessful reads exit 0 even when the inspected check failed or has blocking findings; command errors exit 1.",
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.PersistentFlags().StringVar(&o.org, "org", "", "Organization ID (defaults to the current organization)")
	cmd.PersistentFlags().StringVar(&o.token, "token", "", "Depot API token (or DEPOT_TOKEN)")
	cmd.PersistentFlags().StringVarP(&o.output, "output", "o", "text", "Output format: text or json (transcript: text or jsonl)")
	cmd.AddCommand(newList(o), newGet(o), newAttempts(o), newRun(o), newRetry(o), newTranscript(o), newResolve(o, true), newResolve(o, false), newAgents(o))
	return cmd
}

func (o *options) authenticate(cmd *cobra.Command) error {
	if o.output != "text" && o.output != "json" && !(cmd.Name() == "transcript" && o.output == "jsonl") {
		return fmt.Errorf("unsupported output format %q", o.output)
	}
	if cmd.Name() == "transcript" && o.output == "json" {
		return fmt.Errorf("use --output jsonl for the original transcript archive")
	}
	if o.org == "" {
		o.org = config.GetCurrentOrganization()
	}
	var err error
	o.token, err = helpers.ResolveOrgAuth(cmd.Context(), o.token)
	if err != nil {
		return err
	}
	if o.token == "" {
		return fmt.Errorf("missing API token; pass --token or DEPOT_TOKEN, or run `depot login`")
	}
	return nil
}

func request[T any](o *options, msg *T) *connect.Request[T] {
	return api.WithAuthenticationAndOrg(connect.NewRequest(msg), o.token, o.org)
}

func (o *options) writeJSON(cmd *cobra.Command, msg proto.Message) error {
	b, err := (protojson.MarshalOptions{Indent: "  ", EmitUnpopulated: true}).Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return err
}

func parseRepo(value string) (*checksv1.RepositorySelector, error) {
	forge, name, ok := strings.Cut(value, ":")
	if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\r\n\t") {
		return nil, fmt.Errorf("repository must be <forge>:<name>, for example github:depot/api")
	}
	switch forge {
	case "github", "cursor-origin", "code":
		return &checksv1.RepositorySelector{Forge: forge, Name: name}, nil
	default:
		return nil, fmt.Errorf("unsupported repository forge %q; use github, cursor-origin, or code", forge)
	}
}

// Only a not-found response permits probing the other identity type.
func selectAttempt(ctx context.Context, o *options, id string) (*checksv1.CheckAttempt, error) {
	client := api.NewCheckClient()
	check, err := client.GetCheck(ctx, request(o, &checksv1.GetCheckRequest{CheckId: id}))
	if err == nil {
		attempt := check.Msg.GetCheck().GetCurrentAttempt()
		if attempt == nil || attempt.Id == "" || attempt.CheckId == "" {
			return nil, fmt.Errorf("check %s has no execution attempt", id)
		}
		return attempt, nil
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		return nil, err
	}
	attempt, err := client.GetAttempt(ctx, request(o, &checksv1.GetAttemptRequest{AttemptId: id}))
	if err != nil {
		return nil, fmt.Errorf("get check or attempt %s: %w", id, err)
	}
	if attempt.Msg.Attempt == nil || attempt.Msg.Attempt.Id == "" || attempt.Msg.Attempt.CheckId == "" {
		return nil, fmt.Errorf("attempt %s has no parent check", id)
	}
	return attempt.Msg.Attempt, nil
}

type pagination struct {
	n     int
	token string
	all   bool
}

func (p *pagination) flags(cmd *cobra.Command) {
	cmd.Flags().IntVarP(&p.n, "n", "n", 50, "Results per page (1-100)")
	cmd.Flags().StringVar(&p.token, "page-token", "", "Continue with this page token and the same filters")
	cmd.Flags().BoolVar(&p.all, "all", false, "Fetch every remaining page")
}

func (p *pagination) validate() error {
	if p.n < 1 || p.n > 100 {
		return fmt.Errorf("-n must be between 1 and 100")
	}
	return nil
}

// Bind each continuation token to this traversal so a broken server cannot loop forever.
func (p *pagination) next(token string, seen map[string]bool) (bool, error) {
	if !p.all || token == "" {
		return false, nil
	}
	if seen[token] {
		return false, fmt.Errorf("API returned a repeated page token")
	}
	seen[token] = true
	p.token = token
	return true, nil
}

func pageHint(cmd *cobra.Command, token string) {
	if token != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "Next page: --page-token %s\n", token)
	}
}
