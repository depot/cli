package code

import "github.com/spf13/cobra"

func NewCmdCode() *cobra.Command {
	cmd := &cobra.Command{
		Use: "code", Short: "Manage Depot Code",
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	repo := &cobra.Command{
		Use: "repo", Aliases: []string{"repos", "repositories"}, Short: "Manage Git repositories",
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	repo.AddCommand(newCreate(), newView(), newList())
	cmd.AddCommand(repo)
	return cmd
}
