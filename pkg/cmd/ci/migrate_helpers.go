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

// detectRepoFromGitRemote attempts to extract owner/repo from a GitHub remote URL.
// It checks "origin" first; if origin is a GitHub URL, it is used immediately.
// Otherwise, it falls back to checking all remotes in the order returned by git.
func detectRepoFromGitRemote(dir string) string {
	// Check origin first — it's the most common convention.
	originURL, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err == nil {
		if repo := parseGitHubRepo(strings.TrimSpace(string(originURL))); repo != "" {
			return repo
		}
	}

	// Fall back to scanning all remotes.
	cmd := exec.Command("git", "-C", dir, "remote")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, name := range strings.Fields(string(out)) {
		if name == "origin" {
			continue // already checked
		}
		urlCmd := exec.Command("git", "-C", dir, "remote", "get-url", name)
		urlOut, err := urlCmd.Output()
		if err != nil {
			continue
		}
		if repo := parseGitHubRepo(strings.TrimSpace(string(urlOut))); repo != "" {
			return repo
		}
	}
	return ""
}

func parseGitHubRepo(remoteURL string) string {
	// SSH: git@github.com:owner/repo.git
	if strings.HasPrefix(remoteURL, "git@") {
		idx := strings.Index(remoteURL, ":")
		if idx < 0 {
			return ""
		}
		host := remoteURL[len("git@"):idx]
		if host != "github.com" {
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
	if u.Host != "github.com" {
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
