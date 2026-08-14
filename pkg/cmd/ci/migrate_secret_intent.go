package ci

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/oidc"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
)

var (
	secretMigrationNamePattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	secretMigrationRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	secretMigrationSCPRemotePattern  = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)
)

type secretMigrationIntentRegistrar interface {
	RegisterSecretMigrationIntent(context.Context, *connect.Request[civ2.SecretMigrationIntent]) (*connect.Response[civ2.RegisterSecretMigrationIntentResponse], error)
}

type createSecretMigrationOptions struct {
	dir          string
	remote       string
	token        string
	orgID        string
	secrets      []string
	variables    []string
	branchPrefix string
	now          time.Time
	registrar    secretMigrationIntentRegistrar
}

type createSecretMigrationResult struct {
	branchName string
	commitSHA  string
	intentID   string
}

// createSecretMigration creates a local branch containing a one-shot workflow without changing
// the caller's worktree, then registers its commit as a short-lived migration intent. The caller
// decides when this route is appropriate and asks the user to push the returned branch.
func createSecretMigration(ctx context.Context, opts createSecretMigrationOptions) (*createSecretMigrationResult, error) {
	if err := validateSecretMigrationAuth(opts.token, opts.orgID); err != nil {
		return nil, err
	}

	if len(opts.secrets) == 0 && len(opts.variables) == 0 {
		return nil, fmt.Errorf("at least one secret or variable is required")
	}

	dir := opts.dir
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	remote := opts.remote
	if strings.TrimSpace(remote) == "" {
		remote = "origin"
	}
	remoteCloneURL, err := runGit(ctx, dir, "remote", "get-url", "--push", remote)
	if err != nil {
		return nil, fmt.Errorf("git remote %q is not configured: %w", remote, err)
	}
	repositoryURL, targetRepo, err := normalizeSecretMigrationRemoteURL(remoteCloneURL)
	if err != nil {
		return nil, fmt.Errorf("git remote %q is not a supported repository: %w", remote, err)
	}

	now := opts.now
	if now.IsZero() {
		now = time.Now()
	}
	timestamp := now.UnixMilli()
	branchPrefix := opts.branchPrefix
	if branchPrefix == "" {
		branchPrefix = oidc.SecretMigrationBranchPrefix
	}
	if strings.Contains(branchPrefix, "${{") {
		return nil, fmt.Errorf("invalid branch prefix %q: GitHub expression syntax is not allowed", branchPrefix)
	}
	if _, err := runGit(ctx, dir, "check-ref-format", "--branch", branchPrefix+"0123456789"); err != nil {
		return nil, fmt.Errorf("invalid branch prefix %q: %w", branchPrefix, err)
	}
	workflowName := fmt.Sprintf("migrate-secrets-to-depot-ci-%d.yml", timestamp)
	workflowContent, err := generateSecretMigrationWorkflow(targetRepo, branchPrefix, opts.secrets, opts.variables)
	if err != nil {
		return nil, err
	}
	baseSHA, err := resolveSecretMigrationBaseSHA(ctx, dir, remote)
	if err != nil {
		return nil, err
	}

	tempRoot, err := os.MkdirTemp("", "depot-secret-migration-")
	if err != nil {
		return nil, fmt.Errorf("failed to create a temporary directory: %w", err)
	}
	defer os.RemoveAll(tempRoot)

	worktree := filepath.Join(tempRoot, "worktree")
	if _, err := runGit(ctx, dir, "worktree", "add", "--detach", "--no-checkout", worktree, baseSHA); err != nil {
		return nil, fmt.Errorf("failed to create a temporary git worktree: %w", err)
	}
	defer func() {
		_, _ = runGit(context.Background(), dir, "worktree", "remove", "--force", worktree)
	}()
	if _, err := runGit(ctx, worktree, "read-tree", "HEAD"); err != nil {
		return nil, fmt.Errorf("failed to initialize the migration commit index: %w", err)
	}

	workflowPath := filepath.Join(worktree, ".github", "workflows", workflowName)
	if err := os.MkdirAll(filepath.Dir(workflowPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create the workflow directory: %w", err)
	}
	if err := os.WriteFile(workflowPath, []byte(workflowContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write the migration workflow: %w", err)
	}
	if _, err := runGit(ctx, worktree, "add", "--", filepath.ToSlash(filepath.Join(".github", "workflows", workflowName))); err != nil {
		return nil, fmt.Errorf("failed to stage the migration workflow: %w", err)
	}
	if _, err := runGit(
		ctx,
		worktree,
		"-c", "user.name=Depot CLI",
		"-c", "user.email=hello@depot.dev",
		"-c", "commit.gpgsign=false",
		"-c", "core.hooksPath="+filepath.Join(tempRoot, "hooks"),
		"commit", "--no-verify", "-m", "Add workflow to migrate secrets and variables to Depot CI",
	); err != nil {
		return nil, fmt.Errorf("failed to create the migration commit: %w", err)
	}
	commitSHA, err := runGit(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the migration commit: %w", err)
	}

	registrar := opts.registrar
	if registrar == nil {
		registrar = api.NewWorkflowMigrationClient()
	}
	intent := &civ2.SecretMigrationIntent{Sha: commitSHA, RepositoryUrl: repositoryURL}
	response, err := registrar.RegisterSecretMigrationIntent(ctx, api.WithAuthenticationAndOrg(connect.NewRequest(intent), opts.token, opts.orgID))
	if err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return nil, fmt.Errorf("failed to register the secret migration: %s", connectErr.Message())
		}
		return nil, fmt.Errorf("failed to register the secret migration: %w", err)
	}
	intentID := strings.TrimSpace(response.Msg.GetId())
	if !oidc.SecretMigrationIntentIDPattern.MatchString(intentID) {
		return nil, fmt.Errorf("failed to register the secret migration: invalid intent ID")
	}
	branchName := branchPrefix + intentID
	if _, err := runGit(ctx, dir, "branch", branchName, commitSHA); err != nil {
		return nil, fmt.Errorf("failed to create the local migration branch: %w", err)
	}

	return &createSecretMigrationResult{
		branchName: branchName,
		commitSHA:  commitSHA,
		intentID:   intentID,
	}, nil
}

func resolveSecretMigrationBaseSHA(ctx context.Context, dir, remote string) (string, error) {
	remotePrefix := remote + "/"
	if upstream, err := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil && strings.HasPrefix(upstream, remotePrefix) {
		if sha, err := resolveSecretMigrationCommit(ctx, dir, upstream); err == nil {
			return sha, nil
		}
	}

	if branch, err := runGit(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		if sha, err := resolveSecretMigrationCommit(ctx, dir, "refs/remotes/"+remote+"/"+branch); err == nil {
			return sha, nil
		}
	}

	remoteHead := "refs/remotes/" + remote + "/HEAD"
	if sha, err := resolveSecretMigrationCommit(ctx, dir, remoteHead); err == nil {
		return sha, nil
	}

	refs, err := runGit(ctx, dir, "for-each-ref", "--format=%(refname)", "refs/remotes/"+remote)
	if err == nil {
		var candidates []string
		for _, ref := range strings.Fields(refs) {
			if ref != remoteHead {
				candidates = append(candidates, ref)
			}
		}
		if len(candidates) == 1 {
			if sha, err := resolveSecretMigrationCommit(ctx, dir, candidates[0]); err == nil {
				return sha, nil
			}
		}
	}

	return "", fmt.Errorf("failed to resolve a remote-tracking base commit for %q; set an upstream branch or configure the remote's default branch", remote)
}

func resolveSecretMigrationCommit(ctx context.Context, dir, ref string) (string, error) {
	return runGit(ctx, dir, "rev-parse", "--verify", ref+"^{commit}")
}

func detectSecretMigrationRemote(ctx context.Context, dir string) (string, string, error) {
	remotes, err := runGit(ctx, dir, "remote")
	if err != nil {
		return "", "", fmt.Errorf("failed to list git remotes: %w", err)
	}

	names := strings.Fields(remotes)
	for i, name := range names {
		if name == "origin" {
			names[0], names[i] = names[i], names[0]
			break
		}
	}
	for _, name := range names {
		remoteURL, err := runGit(ctx, dir, "remote", "get-url", "--push", name)
		if err != nil {
			continue
		}
		repositoryURL, _, err := normalizeSecretMigrationRemoteURL(remoteURL)
		if err == nil {
			return name, repositoryURL, nil
		}
	}
	return "", "", fmt.Errorf("could not detect a supported repository from git push remotes")
}

func normalizeSecretMigrationRemoteURL(value string) (string, string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", "", fmt.Errorf("remote URL is empty")
	}

	var host, repositoryPath string
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", fmt.Errorf("invalid remote URL %q", value)
		}
		host = parsed.Hostname()
		repositoryPath = parsed.Path
	} else if match := secretMigrationSCPRemotePattern.FindStringSubmatch(raw); match != nil {
		host = match[1]
		repositoryPath = match[2]
	} else {
		parts := strings.SplitN(raw, "/", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("invalid remote URL %q", value)
		}
		host = parts[0]
		repositoryPath = parts[1]
	}

	host = strings.ToLower(strings.TrimSpace(host))
	repositoryPath = strings.Trim(strings.TrimSpace(repositoryPath), "/")
	if strings.HasSuffix(strings.ToLower(repositoryPath), ".git") {
		repositoryPath = repositoryPath[:len(repositoryPath)-len(".git")]
	}
	segments := strings.Split(repositoryPath, "/")
	if host == "origin.cursor.com" && len(segments) == 3 && segments[0] == "git" {
		segments = segments[1:]
	}
	if (host != "github.com" && host != "origin.cursor.com") || len(segments) != 2 {
		return "", "", fmt.Errorf("remote must point to github.com/owner/repo or origin.cursor.com/namespace/repo")
	}
	repository := strings.Join(segments, "/")
	if !secretMigrationRepositoryPattern.MatchString(repository) {
		return "", "", fmt.Errorf("remote repository must be in owner/repo format")
	}
	return host + "/" + repository, repository, nil
}

func validateSecretMigrationAuth(token, orgID string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("missing API token")
	}
	if !strings.HasPrefix(token, "depot_org_") && strings.TrimSpace(orgID) == "" {
		return fmt.Errorf("missing organization ID for API token")
	}
	return nil
}

func generateSecretMigrationWorkflow(targetRepo, branchPrefix string, secrets, variables []string) (string, error) {
	secretNames, err := secretMigrationNames(secrets, "secret")
	if err != nil {
		return "", err
	}
	secretNames = omitSecretMigrationNames(secretNames, "GITHUB_TOKEN", "DEPOT_TOKEN")
	variableNames, err := secretMigrationNames(variables, "variable")
	if err != nil {
		return "", err
	}
	variableNames = omitSecretMigrationNames(variableNames, "DEPOT_TOKEN")
	if len(secretNames) == 0 && len(variableNames) == 0 {
		return "", fmt.Errorf("at least one secret or variable is required")
	}

	lines := []string{
		"name: Import secrets and variables to Depot CI",
		"on: push",
		"",
		"permissions:",
		"  id-token: write",
		"  contents: read",
		"",
		"jobs:",
	}
	importJobs := make([]string, 0, 2)
	if len(secretNames) > 0 {
		importJobs = append(importJobs, "secrets")
		lines = append(lines,
			"  secrets:",
			"    runs-on: ubuntu-latest",
			"    steps:",
			"      - uses: depot/setup-action@v1",
			"      - name: Import secrets",
		)
		lines = append(lines, "        env:")
		for _, name := range secretNames {
			lines = append(lines, fmt.Sprintf("          %s: ${{ secrets.%s }}", name, name))
		}
		assignments := make([]string, 0, len(secretNames))
		for _, name := range secretNames {
			assignments = append(assignments, fmt.Sprintf(`%s="$%s"`, name, name))
		}
		lines = append(lines,
			"        run: |",
			"          unset DEPOT_TOKEN",
			fmt.Sprintf(
				"          %s=%s depot ci secrets add %s --repo %s",
				oidc.SecretMigrationBranchPrefixEnv,
				shellQuote(branchPrefix),
				strings.Join(assignments, " "),
				targetRepo,
			),
		)
	}
	if len(variableNames) > 0 {
		importJobs = append(importJobs, "variables")
		lines = append(lines,
			"  variables:",
			"    runs-on: ubuntu-latest",
			"    steps:",
			"      - uses: depot/setup-action@v1",
			"      - name: Import variables",
			"        env:",
		)
		assignments := make([]string, 0, len(variableNames))
		for _, name := range variableNames {
			lines = append(lines, fmt.Sprintf("          %s: ${{ vars.%s }}", name, name))
			assignments = append(assignments, fmt.Sprintf(`%s="$%s"`, name, name))
		}
		lines = append(lines,
			"        run: |",
			"          unset DEPOT_TOKEN",
			fmt.Sprintf(
				"          %s=%s depot ci vars add %s --repo %s",
				oidc.SecretMigrationBranchPrefixEnv,
				shellQuote(branchPrefix),
				strings.Join(assignments, " "),
				targetRepo,
			),
		)
	}
	lines = append(lines,
		"  cleanup:",
		fmt.Sprintf("    needs: [%s]", strings.Join(importJobs, ", ")),
		"    if: ${{ always() }}",
		"    runs-on: ubuntu-latest",
		"    permissions:",
		"      contents: write",
		"    steps:",
		"      - name: Delete migration branch",
		"        env:",
		"          GH_TOKEN: ${{ github.token }}",
		fmt.Sprintf("          %s: %s", oidc.SecretMigrationBranchPrefixEnv, strconv.Quote(branchPrefix)),
		"        run: |",
		fmt.Sprintf(`          intent_id="${GITHUB_REF_NAME#"$%s"}"`, oidc.SecretMigrationBranchPrefixEnv),
		`          if [ "$intent_id" = "$GITHUB_REF_NAME" ] || [ "${#intent_id}" -ne 10 ]; then echo "Refusing to delete unexpected branch ${GITHUB_REF_NAME}" >&2; exit 1; fi`,
		`          case "$intent_id" in *[!0123456789bcdfghjklmnpqrstvwxz]*) echo "Refusing to delete unexpected branch ${GITHUB_REF_NAME}" >&2; exit 1;; esac`,
		"          gh api --method DELETE \"repos/${GITHUB_REPOSITORY}/git/refs/heads/${GITHUB_REF_NAME}\"",
	)
	return strings.Join(append(lines, ""), "\n"), nil
}

func secretMigrationNames(names []string, kind string) ([]string, error) {
	seen := make(map[string]struct{}, len(names))
	for _, rawName := range names {
		name := strings.TrimSpace(rawName)
		if !secretMigrationNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid %s name %q", kind, rawName)
		}
		seen[name] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return strings.TrimSpace(string(output)), nil
}
