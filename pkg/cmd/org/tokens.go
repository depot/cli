package org

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	corev1 "github.com/depot/cli/pkg/proto/depot/core/v1"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type tokenOptions struct {
	organization string
	token        string
	output       string
}

type organizationTokenOutput struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
}

func NewCmdTokens() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Manage organization API tokens",
		Long: `List, create, and revoke organization API tokens.

These commands require a user API token belonging to an organization owner.
Use depot login, DEPOT_TOKEN, or --token. Organization and project tokens cannot
manage tokens, including revoking themselves. Select the organization with
--organization, DEPOT_ORG_ID, or depot org switch.`,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newCmdTokensList(), newCmdTokensCreate(), newCmdTokensRevoke())
	return cmd
}

func (options *tokenOptions) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&options.organization, "organization", "", "Depot organization ID (defaults to the current organization)")
	cmd.Flags().StringVar(&options.token, "token", "", "Organization owner's user API token")
	cmd.Flags().StringVarP(&options.output, "output", "o", "", "Output format (json)")
}

func (options *tokenOptions) resolve() error {
	if options.output != "" && options.output != "json" {
		return fmt.Errorf("unsupported output %q (valid: json)", options.output)
	}
	if options.organization == "" {
		options.organization = config.GetCurrentOrganization()
	}
	if options.organization == "" {
		return fmt.Errorf("missing organization ID; use --organization, DEPOT_ORG_ID, or `depot org switch`")
	}
	if options.token == "" {
		options.token = os.Getenv("DEPOT_TOKEN")
	}
	if options.token == "" {
		options.token = config.GetApiToken()
	}
	if options.token == "" {
		return fmt.Errorf("missing user API token; run `depot login` as an organization owner, or set DEPOT_TOKEN or --token")
	}
	return nil
}

func newCmdTokensList() *cobra.Command {
	var options tokenOptions
	var page, pageSize int32
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List organization API token metadata",
		Long: `List one page of active organization API tokens without revealing secrets.
Use --page to retrieve subsequent pages. JSON output includes has_more.`,
		Example: "  depot org tokens list --organization <org-id> --output json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if page < 1 || pageSize < 1 || pageSize > 100 {
				return fmt.Errorf("--page must be at least 1 and --page-size must be between 1 and 100")
			}
			if err := options.resolve(); err != nil {
				return err
			}
			response, err := api.NewOrganizationTokensClient().ListTokens(cmd.Context(), api.WithAuthenticationAndOrg(
				connect.NewRequest(&corev1.ListOrganizationTokensRequest{Page: page, PageSize: pageSize}), options.token, options.organization,
			))
			if err != nil {
				return organizationTokenError("list", err)
			}
			tokens := make([]organizationTokenOutput, 0, len(response.Msg.GetTokens()))
			for _, token := range response.Msg.GetTokens() {
				tokens = append(tokens, tokenMetadata(token))
			}
			if options.output == "json" {
				return writeTokenJSON(cmd.OutOrStdout(), struct {
					Tokens  []organizationTokenOutput `json:"tokens"`
					HasMore bool                      `json:"has_more"`
				}{Tokens: tokens, HasMore: response.Msg.GetHasMore()})
			}
			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(table, "ID\tDESCRIPTION\tCREATED\tLAST USED")
			for _, token := range tokens {
				lastUsed := token.LastUsedAt
				if lastUsed == "" {
					lastUsed = "Never"
				}
				fmt.Fprintf(table, "%s\t%q\t%s\t%s\n", token.ID, token.Description, token.CreatedAt, lastUsed)
			}
			if err := table.Flush(); err != nil {
				return err
			}
			if response.Msg.GetHasMore() {
				fmt.Fprintf(cmd.ErrOrStderr(), "More tokens are available; use --page %d.\n", int64(page)+1)
			}
			return nil
		},
	}
	options.addFlags(cmd)
	cmd.Flags().Int32Var(&page, "page", 1, "Page number")
	cmd.Flags().Int32Var(&pageSize, "page-size", 100, "Number of tokens per page (1-100)")
	return cmd
}

func newCmdTokensCreate() *cobra.Command {
	var options tokenOptions
	var description string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an organization API token and reveal its secret once",
		Long: `Create an organization API token with a description of 1-191 characters.

The secret is returned only by this command and cannot be retrieved later.
Store it securely. Tokens do not expire automatically. To rotate a token, create
a replacement, update its consumers, and revoke the old token by ID.`,
		Example: "  depot org tokens create --organization <org-id> --description 'CI builds' --output json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			description = strings.TrimSpace(description)
			if description == "" || !utf8.ValidString(description) || utf8.RuneCountInString(description) > 191 {
				return fmt.Errorf("--description must contain 1-191 characters after trimming whitespace")
			}
			if err := options.resolve(); err != nil {
				return err
			}
			response, err := api.NewOrganizationTokensClient().CreateToken(cmd.Context(), api.WithAuthenticationAndOrg(
				connect.NewRequest(&corev1.CreateOrganizationTokenRequest{Description: description}), options.token, options.organization,
			))
			if err != nil {
				return organizationTokenError("create", err)
			}
			if response.Msg.GetToken() == nil || response.Msg.GetSecret() == "" {
				return fmt.Errorf("API returned an incomplete token response; list tokens before retrying creation")
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "Save this secret securely; it cannot be retrieved again.")
			metadata := tokenMetadata(response.Msg.GetToken())
			if options.output == "json" {
				return writeTokenJSON(cmd.OutOrStdout(), struct {
					Token  organizationTokenOutput `json:"token"`
					Secret string                  `json:"secret"`
				}{Token: metadata, Secret: response.Msg.GetSecret()})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\nDescription: %q\nCreated: %s\nSecret: %s\n",
				metadata.ID, metadata.Description, metadata.CreatedAt, response.Msg.GetSecret())
			return err
		},
	}
	options.addFlags(cmd)
	cmd.Flags().StringVar(&description, "description", "", "Token description (required, 1-191 characters)")
	return cmd
}

func newCmdTokensRevoke() *cobra.Command {
	var options tokenOptions
	cmd := &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke an organization API token by ID",
		Long: `Revoke a token by its exact ID, as returned by list or create.
Existing authentication caches can continue to accept the token until their
entries expire. Previously issued credentials retain their own lifetimes.
Repeating this command is safe after a successful API revocation. Unknown token
IDs return not found.`,
		Example: "  depot org tokens revoke <token-id> --organization <org-id>",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("token ID must not be empty")
			}
			if err := options.resolve(); err != nil {
				return err
			}
			_, err := api.NewOrganizationTokensClient().RevokeToken(cmd.Context(), api.WithAuthenticationAndOrg(
				connect.NewRequest(&corev1.RevokeOrganizationTokenRequest{Id: args[0]}), options.token, options.organization,
			))
			if err != nil {
				return organizationTokenError("revoke", err)
			}
			if options.output == "json" {
				return writeTokenJSON(cmd.OutOrStdout(), struct{}{})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Token revoked.")
			return err
		},
	}
	options.addFlags(cmd)
	return cmd
}

func tokenMetadata(token *corev1.OrganizationToken) organizationTokenOutput {
	return organizationTokenOutput{
		ID:          token.GetId(),
		Description: token.GetDescription(),
		CreatedAt:   tokenTimestamp(token.GetCreatedAt()),
		LastUsedAt:  tokenTimestamp(token.GetLastUsedAt()),
	}
}

func tokenTimestamp(timestamp *timestamppb.Timestamp) string {
	if timestamp == nil {
		return ""
	}
	return timestamp.AsTime().UTC().Format(time.RFC3339Nano)
}

func writeTokenJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func organizationTokenError(operation string, err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return fmt.Errorf("cannot %s organization tokens: use a user API token belonging to an owner of the selected organization (run `depot login` or set DEPOT_TOKEN or --token): %w", operation, err)
	case connect.CodeUnimplemented:
		return fmt.Errorf("organization token management is not available on this API server: %w", err)
	default:
		return fmt.Errorf("failed to %s organization token: %w", operation, err)
	}
}
