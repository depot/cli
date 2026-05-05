package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/project"
	"github.com/depot/cli/pkg/sandbox"
	"github.com/spf13/cobra"
)

// buildResult is the materialized output of resolveAndBuild — a fully
// qualified image reference, plus the project id and content-addressed
// digest. Downstream callers prefer DigestRef over ImageRef when reproducibility
// matters (e.g. handing a source to ci-convert-action — the convert tag is
// mutable so the digest is the only stable handle for the just-built image).
type buildResult struct {
	ImageRef  string // registry.depot.dev/<projectID>:<buildID> — mutable tag form
	DigestRef string // registry.depot.dev/<projectID>@sha256:<digest> — immutable digest form (empty if buildkit didn't emit a digest)
	Digest    string // raw "sha256:<hex>" so callers can build a different ref or print it
	ProjectID string
}

func newSandboxBuild() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "build [flags]",
		Short: "Build the sandbox image end-to-end (OCI build + convert to ext4 rootfs)",
		Long: `Build the image declared by the [container.build] section of sandbox.depot.yml,
push it to depot's registry, and convert it to the ext4 rootfs the sandbox
runtime boots from.

The output ref is the same one ` + "`depot sandbox up`" + ` would feed StartSandbox,
so a subsequent ` + "`depot sandbox up --no-build`" + ` boots from this image without
rebuilding.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			file, _ := cmd.Flags().GetString("file")
			specPath, err := resolveSpecPath(file)
			if err != nil {
				return err
			}
			spec, err := sandbox.Load(specPath)
			if err != nil {
				return err
			}
			if spec.Container == nil || spec.Container.Build == nil {
				return fmt.Errorf("spec %s has no [container.build] section; nothing to build", specPath)
			}

			orgID, _ := cmd.Flags().GetString("org")
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}
			token, _ := cmd.Flags().GetString("token")
			token, err = helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return fmt.Errorf("resolve token: %w", err)
			}

			ociOrgRef, ext4OrgRef, err := sandboxRegistryRefs(spec, specPath, orgID)
			if err != nil {
				return err
			}

			tagOverride, _ := cmd.Flags().GetString("tag")
			built, err := resolveAndBuild(spec, specPath, tagOverride, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}

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

			fmt.Fprintf(cmd.OutOrStdout(), "Saved %s\n", ext4OrgRef)
			return nil
		},
	}
	cmd.Flags().StringP("file", "f", "", "Path to a sandbox.depot.yml file (default: walk up from cwd)")
	cmd.Flags().String("tag", "", "Friendly --save-tag for the depot UI (the ref is still <projectID>:<buildID>)")
	return cmd
}

// sandboxRegistryRefs returns the conventional OCI and ext4 refs the sandbox
// build/up commands agree on. Both live in the org-tenant subdomain of the
// depot registry (the sandbox runtime only resolves <tenant>.registry.depot.dev
// refs; bare-host fails at boot — see DEP-4388).
//
//	oci  = <orgID>.registry.depot.dev/<projectID>:<sanitized-name>
//	ext4 = <orgID>.registry.depot.dev/<projectID>:<sanitized-name>-ext4
//
// Same spec.Name + same project + same org → same refs, so `sandbox build`
// publishes to ext4 and `sandbox up --no-build` boots from it.
func sandboxRegistryRefs(spec *sandbox.Spec, specPath, orgID string) (string, string, error) {
	if spec.Container == nil || spec.Container.Build == nil {
		return "", "", fmt.Errorf("spec %s: registry refs require a [container.build] section", specPath)
	}
	agentTag := sanitizeTag(spec.Name)
	if agentTag == "" {
		return "", "", fmt.Errorf("spec %s: name is required (it determines the registry tag)", specPath)
	}
	projectID, err := resolvePushProject(spec.Container.Build, filepath.Dir(specPath))
	if err != nil {
		return "", "", err
	}
	base := fmt.Sprintf("%s.registry.depot.dev/%s", orgID, projectID)
	oci := fmt.Sprintf("%s:%s", base, agentTag)
	ext4 := fmt.Sprintf("%s:%s-ext4", base, agentTag)
	return oci, ext4, nil
}

// resolveAndBuild runs the equivalent of
// `depot build --project P --save --save-tag T --metadata-file M -f F [target/buildargs] CTX`
// by re-execing the running depot binary so we get all the buildx-via-depot
// behaviour (cache, oci, project auth) for free. It returns the canonical
// `registry.depot.dev/<projectID>:<buildID>` ref the sandbox compose YAML
// pulls from.
//
// We deliberately avoid `--push -t <orgID>.registry.depot.dev/<repo>:<tag>`:
// depot build's AdditionalCredentials only authorize Host: "registry.depot.dev"
// (no subdomain), so pushing to the org subdomain would need a separate
// client-side `docker login`. `--save` sidesteps that — layers go to depot's
// internal store via the same auth path the build API already uses, and the
// in-VM dockerd can pull from it (with a `depot pull-token --project ...` +
// `docker login` if the sandbox isn't already preauthed).
func resolveAndBuild(spec *sandbox.Spec, specPath, tagOverride string, stdout, stderr io.Writer) (*buildResult, error) {
	specDir := filepath.Dir(specPath)
	build := spec.Container.Build
	contextDir := build.Context
	if contextDir == "" {
		contextDir = "."
	}
	contextAbs, err := filepath.Abs(filepath.Join(specDir, contextDir))
	if err != nil {
		return nil, fmt.Errorf("resolve build context: %w", err)
	}
	dockerfile := build.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	if !filepath.IsAbs(dockerfile) {
		dockerfile = filepath.Join(contextAbs, dockerfile)
	}

	projectID, err := resolvePushProject(build, specDir)
	if err != nil {
		return nil, err
	}
	saveTag := tagOverride
	if saveTag == "" && build.Push != nil {
		saveTag = build.Push.Tag
	}
	if saveTag == "" {
		// Default to the bare spec name (no git-sha suffix) so re-running
		// `up` overwrites the same `<orgID>.registry.depot.dev/<projectID>:<name>`
		// the convert step pulls from. The convert destination derives from
		// this tag too — see the `<name>-ext4` ref in up.go.
		saveTag = sanitizeTag(spec.Name)
		if saveTag == "" {
			saveTag = defaultTag(spec.Name, contextAbs)
		}
	}

	metaFile, err := os.CreateTemp("", "depot-sandbox-build-*.json")
	if err != nil {
		return nil, fmt.Errorf("create metadata file: %w", err)
	}
	metaPath := metaFile.Name()
	_ = metaFile.Close()
	defer func() { _ = os.Remove(metaPath) }()

	// Multi-arch by default: depot picks an arm64 builder, but the
	// sandbox runtime is amd64-only today, so a single-arch arm64 image
	// makes the rootfs pull fail with "no matching manifest for
	// linux/amd64". Build both so the runtime can pick either.
	args := []string{"build",
		"--project", projectID,
		"--save",
		"--save-tag", saveTag,
		"--metadata-file", metaPath,
		"--platform", "linux/amd64,linux/arm64",
		"-f", dockerfile,
	}
	if build.Target != "" {
		args = append(args, "--target", build.Target)
	}
	if build.NoCache {
		args = append(args, "--no-cache")
	}
	for k, v := range build.BuildArgs {
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, contextAbs)

	self := os.Args[0]
	fmt.Fprintf(stdout, "Building (save-tag: %s)\n  context: %s\n  file:    %s\n", saveTag, contextAbs, dockerfile)

	c := exec.Command(self, args...)
	c.Stdout = stdout
	c.Stderr = stderr
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("depot build failed: %w", err)
	}

	// metadata-file's `depot.build` block is canonical:
	// pkg/buildx/commands/build.go:writeMetadataFile. `containerimage.digest`
	// comes from buildkit itself — it's the manifest digest of the just-saved
	// image (an OCI index for multi-platform builds, a manifest otherwise).
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("read build metadata: %w", err)
	}
	var meta struct {
		DepotBuild struct {
			BuildID   string `json:"buildID"`
			ProjectID string `json:"projectID"`
		} `json:"depot.build"`
		ContainerImageDigest string `json:"containerimage.digest"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("parse build metadata (%s): %w", metaPath, err)
	}
	if meta.DepotBuild.BuildID == "" || meta.DepotBuild.ProjectID == "" {
		return nil, fmt.Errorf("build metadata %s missing depot.build buildID/projectID", metaPath)
	}

	imageRef := fmt.Sprintf("registry.depot.dev/%s:%s", meta.DepotBuild.ProjectID, meta.DepotBuild.BuildID)
	res := &buildResult{
		ImageRef:  imageRef,
		ProjectID: meta.DepotBuild.ProjectID,
		Digest:    meta.ContainerImageDigest,
	}
	if meta.ContainerImageDigest != "" {
		res.DigestRef = fmt.Sprintf("registry.depot.dev/%s@%s", meta.DepotBuild.ProjectID, meta.ContainerImageDigest)
		fmt.Fprintf(stdout, "  digest: %s\n", meta.ContainerImageDigest)
	} else {
		fmt.Fprintf(stderr, "  warning: depot build metadata had no containerimage.digest; convert will use the mutable tag instead\n")
	}
	return res, nil
}

// resolvePushProject prefers the explicit override, then the depot.json
// closest to the spec file. We deliberately do not fall back to cwd's
// depot.json — that's surprising when running `depot sandbox up -f` against a
// spec deep in another tree.
func resolvePushProject(build *sandbox.BuildSpec, specDir string) (string, error) {
	if build.Push != nil && build.Push.Project != "" {
		return build.Push.Project, nil
	}
	cfg, _, err := project.ReadConfig(specDir)
	if err != nil {
		return "", fmt.Errorf("resolve push project: no depot.json found near %s; set build.push.project explicitly", specDir)
	}
	if cfg.ID == "" {
		return "", fmt.Errorf("resolve push project: %s has no id; set build.push.project explicitly", specDir)
	}
	return cfg.ID, nil
}

func defaultTag(specName, contextDir string) string {
	prefix := sanitizeTag(specName)
	if prefix == "" {
		prefix = "agent"
	}
	if sha, ok := gitShortSHA(contextDir); ok {
		return fmt.Sprintf("%s-%s", prefix, sha)
	}
	return prefix + "-latest"
}

func gitShortSHA(dir string) (string, bool) {
	c := exec.Command("git", "rev-parse", "--short", "HEAD")
	c.Dir = dir
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = io.Discard
	if err := c.Run(); err != nil {
		return "", false
	}
	sha := strings.TrimSpace(out.String())
	if sha == "" {
		return "", false
	}
	return sha, true
}

// sanitizeTag keeps only the characters Docker tags allow.
func sanitizeTag(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return ""
	}
	return out
}
