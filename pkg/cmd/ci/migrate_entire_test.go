package ci

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
)

type stubEntireRepositoryAccessClient struct {
	response *civ1.GetEntireRepositoryAccessResponse
	repos    []string
}

func (c *stubEntireRepositoryAccessClient) GetEntireRepositoryAccess(
	_ context.Context,
	request *connect.Request[civ1.GetEntireRepositoryAccessRequest],
) (*connect.Response[civ1.GetEntireRepositoryAccessResponse], error) {
	c.repos = append(c.repos, request.Msg.GetRepo())
	return connect.NewResponse(c.response), nil
}

func newEntireMigrationTestRepository(t *testing.T) string {
	t.Helper()
	dir, _ := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://us.entire.io/et/acme/widgets.git")
	return dir
}

func TestMigrateCommandEntireMigratesWorkflowsAndPointsSecretsAtGitHub(t *testing.T) {
	dir := newEntireMigrationTestRepository(t)
	client := &stubEntireRepositoryAccessClient{response: &civ1.GetEntireRepositoryAccessResponse{Installed: true, RepoAccessible: true}}
	var output bytes.Buffer
	cmd := newCmdMigrate(migrateOptions{dir: dir, entireAccessClient: client})
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--yes", "--forge=entire", "--token=depot_org_token"})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.repos) != 1 || client.repos[0] != "et/acme/widgets" {
		t.Fatalf("Entire access checked for %v, want [et/acme/widgets]", client.repos)
	}
	if _, err := os.Stat(filepath.Join(dir, ".depot", "workflows", "ci.yml")); err != nil {
		t.Fatalf("Entire workflow was not migrated: %v", err)
	}
	if !strings.Contains(output.String(), "from a checkout of the GitHub repository that holds them") {
		t.Fatalf("Entire next steps did not direct secrets to a GitHub checkout:\n%s", output.String())
	}
}

func TestEntirePreflightStopsMigrationWithoutRepositoryAccess(t *testing.T) {
	tests := []struct {
		name     string
		response *civ1.GetEntireRepositoryAccessResponse
		want     string
	}{
		{name: "not installed", response: &civ1.GetEntireRepositoryAccessResponse{}, want: "Entire is not connected to this Depot organization"},
		{name: "repository not covered", response: &civ1.GetEntireRepositoryAccessResponse{Installed: true}, want: "does not have access to et/acme/widgets"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := newEntireMigrationTestRepository(t)
			var output bytes.Buffer
			cmd := newCmdMigrate(migrateOptions{
				dir:                dir,
				entireAccessClient: &stubEntireRepositoryAccessClient{response: test.response},
			})
			cmd.SetOut(&output)
			cmd.SetArgs([]string{"--yes", "--forge=entire", "--token=depot_org_token"})

			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("preflight output missing %q:\n%s", test.want, output.String())
			}
			if _, err := os.Stat(filepath.Join(dir, ".depot")); !os.IsNotExist(err) {
				t.Fatalf("workflows were migrated despite failed preflight: %v", err)
			}
		})
	}
}

func TestSecretsAndVarsEntireDirectsToGitHubCheckout(t *testing.T) {
	dir := newEntireMigrationTestRepository(t)
	cmd := newCmdMigrate(migrateOptions{
		dir: dir,
		secretMigrationRegistrar: recordingSecretMigrationRegistrar{
			register: func(*connect.Request[civ2.SecretMigrationIntent]) {
				t.Fatal("registered a secret migration intent for an Entire repository")
			},
		},
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"secrets-and-vars", "--forge=entire", "--token=depot_org_token"})

	if err := cmd.ExecuteContext(context.Background()); !errors.Is(err, errEntireSecretMigration) {
		t.Fatalf("unexpected error: %v", err)
	}
}
