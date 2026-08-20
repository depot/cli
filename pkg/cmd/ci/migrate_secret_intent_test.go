package ci

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/oidc"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
)

type recordingSecretMigrationRegistrar struct {
	register func(*connect.Request[civ2.SecretMigrationIntent])
	intentID string
}

func (r recordingSecretMigrationRegistrar) RegisterSecretMigrationIntent(
	_ context.Context,
	request *connect.Request[civ2.SecretMigrationIntent],
) (*connect.Response[civ2.RegisterSecretMigrationIntentResponse], error) {
	r.register(request)
	return connect.NewResponse(&civ2.RegisterSecretMigrationIntentResponse{Id: r.intentID}), nil
}

func TestValidateSecretMigrationAuth(t *testing.T) {
	if err := validateSecretMigrationAuth("depot_org_token", ""); err != nil {
		t.Fatalf("org token should not require an org ID: %v", err)
	}
	if err := validateSecretMigrationAuth("depot_api_token", "org-id"); err != nil {
		t.Fatalf("API token with org ID should be accepted: %v", err)
	}
	if err := validateSecretMigrationAuth("depot_api_token", ""); err == nil {
		t.Fatal("API token without org ID should be rejected")
	}
}

func TestGenerateSecretMigrationWorkflow(t *testing.T) {
	workflow, err := generateSecretMigrationWorkflow(
		"owner/repo",
		"automation/depot-",
		[]string{"Z_SECRET", "depot_token", "github_token", "A_SECRET", "A_SECRET"},
		[]string{"MY_VAR", "DEPOT_TOKEN"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"permissions:\n  id-token: write\n  contents: read",
		`DEPOT_SECRET_MIGRATION_BRANCH_PREFIX=automation/depot- depot ci secrets add`,
		"A_SECRET: ${{ secrets.A_SECRET }}",
		`DEPOT_SECRET_MIGRATION_BRANCH_PREFIX=automation/depot- depot ci secrets add A_SECRET="$A_SECRET" Z_SECRET="$Z_SECRET" --repo owner/repo`,
		"MY_VAR: ${{ vars.MY_VAR }}",
		`DEPOT_SECRET_MIGRATION_BRANCH_PREFIX=automation/depot- depot ci vars add MY_VAR="$MY_VAR" --repo owner/repo`,
		"  cleanup:\n    needs: [secrets, variables]\n    if: ${{ always() }}",
		"    permissions:\n      contents: write",
		`expected_branch_pattern='^automation/depot-[0123456789bcdfghjklmnpqrstvwxz]{10}$'`,
		`if [[ ! "$GITHUB_REF_NAME" =~ $expected_branch_pattern ]]; then`,
		`gh api --method DELETE "repos/${GITHUB_REPOSITORY}/git/refs/heads/${GITHUB_REF_NAME}"`,
	} {
		if !strings.Contains(workflow, expected) {
			t.Fatalf("workflow does not contain %q:\n%s", expected, workflow)
		}
	}
	if strings.Contains(strings.ToUpper(workflow), "SECRETS.GITHUB_TOKEN") {
		t.Fatalf("workflow must not migrate GITHUB_TOKEN:\n%s", workflow)
	}
	if strings.Contains(strings.ToUpper(workflow), "SECRETS.DEPOT_TOKEN") || strings.Contains(strings.ToUpper(workflow), "VARS.DEPOT_TOKEN") {
		t.Fatalf("workflow must not migrate DEPOT_TOKEN:\n%s", workflow)
	}
	if strings.Contains(workflow, "DEPOT_SECRET_MIGRATION_INTENT_ID") {
		t.Fatalf("workflow must derive the migration intent ID inside the CLI:\n%s", workflow)
	}
	if got := strings.Count(workflow, "depot ci secrets add"); got != 1 {
		t.Fatalf("workflow contains %d secret import commands, want 1:\n%s", got, workflow)
	}
	if got := strings.Count(workflow, "depot ci vars add"); got != 1 {
		t.Fatalf("workflow contains %d variable import commands, want 1:\n%s", got, workflow)
	}
	if _, err := generateSecretMigrationWorkflow("owner/repo", oidc.SecretMigrationBranchPrefix, []string{"bad-name"}, nil); err == nil {
		t.Fatal("invalid secret name should be rejected")
	}

	secretOnlyWorkflow, err := generateSecretMigrationWorkflow("owner/repo", oidc.SecretMigrationBranchPrefix, []string{"MY_SECRET"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(secretOnlyWorkflow, "    needs: [secrets]") {
		t.Fatalf("secret-only cleanup has incorrect dependencies:\n%s", secretOnlyWorkflow)
	}
	if strings.Contains(secretOnlyWorkflow, "\n          DEPOT_SECRET_MIGRATION_BRANCH_PREFIX=") || strings.Contains(secretOnlyWorkflow, "\n          DEPOT_SECRET_MIGRATION_BRANCH_PREFIX:") {
		t.Fatalf("default branch prefix should be implicit:\n%s", secretOnlyWorkflow)
	}
	if !strings.Contains(secretOnlyWorkflow, `expected_branch_pattern='^depot-migrate-secrets-[0123456789bcdfghjklmnpqrstvwxz]{10}$'`) {
		t.Fatalf("default branch pattern is missing:\n%s", secretOnlyWorkflow)
	}

	variableOnlyWorkflow, err := generateSecretMigrationWorkflow("owner/repo", oidc.SecretMigrationBranchPrefix, nil, []string{"MY_VAR"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(variableOnlyWorkflow, "    needs: [variables]") {
		t.Fatalf("variable-only cleanup has incorrect dependencies:\n%s", variableOnlyWorkflow)
	}
}

func TestNormalizeSecretMigrationRemoteURL(t *testing.T) {
	tests := []struct {
		remote     string
		qualified  string
		repository string
	}{
		{"https://github.com/Depot/CLI.git", "github.com/Depot/CLI", "Depot/CLI"},
		{"https://github.com/Depot/CLI.GIT", "github.com/Depot/CLI", "Depot/CLI"},
		{"git@github.com:depot/cli.git", "github.com/depot/cli", "depot/cli"},
		{"ssh://git@github.com/depot/cli.git", "github.com/depot/cli", "depot/cli"},
		{"https://origin.cursor.com/git/acme/api", "origin.cursor.com/acme/api", "acme/api"},
		{"git@origin.cursor.com:git/acme/api.git", "origin.cursor.com/acme/api", "acme/api"},
	}
	for _, test := range tests {
		t.Run(test.remote, func(t *testing.T) {
			qualified, repository, err := normalizeSecretMigrationRemoteURL(test.remote)
			if err != nil {
				t.Fatal(err)
			}
			if qualified != test.qualified || repository != test.repository {
				t.Fatalf("got (%q, %q), want (%q, %q)", qualified, repository, test.qualified, test.repository)
			}
		})
	}

	for _, remote := range []string{"", "/tmp/repo", "https://gitlab.com/depot/cli", "https://github.com/depot"} {
		if _, _, err := normalizeSecretMigrationRemoteURL(remote); err == nil {
			t.Fatalf("expected %q to be rejected", remote)
		}
	}
}

func TestSecretsAndVarsCreatesIntentAndLocalBranchForClientCreatedMigration(t *testing.T) {
	tests := []struct {
		name          string
		pushURL       string
		repositoryURL string
		targetRepo    string
	}{
		{name: "GitHub", pushURL: "https://github.com/owner/repo.git", repositoryURL: "github.com/owner/repo", targetRepo: "owner/repo"},
		{name: "Cursor Origin", pushURL: "https://origin.cursor.com/git/namespace/repo.git", repositoryURL: "origin.cursor.com/namespace/repo", targetRepo: "namespace/repo"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testSecretsAndVarsCreatesIntentAndLocalBranch(t, test.pushURL, test.repositoryURL, test.targetRepo)
		})
	}
}

func testSecretsAndVarsCreatesIntentAndLocalBranch(t *testing.T, pushURL, repositoryURL, targetRepo string) {
	tempDir := t.TempDir()
	remoteDir := filepath.Join(tempDir, "remote.git")
	repoDir := filepath.Join(tempDir, "repo")
	runSecretMigrationTestCommand(t, "", "git", "init", "--bare", remoteDir)
	runSecretMigrationTestCommand(t, "", "git", "init", repoDir)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	githubWorkflowsDir := filepath.Join(repoDir, ".github", "workflows")
	depotWorkflowsDir := filepath.Join(repoDir, ".depot", "workflows")
	if err := os.MkdirAll(githubWorkflowsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(depotWorkflowsDir, 0755); err != nil {
		t.Fatal(err)
	}
	githubWorkflow := "name: GitHub\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo '${{ secrets.GITHUB_SECRET }} ${{ vars.GITHUB_VAR }}'\n"
	depotWorkflow := "name: Depot\non: push\njobs:\n  test:\n    runs-on: depot-ubuntu-latest\n    steps:\n      - run: echo '${{ secrets.DEPOT_SECRET }} ${{ vars.DEPOT_VAR }}'\n"
	if err := os.WriteFile(filepath.Join(githubWorkflowsDir, "github.yml"), []byte(githubWorkflow), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depotWorkflowsDir, "depot.yml"), []byte(depotWorkflow), 0644); err != nil {
		t.Fatal(err)
	}
	runSecretMigrationTestCommand(t, repoDir, "git", "add", ".")
	runSecretMigrationTestCommand(t, repoDir, "git", "-c", "user.name=Depot Test", "-c", "user.email=test@depot.dev", "commit", "-m", "base")
	runSecretMigrationTestCommand(t, repoDir, "git", "remote", "add", "origin", "https://github.com/upstream-owner/upstream-repo.git")
	runSecretMigrationTestCommand(t, repoDir, "git", "remote", "set-url", "--push", "origin", remoteDir)
	runSecretMigrationTestCommand(t, repoDir, "git", "push", "origin", "HEAD:refs/heads/main")
	remoteBaseSHA := runSecretMigrationTestCommand(t, repoDir, "git", "rev-parse", "refs/remotes/origin/main")
	runSecretMigrationTestCommand(t, repoDir, "git", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	runSecretMigrationTestCommand(t, repoDir, "git", "remote", "set-url", "--push", "origin", pushURL)
	runSecretMigrationTestCommand(t, repoDir, "git", "config", "commit.gpgsign", "true")
	if err := os.WriteFile(filepath.Join(repoDir, "LOCAL-WIP.md"), []byte("must not be pushed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runSecretMigrationTestCommand(t, repoDir, "git", "add", "LOCAL-WIP.md")
	runSecretMigrationTestCommand(t, repoDir, "git", "-c", "user.name=Depot Test", "-c", "user.email=test@depot.dev", "-c", "commit.gpgsign=false", "commit", "-m", "local WIP")
	localWIPSHA := runSecretMigrationTestCommand(t, repoDir, "git", "rev-parse", "HEAD")

	const intentID = "0123456789"
	const branchPrefix = "automation/depot-"
	const migrationBranchName = branchPrefix + intentID
	registered := false
	registeredSHA := ""
	registrar := recordingSecretMigrationRegistrar{register: func(request *connect.Request[civ2.SecretMigrationIntent]) {
		registered = true
		if request.Header().Get("Authorization") != "Bearer depot_api_token" {
			t.Fatalf("unexpected authorization header: %q", request.Header().Get("Authorization"))
		}
		if request.Header().Get("x-depot-org") != "org-id" {
			t.Fatalf("unexpected org header: %q", request.Header().Get("x-depot-org"))
		}
		if request.Msg.GetRepositoryUrl() != repositoryURL {
			t.Fatalf("unexpected repository URL: %q", request.Msg.GetRepositoryUrl())
		}
		if len(request.Msg.GetSha()) != 40 {
			t.Fatalf("unexpected commit SHA: %q", request.Msg.GetSha())
		}
		registeredSHA = request.Msg.GetSha()
		cmd := exec.Command("git", "--git-dir", remoteDir, "show-ref", "--verify", "refs/heads/"+migrationBranchName)
		if err := cmd.Run(); err == nil {
			t.Fatal("migration branch was pushed")
		}
	}, intentID: intentID}

	var output bytes.Buffer
	forge := migrationForgeGitHub
	if strings.HasPrefix(repositoryURL, "origin.cursor.com/") {
		forge = migrationForgeCursor
	}
	err := secretsAndVars(context.Background(), migrateOptions{
		dir:                         repoDir,
		stdout:                      &output,
		token:                       "depot_api_token",
		orgID:                       "org-id",
		forge:                       forge,
		secretMigrationBranchPrefix: branchPrefix,
		secretMigrationNow:          time.Unix(1_700_000_000, 0),
		secretMigrationRegistrar:    registrar,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !registered {
		t.Fatal("intent was not registered")
	}
	for _, expected := range []string{
		"We've committed a workflow to migrate your secrets and variables on branch " + migrationBranchName,
		"All you need to do is push it within 5 minutes:",
		"git push origin " + migrationBranchName,
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output does not contain %q:\n%s", expected, output.String())
		}
	}
	localSHA := runSecretMigrationTestCommand(t, repoDir, "git", "rev-parse", "refs/heads/"+migrationBranchName)
	if localSHA != registeredSHA {
		t.Fatalf("local branch points to %q, want registered SHA %q", localSHA, registeredSHA)
	}
	if parentSHA := runSecretMigrationTestCommand(t, repoDir, "git", "rev-parse", migrationBranchName+"^"); parentSHA != remoteBaseSHA {
		t.Fatalf("migration commit parent = %q, want remote base %q", parentSHA, remoteBaseSHA)
	}
	if headSHA := runSecretMigrationTestCommand(t, repoDir, "git", "rev-parse", "HEAD"); headSHA != localWIPSHA {
		t.Fatalf("caller's HEAD = %q, want local WIP commit %q", headSHA, localWIPSHA)
	}
	if cmd := exec.Command("git", "-C", repoDir, "cat-file", "-e", migrationBranchName+":LOCAL-WIP.md"); cmd.Run() == nil {
		t.Fatal("migration branch contains the caller's local WIP commit")
	}
	workflowPath := ".github/workflows/migrate-secrets-to-depot-ci-1700000000000.yml"
	workflow := runSecretMigrationTestCommand(t, repoDir, "git", "show", migrationBranchName+":"+workflowPath)
	for _, expected := range []string{
		`depot ci secrets add DEPOT_SECRET="$DEPOT_SECRET" GITHUB_SECRET="$GITHUB_SECRET" --repo ` + targetRepo,
		`depot ci vars add DEPOT_VAR="$DEPOT_VAR" GITHUB_VAR="$GITHUB_VAR" --repo ` + targetRepo,
	} {
		if !strings.Contains(workflow, expected) {
			t.Fatalf("committed workflow does not contain push-remote target %q:\n%s", expected, workflow)
		}
	}
	if strings.Contains(workflow, "--repo upstream-owner/upstream-repo") {
		t.Fatalf("committed workflow uses the workflow runtime repository instead of the push remote:\n%s", workflow)
	}
	cmd := exec.Command("git", "--git-dir", remoteDir, "show-ref", "--verify", "refs/heads/"+migrationBranchName)
	if err := cmd.Run(); err == nil {
		t.Fatal("migration branch was pushed")
	}
	status := runSecretMigrationTestCommand(t, repoDir, "git", "status", "--short")
	if status != "" {
		t.Fatalf("caller's worktree was modified: %s", status)
	}
}

func runSecretMigrationTestCommand(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
