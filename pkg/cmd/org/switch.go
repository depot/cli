package org

import (
	"fmt"

	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
)

func NewCmdSwitch() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "switch [org-id]",
		Short: "Set the current organization in global Depot settings",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var orgId string

			if len(args) > 0 {
				orgId = args[0]
			} else {
				org, err := helpers.SelectOrganization()
				if err != nil {
					return err
				}
				orgId = org.OrgId
			}

			err := config.SetCurrentOrganization(orgId)
			if err != nil {
				return err
			}

			fmt.Println("Current organization updated.")

			return nil
		},
	}

	return cmd
}
