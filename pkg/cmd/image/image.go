package image

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewCmdImage() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage container images in the registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("missing subcommand, please run `depot image --help`")
		},
	}

	cmd.AddCommand(NewCmdList())
	cmd.AddCommand(NewCmdRM())

	return cmd
}
