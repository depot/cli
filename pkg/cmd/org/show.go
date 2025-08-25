package org

import (
	"fmt"
	"os"

	"github.com/depot/cli/pkg/config"
	"github.com/spf13/cobra"
)

func NewCmdShow() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the current organization",
		Run: func(cmd *cobra.Command, args []string) {
			org := config.GetCurrentOrganization()
			if org == "" {
				fmt.Fprintln(os.Stderr, "No organization selected")
				os.Exit(1)
			}

			fmt.Println(org)
		},
	}

	return cmd
}
