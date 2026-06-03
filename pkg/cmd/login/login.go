package login

import (
	"context"
	"errors"
	"fmt"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdLogin() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate the Depot CLI",
		RunE: func(cmd *cobra.Command, args []string) error {
			clear, _ := cmd.Flags().GetBool("clear")
			quiet, _ := cmd.Flags().GetBool("quiet")

			if clear {
				err := config.ClearApiToken()
				if err != nil {
					return err
				}
			}

			existingToken := config.GetApiToken()
			if existingToken != "" {
				// Already authenticated: --quiet turns this into a silent
				// exit-0 no-op so scripts can call `depot login` defensively
				// without producing output. Without --quiet the notice prints
				// exactly as before.
				if !quiet {
					fmt.Fprintln(cmd.OutOrStdout(), "You are already logged in.")
				}
				return nil
			}

			tokenResponse, err := api.AuthorizeDevice(context.TODO())
			if err != nil {
				return err
			}

			err = config.SetApiToken(tokenResponse.Token)
			if err != nil {
				return err
			}

			orgId, _ := cmd.Flags().GetString("org-id")
			if orgId == "" {
				currentOrganization, err := helpers.SelectOrganization()
				if errors.Is(err, helpers.ErrNoOrganizations) {
					fmt.Println("Successfully authenticated!")
					fmt.Println("")
					fmt.Println("No active organizations found. Visit https://depot.dev to create or reactivate an organization.")
					return nil
				}
				if err != nil {
					return err
				}
				orgId = currentOrganization.OrgId
			}

			err = config.SetCurrentOrganization(orgId)
			if err != nil {
				return err
			}

			fmt.Println("Successfully authenticated!")

			return nil
		},
	}

	cmd.Flags().String("org-id", "", "The organization ID to login to")
	cmd.Flags().Bool("clear", false, "Clear any existing token before logging in")
	cmd.Flags().Bool("quiet", false, "Suppress the notice when already authenticated (exit 0 no-op if logged in)")

	cmd.AddCommand(newCmdLoginToken())

	return cmd
}
