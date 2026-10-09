package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const maxSecretBytes = 64 << 10

func newCmdMcp() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage the remote MCP servers Depot agents can call",
	}
	cmd.AddCommand(newCmdMcpAdd())
	cmd.AddCommand(newCmdMcpList())
	cmd.AddCommand(newCmdMcpRemove())
	cmd.AddCommand(newCmdMcpToken())
	cmd.AddCommand(newCmdMcpLogin())
	return cmd
}

func newCmdMcpAdd() *cobra.Command {
	var (
		auth         authFlags
		scope        string
		headers      []string
		clientID     string
		clientSecret string
		output       string
	)

	cmd := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Add a remote MCP server",
		Long: `Add a remote MCP server whose tools Depot agents can call.

An organization server, the default, serves every message sent in the organization's sessions;
only an owner can add one. A user server (--scope user) serves only the messages you send.

Depot calls the server for the agent, so its credentials never reach a sandbox.
A header can reference a credential as ${secrets.NAME}: the sender's own, then the organization's
(see "depot agent credentials"), then the CI secret NAME. Without an Authorization header,
log in with "depot agent mcp login <name>" or store a token with "depot agent mcp token <name>".`,
		Example: `  # Add a server for your own messages, then log in to it
  depot agent mcp add linear https://mcp.linear.app/mcp --scope user
  depot agent mcp login linear

  # Add a server for the whole organization; each member connects their own SENTRY_TOKEN
  depot agent mcp add sentry https://mcp.sentry.dev/mcp --header 'Authorization: Bearer ${secrets.SENTRY_TOKEN}'`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			if err := validateScope(scope); err != nil {
				return err
			}
			req := &agentv1.CreateMcpServerRequest{Name: args[0], Url: args[1]}
			if scope != "" {
				req.Scope = ptr(scope)
			}
			if len(headers) > 0 {
				req.Headers = map[string]string{}
				for _, h := range headers {
					name, value, ok := strings.Cut(h, ":")
					if !ok {
						return fmt.Errorf("header %q must look like \"Name: value\"", h)
					}
					req.Headers[strings.TrimSpace(name)] = strings.TrimSpace(value)
				}
			}
			if clientSecret != "" && clientID == "" {
				return errors.New("--oauth-client-secret needs --oauth-client-id")
			}
			if clientID != "" {
				req.Oauth = &agentv1.McpOAuthClient{ClientId: clientID}
				if clientSecret != "" {
					req.Oauth.ClientSecret = ptr(clientSecret)
				}
			}

			ctx := cmd.Context()
			s, err := auth.resolve(ctx)
			if err != nil {
				return err
			}
			resp, err := s.client.CreateMcpServer(ctx, authed(s, req))
			if err != nil {
				if scope == "" && connect.CodeOf(err) == connect.CodePermissionDenied {
					return fmt.Errorf("add MCP server %s: %w (only an owner can add an organization server; pass --scope user to add it for yourself)", args[0], err)
				}
				return fmt.Errorf("add MCP server %s: %w", args[0], err)
			}
			if output == "json" {
				return writeProtoJSON(resp.Msg)
			}
			srv := resp.Msg.GetMcpServer()
			fmt.Printf("Added %s MCP server %s (%s)\n", safeText(srv.GetScope()), safeText(srv.GetName()), safeText(srv.GetMcpServerId()))
			if srv.GetAuth() == "none" {
				fmt.Fprintf(os.Stderr, "If it needs a login: depot agent mcp login %s\n", safeText(srv.GetName()))
			}
			return nil
		},
	}

	auth.register(cmd)
	cmd.Flags().StringVar(&scope, "scope", "", `Whose messages the server serves: "organization" (default, owners only) or "user"`)
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "Header to send, as \"Name: value\", whose value references a credential as ${secrets.NAME} (repeatable)")
	cmd.Flags().StringVar(&clientID, "oauth-client-id", "", "OAuth client ID, for a server that does not register clients itself")
	cmd.Flags().StringVar(&clientSecret, "oauth-client-secret", "", "OAuth client secret, as a CI secret reference such as ${secrets.NAME}, bound to the token endpoint's host")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}

func newCmdMcpList() *cobra.Command {
	var (
		auth   authFlags
		output string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the organization's MCP servers, then your own",
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
			resp, err := s.client.ListMcpServers(ctx, authed(s, &agentv1.ListMcpServersRequest{}))
			if err != nil {
				return fmt.Errorf("list MCP servers: %w", err)
			}
			if output == "json" {
				return writeProtoJSON(resp.Msg)
			}
			if err := writeMcpTable(os.Stdout, resp.Msg.GetMcpServers()); err != nil {
				return fmt.Errorf("write MCP server table: %w", err)
			}
			return nil
		},
	}

	auth.register(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}

func newCmdMcpRemove() *cobra.Command {
	var (
		auth  authFlags
		scope string
	)

	cmd := &cobra.Command{
		Use:     "remove <server>",
		Aliases: []string{"rm"},
		Short:   "Remove an MCP server, by name or ID",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, srv, err := resolveMcpServer(ctx, &auth, args[0], scope)
			if err != nil {
				return err
			}
			if _, err := s.client.DeleteMcpServer(ctx, authed(s, &agentv1.DeleteMcpServerRequest{McpServerId: srv.GetMcpServerId()})); err != nil {
				return fmt.Errorf("remove MCP server %s: %w", args[0], err)
			}
			fmt.Printf("Removed %s MCP server %s\n", safeText(srv.GetScope()), safeText(srv.GetName()))
			return nil
		},
	}

	auth.register(cmd)
	registerScopeFilter(cmd, &scope)
	return cmd
}

func newCmdMcpToken() *cobra.Command {
	var (
		auth       authFlags
		scope      string
		clearToken bool
	)

	cmd := &cobra.Command{
		Use:   "token <server>",
		Short: "Store the token Depot sends to an MCP server",
		Long: `Store the token Depot sends to an MCP server as "Authorization: Bearer".

The token is read from stdin, or prompted for without echoing it. Depot never returns it.
The token is your own, unless an owner passes --scope organization for an organization server.
It replaces an OAuth login stored at the same scope.`,
		Example: `  # Prompt for the token
  depot agent mcp token linear

  # Read the token from a pipe
  printenv LINEAR_TOKEN | depot agent mcp token linear

  # Remove the stored token
  depot agent mcp token linear --clear`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateScope(scope); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, srv, err := resolveMcpServer(ctx, &auth, args[0], credentialServerFilter(scope))
			if err != nil {
				return err
			}
			token := ""
			if !clearToken {
				if token, err = readSecret(cmd.InOrStdin(), cmd.ErrOrStderr(), "token"); err != nil {
					return err
				}
				if token == "" {
					return errors.New("empty token: pass --clear to remove the stored one")
				}
			}
			req := &agentv1.SetMcpServerTokenRequest{McpServerId: srv.GetMcpServerId(), Token: token}
			if scope != "" {
				req.Scope = ptr(scope)
			}
			resp, err := s.client.SetMcpServerToken(ctx, authed(s, req))
			if err != nil {
				return fmt.Errorf("set token for MCP server %s: %w", args[0], err)
			}
			name := safeText(srv.GetName())
			if clearToken {
				fmt.Printf("Cleared the token for %s; it now authenticates with: %s\n", name, safeText(resp.Msg.GetMcpServer().GetAuth()))
			} else {
				fmt.Printf("Stored a token for %s\n", name)
			}
			return nil
		},
	}

	auth.register(cmd)
	registerCredentialScope(cmd, &scope)
	cmd.Flags().BoolVar(&clearToken, "clear", false, "Remove the stored token")
	return cmd
}

func newCmdMcpLogin() *cobra.Command {
	var (
		auth      authFlags
		scope     string
		noBrowser bool
	)

	cmd := &cobra.Command{
		Use:   "login <server>",
		Short: "Log in to an MCP server with OAuth",
		Long: `Log in to an MCP server with OAuth, in your browser.

The login is your own, unless an owner passes --scope organization for an organization server.
It finishes in the browser, signed in to Depot, within 10 minutes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateScope(scope); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, srv, err := resolveMcpServer(ctx, &auth, args[0], credentialServerFilter(scope))
			if err != nil {
				return err
			}
			req := &agentv1.StartMcpLoginRequest{McpServerId: srv.GetMcpServerId()}
			if scope != "" {
				req.Scope = ptr(scope)
			}
			resp, err := s.client.StartMcpLogin(ctx, authed(s, req))
			if err != nil {
				return fmt.Errorf("start login to MCP server %s: %w", args[0], err)
			}
			loginURL := resp.Msg.GetAuthorizationUrl()
			if u, err := url.Parse(loginURL); err != nil || u.Scheme != "https" {
				return fmt.Errorf("login URL for MCP server %s is not https: %q", args[0], loginURL)
			}
			name := safeText(srv.GetName())
			fmt.Printf("Open this URL to log in to %s:\n\n    %s\n\n", name, safeText(loginURL))
			if !noBrowser && helpers.IsTerminal() {
				if err := api.OpenURL(loginURL); err != nil {
					fmt.Fprintf(os.Stderr, "Could not open a browser: %s\n", safeText(err.Error()))
				}
			}
			fmt.Printf("Finish logging in in your browser; then \"depot agent mcp list\" shows %s with auth oauth.\n", name)
			return nil
		},
	}

	auth.register(cmd)
	registerCredentialScope(cmd, &scope)
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the login URL without opening a browser")
	return cmd
}

func validateScope(scope string) error {
	if scope == "" || scope == "organization" || scope == "user" {
		return nil
	}
	return fmt.Errorf("unsupported scope %q (valid: organization, user)", scope)
}

func registerScopeFilter(cmd *cobra.Command, scope *string) {
	cmd.Flags().StringVar(scope, "scope", "", `Pick the "organization" or "user" server when both have the name`)
}

func registerCredentialScope(cmd *cobra.Command, scope *string) {
	cmd.Flags().StringVar(scope, "scope", "", `Whose credential: "user" (default, your own) or "organization" (owners, organization servers only)`)
}

func credentialServerFilter(scope string) string {
	if scope == "organization" {
		return scope
	}
	return ""
}

// resolveMcpServer accepts a name as well as an ID, since the name is what users know a server by.
func resolveMcpServer(ctx context.Context, auth *authFlags, ref, scope string) (*session, *agentv1.DepotAgentMcpServer, error) {
	if err := validateScope(scope); err != nil {
		return nil, nil, err
	}
	s, err := auth.resolve(ctx)
	if err != nil {
		return nil, nil, err
	}
	resp, err := s.client.ListMcpServers(ctx, authed(s, &agentv1.ListMcpServersRequest{}))
	if err != nil {
		return nil, nil, fmt.Errorf("list MCP servers: %w", err)
	}
	srv, err := findMcpServer(resp.Msg.GetMcpServers(), ref, scope)
	if err != nil {
		return nil, nil, err
	}
	return s, srv, nil
}

func findMcpServer(servers []*agentv1.DepotAgentMcpServer, ref, scope string) (*agentv1.DepotAgentMcpServer, error) {
	var named []*agentv1.DepotAgentMcpServer
	for _, srv := range servers {
		if srv.GetMcpServerId() == ref {
			return srv, nil
		}
		if srv.GetName() == ref && (scope == "" || srv.GetScope() == scope) {
			named = append(named, srv)
		}
	}
	switch len(named) {
	case 0:
		return nil, fmt.Errorf("no MCP server %q: see depot agent mcp list", ref)
	case 1:
		return named[0], nil
	}
	return nil, fmt.Errorf("an organization and a user MCP server are both named %q: pass its ID from depot agent mcp list", ref)
}

// readSecret reads a piped secret, or prompts on a terminal without echoing it, so it never lands in argv or shell history.
func readSecret(in io.Reader, prompt io.Writer, what string) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(prompt, "%s: ", strings.ToUpper(what[:1])+what[1:])
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", what, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	b, err := io.ReadAll(io.LimitReader(in, maxSecretBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s from stdin: %w", what, err)
	}
	if len(b) > maxSecretBytes {
		return "", fmt.Errorf("%s is over %d bytes", what, maxSecretBytes)
	}
	return strings.TrimSpace(string(b)), nil
}

func writeMcpTable(w io.Writer, servers []*agentv1.DepotAgentMcpServer) error {
	if len(servers) == 0 {
		_, err := fmt.Fprintln(w, "No MCP servers found. Add one with: depot agent mcp add <name> <url>")
		return err
	}
	tw := tabwriter.NewWriter(safeWriter{w}, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSCOPE\tAUTH\tCREDENTIAL\tURL\tID")
	for _, srv := range servers {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", oneLine(srv.GetName()), oneLine(srv.GetScope()), oneLine(srv.GetAuth()), credentialCell(srv), oneLine(srv.GetUrl()), oneLine(srv.GetMcpServerId()))
	}
	return tw.Flush()
}

func credentialCell(srv *agentv1.DepotAgentMcpServer) string {
	switch {
	case srv.GetCredentialName() == "":
		return "-"
	case srv.GetCredentialScope() == "":
		return oneLine(srv.GetCredentialName()) + " (not connected)"
	}
	return fmt.Sprintf("%s (%s)", oneLine(srv.GetCredentialName()), oneLine(srv.GetCredentialScope()))
}
