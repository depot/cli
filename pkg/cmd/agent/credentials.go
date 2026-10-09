package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
)

func newCmdCredentials() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "credentials",
		Aliases: []string{"credential"},
		Short:   "Manage the named credentials MCP servers sign in with",
		Long: `Manage the named credentials MCP servers sign in with.

An MCP server header such as 'Authorization: Bearer ${secrets.LINEAR_TOKEN}' resolves to the
LINEAR_TOKEN credential of the member who sent the message, then the organization's, then the
CI secret LINEAR_TOKEN. Depot sends a credential only to its hosts, and never to a sandbox.`,
	}
	cmd.AddCommand(newCmdCredentialsList())
	cmd.AddCommand(newCmdCredentialsSet())
	cmd.AddCommand(newCmdCredentialsRemove())
	return cmd
}

func newCmdCredentialsList() *cobra.Command {
	var (
		auth   authFlags
		output string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the organization's credentials, then your own",
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
			resp, err := s.client.ListAgentCredentials(ctx, authed(s, &agentv1.ListAgentCredentialsRequest{}))
			if err != nil {
				return fmt.Errorf("list credentials: %w", err)
			}
			if output == "json" {
				return writeProtoJSON(resp.Msg)
			}
			if err := writeCredentialsTable(os.Stdout, resp.Msg.GetCredentials()); err != nil {
				return fmt.Errorf("write credentials table: %w", err)
			}
			return nil
		},
	}

	auth.register(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}

func newCmdCredentialsSet() *cobra.Command {
	var (
		auth  authFlags
		scope string
		hosts []string
	)

	cmd := &cobra.Command{
		Use:   "set <name> --host <host>",
		Short: "Store a credential, or replace the one of the same name",
		Long: `Store a credential, or replace the one of the same name and scope.

The value is read from stdin, or prompted for without echoing it. Depot never returns it.
The credential is your own, unless an owner passes --scope organization.`,
		Example: `  # Prompt for your own Linear token, sent only to mcp.linear.app
  depot agent credentials set LINEAR_TOKEN --host mcp.linear.app

  # Store the organization's Sentry token from a pipe
  printenv SENTRY_TOKEN | depot agent credentials set SENTRY_TOKEN --host mcp.sentry.dev --scope organization`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateScope(scope); err != nil {
				return err
			}
			if len(hosts) == 0 {
				return errors.New("pass --host with each host the credential may be sent to")
			}
			value, err := readSecret(cmd.InOrStdin(), cmd.ErrOrStderr(), "value")
			if err != nil {
				return err
			}
			if value == "" {
				return errors.New("empty value: remove a credential with depot agent credentials remove")
			}
			req := &agentv1.SetAgentCredentialRequest{Name: args[0], Value: value, Hosts: hosts}
			if scope != "" {
				req.Scope = ptr(scope)
			}
			ctx := cmd.Context()
			s, err := auth.resolve(ctx)
			if err != nil {
				return err
			}
			resp, err := s.client.SetAgentCredential(ctx, authed(s, req))
			if err != nil {
				return fmt.Errorf("set credential %s: %w", args[0], err)
			}
			c := resp.Msg.GetCredential()
			fmt.Printf("Stored %s credential %s for %s\n", safeText(c.GetScope()), safeText(c.GetName()), safeText(strings.Join(c.GetHosts(), ", ")))
			return nil
		},
	}

	auth.register(cmd)
	cmd.Flags().StringArrayVar(&hosts, "host", nil, "Exact host name Depot may send the credential to, without scheme or port (repeatable; required)")
	cmd.Flags().StringVar(&scope, "scope", "", `Whose credential: "user" (default, your own) or "organization" (owners only)`)
	return cmd
}

func newCmdCredentialsRemove() *cobra.Command {
	var (
		auth  authFlags
		scope string
	)

	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a credential",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateScope(scope); err != nil {
				return err
			}
			req := &agentv1.DeleteAgentCredentialRequest{Name: args[0]}
			if scope != "" {
				req.Scope = ptr(scope)
			}
			ctx := cmd.Context()
			s, err := auth.resolve(ctx)
			if err != nil {
				return err
			}
			if _, err := s.client.DeleteAgentCredential(ctx, authed(s, req)); err != nil {
				return fmt.Errorf("remove credential %s: %w", args[0], err)
			}
			fmt.Printf("Removed credential %s\n", safeText(args[0]))
			return nil
		},
	}

	auth.register(cmd)
	cmd.Flags().StringVar(&scope, "scope", "", `Whose credential: "user" (default, your own) or "organization" (owners only)`)
	return cmd
}

func writeCredentialsTable(w io.Writer, creds []*agentv1.DepotAgentCredential) error {
	if len(creds) == 0 {
		_, err := fmt.Fprintln(w, "No credentials found. Store one with: depot agent credentials set <name> --host <host>")
		return err
	}
	tw := tabwriter.NewWriter(safeWriter{w}, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSCOPE\tKIND\tHOSTS\tUPDATED")
	for _, c := range creds {
		updated := "-"
		if c.GetUpdatedAt() != nil {
			updated = c.GetUpdatedAt().AsTime().Local().Format(time.DateTime)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", oneLine(c.GetName()), oneLine(c.GetScope()), oneLine(c.GetKind()), oneLine(strings.Join(c.GetHosts(), ",")), updated)
	}
	return tw.Flush()
}
