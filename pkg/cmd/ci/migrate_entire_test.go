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
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
)

func TestMigrateCommandEntireMigratesWorkflowsAndPointsSecretsAtGitHub(t *testing.T) {
	dir, _ := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://us.entire.io/et/acme/widgets")
	var output bytes.Buffer
	cmd := newCmdMigrate(migrateOptions{dir: dir})
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--yes", "--forge=entire", "--token=depot_org_token"})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".depot", "workflows", "ci.yml")); err != nil {
		t.Fatalf("Entire workflow was not migrated: %v", err)
	}
	if !strings.Contains(output.String(), "from a checkout of the GitHub repository that holds them") {
		t.Fatalf("Entire next steps did not direct secrets to a GitHub checkout:\n%s", output.String())
	}
}

func TestSecretsAndVarsEntireDirectsToGitHubCheckout(t *testing.T) {
	dir, _ := newMigrationTestRepository(t)
	runSecretMigrationTestCommand(t, dir, "git", "remote", "add", "origin", "https://us.entire.io/et/acme/widgets")
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
