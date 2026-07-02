package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
)

func TestSplitDefaultsToTimingSplit(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) { return "oidc-token", nil }
	runShellCommandFunc = func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
		t.Fatal("split should not run a command")
		return 0, nil
	}
	reportTestResultsFunc = func(context.Context, string, *testresultsv1.ReportTestResultsRequest) (*testresultsv1.ReportTestResultsResponse, error) {
		t.Fatal("split should not report test results")
		return nil, nil
	}

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
		"--split-key", "unit",
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

func TestSplitSupportsLocalNameMode(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("name splitting should not need OIDC")
		return "", nil
	}
	splitTestsFunc = func(context.Context, string, *testresultsv1.SplitTestsRequest) (*testresultsv1.SplitTestsResponse, error) {
		t.Fatal("name splitting should not call SplitTests")
		return nil, nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		"c.test.ts\na.test.ts\nb.test.ts\n",
		"split",
		"--split-by", "name",
		"--index", "1",
		"--total", "2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "b.test.ts\n" {
		t.Fatalf("expected selected name shard on stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "using name") {
		t.Fatalf("expected name summary, got %q", stderr)
	}
}

func TestSplitJSONOutputIncludesLocalCounts(t *testing.T) {
	resetTestHooks(t)
	resolveOIDCCredentialFunc = func(context.Context) (string, error) {
		t.Fatal("name splitting should not need OIDC")
		return "", nil
	}

	stdout, stderr, err := executeCommandWithInputOutput(
		"c.test.ts\na.test.ts\nb.test.ts\n",
		"split",
		"--split-by", "name",
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
		t.Fatalf("expected selected local candidate in json, got %v", out.Candidates)
	}
	if out.CandidatesRequested != 3 || out.CandidatesSelected != 1 || out.CandidatesWithTimings != 0 {
		t.Fatalf("expected local counts in json, got %#v", out)
	}
	if out.SplitBy != splitModeName {
		t.Fatalf("expected name split metadata, got %#v", out)
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
		"ignored.test.ts\n",
		"split",
		"--candidates", candidatesPath,
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

func TestSplitRejectsUnknownOutput(t *testing.T) {
	resetTestHooks(t)

	_, _, err := executeCommandOutput(
		"split",
		"--output", "yaml",
		"--index", "0",
		"--total", "1",
	)
	if err == nil || !strings.Contains(err.Error(), "unknown output format") {
		t.Fatalf("expected output validation error, got %v", err)
	}
}

func TestSplitRequiresCandidatesWhenStdinIsTerminal(t *testing.T) {
	resetTestHooks(t)

	_, _, err := executeCommandOutput(
		"split",
		"--index", "0",
		"--total", "1",
	)
	if err == nil || !strings.Contains(err.Error(), "pipe newline-delimited candidates") {
		t.Fatalf("expected missing candidates error, got %v", err)
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
