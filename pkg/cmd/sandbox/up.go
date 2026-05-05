package sandbox

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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

			if spec.Container != nil && spec.Container.Build != nil && !noBuild {
				built, err := resolveAndBuild(spec, specPath, tagOverride, cmd.OutOrStdout(), cmd.ErrOrStderr())
				if err != nil {
					return err
				}

				// All registry refs derive from spec.Name + project + org.
				// `--save-tag <name>` already published the OCI image at
				// <orgID>.registry.depot.dev/<projectID>:<name>; we convert
				// from that to a sibling :<name>-ext4 ref. StartSandbox
				// receives the org-tenant form — the sandbox runtime only
				// resolves `<tenant>.registry.depot.dev/...` refs, so the
				// bare-host form fails with "remote fetch error: token
				// request returned 404 Not Found" at sandbox boot.
				// (DEP-4388 expanded the API-side validator to accept this
				// form.)
				agentTag := sanitizeTag(spec.Name)
				if agentTag == "" {
					return fmt.Errorf("spec %s: name is required (it determines the registry tag)", specPath)
				}
				orgRegistryHost := fmt.Sprintf("%s.registry.depot.dev", orgID)
				ext4Tag := agentTag + "-ext4"
				ociOrgRef := fmt.Sprintf("%s/%s:%s", orgRegistryHost, built.ProjectID, agentTag)
				ext4OrgRef := fmt.Sprintf("%s/%s:%s", orgRegistryHost, built.ProjectID, ext4Tag)
				ext4SandboxRef := ext4OrgRef

				if !noConvert {
					// Prefer the digest ref over the mutable
					// :<name> tag so the convert step pins to the exact
					// bytes we just saved. depot build's metadata-file
					// emits the digest under containerimage.digest; if
					// that's missing (older depot CLI), fall back to the
					// org-scoped tag.
					source := built.DigestRef
					if source == "" {
						source = ociOrgRef
					}
					specDir := filepath.Dir(specPath)
					if _, err := convertOCIToExt4(
						ctx, token, orgID,
						specDir,
						source, ext4OrgRef,
						cmd.OutOrStdout(), cmd.ErrOrStderr(),
					); err != nil {
						return err
					}
					spec.Image = ext4SandboxRef
				} else {
					// --no-convert: hand the OCI ref through verbatim.
					// The sandbox runtime will reject it; this path
					// exists for debugging the build half in isolation.
					spec.Image = built.ImageRef
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
			if conn := msg.SshConnection; conn != nil {
				printSSHHint(cmd.OutOrStdout(), msg.SandboxId, conn)
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
			fmt.Fprintf(cmd.OutOrStdout(),
				"Logs:    depot sandbox logs %s -f\n         Axiom: ['vm-execution-log'] | where sandbox_id == \"%s\"\n",
				msg.SandboxId, msg.SandboxId)
			fmt.Fprintf(cmd.OutOrStdout(), "Kill:    depot sandbox kill %s\n", msg.SandboxId)

			if follow {
				if spec.AgentType == "" {
					return fmt.Errorf("--logs requires a modal sandbox (spec.agent_type set); this spec has no agent_type, so the modal log stream would be empty.\n  Axiom: ['vm-execution-log'] | where sandbox_id == \"%s\"\n  SSH:   depot sandbox ssh %s\n", msg.SandboxId, msg.SandboxId)
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

func printSSHHint(w io.Writer, sandboxID string, conn *agentv1.SSHConnection) {
	keyPath, err := writeSandboxSSHKey(sandboxID, conn.PrivateKey)
	if err != nil {
		fmt.Fprintf(w, "SSH:     %s@%s -p %d  (failed to write key: %v)\n", conn.Username, conn.Host, conn.Port, err)
		return
	}
	fmt.Fprintf(w, "SSH:     ssh -i %s -p %d %s@%s\n", keyPath, conn.Port, conn.Username, conn.Host)
}

// writeSandboxSSHKey decodes the base64 PEM private key from the API and
// writes it to a temp file with 0600 permissions so the user can immediately
// invoke ssh with -i. The path is deterministic per sandbox so re-running
// `up` overwrites the previous key.
func writeSandboxSSHKey(sandboxID, b64Key string) (string, error) {
	pem, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil {
		return "", fmt.Errorf("decode key: %w", err)
	}
	dir := filepath.Join(os.TempDir(), "depot-sandbox-keys")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s.key", sanitizeID(sandboxID)))
	if err := os.WriteFile(path, pem, 0600); err != nil {
		return "", err
	}
	return path, nil
}

func sanitizeID(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, id)
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
