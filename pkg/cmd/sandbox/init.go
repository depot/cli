package sandbox

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const sampleSpec = `# sandbox.depot.yml — declarative spec for a Depot sandbox
name: my-agent
agent_type: claude_code

# Container image for the sandbox rootfs. The CLI builds your Dockerfile
# (depot build --save) and runs an OCI→ext4 convert CI run, then hands the
# resulting registry.depot.dev/<projectID>:<name>-ext4 ref to StartSandbox.
# Omit the section to inherit the API default image.
# container:
#   build:
#     context: .
#     dockerfile: Dockerfile

# Argv passed to the sandbox entrypoint. Use ${input.foo} placeholders to fill
# values from "depot sandbox up --set foo=bar".
argv: claude --dangerously-skip-permissions

# Optional one-shot command to run inside the sandbox after boot. Setting this
# along with detach: false makes "depot sandbox up" wait and return its result.
# command: /usr/local/bin/run-agent
# detach: true

env:
  LOG_LEVEL: info
  # THREAD_ID: ${input.thread_id}

# Clone a git repo into the sandbox at boot.
# git:
#   url: https://github.com/your-org/your-repo
#   branch: main
#   secret: github-token

# Interactive debugging is via "depot sandbox shell <id>" (compute exec
# channel, no SSH bastion). The spec's ssh block is reserved for a future
# real-SSH path; leave it off for now.

# MCP servers serialized to DEPOT_AGENT_MCP_CONFIG. Your image's boot script
# materializes them into the right location for whichever runtime you're using.
# mcp:
#   servers:
#     plain:
#       command: ["plain-mcp"]
#       env:
#         PLAIN_API_KEY: $PLAIN_API_KEY
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
