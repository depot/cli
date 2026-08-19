package ci

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

type runRepository struct {
	forge civ1.Forge
	repo  string
}

func parseRunForge(value string) (civ1.Forge, error) {
	switch value {
	case "github":
		return civ1.Forge_FORGE_GITHUB, nil
	case "cursor-origin":
		return civ1.Forge_FORGE_CURSOR_ORIGIN, nil
	default:
		return civ1.Forge_FORGE_UNSPECIFIED, fmt.Errorf("unsupported forge %q; expected github or cursor-origin", value)
	}
}

func parseRunRepository(remoteURL string) (runRepository, bool) {
	host, path, ok := splitGitRemoteURL(strings.TrimSpace(remoteURL))
	if !ok {
		return runRepository{}, false
	}

	host = strings.ToLower(host)
	path = strings.Trim(strings.TrimSuffix(path, ".git"), "/")
	if host == "origin.cursor.com" {
		path = strings.TrimPrefix(path, "git/")
	}

	var forge civ1.Forge
	switch {
	case host == "github.com":
		forge = civ1.Forge_FORGE_GITHUB
	case host == "origin.cursor.com":
		forge = civ1.Forge_FORGE_CURSOR_ORIGIN
	default:
		return runRepository{}, false
	}

	if path == "" || strings.ContainsAny(path, "?#") {
		return runRepository{}, false
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return runRepository{}, false
	}
	return runRepository{forge: forge, repo: path}, true
}

func splitGitRemoteURL(remoteURL string) (host, path string, ok bool) {
	if !strings.Contains(remoteURL, "://") {
		colon := strings.IndexByte(remoteURL, ':')
		if colon <= 0 || colon == len(remoteURL)-1 {
			return "", "", false
		}
		hostPart := remoteURL[:colon]
		if at := strings.LastIndexByte(hostPart, '@'); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		return hostPart, remoteURL[colon+1:], hostPart != ""
	}

	parsed, err := url.Parse(remoteURL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "ssh") {
		return "", "", false
	}
	return parsed.Hostname(), parsed.Path, true
}

func detectRunRepositories(dir string) (*runRepository, []runRepository) {
	out, err := exec.Command("git", "-C", dir, "remote").Output()
	if err != nil {
		return nil, nil
	}

	seen := make(map[runRepository]struct{})
	var origin *runRepository
	var repositories []runRepository
	for _, name := range strings.Fields(string(out)) {
		urlOut, err := exec.Command("git", "-C", dir, "remote", "get-url", "--all", name).Output()
		if err != nil {
			continue
		}
		for _, remoteURL := range strings.Split(strings.TrimSpace(string(urlOut)), "\n") {
			repository, ok := parseRunRepository(remoteURL)
			if !ok {
				continue
			}
			if _, ok := seen[repository]; ok {
				continue
			}
			seen[repository] = struct{}{}
			repositories = append(repositories, repository)
			if name == "origin" && origin == nil {
				origin = &repository
			}
		}
	}
	return origin, repositories
}

func resolveRunRepository(dir, explicitRepo, forgeFlag string) (runRepository, error) {
	var selectedForge civ1.Forge
	var err error
	if forgeFlag != "" {
		selectedForge, err = parseRunForge(forgeFlag)
		if err != nil {
			return runRepository{}, err
		}
	}

	origin, detected := detectRunRepositories(dir)
	if explicitRepo != "" {
		if selectedForge != civ1.Forge_FORGE_UNSPECIFIED {
			return runRepository{forge: selectedForge, repo: explicitRepo}, nil
		}
		forges := make(map[civ1.Forge]struct{})
		for _, repository := range detected {
			forges[repository.forge] = struct{}{}
		}
		if len(forges) > 1 {
			return runRepository{}, fmt.Errorf("multiple supported forges found in git remotes; use --forge github|cursor-origin")
		}
		for forge := range forges {
			return runRepository{forge: forge, repo: explicitRepo}, nil
		}
		return runRepository{forge: civ1.Forge_FORGE_GITHUB, repo: explicitRepo}, nil
	}

	if selectedForge != civ1.Forge_FORGE_UNSPECIFIED {
		filtered := detected[:0]
		for _, repository := range detected {
			if repository.forge == selectedForge {
				filtered = append(filtered, repository)
			}
		}
		detected = filtered
		if origin != nil && origin.forge != selectedForge {
			origin = nil
		}
	}
	if len(detected) == 0 {
		return runRepository{}, fmt.Errorf("no supported repository found in git remotes; use --repo and optionally --forge (github or cursor-origin)")
	}
	if len(detected) > 1 {
		forges := make(map[civ1.Forge]struct{})
		for _, repository := range detected {
			forges[repository.forge] = struct{}{}
		}
		if len(forges) > 1 {
			return runRepository{}, fmt.Errorf("multiple supported forges found in git remotes; use --forge github|cursor-origin")
		}
		if origin != nil {
			return *origin, nil
		}
		return runRepository{}, fmt.Errorf("multiple repositories found for the selected forge; use --repo to select one")
	}
	return detected[0], nil
}
