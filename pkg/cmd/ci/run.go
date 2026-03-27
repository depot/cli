package ci

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/pty"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const cacheBaseURL = "https://cache.depot.dev"

func NewCmdRun() *cobra.Command {
	var (
		orgID        string
		token        string
		workflowPath string
		jobNames     []string
		sshAfterStep int
		ssh          bool
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a local CI workflow [beta]",
		Long: `Run a local CI workflow YAML via the Depot CI API.

If there are local changes relative to the remote state of your branch, they are
automatically uploaded as a patch and applied during the workflow run. For pushed
branches, the patch contains only unpushed changes; for unpushed branches, the
patch is relative to the default branch.

This command is in beta and subject to change.`,
		Example: `  # Run a workflow
  depot ci run --workflow .depot/workflows/ci.yml

  # Run specific jobs
  depot ci run --workflow .depot/workflows/ci.yml --job build --job test

  # Run a job and connect to its terminal via SSH
  depot ci run --workflow .depot/workflows/ci.yml --job build --ssh

  # Debug with SSH after a specific step (pauses workflow until you continue)
  depot ci run --workflow .depot/workflows/ci.yml --job build --ssh-after-step 3`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workflowPath == "" {
				return cmd.Help()
			}

			ctx := cmd.Context()

			if sshAfterStep > 0 && len(jobNames) != 1 {
				return fmt.Errorf("--ssh-after-step requires exactly one --job")
			}

			if ssh && len(jobNames) != 1 {
				return fmt.Errorf("--ssh requires exactly one --job")
			}

			if ssh && sshAfterStep > 0 {
				return fmt.Errorf("--ssh and --ssh-after-step are mutually exclusive")
			}

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			// Load and parse workflow YAML
			workflowBytes, err := os.ReadFile(workflowPath)
			if err != nil {
				return fmt.Errorf("failed to read workflow file: %w", err)
			}

			var workflow map[string]interface{}
			if err := yaml.Unmarshal(workflowBytes, &workflow); err != nil {
				return fmt.Errorf("failed to parse workflow YAML: %w", err)
			}

			jobsRaw, ok := workflow["jobs"]
			if !ok {
				return fmt.Errorf("workflow has no 'jobs' key")
			}
			jobs, ok := jobsRaw.(map[string]interface{})
			if !ok {
				return fmt.Errorf("workflow 'jobs' is not a map")
			}

			allJobNames := make([]string, 0, len(jobs))
			for name := range jobs {
				allJobNames = append(allJobNames, name)
			}

			// Validate requested jobs exist
			for _, name := range jobNames {
				if _, exists := jobs[name]; !exists {
					return fmt.Errorf("job %q not found in workflow. Available jobs: %s", name, strings.Join(allJobNames, ", "))
				}
			}

			// Determine which jobs to include
			selectedJobs := jobNames
			if len(selectedJobs) == 0 {
				selectedJobs = allJobNames
			}

			// Pare workflow to selected jobs (plus transitive dependencies) if a subset was specified
			if len(jobNames) > 0 {
				needed := resolveJobDeps(jobs, jobNames)
				paredJobs := make(map[string]interface{})
				for name := range needed {
					paredJobs[name] = jobs[name]
				}
				workflow["jobs"] = paredJobs
				jobs = paredJobs

				// Update selectedJobs to include deps for display
				selectedJobs = make([]string, 0, len(needed))
				for name := range needed {
					selectedJobs = append(selectedJobs, name)
				}
			}

			// Resolve repo from git remote
			workflowDir := filepath.Dir(workflowPath)
			if !filepath.IsAbs(workflowDir) {
				cwd, _ := os.Getwd()
				workflowDir = filepath.Join(cwd, workflowDir)
			}

			repo, err := resolveRepo(workflowDir)
			if err != nil {
				return fmt.Errorf("failed to resolve repo: %w", err)
			}

			// Detect local changes as a patch
			patch := detectPatch(workflowDir)

			if patch != nil {
				fmt.Printf("Base: %s\n", patch.baseBranch)
				fmt.Printf("Merge base: %s\n", patch.mergeBase)
				fmt.Printf("Patch size: %d bytes\n", len(patch.content))

				hash := sha256.Sum256([]byte(patch.content))
				patchHash := fmt.Sprintf("%x", hash)[:16]
				cacheKey := fmt.Sprintf("patch/%s/%s", patch.mergeBase[:12], patchHash)
				fmt.Printf("Cache key: %s\n", cacheKey)

				if err := api.UploadCacheEntry(ctx, tokenVal, orgID, cacheKey, []byte(patch.content)); err != nil {
					return fmt.Errorf("failed to upload patch: %w", err)
				}
				fmt.Println("Patch uploaded to Depot Cache")

				// Inject patch step into each selected job that has actions/checkout
				for _, jobName := range selectedJobs {
					injectPatchStep(jobs, jobName, patch.mergeBase, cacheKey)
				}
			}

			// Insert debug pause step if requested
			if sshAfterStep > 0 {
				jobName := jobNames[0]
				if err := injectDebugStep(jobs, jobName, sshAfterStep, patch != nil); err != nil {
					return err
				}
			}

			fmt.Printf("Repo: %s\n", repo)
			if len(jobNames) > 0 {
				fmt.Printf("Jobs: %s\n", strings.Join(selectedJobs, ", "))
			} else {
				fmt.Printf("Jobs: (all) %s\n", strings.Join(selectedJobs, ", "))
			}
			if patch != nil {
				fmt.Printf("Checking out commit: %s\n", patch.mergeBase)
			}
			if sshAfterStep > 0 {
				fmt.Printf("Inserting debug step after step %d\n", sshAfterStep)
			}
			if headSHA, err := resolveHEAD(workflowDir); err == nil {
				fmt.Printf("HEAD: %s\n", headSHA)
			}
			fmt.Println()

			// Serialize workflow back to YAML
			yamlBytes, err := yaml.Marshal(workflow)
			if err != nil {
				return fmt.Errorf("failed to serialize workflow: %w", err)
			}

			req := &civ1.RunRequest{
				Repo:            repo,
				WorkflowContent: []string{string(yamlBytes)},
			}

			if patch != nil {
				req.Sha = &patch.mergeBase
			} else if headSHA, err := resolveHEAD(workflowDir); err == nil {
				req.Sha = &headSHA
			}

			resp, err := api.CIRun(ctx, tokenVal, orgID, req)
			if err != nil {
				return fmt.Errorf("failed to start CI run: %w", err)
			}

			fmt.Printf("Org: %s\n", resp.OrgId)
			fmt.Printf("Run: %s\n", resp.RunId)
			fmt.Println()

			if sshAfterStep > 0 || ssh {
				if sshAfterStep > 0 {
					fmt.Printf("Waiting for debug step to activate...\n")
				} else {
					fmt.Printf("Waiting for job to start...\n")
				}
				sandboxID, sessionID, err := waitForSandbox(ctx, tokenVal, orgID, resp.RunId, jobNames[0], "")
				if err != nil {
					return err
				}

				// When --ssh-after-step is used, wait for the debug step to
				// actually be running before connecting, so the user lands in
				// the sandbox after step N has completed.
				if sshAfterStep > 0 {
					fmt.Fprintf(os.Stderr, "Waiting for step %d to complete...\n", sshAfterStep)
					if err := waitForLogMarker(ctx, tokenVal, orgID, resp.RunId, jobNames[0], "::depot-ssh-ready::"); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: could not confirm debug step is active: %v\n", err)
						fmt.Fprintf(os.Stderr, "Connecting anyway...\n")
					}
				}

				if sshAfterStep > 0 {
					fmt.Fprintf(os.Stderr, "Run 'touch /tmp/depot-continue' to resume the workflow. (Your session will not end.)\n")
				}
				if !helpers.IsTerminal() {
					return printSSHInfo(resp.RunId, sandboxID, sessionID, "")
				}
				return pty.Run(ctx, pty.SessionOptions{
					Token:     tokenVal,
					OrgID:     orgID,
					SandboxID: sandboxID,
					SessionID: sessionID,
				})
			}

			if sshAfterStep > 0 {
				fmt.Fprintf(os.Stderr, "Waiting for tmate session to start...\n")
				sshTarget, err := waitForTmateSSH(ctx, tokenVal, orgID, resp.RunId, jobNames[0])
				if err != nil {
					return err
				}
				if !helpers.IsTerminal() {
					fmt.Printf("ssh %s\n", sshTarget)
					return nil
				}
				fmt.Fprintf(os.Stderr, "Connecting: ssh %s\n", sshTarget)
				return execSSH(sshTarget)
			}

			orgFlag := ""
			if cmd.Flags().Changed("org") {
				orgFlag = " --org " + orgID
			}
			fmt.Printf("Check status:  depot ci status %s%s\n", resp.RunId, orgFlag)
			fmt.Printf("View in Depot: https://depot.dev/orgs/%s/workflows/%s\n", resp.OrgId, resp.RunId)

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&workflowPath, "workflow", "", "Path to workflow YAML file")
	cmd.Flags().StringSliceVar(&jobNames, "job", nil, "Job name(s) to run (repeatable; omit to run all)")
	cmd.Flags().IntVar(&sshAfterStep, "ssh-after-step", 0, "1-based step index to pause and connect via SSH after (requires single --job)")
	cmd.Flags().BoolVar(&ssh, "ssh", false, "Start the run and connect to the job's sandbox via interactive terminal (requires single --job)")

	cmd.AddCommand(NewCmdRunList())

	return cmd
}

func resolveHEAD(workflowDir string) (string, error) {
	out, err := exec.Command("git", "-C", workflowDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

type patchInfo struct {
	baseBranch string
	mergeBase  string
	content    string
}

// findMergeBase picks the best base commit for patch generation.
//
// If the current branch has been pushed (origin/<branch> exists locally),
// we use its SHA as the merge base — the patch is just unpushed local changes.
// Otherwise we fall back to the merge base with the default branch (origin/main).
//
// We use the local tracking ref (origin/<branch>), not a live fetch, because
// the user may not have pulled — the tracking ref reflects what they last fetched,
// which is guaranteed to exist on GitHub.
func findMergeBase(workflowDir string) (baseBranch string, mergeBase string, err error) {
	// Try the current branch's remote tracking ref first
	branchOut, err := exec.Command("git", "-C", workflowDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err == nil {
		branch := strings.TrimSpace(string(branchOut))
		if branch != "" && branch != "HEAD" { // not detached
			remoteBranch := "origin/" + branch
			// Verify the remote branch exists
			_, err := exec.Command("git", "-C", workflowDir, "rev-parse", "--verify", remoteBranch).Output()
			if err == nil {
				// Use merge-base to find common ancestor
				shaOut, err := exec.Command("git", "-C", workflowDir, "merge-base", "HEAD", remoteBranch).Output()
				if err == nil {
					sha := strings.TrimSpace(string(shaOut))
					if sha != "" {
						return remoteBranch, sha, nil
					}
				}
			}
		}
	}

	// Fall back to merge base with default branch
	defaultBranchOut, err := exec.Command("git", "-C", workflowDir, "symbolic-ref", "refs/remotes/origin/HEAD").Output()
	if err != nil {
		return "", "", fmt.Errorf("cannot determine default branch: %w", err)
	}
	defaultRef := strings.TrimSpace(string(defaultBranchOut))
	defaultBranch := strings.TrimPrefix(defaultRef, "refs/remotes/")

	mergeBaseOut, err := exec.Command("git", "-C", workflowDir, "merge-base", "HEAD", defaultBranch).Output()
	if err != nil {
		return "", "", fmt.Errorf("cannot find merge base with %s: %w", defaultBranch, err)
	}

	return defaultBranch, strings.TrimSpace(string(mergeBaseOut)), nil
}

func detectPatch(workflowDir string) *patchInfo {
	baseBranch, mergeBase, err := findMergeBase(workflowDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not determine merge base, skipping patch: %v\n", err)
		return nil
	}

	diffOut, err := exec.Command("git", "-C", workflowDir, "diff", "--binary", mergeBase).Output()
	if err != nil {
		return nil
	}

	content := string(diffOut)
	if strings.TrimSpace(content) == "" {
		return nil
	}

	return &patchInfo{
		baseBranch: baseBranch,
		mergeBase:  mergeBase,
		content:    content,
	}
}

var repoPattern = regexp.MustCompile(`[/:]([^/:]+/[^/.]+?)(?:\.git)?$`)

// resolveJobDeps returns the set of job names that must be included to satisfy
// the transitive `needs` dependencies of the requested jobs.
func resolveJobDeps(allJobs map[string]interface{}, requested []string) map[string]struct{} {
	needed := make(map[string]struct{})
	var walk func(name string)
	walk = func(name string) {
		if _, ok := needed[name]; ok {
			return
		}
		jobRaw, exists := allJobs[name]
		if !exists {
			return
		}
		needed[name] = struct{}{}
		job, ok := jobRaw.(map[string]interface{})
		if !ok {
			return
		}
		switch deps := job["needs"].(type) {
		case string:
			walk(deps)
		case []interface{}:
			for _, d := range deps {
				if s, ok := d.(string); ok {
					walk(s)
				}
			}
		}
	}
	for _, name := range requested {
		walk(name)
	}
	return needed
}

func resolveRepo(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("failed to get git remote URL: %w", err)
	}
	url := strings.TrimSpace(string(out))

	matches := repoPattern.FindStringSubmatch(url)
	if matches == nil {
		return "", fmt.Errorf("could not parse repo from remote URL: %s", url)
	}
	return matches[1], nil
}

func injectPatchStep(jobs map[string]interface{}, jobName, mergeBase, cacheKey string) {
	jobRaw, ok := jobs[jobName]
	if !ok {
		return
	}
	job, ok := jobRaw.(map[string]interface{})
	if !ok {
		return
	}
	stepsRaw, ok := job["steps"]
	if !ok {
		return
	}
	steps, ok := stepsRaw.([]interface{})
	if !ok {
		return
	}

	checkoutIndex := -1
	for i, stepRaw := range steps {
		step, ok := stepRaw.(map[string]interface{})
		if !ok {
			continue
		}
		uses, ok := step["uses"].(string)
		if ok && strings.HasPrefix(uses, "actions/checkout") {
			checkoutIndex = i
			break
		}
	}

	if checkoutIndex == -1 {
		fmt.Printf("Job %q: no actions/checkout step, skipping patch injection\n", jobName)
		return
	}

	// Modify checkout step to check out the merge-base commit
	checkoutStep := steps[checkoutIndex].(map[string]interface{})
	withMap, ok := checkoutStep["with"].(map[string]interface{})
	if !ok {
		withMap = make(map[string]interface{})
		checkoutStep["with"] = withMap
	}
	withMap["ref"] = mergeBase

	// Create patch application step
	patchStep := map[string]interface{}{
		"name": "Apply local patch from Depot Cache",
		"run": fmt.Sprintf(`set -euo pipefail
# Get download URL from Depot Cache service
CACHE_KEY="%s"
echo "Fetching download URL for patch..."
DOWNLOAD_RESPONSE=$(curl -fsSL -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $DEPOT_TOKEN" \
  "%s/depot.cache.v1.CacheService/GetDownloadURL" \
  -d '{"entry_type":"generic","key":"'"$CACHE_KEY"'"}')

PATCH_URL=$(echo "$DOWNLOAD_RESPONSE" | jq -r '.url')
if [ -z "$PATCH_URL" ] || [ "$PATCH_URL" = "null" ]; then
  echo "Failed to get download URL: $DOWNLOAD_RESPONSE"
  exit 1
fi

echo "Downloading patch..."
curl -fsSL "$PATCH_URL" -o /tmp/local.patch

echo "Applying patch..."
git apply --allow-empty /tmp/local.patch
rm /tmp/local.patch
echo "Patch applied successfully"`, cacheKey, cacheBaseURL),
		"env": map[string]interface{}{
			"DEPOT_TOKEN": "${{ secrets.DEPOT_TOKEN }}",
		},
	}

	// Insert patch step after checkout
	newSteps := make([]interface{}, 0, len(steps)+1)
	newSteps = append(newSteps, steps[:checkoutIndex+1]...)
	newSteps = append(newSteps, patchStep)
	newSteps = append(newSteps, steps[checkoutIndex+1:]...)
	job["steps"] = newSteps
}

func injectDebugStep(jobs map[string]interface{}, jobName string, afterStep int, patchInjected bool) error {
	jobRaw, ok := jobs[jobName]
	if !ok {
		return fmt.Errorf("job %q not found", jobName)
	}
	job, ok := jobRaw.(map[string]interface{})
	if !ok {
		return fmt.Errorf("job %q is not a map", jobName)
	}
	stepsRaw, ok := job["steps"]
	if !ok {
		return fmt.Errorf("job %q has no steps", jobName)
	}
	steps, ok := stepsRaw.([]interface{})
	if !ok {
		return fmt.Errorf("job %q steps is not a list", jobName)
	}

	debugStep := map[string]interface{}{
		"name": "Depot SSH Debug",
		"run": "echo '::depot-ssh-ready::'\n" +
			"echo 'SSH session active. Run: touch /tmp/depot-continue to resume workflow.'\n" +
			"while [ ! -f /tmp/depot-continue ]; do sleep 5; done\n" +
			"echo 'Continuing workflow...'",
	}

	insertAt := afterStep
	if patchInjected {
		// Find checkout index to adjust for the injected patch step
		checkoutIndex := -1
		for i, stepRaw := range steps {
			step, ok := stepRaw.(map[string]interface{})
			if !ok {
				continue
			}
			uses, ok := step["uses"].(string)
			if ok && strings.HasPrefix(uses, "actions/checkout") {
				checkoutIndex = i
				break
			}
		}

		if checkoutIndex != -1 && afterStep > checkoutIndex {
			insertAt = afterStep + 1
		}
	}

	if insertAt > len(steps) {
		return fmt.Errorf("--ssh-after-step %d is out of range (workflow has %d steps)", afterStep, len(steps))
	}

	newSteps := make([]interface{}, 0, len(steps)+1)
	newSteps = append(newSteps, steps[:insertAt]...)
	newSteps = append(newSteps, debugStep)
	newSteps = append(newSteps, steps[insertAt:]...)
	job["steps"] = newSteps

	return nil
}

// waitForTmateSSH polls the job's logs until the tmate SSH connection string
// appears, then returns the SSH target (e.g. "user@nyc1.tmate.io").
func waitForTmateSSH(ctx context.Context, token, orgID, runID, jobKey string) (string, error) {
	const pollInterval = 3 * time.Second
	const timeout = 10 * time.Minute

	tmatePattern := regexp.MustCompile(`SSH: ssh (\S+@\S+\.tmate\.io)`)
	deadline := time.Now().Add(timeout)

	const (
		stateInit = iota
		stateWaitingForJob
		stateWaitingForStart
		stateWaitingForLogs
	)
	currentState := stateInit

	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for tmate session (waited %s)", timeout)
		}

		resp, err := api.CIGetRunStatus(ctx, token, orgID, runID)
		if err != nil {
			return "", fmt.Errorf("failed to get run status: %w", err)
		}

		targetJob, err := findJob(resp, jobKey, "")
		if err != nil {
			if isRetryableJobError(err) {
				if resp.Status == "finished" || resp.Status == "failed" || resp.Status == "cancelled" {
					return "", fmt.Errorf("%s (run status: %s)", err, resp.Status)
				}
				if currentState != stateWaitingForJob {
					fmt.Fprintf(os.Stderr, "Waiting for job to be created...\n")
					currentState = stateWaitingForJob
				}
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(pollInterval):
				}
				continue
			}
			return "", err
		}

		// Latch the resolved job key so subsequent polls use exact matching.
		jobKey = targetJob.JobKey

		attempt := latestAttempt(targetJob)
		if attempt == nil {
			if currentState != stateWaitingForStart {
				fmt.Fprintf(os.Stderr, "Waiting for job %q to start...\n", targetJob.JobKey)
				currentState = stateWaitingForStart
			}
		} else {
			switch attempt.Status {
			case "finished", "failed", "cancelled":
				return "", fmt.Errorf("job %q completed before tmate session started (status: %s)", targetJob.JobKey, attempt.Status)
			default:
				lines, err := api.CIGetJobAttemptLogs(ctx, token, orgID, attempt.AttemptId)
				if err == nil {
					for _, line := range lines {
						if matches := tmatePattern.FindStringSubmatch(line.Body); matches != nil {
							return matches[1], nil
						}
					}
				}
				if currentState != stateWaitingForLogs {
					fmt.Fprintf(os.Stderr, "Waiting for tmate session in logs...\n")
					currentState = stateWaitingForLogs
				}
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// execSSH runs an interactive SSH session to the given target (user@host).
func execSSH(target string) error {
	cmd := exec.Command("ssh",
		"-t",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		target,
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// validStatuses are the user-facing status names accepted by --status.
var validStatuses = []string{"queued", "running", "finished", "failed", "cancelled"}

func parseStatus(s string) (civ1.CIRunStatus, error) {
	switch strings.ToLower(s) {
	case "queued":
		return civ1.CIRunStatus_CI_RUN_STATUS_QUEUED, nil
	case "running":
		return civ1.CIRunStatus_CI_RUN_STATUS_RUNNING, nil
	case "finished":
		return civ1.CIRunStatus_CI_RUN_STATUS_FINISHED, nil
	case "failed":
		return civ1.CIRunStatus_CI_RUN_STATUS_FAILED, nil
	case "cancelled":
		return civ1.CIRunStatus_CI_RUN_STATUS_CANCELLED, nil
	default:
		return 0, fmt.Errorf("invalid status %q, valid values: %s", s, strings.Join(validStatuses, ", "))
	}
}

func formatStatus(s civ1.CIRunStatus) string {
	switch s {
	case civ1.CIRunStatus_CI_RUN_STATUS_QUEUED:
		return "queued"
	case civ1.CIRunStatus_CI_RUN_STATUS_RUNNING:
		return "running"
	case civ1.CIRunStatus_CI_RUN_STATUS_FINISHED:
		return "finished"
	case civ1.CIRunStatus_CI_RUN_STATUS_FAILED:
		return "failed"
	case civ1.CIRunStatus_CI_RUN_STATUS_CANCELLED:
		return "cancelled"
	default:
		return "unknown"
	}
}

// waitForLogMarker polls the job attempt logs until a line containing marker
// appears. This is used to detect when the injected debug step is running.
func waitForLogMarker(ctx context.Context, token, orgID, runID, jobKey, marker string) error {
	const pollInterval = 3 * time.Second

	for {

		// Resolve the latest attempt ID for the job.
		resp, err := api.CIGetRunStatus(ctx, token, orgID, runID)
		if err != nil {
			// Transient error, keep polling.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
			}
			continue
		}

		targetJob, err := findJob(resp, jobKey, "")
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
			}
			continue
		}

		attempt := latestAttempt(targetJob)
		if attempt == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
			}
			continue
		}

		// Early exit if the job has already completed — the marker will never appear.
		switch attempt.Status {
		case "finished", "failed", "cancelled":
			return fmt.Errorf("job completed before debug step was reached (status: %s)", attempt.Status)
		}

		lines, err := api.CIGetJobAttemptLogs(ctx, token, orgID, attempt.AttemptId)
		if err == nil {
			for _, line := range lines {
				if strings.Contains(line.Body, marker) {
					return nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func NewCmdRunList() *cobra.Command {
	var (
		orgID    string
		token    string
		statuses []string
		n        int32
		output   string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List CI runs",
		Long:  `List CI runs for your organization.`,
		Example: `  # List runs (defaults to queued and running)
  depot ci run list

  # List failed runs
  depot ci run list --status failed

  # List finished and failed runs
  depot ci run list --status finished --status failed

  # List the 5 most recent runs
  depot ci run list -n 5

  # Output as JSON
  depot ci run list --output json`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if n <= 0 {
				return fmt.Errorf("page size (-n) must be greater than 0")
			}

			ctx := cmd.Context()

			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}

			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			var protoStatuses []civ1.CIRunStatus
			for _, s := range statuses {
				ps, err := parseStatus(s)
				if err != nil {
					return err
				}
				protoStatuses = append(protoStatuses, ps)
			}

			runs, err := api.CIListRuns(ctx, tokenVal, orgID, protoStatuses, n)
			if err != nil {
				return fmt.Errorf("failed to list runs: %w", err)
			}

			if output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(runs)
			}

			if len(runs) == 0 {
				if len(statuses) == 0 {
					fmt.Println("No queued or active runs found. Use --status to view other runs.")
				} else {
					fmt.Println("No matching runs found.")
				}
				return nil
			}

			fmt.Printf("%-24s %-30s %-12s %-10s %-12s %s\n", "RUN ID", "REPO", "SHA", "STATUS", "TRIGGER", "CREATED")
			fmt.Printf("%-24s %-30s %-12s %-10s %-12s %s\n",
				strings.Repeat("-", 24),
				strings.Repeat("-", 30),
				strings.Repeat("-", 12),
				strings.Repeat("-", 10),
				strings.Repeat("-", 12),
				strings.Repeat("-", 20),
			)

			for _, run := range runs {
				repo := run.Repo
				if len(repo) > 30 {
					repo = repo[:27] + "..."
				}

				sha := run.Sha
				if len(sha) > 12 {
					sha = sha[:12]
				}

				trigger := run.Trigger
				if len(trigger) > 12 {
					trigger = trigger[:9] + "..."
				}

				fmt.Printf("%-24s %-30s %-12s %-10s %-12s %s\n",
					run.RunId,
					repo,
					sha,
					formatStatus(run.Status),
					trigger,
					run.CreatedAt,
				)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringSliceVar(&statuses, "status", nil, "Filter by status (repeatable: queued, running, finished, failed, cancelled)")
	cmd.Flags().Int32VarP(&n, "n", "n", 50, "Number of runs to return")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")

	return cmd
}
