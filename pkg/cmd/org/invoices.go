package org

import (
	"context"
	"fmt"
	"time"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// NewCmdInvoices exposes the finalized invoice ledger without downloading or
// opening the confidential document links returned by the API.
func NewCmdInvoices() *cobra.Command {
	var token, orgID, output string
	cmd := &cobra.Command{
		Use:   "invoices",
		Short: "Read finalized organization invoices as JSON",
		Long: `Read finalized invoices for the organization's linked Stripe customer,
including previous/canceled subscriptions and standalone invoices.

Supply an organization API token with --token, DEPOT_TOKEN, or CLI configuration.
An organization owner's user token is also supported with --org. The organization
selected by org switch is not used. These commands do not prompt for login.

Each list command reads one page. Continue with --page-token until nextPageToken
is absent, even when a page is empty. JSON preserves integer amounts as strings
in currency minor units. Hosted/PDF invoice links are confidential.`,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.PersistentFlags().StringVar(&token, "token", "", "Depot organization or organization-owner API token")
	cmd.PersistentFlags().StringVar(&orgID, "org", "", "Organization ID for an owner's user token")
	cmd.PersistentFlags().StringVarP(&output, "output", "o", "json", "Output format (json)")
	validate := func(_ *cobra.Command, _ []string) error {
		if output != "json" {
			return fmt.Errorf("--output must be json")
		}
		if helpers.ResolveStaticOrgAuth(token) == "" {
			return fmt.Errorf("missing API token: set DEPOT_TOKEN or pass --token")
		}
		return nil
	}

	var invoicePageSize, linePageSize uint32
	var invoicePageToken, linePageToken string
	list := &cobra.Command{
		Use: "list", Short: "List one page of finalized invoices", Args: cobra.NoArgs,
		Example: "  depot org invoices list --page-size 100\n  depot org invoices list --page-token '<nextPageToken>'",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateInvoicePageSize(invoicePageSize); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			resp, err := api.NewBillingClient().ListInvoices(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(&corev1.ListInvoicesRequest{
				PageSize: invoicePageSize, PageToken: invoicePageToken,
			}), helpers.ResolveStaticOrgAuth(token), orgID))
			if err != nil {
				return fmt.Errorf("failed to list invoices: %w", err)
			}
			return writeInvoiceJSON(cmd, resp.Msg)
		},
	}
	list.Flags().Uint32Var(&invoicePageSize, "page-size", 50, "Invoices per page (1-100)")
	list.Flags().StringVar(&invoicePageToken, "page-token", "", "Opaque nextPageToken from the previous invoice page")
	get := &cobra.Command{
		Use: "get <invoice-id>", Short: "Get a finalized invoice", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			resp, err := api.NewBillingClient().GetInvoice(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(&corev1.GetInvoiceRequest{
				InvoiceId: args[0],
			}), helpers.ResolveStaticOrgAuth(token), orgID))
			if err != nil {
				return fmt.Errorf("failed to get invoice: %w", err)
			}
			if resp.Msg.Invoice == nil {
				return fmt.Errorf("invalid invoice response: missing invoice")
			}
			return writeInvoiceJSON(cmd, resp.Msg)
		},
	}
	lines := &cobra.Command{
		Use: "line-items <invoice-id>", Aliases: []string{"lines"}, Short: "List one page of invoice line items", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateInvoicePageSize(linePageSize); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			resp, err := api.NewBillingClient().ListInvoiceLineItems(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(&corev1.ListInvoiceLineItemsRequest{
				InvoiceId: args[0], PageSize: linePageSize, PageToken: linePageToken,
			}), helpers.ResolveStaticOrgAuth(token), orgID))
			if err != nil {
				return fmt.Errorf("failed to list invoice line items: %w", err)
			}
			return writeInvoiceJSON(cmd, resp.Msg)
		},
	}
	lines.Flags().Uint32Var(&linePageSize, "page-size", 50, "Line items per page (1-100)")
	lines.Flags().StringVar(&linePageToken, "page-token", "", "Opaque nextPageToken from the previous line-item page")
	list.PreRunE = validate
	get.PreRunE = validate
	lines.PreRunE = validate
	cmd.AddCommand(list, get, lines)
	return cmd
}

func validateInvoicePageSize(size uint32) error {
	if size < 1 || size > 100 {
		return fmt.Errorf("--page-size must be between 1 and 100")
	}
	return nil
}

func writeInvoiceJSON(cmd *cobra.Command, msg proto.Message) error {
	data, err := (protojson.MarshalOptions{Indent: "  "}).Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", data)
	return err
}
