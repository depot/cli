package sandbox

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/depot/cli/pkg/api"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"gopkg.in/yaml.v3"
)

// snapshotVersion pins the snapshot binary the inline workflow installs. We
// pin (rather than `latest`) so a `depot sandbox up` from N weeks ago still
// produces a comparable image. Bump in lockstep with the version pinned in
// other depot/* workflows.
const snapshotVersion = "1.2.16"

// convertOCIToExt4 runs a Depot CI inline workflow that takes the just-saved
// OCI image, extracts its rootfs, and runs `snapshot build-ext4` to produce
// the ext4 base image the sandbox runtime mounts as rootfs. Returns destRef
// so the caller can pass it to StartSandbox unchanged.
//
// destRef must be a depot registry tenant the caller's token can push to
// and the sandbox runtime can pull from at boot. The same DEPOT_TOKEN that
// authorizes the source OCI pull authorizes the destination push.
//
// We don't use depot/ci-convert-action because it hardcodes `snapshot convert`
// (the EROFS path). The sandbox rootfs path expects ext4, so we drop one
// level and inline the docker-extract + build-ext4 dance ourselves.
func convertOCIToExt4(
	ctx context.Context,
	token, orgID string,
	specDir string,
	ociRef, destRef string,
	stdout, stderr io.Writer,
) (string, error) {
	repo := detectGitRepoFromDir(specDir)
	if repo == "" {
		return "", fmt.Errorf("convert: could not detect a github.com remote in %s; depot ci run needs a repo to dispatch the inline workflow against. cd into a git working tree (or set the spec via --file in one) and retry", specDir)
	}

	fmt.Fprintf(stdout, "Converting (source: %s)\n  destination: %s\n  repo:        %s\n", ociRef, destRef, repo)

	yamlBytes, err := buildExt4Workflow(ociRef, destRef)
	if err != nil {
		return "", fmt.Errorf("convert: build workflow: %w", err)
	}

	runReq := &civ1.RunRequest{
		Repo:            repo,
		WorkflowContent: []string{string(yamlBytes)},
	}

	runResp, err := api.CIRun(ctx, token, orgID, runReq)
	if err != nil {
		return "", fmt.Errorf("convert: start ci run: %w", err)
	}

	fmt.Fprintf(stdout, "  run:         %s\n  view:        https://depot.dev/orgs/%s/workflows/%s\n", runResp.RunId, runResp.OrgId, runResp.RunId)

	if err := waitForRun(ctx, token, orgID, runResp.RunId, stdout, stderr); err != nil {
		return "", err
	}

	return destRef, nil
}

// buildExt4Workflow returns a single-job inline workflow YAML. The job logs
// into the depot registry hosts the script touches, installs snapshot,
// docker-pulls and extracts the source OCI rootfs, then runs snapshot
// build-ext4 to push the result directly to destRef.
//
// We construct via yaml.Marshal so input strings with quotes / colons can't
// escape into adjacent YAML keys.
func buildExt4Workflow(ociRef, destRef string) ([]byte, error) {
	// Match snapshot's expectation: REGISTRY_USERNAME=x-token,
	// REGISTRY_PASSWORD=<DEPOT_TOKEN>. The same DEPOT_TOKEN authorizes both
	// the source pull (registry.depot.dev) and the destination push (whatever
	// tenant destRef points at, modulo policy.ts).
	const convert = `set -euxo pipefail

# Log into the depot registry hosts we touch. docker keeps creds
# per-host, so the bare-host login that authorizes SOURCE_REF reads
# doesn't also authorize the org-subdomain push.
host_of() { echo "${1%%/*}"; }
{
  echo registry.depot.dev
  host_of "$DEST_REGISTRY"
} | sort -u | while read -r host; do
  echo "$REGISTRY_PASSWORD" | docker login "$host" -u "$REGISTRY_USERNAME" --password-stdin
done

# Install snapshot binary at a pinned version.
install_dir=/tmp/snapshot-bin
mkdir -p "$install_dir"
url=$(curl -fsSL "https://dl.depot.dev/snapshot/release/linux/x64/${SNAPSHOT_VERSION}" | jq -r .url)
curl -fsSL -o /tmp/snapshot.tar.gz "$url"
tar -xzf /tmp/snapshot.tar.gz -C "$install_dir"
"$install_dir/snapshot" --version

# Extract the OCI rootfs. docker create + docker export gives us a
# flattened tar of the image's filesystem — exactly what build-ext4 wants.
# --platform=linux/amd64 picks the amd64 manifest from a multi-arch index;
# the sandbox runtime only runs amd64 today.
#
# Tar runs under sudo so file ownership in the tarball is preserved
# verbatim. Without this, a non-root tar invocation rewrites every
# uid/gid to the runner's, which silently breaks images that ship a
# UID >= 1000 user (vm3's init drops to such a user when present --
# see vm3/internal/init/user_linux.go). The result is a boot-time
# "fork/exec /bin/bash: permission denied" on any ubuntu:24.04 base
# that still carries the stock ubuntu user with /home/ubuntu.
docker pull --platform=linux/amd64 "$SOURCE_REF"
container=$(docker create --platform=linux/amd64 "$SOURCE_REF")
rootfs_dir=/mnt/rootfs
sudo mkdir -p "$rootfs_dir"
docker export "$container" | sudo tar -xpC "$rootfs_dir"
docker rm "$container" >/dev/null

# Pack into ext4 and push directly to destRef. snapshot picks
# --image-size itself (data_bytes * 1.20, 64 MiB floor) when
# omitted, then resize2fs -M's the filesystem before push so
# the OCI artifact is minimum size regardless. snapshot reads
# REGISTRY_USERNAME / REGISTRY_PASSWORD from the env (same
# vars docker uses) for the push.
sudo -E "$install_dir/snapshot" build-ext4 \
  --source-dir "$rootfs_dir" \
  -o /tmp/rootfs.ext4 \
  --registry "$DEST_REGISTRY"
`

	workflow := map[string]any{
		"name": "depot-sandbox-convert",
		"on":   map[string]any{"workflow_dispatch": map[string]any{}},
		"jobs": map[string]any{
			"convert": map[string]any{
				// 24.04-32 gives us enough scratch space + CPU for
				// `snapshot build-ext4` on agent-sized images without
				// leaning on free-disk-space.
				"runs-on": "depot-ubuntu-24.04-32",
				"steps": []any{
					map[string]any{
						"name": "Build ext4 rootfs and push to registry",
						"env": map[string]any{
							"SNAPSHOT_VERSION":  snapshotVersion,
							"SOURCE_REF":        ociRef,
							"DEST_REGISTRY":     destRef,
							"REGISTRY_USERNAME": "x-token",
							"REGISTRY_PASSWORD": "${{ secrets.DEPOT_TOKEN }}",
						},
						"run": convert,
					},
				},
			},
		},
	}
	return yaml.Marshal(workflow)
}

// latestAttemptIDFromStatus picks the highest-attempt id across all jobs in
// a run-status snapshot. Inline workflows produce one workflow with one job
// and one attempt, but we iterate defensively in case the shape changes.
func latestAttemptIDFromStatus(status *civ1.GetRunStatusResponse) string {
	var latest *civ1.AttemptStatus
	for _, wf := range status.Workflows {
		for _, j := range wf.Jobs {
			for _, a := range j.Attempts {
				if a.AttemptId == "" {
					continue
				}
				if latest == nil || a.Attempt > latest.Attempt {
					latest = a
				}
			}
		}
	}
	if latest == nil {
		return ""
	}
	return latest.AttemptId
}

// waitForRun follows the convert run from dispatch to terminal state. It
// polls until the convert job appears, streams that job's logs to stdout
// via the CI log stream, then re-polls the run until it settles. Status
// transitions print as `status: <state>` lines so the user sees forward
// progress while the stream is idle.
func waitForRun(ctx context.Context, token, orgID, runID string, stdout, stderr io.Writer) error {
	const jobAppearPollInterval = 2 * time.Second
	const settlePollInterval = 1 * time.Second
	const stallTimeout = 30 * time.Minute

	deadline := time.Now().Add(stallTimeout)
	start := time.Now()
	lastStatus := ""
	reportStatus := func(s string) {
		if s == "" || s == lastStatus {
			return
		}
		fmt.Fprintf(stdout, "  status:      %s (elapsed %s)\n", s, time.Since(start).Round(time.Second))
		lastStatus = s
	}

	// Phase 1: wait until the convert run has a job AND an attempt visible.
	// We can't stream by job_id alone — the api's resolver joins CIJob with
	// CIJobAttempt, so a job whose first attempt hasn't been persisted yet
	// returns NotFound. Stream by attempt_id once it shows up. A run can
	// also reach a terminal state without ever spawning an attempt (workflow
	// YAML validation failure, etc.), which we surface before bailing.
	var attemptID string
	for attemptID == "" {
		if time.Now().After(deadline) {
			return fmt.Errorf("convert: timed out after %s waiting for run %s to start", stallTimeout, runID)
		}
		status, err := api.CIGetRunStatus(ctx, token, orgID, runID)
		if err != nil {
			return fmt.Errorf("convert: poll run: %w", err)
		}
		reportStatus(status.Status)
		attemptID = latestAttemptIDFromStatus(status)
		if attemptID != "" {
			break
		}
		switch status.Status {
		case "failed", "cancelled":
			for _, wf := range status.Workflows {
				if wf.ErrorMessage != "" {
					fmt.Fprintf(stderr, "  workflow:    %s\n", wf.ErrorMessage)
				}
			}
			return fmt.Errorf("convert: run %s ended with status %s before any attempt ran", runID, status.Status)
		case "finished":
			// Cache hit can finish the whole run before any attempt is
			// emitted in the run-status snapshot. Nothing to stream; bail.
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jobAppearPollInterval):
		}
	}

	// Phase 2: stream the attempt's logs. The stream returns once the
	// attempt reaches a terminal state. Transient connect errors are
	// retried inside the helper.
	target := api.CILogStreamTarget{AttemptID: attemptID}
	if err := api.CIStreamJobAttemptLogs(ctx, token, orgID, target, stdout, reportStatus); err != nil {
		return fmt.Errorf("convert: stream logs: %w", err)
	}

	// Phase 3: settle on the run's terminal state. The stream tracks the
	// attempt, not the run, so the run can briefly remain "running" after
	// the stream returns while the API finalizes.
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("convert: timed out after %s waiting for run %s to settle", stallTimeout, runID)
		}
		status, err := api.CIGetRunStatus(ctx, token, orgID, runID)
		if err != nil {
			return fmt.Errorf("convert: final poll: %w", err)
		}
		reportStatus(status.Status)
		switch status.Status {
		case "finished":
			return nil
		case "failed", "cancelled":
			return fmt.Errorf("convert: run %s ended with status %s", runID, status.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(settlePollInterval):
		}
	}
}

// detectGitRepoFromDir extracts owner/repo from `git remote get-url origin`
// for dir, returning "" if dir is not a git tree or origin is not a github.com
// remote. Mirrors pkg/cmd/ci/migrate_helpers.go:detectRepoFromGitRemote, but
// duplicated here to avoid importing the ci command package from sandbox.
func detectGitRepoFromDir(dir string) string {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(out))

	// SSH: git@github.com:owner/repo[.git]
	if strings.HasPrefix(url, "git@github.com:") {
		path := strings.TrimPrefix(url, "git@github.com:")
		path = strings.TrimSuffix(path, ".git")
		parts := strings.SplitN(path, "/", 3)
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			return parts[0] + "/" + parts[1]
		}
		return ""
	}

	// HTTPS: https://github.com/owner/repo[.git]
	for _, prefix := range []string{"https://github.com/", "http://github.com/"} {
		if strings.HasPrefix(url, prefix) {
			path := strings.TrimPrefix(url, prefix)
			path = strings.TrimSuffix(path, ".git")
			parts := strings.SplitN(path, "/", 3)
			if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
				return parts[0] + "/" + parts[1]
			}
			return ""
		}
	}
	return ""
}
