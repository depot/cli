package transform

import (
	"strings"
	"testing"

	"github.com/depot/cli/pkg/ci/compat"
	"github.com/depot/cli/pkg/ci/migrate"
	"gopkg.in/yaml.v3"
)

// body strips the generated header for byte-level assertions.
func body(t *testing.T, content string) string {
	t.Helper()
	idx := strings.Index(content, "\n\n")
	if idx < 0 {
		t.Fatalf("no header found in:\n%s", content)
	}
	return content[idx+2:]
}

func runsOnNote(t *testing.T, label string) (string, string) {
	t.Helper()
	newLabel, changed, reason := migrate.MapLabel(label)
	if !changed {
		t.Fatalf("expected MapLabel(%q) to remap", label)
	}
	return newLabel, "# was: " + label + ". " + reason
}

// fidelityWorkflow includes formatting and trailing whitespace that yaml.v3
// re-encoding loses.
var fidelityWorkflow = strings.ReplaceAll(`name: CI

# Build and test everything.
on:
  push:
    branches: ['main']

env:
    LOG_LEVEL: "debug"

jobs:
    build:
        runs-on: ubuntu-latest

        steps:
            - uses: actions/checkout@v4

            - name: Build
              run: |
                set -euo pipefail
                make build<SP>

                make test
`, "<SP>", " ")

func TestTransformWorkflow_PreservesFormatting(t *testing.T) {
	raw := []byte(fidelityWorkflow)
	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "ubuntu-latest"}},
	}

	result, err := TransformWorkflow(raw, wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newLabel, note := runsOnNote(t, "ubuntu-latest")
	want := strings.Replace(fidelityWorkflow,
		"runs-on: ubuntu-latest",
		"runs-on: "+newLabel+" "+note, 1)

	got := body(t, string(result.Content))
	if got != want {
		t.Errorf("transformed body is not the input with runs-on remapped\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestTransformWorkflow_NoChangesLeavesBodyIdentical(t *testing.T) {
	raw := `name: CI

on:
  push:
    branches: [main]

jobs:
    build:
        runs-on: depot-ubuntu-latest

        steps:
            - run: |
                echo one

                echo two
`
	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}

	result, err := TransformWorkflow([]byte(raw), wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Changes) != 0 {
		t.Fatalf("expected no changes, got %v", result.Changes)
	}
	if got := body(t, string(result.Content)); got != raw {
		t.Errorf("body was rewritten\n--- want ---\n%s\n--- got ---\n%s", raw, got)
	}
}

func TestTransformWorkflow_TriggerRemovalKeepsBlankLines(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "block mapping",
			raw: `name: CI

on:
  push:
    branches: [main]

  release:
    types: [published]

jobs:
  build:
    runs-on: depot-ubuntu-latest
`,
			want: `name: CI

on:
  push:
    branches: [main]

jobs:
  build:
    runs-on: depot-ubuntu-latest
`,
		},
		{
			name: "block sequence",
			raw: `name: CI

on:
  - push
  - release

jobs:
  build:
    runs-on: depot-ubuntu-latest
`,
			want: `name: CI

on:
  - push

jobs:
  build:
    runs-on: depot-ubuntu-latest
`,
		},
		{
			name: "flow sequence keeps quoting",
			raw: `name: CI

on: ['push', release]

jobs:
  build:
    runs-on: depot-ubuntu-latest
`,
			want: `name: CI

on: ['push']

jobs:
  build:
    runs-on: depot-ubuntu-latest
`,
		},
		{
			name: "every trigger unsupported",
			raw: `name: CI

on:
  release:
    types: [published]

jobs:
  build:
    runs-on: depot-ubuntu-latest
`,
			want: `name: CI

on: {}

jobs:
  build:
    runs-on: depot-ubuntu-latest
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := &migrate.WorkflowFile{
				Path:     ".github/workflows/ci.yml",
				Name:     "CI",
				Triggers: []string{"push", "release"},
				Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
			}

			result, err := TransformWorkflow([]byte(tt.raw), wf, compat.AnalyzeWorkflow(wf), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got := body(t, string(result.Content))
			note := "# Removed unsupported trigger: release. " + compat.TriggerRules["release"].Note + "\n"
			want := strings.Replace(tt.want, "on:", note+"on:", 1)
			if got != want {
				t.Errorf("unexpected body\n--- want ---\n%s\n--- got ---\n%s", want, got)
			}
		})
	}
}

func TestTransformWorkflow_DisablesJobAtFourSpaceIndent(t *testing.T) {
	raw := []byte(`name: CI
on: push
jobs:
    build:
        runs-on: ubuntu-latest
        steps:
            - run: make build

    deploy:
        runs-on: [self-hosted, linux]
        strategy:
            matrix:
                env: [staging, prod]
        steps:
            - run: make deploy

    publish:
        runs-on: ubuntu-latest
        steps:
            - run: make publish
`)

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs: []migrate.JobInfo{
			{Name: "build", RunsOn: "ubuntu-latest"},
			{Name: "deploy", RunsOn: "self-hosted,linux", HasMatrix: true},
			{Name: "publish", RunsOn: "ubuntu-latest"},
		},
	}

	result, err := TransformWorkflow(raw, wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasCritical {
		t.Fatalf("expected HasCritical, changes: %v", result.Changes)
	}

	content := string(result.Content)
	if !strings.Contains(content, "    # DISABLED:") {
		t.Errorf("expected DISABLED marker at the job's own indent, got:\n%s", content)
	}
	for _, want := range []string{
		"    #     deploy:",
		"    #         runs-on: [self-hosted, linux]",
		"    #                 env: [staging, prod]",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("expected %q in commented-out job, got:\n%s", want, content)
		}
	}

	newLabel, _ := runsOnNote(t, "ubuntu-latest")
	if n := strings.Count(content, "runs-on: "+newLabel); n != 2 {
		t.Errorf("expected 2 live remapped labels, got %d:\n%s", n, content)
	}
	if strings.Contains(content, "# runs-on: "+newLabel) {
		t.Errorf("disabled job should not have been remapped, got:\n%s", content)
	}
}

func TestTransformWorkflow_RemapsRunsOnSequenceInPlace(t *testing.T) {
	raw := `name: CI

on: push

jobs:
  build:
    runs-on: [ubuntu-latest]
    steps:
      - run: make build
`
	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "ubuntu-latest"}},
	}

	result, err := TransformWorkflow([]byte(raw), wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newLabel, note := runsOnNote(t, "ubuntu-latest")
	want := strings.Replace(raw, "runs-on: [ubuntu-latest]", "runs-on: ["+newLabel+"] "+note, 1)
	if got := body(t, string(result.Content)); got != want {
		t.Errorf("unexpected body\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestTransformWorkflow_KeepsExistingLineComment(t *testing.T) {
	raw := `name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest # pinned deliberately
    steps:
      - run: make build
`
	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "ubuntu-latest"}},
	}

	result, err := TransformWorkflow([]byte(raw), wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newLabel, note := runsOnNote(t, "ubuntu-latest")
	want := strings.Replace(raw,
		"    runs-on: ubuntu-latest # pinned deliberately",
		"    "+note+"\n    runs-on: "+newLabel+" # pinned deliberately", 1)
	if got := body(t, string(result.Content)); got != want {
		t.Errorf("unexpected body\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestTransformWorkflow_KeepsLabelQuotingStyle(t *testing.T) {
	tests := []struct{ name, original, want string }{
		{name: "double quoted", original: `"ubuntu-latest"`, want: `"depot-ubuntu-latest"`},
		{name: "single quoted", original: `'ubuntu-latest'`, want: `'depot-ubuntu-latest'`},
		{name: "plain", original: `ubuntu-latest`, want: `depot-ubuntu-latest`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := "name: CI\non: push\njobs:\n  build:\n    runs-on: " + tt.original + "\n    steps:\n      - run: make build\n"
			wf := &migrate.WorkflowFile{
				Path:     ".github/workflows/ci.yml",
				Name:     "CI",
				Triggers: []string{"push"},
				Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "ubuntu-latest"}},
			}

			result, err := TransformWorkflow([]byte(raw), wf, compat.AnalyzeWorkflow(wf), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			_, note := runsOnNote(t, "ubuntu-latest")
			want := strings.Replace(raw, "runs-on: "+tt.original, "runs-on: "+tt.want+" "+note, 1)
			if got := body(t, string(result.Content)); got != want {
				t.Errorf("unexpected body\n--- want ---\n%s\n--- got ---\n%s", want, got)
			}
		})
	}
}

func TestTransformWorkflow_CollapsedTriggerKeepsLineComment(t *testing.T) {
	raw := `name: CI
on: # only cut releases
  release:
    types: [published]
jobs:
  build:
    runs-on: depot-ubuntu-latest
`
	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"release"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}

	result, err := TransformWorkflow([]byte(raw), wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "name: CI\n# Removed unsupported trigger: release. " + compat.TriggerRules["release"].Note + "\n" +
		`on: {} # only cut releases
jobs:
  build:
    runs-on: depot-ubuntu-latest
`
	if got := body(t, string(result.Content)); got != want {
		t.Errorf("unexpected body\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestTransformWorkflow_RemapsAfterMultibyteOnSameLine(t *testing.T) {
	raw := `name: CI

on: push

jobs:
  build:
    runs-on: [ubuntü-runner, ubuntu-latest]
    steps:
      - run: make build
`
	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "ubuntü-runner,ubuntu-latest"}},
	}

	result, err := TransformWorkflow([]byte(raw), wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	firstLabel, firstNote := runsOnNote(t, "ubuntü-runner")
	secondLabel, secondNote := runsOnNote(t, "ubuntu-latest")
	want := strings.Replace(raw,
		"runs-on: [ubuntü-runner, ubuntu-latest]",
		"runs-on: ["+firstLabel+", "+secondLabel+"] "+firstNote+" "+strings.TrimPrefix(secondNote, "# "), 1)
	if got := body(t, string(result.Content)); got != want {
		t.Errorf("unexpected body\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestTransformWorkflow_HandlesCRLF(t *testing.T) {
	lf := "name: CI\n\non: push\n\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make build\n"
	raw := strings.ReplaceAll(lf, "\n", "\r\n")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "ubuntu-latest"}},
	}

	result, err := TransformWorkflow([]byte(raw), wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newLabel, note := runsOnNote(t, "ubuntu-latest")
	want := strings.Replace(raw, "runs-on: ubuntu-latest", "runs-on: "+newLabel+" "+note, 1)
	if got := body(t, string(result.Content)); got != want {
		t.Errorf("unexpected body\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(result.Content, &parsed); err != nil {
		t.Errorf("transformed CRLF workflow does not parse: %v", err)
	}
}

func TestTransformWorkflow_FallsBackWhenExtentUnknown(t *testing.T) {
	raw := []byte(strings.Replace(fidelityWorkflow,
		"runs-on: ubuntu-latest", "runs-on: &label ubuntu-latest", 1))

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "ubuntu-latest"}},
	}

	result, err := TransformWorkflow(raw, wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newLabel, _ := runsOnNote(t, "ubuntu-latest")
	content := string(result.Content)
	if !strings.Contains(content, newLabel) {
		t.Errorf("expected label remapped even on the fallback path, got:\n%s", content)
	}

	if strings.Contains(body(t, content), "\n\n") {
		t.Errorf("expected re-encoding to drop blank lines, got:\n%s", content)
	}
	if !strings.Contains(content, `\n`) {
		t.Errorf("expected re-encoding to collapse the run block into an escaped string, got:\n%s", content)
	}
	if strings.Contains(content, "run: |") {
		t.Errorf("expected the run block style to be lost on re-encoding, got:\n%s", content)
	}
}

func TestAnnotateLineFlattensNewlinesInNotes(t *testing.T) {
	s := newSource([]byte("jobs:\n  build:\n    runs-on: x\n"))

	e, ok := annotateLine(s, 3, 0, []string{"was: self-hosted\ninjected: pwned. Nonstandard runner."})
	if !ok {
		t.Fatalf("annotateLine refused a line it should annotate")
	}
	if strings.ContainsAny(e.text, "\n\r") {
		t.Errorf("note text carries a line break, which would end the comment: %q", e.text)
	}
	if !strings.Contains(e.text, "injected: pwned") {
		t.Errorf("the note's content should be kept, only flattened: %q", e.text)
	}
}

func TestTransformWorkflow_NewlineInLabelStaysInComment(t *testing.T) {
	raw := "name: CI\non: push\njobs:\n  build:\n    runs-on: \"self-hosted\\ninjected: pwned\"\n    steps:\n      - run: make build\n"

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "self-hosted"}},
	}

	result, err := TransformWorkflow([]byte(raw), wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	if err := yaml.Unmarshal(result.Content, &out); err != nil {
		t.Fatalf("migrated workflow does not parse: %v\n%s", err, result.Content)
	}
	if _, injected := out["injected"]; injected {
		t.Errorf("label content materialized as a top-level key:\n%s", result.Content)
	}
	if len(out) != 3 {
		t.Errorf("expected exactly name, on and jobs, got %v:\n%s", out, result.Content)
	}
}

func TestTransformWorkflow_RewritesPathsWithoutReformatting(t *testing.T) {
	raw := strings.ReplaceAll(`name: CI

on:
  push:
    branches: [main]

jobs:
    build:
        runs-on: depot-ubuntu-latest

        steps:
            - uses: ./.github/actions/setup

            - uses: acme/toolkit/.github/actions/probe@v1

            - name: Build
              run: |
                ./.github/actions/build.sh<SP>

                make test
`, "<SP>", " ")

	wf := &migrate.WorkflowFile{
		Path:     ".github/workflows/ci.yml",
		Name:     "CI",
		Triggers: []string{"push"},
		Jobs:     []migrate.JobInfo{{Name: "build", RunsOn: "depot-ubuntu-latest"}},
	}

	result, err := TransformWorkflow([]byte(raw), wf, compat.AnalyzeWorkflow(wf), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := strings.ReplaceAll(raw, "./.github/actions/", "./.depot/actions/")
	if got := body(t, string(result.Content)); got != want {
		t.Errorf("expected only the local paths rewritten\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}

	if !strings.Contains(string(result.Content), "acme/toolkit/.github/actions/probe@v1") {
		t.Errorf("remote reference should not be rewritten, got:\n%s", result.Content)
	}

	var rewrote bool
	for _, c := range result.Changes {
		if c.Type == ChangePathRewritten {
			rewrote = true
		}
	}
	if !rewrote {
		t.Errorf("expected a ChangePathRewritten record, got %v", result.Changes)
	}
}
