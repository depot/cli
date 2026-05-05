package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

func newSandboxUp() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up [flags]",
		Short: "Start a sandbox from a sandbox.depot.yml",
		Long: `Start a sandbox from a declarative sandbox.depot.yml.

The spec file is searched in the current directory and ancestors. Use --file
to point at an explicit path, and --set KEY=VALUE to fill in ${input.KEY}
references inside the spec.`,
		Example: `
  # Start the sandbox declared by sandbox.depot.yml in the current tree
  depot sandbox up

  # Stream entrypoint logs once the sandbox boots
  depot sandbox up --logs

  # Override the spec location and provide template inputs
  depot sandbox up -f ./agents/investigator/sandbox.depot.yml --set thread_id=th_123
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			file, _ := cmd.Flags().GetString("file")
			setPairs, _ := cmd.Flags().GetStringArray("set")
			follow, _ := cmd.Flags().GetBool("logs")

			specPath, err := resolveSpecPath(file)
			if err != nil {
				return err
			}
			spec, err := sandbox.Load(specPath)
			if err != nil {
				return err
			}
			inputs, err := sandbox.ParseInputs(setPairs)
			if err != nil {
				return err
			}

			// Resolve the org for the StartSandbox call. The build itself
			// is project-scoped (depot build --save) so doesn't need it.
			orgID, _ := cmd.Flags().GetString("org")
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			// When the spec declares a [build] section, build first and
			// pin req.image to the resulting saved-image ref. Skip with
			// --no-build if the caller has just published the image
			// themselves.
			noBuild, _ := cmd.Flags().GetBool("no-build")
			noConvert, _ := cmd.Flags().GetBool("no-convert")
			tagOverride, _ := cmd.Flags().GetString("tag")

			token, _ := cmd.Flags().GetString("token")
			token, err = helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return fmt.Errorf("resolve token: %w", err)
			}

			if spec.Container != nil && spec.Container.Build != nil {
				ociOrgRef, ext4OrgRef, err := sandboxRegistryRefs(spec, specPath, orgID)
				if err != nil {
					return err
				}

				if !noBuild {
					built, err := resolveAndBuild(spec, specPath, tagOverride, cmd.OutOrStdout(), cmd.ErrOrStderr())
					if err != nil {
						return err
					}

					if !noConvert {
						// Prefer the digest ref over the mutable :<name> tag
						// so the convert step pins to the exact bytes we
						// just saved. depot build's metadata-file emits
						// the digest under containerimage.digest; if that's
						// missing (older depot CLI), fall back to the
						// org-scoped tag.
						source := built.DigestRef
						if source == "" {
							source = ociOrgRef
						}
						if _, err := convertOCIToExt4(
							ctx, token, orgID,
							filepath.Dir(specPath),
							source, ext4OrgRef,
							cmd.OutOrStdout(), cmd.ErrOrStderr(),
						); err != nil {
							return err
						}
						spec.Image = ext4OrgRef
					} else {
						// --no-convert: hand the OCI ref through verbatim.
						// The sandbox runtime will reject it; this path
						// exists for debugging the build half in isolation.
						spec.Image = built.ImageRef
					}
				} else {
					// --no-build: trust that a prior `depot sandbox build`
					// (or `depot sandbox up`) already published the
					// conventional ext4 ref. The sandbox runtime will
					// surface a clear pull error at boot if it doesn't.
					spec.Image = ext4OrgRef
				}
			}

			req, err := spec.ToStartSandboxRequest(inputs)
			if err != nil {
				return err
			}

			client := api.NewSandboxClient()

			// The proto doesn't carry a name field yet (sandbox.proto:Sandbox),
			// so dedupe-by-name lives on the client. Skip with --force when
			// you've manually killed the prior sandbox out-of-band and the
			// state file still claims it's alive.
			force, _ := cmd.Flags().GetBool("force")
			if !force {
				if err := assertNoLiveSandbox(ctx, client, token, orgID, spec.Name); err != nil {
					return err
				}
			}

			res, err := client.StartSandbox(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
			if err != nil {
				return fmt.Errorf("start sandbox: %w", err)
			}
			msg := res.Msg

			if err := saveSandboxState(spec.Name, msg.SandboxId); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to save sandbox state for %q: %v\n", spec.Name, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Sandbox %s started (session %s)\n", msg.SandboxId, msg.SessionId)
			if msg.OrganizationId != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Org:     %s\n", msg.OrganizationId)
			}
			if r := msg.CommandResult; r != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Command exit %d (%d bytes stdout, %d bytes stderr)\n",
					r.ExitCode, len(r.Stdout), len(r.Stderr))
				if r.Stdout != "" {
					fmt.Fprintln(cmd.OutOrStdout(), r.Stdout)
				}
				if r.Stderr != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), r.Stderr)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Shell:   depot sandbox shell %s\n", msg.SandboxId)
			fmt.Fprintf(cmd.OutOrStdout(),
				"Logs:    depot sandbox logs %s -f\n         Axiom: ['vm-execution-log'] | where sandbox_id == \"%s\"\n",
				msg.SandboxId, msg.SandboxId)
			fmt.Fprintf(cmd.OutOrStdout(), "Kill:    depot sandbox kill %s\n", msg.SandboxId)

			if follow {
				if spec.AgentType == "" {
					return fmt.Errorf("--logs requires a modal sandbox (spec.agent_type set); this spec has no agent_type, so the modal log stream would be empty.\n  Axiom: ['vm-execution-log'] | where sandbox_id == \"%s\"\n  Shell: depot sandbox shell %s\n", msg.SandboxId, msg.SandboxId)
				}
				return streamLogs(ctx, client, token, orgID, msg.SandboxId, cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			return nil
		},
	}

	cmd.Flags().StringP("file", "f", "", "Path to a sandbox.depot.yml file (default: walk up from cwd)")
	cmd.Flags().StringArray("set", nil, "Inputs as KEY=VALUE for ${input.KEY} substitution; repeatable")
	cmd.Flags().Bool("logs", false, "Stream entrypoint logs after the sandbox starts")
	cmd.Flags().Bool("no-build", false, "Skip the build step even if [build] is present in the spec")
	cmd.Flags().Bool("no-convert", false, "Skip the OCI→ext4 convert step (pass the OCI ref to StartSandbox unchanged; the rootfs mount will fail unless the API has a fallback)")
	cmd.Flags().Bool("force", false, "Start a new sandbox even if a previously launched one with the same spec name is still running (per local state)")
	cmd.Flags().String("tag", "", "Override the resolved image tag for the build step")

	return cmd
}

func resolveSpecPath(file string) (string, error) {
	if file != "" {
		if _, err := os.Stat(file); err != nil {
			return "", fmt.Errorf("spec %s: %w", file, err)
		}
		abs, err := filepath.Abs(file)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return sandbox.FindSpec(cwd)
}

// streamLogs is shared with `depot sandbox logs -f`. Defined here so `up
// --logs` doesn't depend on the logs.go file directly; both call into it.
func streamLogs(ctx context.Context, client interface {
	StreamSandboxLogs(context.Context, *connect.Request[agentv1.StreamSandboxLogsRequest]) (*connect.ServerStreamForClient[agentv1.StreamSandboxLogsResponse], error)
}, token, orgID, sandboxID string, stdout, stderr io.Writer) error {
	req := &agentv1.StreamSandboxLogsRequest{SandboxId: sandboxID}
	stream, err := client.StreamSandboxLogs(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return fmt.Errorf("stream logs: %w", err)
	}
	defer func() { _ = stream.Close() }()
	for stream.Receive() {
		ev := stream.Msg().Event
		if ev == nil {
			continue
		}
		w := stdout
		if ev.Type == agentv1.StreamSandboxLogsResponse_LogEvent_LOG_TYPE_STDERR {
			w = stderr
		}
		_, _ = w.Write(ev.Data)
	}
	if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("log stream: %w", err)
	}
	return nil
}
