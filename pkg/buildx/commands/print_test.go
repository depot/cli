package commands

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestBuildInfoOutput(t *testing.T) {
	for _, tc := range []struct {
		name       string
		automation string
		noSummary  string
		quiet      bool
	}{
		{name: "default"},
		{name: "automation", automation: "1", quiet: true},
		{name: "any nonempty automation value", automation: "true", quiet: true},
		{name: "legacy summary suppression", noSummary: "1", quiet: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEPOT_IN_AUTOMATION", tc.automation)
			t.Setenv("DEPOT_NO_SUMMARY_LINK", tc.noSummary)
			original := os.Stderr
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			os.Stderr = writer
			defer func() { os.Stderr = original }()

			PrintBuildURL("https://depot.dev/build/test", "plain")
			printSaveHelp("project", "build", "plain", []string{"app"}, nil)
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			output, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if tc.quiet {
				if len(output) != 0 {
					t.Fatalf("expected no informational output, got %q", output)
				}
			} else {
				for _, want := range []string{"Build Summary: https://depot.dev/build/test", "Saved target: app", "depot pull --project project build", "depot push --target <TARGET> --project project --tag <REPOSITORY:TAG> build"} {
					if !strings.Contains(string(output), want) {
						t.Errorf("missing %q in output %q", want, output)
					}
				}
			}
		})
	}
}
