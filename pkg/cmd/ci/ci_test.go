package ci

import (
	"testing"

	"github.com/spf13/pflag"
)

// TestCICommandRegistration guards `depot ci`'s subcommand surface so a future
// refactor doesn't accidentally drop a verb. New verbs should be added to the
// `wanted` slice below.
func TestCICommandRegistration(t *testing.T) {
	cmd := NewCmdCI()

	wanted := []string{
		// inspection / interactive
		"run",
		"status",
		"logs",
		"summary",
		"ssh",
		// mutation verbs (added in DEP-4221)
		"cancel",
		"dispatch",
		"rerun",
		"retry",
		// project resources
		"secrets",
		"vars",
		// migration helper
		"migrate",
	}

	registered := map[string]bool{}
	for _, sub := range cmd.Commands() {
		registered[sub.Name()] = true
	}

	for _, name := range wanted {
		if !registered[name] {
			t.Errorf("subcommand %q not registered under `depot ci`", name)
		}
	}
}

func TestSecretsAddKeepsHiddenValueFlagForCompatibility(t *testing.T) {
	cmd := NewCmdSecretsAdd()

	valueFlag := cmd.Flags().Lookup("value")
	if valueFlag == nil {
		t.Fatal("expected hidden --value compatibility flag")
	}
	if !valueFlag.Hidden {
		t.Fatal("expected --value to stay hidden from help")
	}
}

func TestVariantSelectorFlagsAreRepeatable(t *testing.T) {
	for name, cmd := range map[string]commandWithFlags{
		"secrets set":    {flags: NewCmdSecretsSet().Flags()},
		"secrets add":    {flags: NewCmdSecretsAdd().Flags()},
		"secrets bulk":   {flags: NewCmdSecretsBulk().Flags()},
		"secrets get":    {flags: NewCmdSecretsGet().Flags()},
		"secrets list":   {flags: NewCmdSecretsList().Flags()},
		"secrets remove": {flags: NewCmdSecretsRemove().Flags()},
		"vars set":       {flags: NewCmdVarsSet().Flags()},
		"vars add":       {flags: NewCmdVarsAdd().Flags()},
		"vars list":      {flags: NewCmdVarsList().Flags()},
		"vars remove":    {flags: NewCmdVarsRemove().Flags()},
	} {
		t.Run(name, func(t *testing.T) {
			repo := cmd.flags.Lookup("repo")
			if repo == nil {
				t.Fatal("expected --repo flag")
			}
			if repo.Value.Type() != "stringArray" {
				t.Fatalf("--repo type = %q, want stringArray", repo.Value.Type())
			}
		})
	}
}

func TestVariantNameIsPositionalNotFlag(t *testing.T) {
	for name, cmd := range map[string]commandWithFlags{
		"secrets set":    {flags: NewCmdSecretsSet().Flags()},
		"secrets add":    {flags: NewCmdSecretsAdd().Flags()},
		"secrets bulk":   {flags: NewCmdSecretsBulk().Flags()},
		"secrets get":    {flags: NewCmdSecretsGet().Flags()},
		"secrets remove": {flags: NewCmdSecretsRemove().Flags()},
		"vars set":       {flags: NewCmdVarsSet().Flags()},
		"vars add":       {flags: NewCmdVarsAdd().Flags()},
		"vars remove":    {flags: NewCmdVarsRemove().Flags()},
	} {
		t.Run(name, func(t *testing.T) {
			if flag := cmd.flags.Lookup("variant"); flag != nil {
				t.Fatalf("did not expect --variant flag: %#v", flag)
			}
		})
	}
}

func TestSecretsBulkUsesFileFlagAndPositionalVariant(t *testing.T) {
	cmd := NewCmdSecretsBulk()

	if cmd.Use != "bulk [variant]" {
		t.Fatalf("Use = %q, want %q", cmd.Use, "bulk [variant]")
	}
	if flag := cmd.Flags().Lookup("file"); flag == nil {
		t.Fatal("expected --file flag")
	}
	if flag := cmd.Flags().Lookup("variant"); flag != nil {
		t.Fatalf("did not expect --variant flag: %#v", flag)
	}
}

func TestParseSecretBulkEnv(t *testing.T) {
	secrets, err := parseSecretBulkEnv([]byte(`
# comment
PLAIN=value
QUOTED="two words"
EMPTY=
`))
	if err != nil {
		t.Fatal(err)
	}

	want := []secretInput{
		{name: "EMPTY", value: ""},
		{name: "PLAIN", value: "value"},
		{name: "QUOTED", value: "two words"},
	}
	if len(secrets) != len(want) {
		t.Fatalf("len(secrets) = %d, want %d: %#v", len(secrets), len(want), secrets)
	}
	for i := range want {
		if secrets[i] != want[i] {
			t.Fatalf("secrets[%d] = %#v, want %#v", i, secrets[i], want[i])
		}
	}
}

type commandWithFlags struct {
	flags flagSet
}

type flagSet interface {
	Lookup(string) *pflag.Flag
}
