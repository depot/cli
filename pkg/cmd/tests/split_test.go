package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/depot/cli/pkg/oidc"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
)

func TestSplitDefaultsToTimingSplit(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }

	var splitReq *testresultsv1.SplitTestsRequest
	splitTestsFunc = func(ctx context.Context, token string, req *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		requireContextDeadline(t, ctx, "split tests")
		if token != "oidc-token" {
			t.Fatalf("expected OIDC token, got %q", token)
		}
		splitReq = req
		return &testresultsv1.SplitTestsResponse{
			Candidates:            []string{"b.test.ts"},
			CandidatesRequested:   2,
			CandidatesSelected:    1,
			CandidatesWithTimings: 1,
		}, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		"a.test.ts\nb.test.ts\n",
		"split",
		"--candidate-type", "filename",
		"--timings-type", "testname",
		"--key", "unit",
		"--index", "1",
		"--total", "2",
	)
	if err != nil {
		t.Fatal(err)
	}

	if stdout != "b.test.ts\n" {
		t.Fatalf("expected selected candidate on stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "using timings") || !strings.Contains(stderr, "Depot found timings for 1 candidate") {
		t.Fatalf("expected split summary on stderr, got %q", stderr)
	}
	if splitReq == nil {
		t.Fatal("expected SplitTests to be called")
	}
	if splitReq.GetCandidateIdentity() != testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME {
		t.Fatalf("expected filename candidate identity, got %v", splitReq.GetCandidateIdentity())
	}
	if splitReq.GetTimingIdentity() != testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_TESTNAME {
		t.Fatalf("expected testname timing identity, got %v", splitReq.GetTimingIdentity())
	}
	if splitReq.GetShardIndex() != 1 || splitReq.GetShardTotal() != 2 {
		t.Fatalf("expected shard 1/2, got %d/%d", splitReq.GetShardIndex(), splitReq.GetShardTotal())
	}
	if splitReq.GetSplitKey() != "unit" {
		t.Fatalf("expected split key unit, got %q", splitReq.GetSplitKey())
	}
}

func TestSplitSupportsJSONOutput(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }

	splitTestsFunc = func(_ context.Context, _ string, _ *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{
			Candidates:            []string{"b.test.ts"},
			CandidatesRequested:   2,
			CandidatesSelected:    1,
			CandidatesWithTimings: 1,
		}, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		"a.test.ts\nb.test.ts\n",
		"split",
		"--index", "1",
		"--total", "2",
		"--output", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("expected no text summary for json output, got %q", stderr)
	}

	var out splitOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("expected valid json, got %q: %v", stdout, err)
	}
	if !equalStrings(out.Candidates, []string{"b.test.ts"}) {
		t.Fatalf("expected selected candidate in json, got %v", out.Candidates)
	}
	if out.CandidatesRequested != 2 || out.CandidatesSelected != 1 || out.CandidatesWithTimings != 1 {
		t.Fatalf("expected response counts in json, got %#v", out)
	}
	if out.ShardIndex != 1 || out.ShardTotal != 2 || out.SplitBy != splitModeTimings {
		t.Fatalf("expected shard metadata in json, got %#v", out)
	}
}

func TestSplitOIDCDebugOutputDoesNotCorruptJSONStdout(t *testing.T) {
	resetTestHooks(t)
	t.Setenv("DEPOT_DEBUG_OIDC", "1")

	var stdout, stderr bytes.Buffer
	oidcDebugWriter = &stderr
	resolveOIDCCredentialFunc = func(ctx context.Context) (string, error) {
		return resolveOIDCCredentialWithProviders(ctx, []oidc.OIDCProvider{
			fakeOIDCProvider{name: "empty", err: errors.New("missing env")},
			fakeOIDCProvider{name: "github", token: "oidc-token"},
		})
	}
	splitTestsFunc = func(_ context.Context, _ string, _ *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{
			Candidates:            []string{"b.test.ts"},
			CandidatesRequested:   2,
			CandidatesSelected:    1,
			CandidatesWithTimings: 1,
		}, nil
	}

	cmd := NewCmdTests()
	cmd.SetIn(strings.NewReader("a.test.ts\nb.test.ts\n"))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"split", "--index", "1", "--total", "2", "--output", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "OIDC") {
		t.Fatalf("expected stdout to contain only JSON, got %q", stdout.String())
	}

	var out splitOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("expected valid json stdout, got %q: %v", stdout.String(), err)
	}
	if !equalStrings(out.Candidates, []string{"b.test.ts"}) {
		t.Fatalf("expected selected candidate in json, got %v", out.Candidates)
	}
	if !strings.Contains(stderr.String(), "Trying OIDC provider empty") ||
		!strings.Contains(stderr.String(), "OIDC provider empty failed") ||
		!strings.Contains(stderr.String(), "Trying OIDC provider github") {
		t.Fatalf("expected OIDC debug logs on stderr, got %q", stderr.String())
	}
}

func TestSplitTotalOnePrintsAllCandidatesWithoutSplitting(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("split should not need OIDC for total=1")
		return "", nil
	}
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("SplitTests should not be called for total=1")
		return nil, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		"a.test.ts\nb.test.ts\n",
		"split",
		"--index", "0",
		"--total", "1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "a.test.ts\nb.test.ts\n" {
		t.Fatalf("expected all candidates on stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "selected all 2 candidate") {
		t.Fatalf("expected single-shard summary, got %q", stderr)
	}
}

func TestSplitTotalOneSupportsJSONOutput(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("single-shard split should not need OIDC")
		return "", nil
	}
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("SplitTests should not be called for total=1")
		return nil, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		"a.test.ts\nb.test.ts\n",
		"split",
		"--index", "0",
		"--total", "1",
		"--output", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("expected no text summary for json output, got %q", stderr)
	}

	var out splitOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("expected valid json, got %q: %v", stdout, err)
	}
	if !equalStrings(out.Candidates, []string{"a.test.ts", "b.test.ts"}) {
		t.Fatalf("expected all candidates in json, got %v", out.Candidates)
	}
	if out.CandidatesRequested != 2 || out.CandidatesSelected != 2 || out.CandidatesWithTimings != 0 {
		t.Fatalf("expected local single-shard counts in json, got %#v", out)
	}
	if out.SplitBy != splitModePassthrough {
		t.Fatalf("expected passthrough split mode for single-shard json, got %q", out.SplitBy)
	}
}

func TestSplitTotalOneIgnoresUnusedTimingIdentityMismatch(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("single-shard split should not need OIDC")
		return "", nil
	}
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("SplitTests should not be called for total=1")
		return nil, nil
	}

	stdout, _, err := executeCommandWithInputOutput(
		"a.test.ts\nb.test.ts\n",
		"split",
		"--candidate-type", "filename",
		"--timings-type", "classname",
		"--index", "0",
		"--total", "1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "a.test.ts\nb.test.ts\n" {
		t.Fatalf("expected all candidates on stdout, got %q", stdout)
	}
}

func TestSplitEmptyCandidatesErrorsBeforeOIDCAndAPI(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("empty split should fail before OIDC")
		return "", nil
	}
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("SplitTests should not be called for empty candidates")
		return nil, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		"\n",
		"split",
		"--index", "0",
		"--total", "4",
	)
	if err == nil || !strings.Contains(err.Error(), "no candidates provided") {
		t.Fatalf("expected missing candidates error, got %v", err)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout for empty candidates, got %q", stdout)
	}
	if !strings.Contains(stderr, "no candidates provided") || strings.Contains(stderr, "Depot selected") {
		t.Fatalf("expected only missing candidates error on stderr, got %q", stderr)
	}
}

func TestSplitReportsFileSizeFallbackFailure(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{
			Candidates:            []string{"missing.test.ts"},
			CandidatesRequested:   1,
			CandidatesSelected:    1,
			CandidatesWithTimings: 0,
		}, nil
	}

	stdout, _, err := executeCommandWithInputOutput(
		"missing.test.ts\n",
		"split",
		"--candidate-type", "filename",
		"--index", "0",
		"--total", "2",
	)
	if err == nil || !strings.Contains(err.Error(), "failed to split by filesize fallback") {
		t.Fatalf("expected filesize fallback error, got %v", err)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout on fallback error, got %q", stdout)
	}
}

func TestSplitFallsBackToFileSizeWhenNoTimingsAreAvailable(t *testing.T) {
	resetTestHooks(t)
	dir := t.TempDir()
	small := writeSizedFile(t, dir, "small.test.ts", 1)
	large := writeSizedFile(t, dir, "large.test.ts", 100)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{
			Candidates:            []string{large},
			CandidatesRequested:   2,
			CandidatesSelected:    1,
			CandidatesWithTimings: 0,
		}, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		small+"\n"+large+"\n",
		"split",
		"--candidate-type", "filename",
		"--index", "1",
		"--total", "2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != small+"\n" {
		t.Fatalf("expected selected filesize fallback shard on stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "using filesize") {
		t.Fatalf("expected filesize summary, got %q", stderr)
	}
}

func TestSplitJSONOutputIncludesFileSizeFallbackCounts(t *testing.T) {
	resetTestHooks(t)
	dir := t.TempDir()
	small := writeSizedFile(t, dir, "small.test.ts", 1)
	large := writeSizedFile(t, dir, "large.test.ts", 100)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		return &testresultsv1.SplitTestsResponse{
			Candidates:            []string{large},
			CandidatesRequested:   2,
			CandidatesSelected:    2,
			CandidatesWithTimings: 0,
		}, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		small+"\n"+large+"\n",
		"split",
		"--candidate-type", "filename",
		"--index", "1",
		"--total", "2",
		"--output", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("expected no text summary for json output, got %q", stderr)
	}

	var out splitOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("expected valid json, got %q: %v", stdout, err)
	}
	if !equalStrings(out.Candidates, []string{small}) {
		t.Fatalf("expected selected filesize fallback candidate in json, got %v", out.Candidates)
	}
	if out.CandidatesRequested != 2 || out.CandidatesSelected != 1 || out.CandidatesWithTimings != 0 {
		t.Fatalf("expected local counts in json, got %#v", out)
	}
	if out.SplitBy != splitModeFileSize {
		t.Fatalf("expected filesize split metadata, got %#v", out)
	}
}

func TestSplitInfersClassnameWhenCandidatesAreMixed(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }

	var splitReq *testresultsv1.SplitTestsRequest
	splitTestsFunc = func(_ context.Context, _ string, req *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		splitReq = req
		return &testresultsv1.SplitTestsResponse{
			Candidates:            []string{"com.example.Test"},
			CandidatesRequested:   2,
			CandidatesSelected:    1,
			CandidatesWithTimings: 0,
		}, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		"com.example.Test\nsrc/foo.test.ts\n",
		"split",
		"--index", "0",
		"--total", "2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "com.example.Test\n" {
		t.Fatalf("expected selected classname candidate on stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "using timings") {
		t.Fatalf("expected timings summary, got %q", stderr)
	}
	if splitReq.GetCandidateIdentity() != testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME {
		t.Fatalf("expected classname candidate identity for mixed candidates, got %v", splitReq.GetCandidateIdentity())
	}
}

func TestSplitReadsCandidatesFile(t *testing.T) {
	resetTestHooks(t)
	candidatesPath := writeTempFile(t, "candidates.txt", "a.test.ts\nb.test.ts\n")
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("single-shard split should not need OIDC")
		return "", nil
	}

	stdout, _, err := executeCommandWithInputOutput(
		"",
		"split",
		"--candidates-file", candidatesPath,
		"--index", "0",
		"--total", "1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "a.test.ts\nb.test.ts\n" {
		t.Fatalf("expected candidates file to be used, got %q", stdout)
	}
}

func TestSplitReadsCandidatesCommand(t *testing.T) {
	resetTestHooks(t)
	runCandidatesCommandFunc = func(_ context.Context, command string, stdout, _ io.Writer) error {
		if command != "discover-tests" {
			t.Fatalf("expected candidate command %q, got %q", "discover-tests", command)
		}
		_, _ = io.WriteString(stdout, "a.test.ts\nb.test.ts\n")
		return nil
	}
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("single-shard split should not need OIDC")
		return "", nil
	}

	stdout, _, err := executeCommandWithInputOutput(
		"",
		"split",
		"--candidates-command", "discover-tests",
		"--index", "0",
		"--total", "1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "a.test.ts\nb.test.ts\n" {
		t.Fatalf("expected candidates command output, got %q", stdout)
	}
}

func TestSplitRejectsPipedCandidatesWithCandidatesCommand(t *testing.T) {
	resetTestHooks(t)
	runCandidatesCommandFunc = func(context.Context, string, io.Writer, io.Writer) error {
		t.Fatal("candidate command should not run for ambiguous sources")
		return nil
	}
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("ambiguous sources should not need OIDC")
		return "", nil
	}

	_, _, err := executeCommandWithInputOutput(
		"piped.test.ts\n",
		"split",
		"--candidates-command", "discover-tests",
		"--index", "0",
		"--total", "1",
	)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected source conflict, got %v", err)
	}
}

func TestSplitRejectsAmbiguousCandidateSources(t *testing.T) {
	candidatesPath := writeTempFile(t, "candidates.txt", "from-file.test.ts\n")
	tests := []struct {
		name  string
		input string
		args  []string
	}{
		{
			name:  "stdin and file",
			input: "from-stdin.test.ts\n",
			args:  []string{"split", "--candidates-file", candidatesPath, "--index", "0", "--total", "1"},
		},
		{
			name: "file and command",
			args: []string{
				"split", "--candidates-file", candidatesPath, "--candidates-command", "discover-tests", "--index", "0", "--total", "1",
			},
		},
		{
			name:  "stdin file and command",
			input: "from-stdin.test.ts\n",
			args: []string{
				"split", "--candidates-file", candidatesPath, "--candidates-command", "discover-tests", "--index", "0", "--total", "1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetTestHooks(t)
			runCandidatesCommandFunc = func(context.Context, string, io.Writer, io.Writer) error {
				t.Fatal("candidate command should not run for ambiguous sources")
				return nil
			}
			resolveOIDCCredentialFunc = func(context.Context) (string, error) {
				t.Fatal("ambiguous sources should not need OIDC")
				return "", nil
			}

			_, _, err := executeCommandWithInputOutput(tt.input, tt.args...)
			if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
				t.Fatalf("expected source conflict, got %v", err)
			}
		})
	}
}

func TestSplitCandidatesCommandFailureDoesNotUseOIDCOrAPI(t *testing.T) {
	resetTestHooks(t)
	runCandidatesCommandFunc = func(context.Context, string, io.Writer, io.Writer) error {
		return errors.New("exit status 1")
	}
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("candidate command failure should not need OIDC")
		return "", nil
	}
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("candidate command failure should not call SplitTests")
		return nil, nil
	}

	_, _, err := executeCommandWithInputOutput(
		"",
		"split",
		"--candidates-command", "discover-tests",
		"--index", "0",
		"--total", "2",
	)
	if err == nil || !strings.Contains(err.Error(), "candidate command failed") {
		t.Fatalf("expected candidate command failure, got %v", err)
	}
}

func TestSplitCandidatesFileFailureDoesNotUseStdinOrAPI(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("missing candidates file should fail before OIDC")
		return "", nil
	}
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("missing candidates file should fail before SplitTests")
		return nil, nil
	}

	stdout, _, err := executeCommandWithInputOutput(
		"",
		"split",
		"--candidates-file", "missing-candidates.txt",
		"--index", "0",
		"--total", "2",
	)
	if err == nil || !strings.Contains(err.Error(), "failed to load candidates") {
		t.Fatalf("expected candidates file load error, got %v", err)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout on candidates file load error, got %q", stdout)
	}
}

func TestSplitRejectsUnknownOutput(t *testing.T) {
	tests := []string{"yaml", "auto"}

	for _, output := range tests {
		t.Run(output, func(t *testing.T) {
			resetTestHooks(t)

			_, _, err := executeCommandOutput(
				"split",
				"--output", output,
				"--index", "0",
				"--total", "1",
			)
			if err == nil || !strings.Contains(err.Error(), "Requires text or json") {
				t.Fatalf("expected output validation error, got %v", err)
			}
		})
	}
}

func TestSplitRejectsInvalidIdentityFlagsBeforeOIDC(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		input string
		want  string
	}{
		{
			name:  "unknown candidate type",
			args:  []string{"split", "--candidate-type", "method", "--index", "0", "--total", "2"},
			input: "a.test.ts\n",
			want:  `unknown candidate type "method"`,
		},
		{
			name:  "unknown timings type",
			args:  []string{"split", "--timings-type", "method", "--index", "0", "--total", "2"},
			input: "a.test.ts\n",
			want:  `unknown timings type "method"`,
		},
		{
			name:  "mismatched identity",
			args:  []string{"split", "--candidate-type", "filename", "--timings-type", "classname", "--index", "0", "--total", "2"},
			input: "a.test.ts\n",
			want:  "--timings-type must match --candidate-type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetTestHooks(t)
			resolveOIDCCredentialFunc = func(context.Context) (string, error) {
				t.Fatal("invalid identity flags should not need OIDC")
				return "", nil
			}
			splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
				t.Fatal("invalid identity flags should not call SplitTests")
				return nil, nil
			}

			stdout, _, err := executeCommandWithInputOutput(tt.input, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
			if stdout != "" {
				t.Fatalf("expected no stdout on validation error, got %q", stdout)
			}
		})
	}
}

func TestSplitRequiresShardIndexAndTotal(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing index", args: []string{"split", "--total", "2"}, want: "--index must be greater than or equal to 0"},
		{name: "missing total", args: []string{"split", "--index", "0"}, want: "--total must be greater than 0"},
		{name: "total too large", args: []string{"split", "--index", "0", "--total", "10001"}, want: "--total must be <= 10000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetTestHooks(t)

			_, _, err := executeCommandWithInputOutput("a.test.ts\n", tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestSplitReportsOIDCAndAPIErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
		want  string
	}{
		{
			name: "oidc error",
			setup: func(t *testing.T) {
				t.Helper()
				resolveOIDCCredentialFunc = func(context.Context) (string, error) {
					return "", errors.New("missing OIDC credential")
				}
			},
			want: "missing OIDC credential",
		},
		{
			name: "api error",
			setup: func(t *testing.T) {
				t.Helper()
				resolveOIDCCredentialFunc = func(context.Context) (string, error) {
					return "oidc-token", nil
				}
				splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
					return nil, errors.New("unavailable")
				}
			},
			want: "failed to split tests by timings: unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetTestHooks(t)
			tt.setup(t)

			stdout, _, err := executeCommandWithInputOutput(
				"a.test.ts\nb.test.ts\n",
				"split",
				"--candidate-type", "filename",
				"--index", "0",
				"--total", "2",
			)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
			if stdout != "" {
				t.Fatalf("expected no stdout on split error, got %q", stdout)
			}
		})
	}
}

func TestSplitUsesGitHubActionAsDefaultKey(t *testing.T) {
	resetTestHooks(t)
	t.Setenv("GITHUB_ACTION", " go-test ")
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }

	var splitReq *testresultsv1.SplitTestsRequest
	splitTestsFunc = func(_ context.Context, _ string, req *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		splitReq = req
		return &testresultsv1.SplitTestsResponse{
			Candidates:            []string{"a.test.ts"},
			CandidatesRequested:   1,
			CandidatesSelected:    1,
			CandidatesWithTimings: 1,
		}, nil
	}

	_, _, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"split",
		"--candidate-type", "filename",
		"--index", "0",
		"--total", "2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if splitReq.GetSplitKey() != "go-test" {
		t.Fatalf("expected GITHUB_ACTION split key, got %q", splitReq.GetSplitKey())
	}
}

func TestSplitRequiresCandidatesWhenStdinIsTerminal(t *testing.T) {
	resetTestHooks(t)

	_, _, err := executeCommandOutput(
		"split",
		"--index", "0",
		"--total", "1",
	)
	if err == nil || !strings.Contains(err.Error(), "pipe newline-delimited candidates") || !strings.Contains(err.Error(), "--candidates-file") {
		t.Fatalf("expected missing candidates error, got %v", err)
	}
}

func TestSplitReadsPipedOSStdinWhenStdoutIsTerminal(t *testing.T) {
	resetTestHooks(t)
	isTerminalFunc = func() bool { return true }
	isStdinTerminalFunc = func() bool { return false }
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("single-shard split should not need OIDC")
		return "", nil
	}

	stdin := writeTempFile(t, "stdin-candidates.txt", "a.test.ts\nb.test.ts\n")
	file, err := os.Open(stdin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	previousStdin := os.Stdin
	os.Stdin = file
	t.Cleanup(func() { os.Stdin = previousStdin })

	stdout, _, err := executeCommandOutput(
		"split",
		"--index", "0",
		"--total", "1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "a.test.ts\nb.test.ts\n" {
		t.Fatalf("expected candidates from stdin, got %q", stdout)
	}
}

func TestSplitSubcommandTakesPrecedenceOverResultID(t *testing.T) {
	resetTestHooks(t)
	ciGetRunStatusFunc = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
		t.Fatal("parent tests command should not try to resolve split as an ID")
		return nil, nil
	}
	listTestResultsFunc = func(context.Context, string, string, *testresultsv1.ListTestResultsRequest) (*testresultsv1.ListTestResultsResponse, error) {
		t.Fatal("parent tests command should not list test results")
		return nil, nil
	}

	stdout, _, err := executeCommandWithInputOutput(
		"a.test.ts\n",
		"split",
		"--index", "0",
		"--total", "1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "a.test.ts\n" {
		t.Fatalf("expected split subcommand output, got %q", stdout)
	}
}

func executeCommandWithInputOutput(input string, args ...string) (string, string, error) {
	cmd := NewCmdTests()
	var stdout, stderr bytes.Buffer
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func executeCommandOutput(args ...string) (string, string, error) {
	cmd := NewCmdTests()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}
