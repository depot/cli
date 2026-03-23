package ci

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/depot/cli/pkg/ci/migrate"
)

func parseWorkflowDirWithWarnings(workflowsDir string) ([]*migrate.WorkflowFile, []string, error) {
	workflows := make([]*migrate.WorkflowFile, 0)
	warnings := make([]string, 0)

	err := filepath.WalkDir(workflowsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}

		workflow, parseErr := migrate.ParseWorkflowFile(path)
		if parseErr != nil {
			warnings = append(warnings, fmt.Sprintf("skipped invalid workflow %s: %v", path, parseErr))
			return nil
		}

		workflows = append(workflows, workflow)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	sort.Slice(workflows, func(i, j int) bool {
		return workflows[i].Path < workflows[j].Path
	})

	return workflows, warnings, nil
}

func detectSecretsFromWorkflows(workflows []*migrate.WorkflowFile) ([]string, error) {
	all := make([]string, 0)
	for _, workflow := range workflows {
		secrets, err := migrate.DetectSecretsFromFile(workflow.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to detect secrets in %s: %w", workflow.Path, err)
		}
		all = append(all, secrets...)
	}

	if len(all) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{}, len(all))
	for _, secret := range all {
		if secret != "" {
			seen[secret] = struct{}{}
		}
	}

	deduped := make([]string, 0, len(seen))
	for secret := range seen {
		deduped = append(deduped, secret)
	}
	sort.Strings(deduped)

	return deduped, nil
}

func detectVariablesFromWorkflows(workflows []*migrate.WorkflowFile) ([]string, error) {
	all := make([]string, 0)
	for _, workflow := range workflows {
		variables, err := migrate.DetectVariablesFromFile(workflow.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to detect variables in %s: %w", workflow.Path, err)
		}
		all = append(all, variables...)
	}

	if len(all) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{}, len(all))
	for _, v := range all {
		if v != "" {
			seen[v] = struct{}{}
		}
	}

	deduped := make([]string, 0, len(seen))
	for v := range seen {
		deduped = append(deduped, v)
	}
	sort.Strings(deduped)

	return deduped, nil
}

// detectRepoFromGitRemote attempts to extract owner/repo from the origin remote URL.
func detectRepoFromGitRemote(dir string) string {
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return parseGitHubRepo(strings.TrimSpace(string(out)))
}

func parseGitHubRepo(remoteURL string) string {
	// SSH: git@github.com:owner/repo.git
	if strings.HasPrefix(remoteURL, "git@") {
		idx := strings.Index(remoteURL, ":")
		if idx < 0 {
			return ""
		}
		path := remoteURL[idx+1:]
		path = strings.TrimSuffix(path, ".git")
		parts := strings.SplitN(path, "/", 3)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}

	// HTTPS: https://github.com/owner/repo.git
	u, err := url.Parse(remoteURL)
	if err != nil {
		return ""
	}
	path := strings.TrimPrefix(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimRight(path, "/")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
