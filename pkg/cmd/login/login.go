package login

import (
	"context"
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

			if clear {
				err := config.ClearApiToken()
				if err != nil {
					return err
				}
			}

			existingToken := config.GetApiToken()
			if existingToken != "" {
				fmt.Println("You are already logged in.")
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

	return cmd
}
