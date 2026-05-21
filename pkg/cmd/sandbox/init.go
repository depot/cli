package sandbox

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const sampleSpec = `# sandbox.depot.yml — declarative spec for a Depot sandbox
name: my-sandbox

# Container image for the sandbox rootfs. The CLI builds your Dockerfile
# (depot build --save) and runs an OCI→ext4 convert CI run, then hands the
# resulting registry.depot.dev/<projectID>:<name>-ext4 ref to StartSandbox.
# Omit the section to inherit the API default image.
# container:
#   build:
#     context: .
#     dockerfile: Dockerfile

env:
  LOG_LEVEL: info

# Lifecycle hooks. Each stage is a list of {name, command, detach?, timeout_seconds?}
# entries; bare strings are sugar for {command: "..."}.
#
#   create   — once after the sandbox first boots (one-shot setup).
#   start    — every boot; mark long-running entries with detach: true.
#   exec     — before each "depot sandbox exec" user command.
#   shell    — prefixed onto the pty stdin by "depot sandbox shell" so the
#              login shell runs them; chain with "; " across entries.
#   snapshot — before "depot sandbox snapshot" runs the tar pipeline
#              (use to scrub credentials, .git dirs, history, etc.).
#
# Skip a stage at run time with "--no-hook" on the corresponding command.
# on:
#   create:
#     - name: install-tools
#       command: apt-get update && apt-get install -y jq ripgrep
#   start:
#     - name: heartbeat
#       detach: true
#       command: while true; do echo alive; sleep 60; done
#   shell:
#     - command: tmux attach -t work || tmux new -s work
#   snapshot:
#     - name: scrub
#       command: rm -rf ~/.aws ~/.gitconfig

# Clone a git repo into the sandbox at boot.
# git:
#   url: https://github.com/your-org/your-repo
#   branch: main
#   secret: github-token
`

func newSandboxInit() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [<dir>]",
		Short: "Scaffold a sandbox.depot.yml",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			path := filepath.Join(abs, "sandbox.depot.yml")
			force, _ := cmd.Flags().GetBool("force")
			if !force {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("%s already exists; pass --force to overwrite", path)
				}
			}
			if err := os.WriteFile(path, []byte(sampleSpec), 0644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", path)
			return nil
		},
	}
	cmd.Flags().Bool("force", false, "Overwrite an existing sandbox.depot.yml")
	return cmd
}
