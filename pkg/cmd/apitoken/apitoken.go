package apitoken

import (
	"fmt"

	"github.com/depot/cli/pkg/config"
	"github.com/spf13/cobra"
)

func NewCmdApiToken() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api-token",
		Short: "Get the API token for a Depot login",
		RunE: func(cmd *cobra.Command, args []string) error {
			token := config.GetApiToken()
			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			fmt.Println(token)
			return nil
		},
	}
	return cmd
}
