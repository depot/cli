package org

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
	"golang.org/x/text/currency"
	"google.golang.org/protobuf/encoding/protojson"
)

func NewCmdCredits() *cobra.Command {
	var token, output string
	cmd := &cobra.Command{
		Use:   "credits",
		Short: "Show remaining organization invoice credits",
		Long: `Read the organization's posted Stripe invoice-credit balance.
Unbilled usage is not deducted; free-trial and included plan allowances are excluded.

Requires an organization API token or a read-only organization token, supplied
with --token, DEPOT_TOKEN, or the CLI configuration. The token determines the
organization; the current organization selected by org switch is not used.
This command does not prompt for login.

Reads are limited to once per organization per minute and share an API-wide
polling limit. Poll hourly for most monitoring needs. Errors are returned
without retrying or reporting a zero balance.`,
		Example: `  # Show remaining credits using an organization token in DEPOT_TOKEN
  depot org credits

  # Read the exact API response for a monitoring script
  depot org credits --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "" && output != "json" {
				return fmt.Errorf("--output must be json")
			}
			resolvedToken := helpers.ResolveStaticOrgAuth(token)
			if resolvedToken == "" {
				return fmt.Errorf("missing organization API token: set DEPOT_TOKEN or pass --token (read-only organization tokens are supported)")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			resp, err := api.NewCreditClient().GetCreditBalance(ctx,
				api.WithAuthentication(connect.NewRequest(&corev1.GetCreditBalanceRequest{}), resolvedToken))
			if err != nil {
				var rpcErr *connect.Error
				if errors.As(err, &rpcErr) && rpcErr.Code() == connect.CodeResourceExhausted {
					if seconds, parseErr := strconv.Atoi(rpcErr.Meta().Get("Retry-After")); parseErr == nil && seconds > 0 {
						return fmt.Errorf("credit balance polling limit exceeded; retry after %d seconds: %w", seconds, err)
					}
				}
				return fmt.Errorf("failed to read credit balance: %w", err)
			}

			balance := resp.Msg
			if balance.RemainingCreditMinor == nil || balance.GetRemainingCreditMinor() < 0 {
				return fmt.Errorf("invalid credit balance response: missing or negative amount")
			}
			unit, err := currency.ParseISO(balance.Currency)
			if err != nil || unit == currency.XXX {
				return fmt.Errorf("invalid credit balance response: unknown currency")
			}
			if balance.ObservedAt == nil || balance.ObservedAt.CheckValid() != nil {
				return fmt.Errorf("invalid credit balance response: missing or invalid observation time")
			}
			if output == "json" {
				data, err := (protojson.MarshalOptions{Indent: "  "}).Marshal(balance)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", data)
				return err
			}

			// Keep the calculation in integers so even large balances remain exact.
			minor := balance.GetRemainingCreditMinor()
			scale, _ := currency.Standard.Rounding(unit)
			divisor := int64(1)
			for range scale {
				divisor *= 10
			}
			amount := strconv.FormatInt(minor/divisor, 10)
			if scale > 0 {
				amount += fmt.Sprintf(".%0*d", scale, minor%divisor)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Remaining credits: %s %s\nObserved at: %s\n",
				unit, amount, balance.ObservedAt.AsTime().UTC().Format(time.RFC3339Nano))
			return err
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "Depot organization API token (read-only tokens supported)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}
