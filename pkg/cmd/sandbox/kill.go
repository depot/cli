package sandbox

import (
	"fmt"
	"os"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

func newSandboxKill() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kill [<sandbox-id>...]",
		Short: "Terminate one or more sandboxes",
		Long: `Terminate sandboxes by id.

With no arguments, walks up from the cwd for a sandbox.depot.yml and
kills the sandbox last started under that spec's name (per
~/.depot/sandbox-state/<name>.id). Useful inside a demo dir where you
just want to undo the last "up".`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			token, orgID, err := resolveAuthAndOrg(ctx, cmd)
			if err != nil {
				return err
			}

			ids := args
			if len(ids) == 0 {
				file, _ := cmd.Flags().GetString("file")
				id, err := sandboxIDFromLocalSpec(file)
				if err != nil {
					return err
				}
				ids = []string{id}
			}

			client := api.NewSandboxClient()
			var failures []string
			for _, id := range ids {
				_, err := client.KillSandbox(ctx, api.WithAuthenticationAndOrg(
					connect.NewRequest(&agentv1.KillSandboxRequest{SandboxId: id}), token, orgID))
				if err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", id, err))
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "killed %s\n", id)
			}
			if len(failures) > 0 {
				return fmt.Errorf("kill failed:\n  %s", failures)
			}
			return nil
		},
	}
	cmd.Flags().StringP("file", "f", "", "Path to a sandbox.depot.yml file when resolving by spec (default: walk up from cwd)")
	return cmd
}

// sandboxIDFromLocalSpec resolves the most recent sandbox id launched
// from the spec at `file` (or the nearest sandbox.depot.yml if file is
// empty). Errors if no spec is found or no state record exists yet.
func sandboxIDFromLocalSpec(file string) (string, error) {
	var path string
	if file != "" {
		path = file
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		p, err := sandbox.FindSpec(cwd)
		if err != nil {
			return "", fmt.Errorf("no sandbox-id given and no sandbox.depot.yml found from cwd; pass an id or run from inside a spec dir")
		}
		path = p
	}
	spec, err := sandbox.Load(path)
	if err != nil {
		return "", err
	}
	if spec.Name == "" {
		return "", fmt.Errorf("spec %s has no name; can't resolve a state file without one", path)
	}
	id, err := loadSandboxState(spec.Name)
	if err != nil {
		return "", fmt.Errorf("read state for %q: %w", spec.Name, err)
	}
	if id == "" {
		return "", fmt.Errorf("no recorded sandbox for spec %q (no ~/.depot/sandbox-state/<name>.id); pass an id explicitly", spec.Name)
	}
	return id, nil
}
