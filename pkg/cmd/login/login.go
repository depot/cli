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

			currentOrganization, err := helpers.SelectOrganization()
			if err != nil {
				return err
			}

			fmt.Println("Successfully authenticated!")

			err = config.SetApiToken(tokenResponse.Token)
			if err != nil {
				return err
			}

			err = config.SetCurrentOrganization(currentOrganization.OrgId)
			if err != nil {
				return err
			}

			return nil
		},
	}

	cmd.Flags().Bool("clear", false, "Clear any existing token before logging in")

	return cmd
}
