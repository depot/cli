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
	return &cobra.Command{
		Use:    "token",
		Short:  "Print the Depot token used by package manager credential helpers",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := resolveToken()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), token)
			return nil
		},
	}
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
