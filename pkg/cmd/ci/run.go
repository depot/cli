package ci

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const cacheBaseURL = "https://cache.depot.dev"

func NewCmdRun() *cobra.Command {
	var (
		token        string
		workflowPath string
		jobNames     []string
		sshAfterStep int
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a local CI workflow [beta]",
		Long: `Run a local CI workflow YAML via the Depot CI API.

If there are uncommitted changes relative to the default branch, they are automatically
uploaded as a patch and applied during the workflow run.

This command is in beta and subject to change.`,
		Example: `  # Run a workflow
  depot ci run --workflow .depot/workflows/ci.yml

  # Run specific jobs
  depot ci run --workflow .depot/workflows/ci.yml --job build --job test

  # Debug with SSH after a specific step
  depot ci run --workflow .depot/workflows/ci.yml --job build --ssh-after-step 3`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workflowPath == "" {
				return cmd.Help()
			}

			ctx := cmd.Context()

			if sshAfterStep > 0 && len(jobNames) != 1 {
				return fmt.Errorf("--ssh-after-step requires exactly one --job")
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

			// Pare workflow to selected jobs if a subset was specified
			if len(jobNames) > 0 {
				paredJobs := make(map[string]interface{})
				for _, name := range jobNames {
					paredJobs[name] = jobs[name]
				}
				workflow["jobs"] = paredJobs
				jobs = paredJobs
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
				fmt.Printf("Default branch: %s\n", patch.defaultBranch)
				fmt.Printf("Merge base: %s\n", patch.mergeBase)
				fmt.Printf("Patch size: %d bytes\n", len(patch.content))

				hash := sha256.Sum256([]byte(patch.content))
				patchHash := fmt.Sprintf("%x", hash)[:16]
				cacheKey := fmt.Sprintf("patch/%s/%s", patch.mergeBase[:12], patchHash)
				fmt.Printf("Cache key: %s\n", cacheKey)

				if err := api.UploadPatchToCache(ctx, tokenVal, cacheKey, patch.content); err != nil {
					return fmt.Errorf("failed to upload patch: %w", err)
				}
				fmt.Println("Patch uploaded to Depot Cache")

				// Inject patch step into each selected job that has actions/checkout
				for _, jobName := range selectedJobs {
					injectPatchStep(jobs, jobName, patch.mergeBase, cacheKey)
				}
			}

			// Insert tmate debug step if requested
			if sshAfterStep > 0 {
				jobName := jobNames[0]
				if err := injectTmateStep(jobs, jobName, sshAfterStep, patch != nil); err != nil {
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
				fmt.Printf("Inserting tmate step after step %d\n", sshAfterStep)
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

			if len(jobNames) > 0 {
				job := jobNames[0]
				req.Job = &job
			}

			resp, err := api.CIRun(ctx, tokenVal, req)
			if err != nil {
				return fmt.Errorf("failed to start CI run: %w", err)
			}

			fmt.Printf("Org: %s\n", resp.OrgId)
			fmt.Printf("Run: %s\n", resp.RunId)

			return nil
		},
	}

	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&workflowPath, "workflow", "", "Path to workflow YAML file")
	cmd.Flags().StringSliceVar(&jobNames, "job", nil, "Job name(s) to run (repeatable; omit to run all)")
	cmd.Flags().IntVar(&sshAfterStep, "ssh-after-step", 0, "1-based step index to insert a tmate debug step after (requires single --job)")

	return cmd
}

type patchInfo struct {
	defaultBranch string
	mergeBase     string
	content       string
}

func detectPatch(workflowDir string) *patchInfo {
	defaultBranchOut, err := exec.Command("git", "-C", workflowDir, "symbolic-ref", "refs/remotes/origin/HEAD").Output()
	if err != nil {
		return nil
	}
	defaultBranch := strings.TrimSpace(string(defaultBranchOut))
	defaultBranch = strings.TrimPrefix(defaultBranch, "refs/remotes/origin/")

	mergeBaseOut, err := exec.Command("git", "-C", workflowDir, "merge-base", "HEAD", "origin/"+defaultBranch).Output()
	if err != nil {
		return nil
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOut))

	diffOut, err := exec.Command("git", "-C", workflowDir, "diff", "--binary", mergeBase).Output()
	if err != nil {
		return nil
	}

	content := string(diffOut)
	if strings.TrimSpace(content) == "" {
		return nil
	}

	return &patchInfo{
		defaultBranch: defaultBranch,
		mergeBase:     mergeBase,
		content:       content,
	}
}

var repoPattern = regexp.MustCompile(`[/:]([^/:]+/[^/.]+?)(?:\.git)?$`)

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

func injectTmateStep(jobs map[string]interface{}, jobName string, afterStep int, patchInjected bool) error {
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

	tmateStep := map[string]interface{}{
		"uses": "mxschmitt/action-tmate@v3",
		"with": map[string]interface{}{
			"limit-access-to-actor": "false",
		},
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
	newSteps = append(newSteps, tmateStep)
	newSteps = append(newSteps, steps[insertAt:]...)
	job["steps"] = newSteps

	return nil
}
