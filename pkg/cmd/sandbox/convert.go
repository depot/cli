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

	// Cache key (workflow-level workaround until DEP-4394 ships):
	// a content-addressed sibling tag of destRef whose digest segment is
	// the linux/amd64 platform manifest digest of the source image. The
	// workflow fills the digest in itself — we only have the list digest
	// here, which drifts every build because depot's attestation manifest
	// drifts even when the actual image layers are CACHED. The amd64-only
	// digest is content-stable.
	//
	// Format: <destRepo>:src-<placeholder>-<destTag>, with the placeholder
	// substituted to a 12-hex-char prefix of the amd64 digest at runtime.

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

// destRepoAndTag splits destRef into the "<host>/<repo>" prefix and the
// trailing tag. up.go always constructs <host>/<repo>:<name>-ext4 so the
// caller-side guarantee is that there's exactly one ':' after the last '/'.
// Returned as two strings to keep the workflow shell side simple (no
// reparsing in bash).
func destRepoAndTag(destRef string) (string, string) {
	colon := strings.LastIndex(destRef, ":")
	slash := strings.LastIndex(destRef, "/")
	if colon < 0 || colon < slash {
		return destRef, ""
	}
	return destRef[:colon], destRef[colon+1:]
}

// buildExt4Workflow returns a single-job inline workflow YAML. The job:
//
//  1. Logs into every depot registry host the script will touch (source
//     pull, cache probe, dest push).
//  2. Inspects the source manifest list and extracts the linux/amd64
//     platform manifest digest — content-stable across attestation drift.
//     Constructs cacheRef = <destRepo>:src-<short-amd64-digest>-<destTag>.
//  3. Probes the cache ref. If it exists, an identical ext4 was already
//     built from the same source on a prior run; skip to step 6.
//  4. (Cache miss) Installs snapshot, docker-pulls and extracts the OCI
//     rootfs, runs snapshot build-ext4, pushes the result to cacheRef.
//  5. (Falls through.)
//  6. Retags cacheRef to destRef via crane copy — manifest-only, no blob
//     transfer. Both hit and miss paths funnel through this so destRef is
//     produced identically.
//
// We construct via yaml.Marshal so input strings with quotes / colons can't
// escape into adjacent YAML keys.
func buildExt4Workflow(ociRef, destRef string) ([]byte, error) {
	destRepo, destTag := destRepoAndTag(destRef)
	// Match snapshot's expectation: REGISTRY_USERNAME=x-token,
	// REGISTRY_PASSWORD=<DEPOT_TOKEN>. The same DEPOT_TOKEN authorizes both
	// the source pull (registry.depot.dev) and the destination push (whatever
	// tenant destRef points at, modulo policy.ts).
	const cacheCheckAndConvert = `set -euxo pipefail

# 1. Log into the depot registry hosts we touch. docker keeps creds
#    per-host, so the bare-host login that authorizes SOURCE_REF reads
#    doesn't also authorize org-subdomain probes or the final retag.
#    DEST_REPO is "<host>/<repo>" — its host is enough to derive the
#    other login target.
host_of() { echo "${1%%/*}"; }
{
  echo registry.depot.dev
  host_of "$DEST_REPO"
} | sort -u | while read -r host; do
  echo "$REGISTRY_PASSWORD" | docker login "$host" -u "$REGISTRY_USERNAME" --password-stdin
done

# Install crane for manifest-only retags + manifest-list inspection.
# docker buildx imagetools create wraps single-platform manifests into a
# manifest list whose entries have empty platform descriptors — the
# sandbox runtime then can't satisfy a linux/amd64 lookup and rejects
# the image with "no manifest found for platform linux/amd64". crane
# copy preserves the manifest verbatim. Pinned to v0.20.x via
# go-containerregistry releases.
crane_url="https://github.com/google/go-containerregistry/releases/download/v0.20.2/go-containerregistry_Linux_x86_64.tar.gz"
curl -fsSL -o /tmp/crane.tgz "$crane_url"
mkdir -p /tmp/crane-bin
tar -xzf /tmp/crane.tgz -C /tmp/crane-bin crane
crane=/tmp/crane-bin/crane
"$crane" version

# 2. Compute a content-stable cache key. depot/build-push-action's saved
#    image is a manifest LIST whose digest drifts on every build because
#    the attestation/provenance manifest carries timestamps even when the
#    actual image layers are CACHED. The per-platform amd64 manifest
#    digest IS stable, so we key the cache on that.
amd64_digest=$("$crane" manifest "$SOURCE_REF" \
  | jq -r '.manifests[] | select(.platform.architecture=="amd64" and .platform.os=="linux") | .digest')
if [ -z "$amd64_digest" ] || [ "$amd64_digest" = "null" ]; then
  echo "could not extract linux/amd64 manifest digest from $SOURCE_REF; cache disabled for this run" >&2
  cache_ref="$DEST_REGISTRY"
else
  short=${amd64_digest##sha256:}
  short=${short:0:12}
  cache_ref="${DEST_REPO}:src-${short}-${DEST_TAG}"
fi
echo "cache_ref=$cache_ref"

# 3. Cache probe.
cache_hit=false
if [ "$cache_ref" != "$DEST_REGISTRY" ] && "$crane" manifest "$cache_ref" >/dev/null 2>&1; then
  echo "ext4 cache hit at $cache_ref; skipping convert"
  cache_hit=true
else
  echo "ext4 cache miss; running convert"
fi

if [ "$cache_hit" = "false" ]; then
  # 3. Install snapshot binary at a pinned version.
  install_dir=/tmp/snapshot-bin
  mkdir -p "$install_dir"
  url=$(curl -fsSL "https://dl.depot.dev/snapshot/release/linux/x64/${SNAPSHOT_VERSION}" | jq -r .url)
  curl -fsSL -o /tmp/snapshot.tar.gz "$url"
  tar -xzf /tmp/snapshot.tar.gz -C "$install_dir"
  "$install_dir/snapshot" --version

  # 4. Extract the OCI rootfs. docker create + docker export gives us a
  #    flattened tar of the image's filesystem — exactly what build-ext4 wants.
  #    --platform=linux/amd64 picks the amd64 manifest from a multi-arch index;
  #    the sandbox runtime only runs amd64 today.
  docker pull --platform=linux/amd64 "$SOURCE_REF"
  container=$(docker create --platform=linux/amd64 "$SOURCE_REF")
  rootfs_dir=/mnt/rootfs
  sudo mkdir -p "$rootfs_dir"
  sudo chown "$(id -u):$(id -g)" "$rootfs_dir"
  docker export "$container" | tar -xC "$rootfs_dir"
  docker rm "$container" >/dev/null

  rootfs_bytes=$(sudo du -sb "$rootfs_dir" | awk '{print $1}')
  image_size=$((rootfs_bytes + 1024 * 1024 * 1024))

  # 5. Pack into ext4 and push to the cache tag. snapshot reads
  #    REGISTRY_USERNAME / REGISTRY_PASSWORD from the env (same vars
  #    docker uses) for the push.
  sudo -E "$install_dir/snapshot" build-ext4 \
    --source-dir "$rootfs_dir" \
    --image-size "$image_size" \
    -o /tmp/rootfs.ext4 \
    --registry "$cache_ref"
fi

# 6. Promote the cache tag to the consumer-facing destRef.
#    crane copy preserves snapshot's single-manifest shape (no list
#    wrapping). Both refs live in the same registry repo so blobs aren't
#    transferred; only the manifest gets a new tag. No-op when
#    cache_ref == DEST_REGISTRY (cache disabled).
if [ "$cache_ref" != "$DEST_REGISTRY" ]; then
  "$crane" copy "$cache_ref" "$DEST_REGISTRY"
fi
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
							"DEST_REPO":         destRepo,
							"DEST_TAG":          destTag,
							"REGISTRY_USERNAME": "x-token",
							"REGISTRY_PASSWORD": "${{ secrets.DEPOT_TOKEN }}",
						},
						"run": cacheCheckAndConvert,
					},
				},
			},
		},
	}
	return yaml.Marshal(workflow)
}

// waitForRun polls the run until it reaches a terminal state, periodically
// printing a status line and finally dumping the convert job's logs.
//
// TODO(DEP-4262): swap to live streaming once the API's job-attempt log
// stream RPC lands. The convert job runs ~90s on agent-sized images and
// watching it idle is painful. Until then, status-only-then-dump is
// acceptable.
func waitForRun(ctx context.Context, token, orgID, runID string, stdout, stderr io.Writer) error {
	const pollInterval = 5 * time.Second
	const stallTimeout = 30 * time.Minute

	deadline := time.Now().Add(stallTimeout)
	start := time.Now()
	lastStatus := ""
	var lastAttemptID string

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("convert: timed out after %s waiting for run %s to finish", stallTimeout, runID)
		}

		status, err := api.CIGetRunStatus(ctx, token, orgID, runID)
		if err != nil {
			return fmt.Errorf("convert: poll run: %w", err)
		}

		// Record the latest attempt id we've seen — used to dump logs at the
		// end (success and failure paths).
		if id := latestAttemptIDFromStatus(status); id != "" {
			lastAttemptID = id
		}

		if status.Status != lastStatus {
			fmt.Fprintf(stdout, "  status:      %s (elapsed %s)\n", status.Status, time.Since(start).Round(time.Second))
			lastStatus = status.Status
		}

		switch status.Status {
		case "finished":
			dumpAttemptLogs(ctx, token, orgID, lastAttemptID, stdout)
			return nil
		case "failed", "cancelled":
			dumpAttemptLogs(ctx, token, orgID, lastAttemptID, stderr)
			return fmt.Errorf("convert: run %s ended with status %s", runID, status.Status)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// latestAttemptIDFromStatus picks the convert job's latest attempt id.
// Inline workflows have one workflow with one job; we still iterate
// defensively to avoid panicking on unexpected shapes.
func latestAttemptIDFromStatus(status *civ1.GetRunStatusResponse) string {
	for _, wf := range status.Workflows {
		for _, j := range wf.Jobs {
			var latest *civ1.AttemptStatus
			for _, a := range j.Attempts {
				if latest == nil || a.Attempt > latest.Attempt {
					latest = a
				}
			}
			if latest != nil {
				return latest.AttemptId
			}
		}
	}
	return ""
}

func dumpAttemptLogs(ctx context.Context, token, orgID, attemptID string, w io.Writer) {
	if attemptID == "" {
		return
	}
	lines, err := api.CIGetJobAttemptLogs(ctx, token, orgID, attemptID)
	if err != nil {
		fmt.Fprintf(w, "  (failed to fetch logs for attempt %s: %v)\n", attemptID, err)
		return
	}
	for _, line := range lines {
		fmt.Fprintln(w, line.Body)
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
