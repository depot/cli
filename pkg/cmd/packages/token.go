package packages

import (
	"errors"
	"fmt"
	"os"

	"github.com/depot/cli/pkg/config"
	"github.com/spf13/cobra"
)

var errNoToken = errors.New("no Depot token found: run `depot login` or set DEPOT_TOKEN")

func NewCmdToken() *cobra.Command {
	var bearer bool

	cmd := &cobra.Command{
		Use:    "token",
		Short:  "Print the Depot token used by package manager credential helpers",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := resolveToken()
			if err != nil {
				return err
			}
			if bearer {
				token = "Bearer " + token
			}
			fmt.Fprintln(cmd.OutOrStdout(), token)
			return nil
		},
	}

	cmd.Flags().BoolVar(&bearer, "bearer", false, "Print the token as a Bearer authorization value")
	return cmd
}

func resolveToken() (string, error) {
	if token := os.Getenv("DEPOT_TOKEN"); token != "" {
		return token, nil
	}
	if token := config.GetApiToken(); token != "" {
		return token, nil
	}
	return "", errNoToken
}
