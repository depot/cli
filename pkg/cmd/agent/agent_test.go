package agent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func helpOutput(t *testing.T, args ...string) string {
	t.Helper()
	root := &cobra.Command{Use: "depot"}
	root.AddCommand(&cobra.Command{Use: "build", Short: "Build an image", Run: func(*cobra.Command, []string) {}})
	root.AddCommand(NewCmdAgent())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute %v: %v", args, err)
	}
	return out.String()
}

func TestAgentIsHiddenFromRootHelp(t *testing.T) {
	out := helpOutput(t, "--help")
	if !strings.Contains(out, "build") {
		t.Fatalf("root help lists no commands:\n%s", out)
	}
	if strings.Contains(out, "agent") {
		t.Fatalf("root help lists the hidden agent command:\n%s", out)
	}
}

func TestAgentHelpStillWorks(t *testing.T) {
	out := helpOutput(t, "agent", "--help")
	if !strings.Contains(out, "session") {
		t.Fatalf("agent help is missing the session subcommand:\n%s", out)
	}
}
