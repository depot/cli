package org

import (
	"fmt"

	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdList() *cobra.Command {
	var token string

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all organizations",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token, err := helpers.ResolveToken(ctx, token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			// TODO: Implement when organization API is available
			return fmt.Errorf("organization API not yet implemented")
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&token, "token", "", "Depot token")

	return cmd
}