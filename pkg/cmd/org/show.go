package org

import (
	"fmt"

	"github.com/depot/cli/pkg/config"
	"github.com/spf13/cobra"
)

func NewCmdShow() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the current organization",
		Run: func(cmd *cobra.Command, args []string) {
			org := config.GetCurrentOrganization()
			fmt.Println(org)
		},
	}

	return cmd
}
