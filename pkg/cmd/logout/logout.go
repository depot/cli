package logout

import (
	"fmt"

	"github.com/depot/cli/pkg/config"
	"github.com/spf13/cobra"
)

func NewCmdLogout() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Clear authentication token",
		RunE: func(cmd *cobra.Command, args []string) error {
			err := config.ClearApiToken()
			if err != nil {
				return err
			}

			fmt.Println("Logout successful!")

			return nil
		},
	}

	return cmd
}
