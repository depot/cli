package agent

import (
	"context"
	"fmt"
	"os"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/proto/depot/agent/v1/agentv1connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// newClient is swapped in tests to point at a local handler.
var newClient = func() agentv1connect.DepotAgentServiceClient {
	return api.NewDepotAgentClient()
}

func NewCmdAgent() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run Depot coding agents",
	}
	cmd.AddCommand(newCmdSession())
	return cmd
}

func newCmdSession() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Create, message, and watch Depot agent sessions",
	}
	cmd.AddCommand(newCmdSessionCreate())
	cmd.AddCommand(newCmdSessionSend())
	cmd.AddCommand(newCmdSessionInterrupt())
	cmd.AddCommand(newCmdSessionList())
	cmd.AddCommand(newCmdSessionWatch())
	cmd.AddCommand(newCmdSessionAttach())
	return cmd
}

type authFlags struct {
	orgID string
	token string
}

func (f *authFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&f.token, "token", "", "Depot API token")
}

// session carries the resolved credentials for one command invocation.
type session struct {
	client agentv1connect.DepotAgentServiceClient
	token  string
	orgID  string
}

func (f *authFlags) resolve(ctx context.Context) (*session, error) {
	orgID := f.orgID
	if orgID == "" {
		orgID = config.GetCurrentOrganization()
	}
	token, err := helpers.ResolveOrgAuth(ctx, f.token)
	if err != nil {
		return nil, fmt.Errorf("resolve API token: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("missing API token, please run `depot login`")
	}
	return &session{client: newClient(), token: token, orgID: orgID}, nil
}

func authed[T any](s *session, msg *T) *connect.Request[T] {
	return api.WithAuthenticationAndOrg(connect.NewRequest(msg), s.token, s.orgID)
}

func validateOutput(output string) error {
	if output == "" || output == "text" || output == "json" {
		return nil
	}
	return fmt.Errorf("unsupported output %q (valid: text, json)", output)
}

func writeProtoJSON(msg proto.Message) error {
	out, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	_, err = fmt.Fprintln(os.Stdout, string(out))
	return err
}
