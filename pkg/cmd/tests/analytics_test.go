package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
)

func analyticsHooks(t *testing.T) {
	resetTestHooks(t)
	previous := listTestAnalyticsFunc
	t.Cleanup(func() { listTestAnalyticsFunc = previous })
}

func TestAnalyticsPipedJSONHasFailureContextWithoutPrompt(t *testing.T) {
	analyticsHooks(t)
	isTerminalFunc = func() bool { return false }
	resolveOrgAuthFunc = func(context.Context, string) (string, error) { t.Fatal("must not prompt"); return "", nil }
	listTestAnalyticsFunc = func(_ context.Context, token, org string, req *testresultsv1.ListTestAnalyticsRequest) (*testresultsv1.ListTestAnalyticsResponse, error) {
		if token != "token-1" || org != "org-1" || req.Repo != "acme/api" || req.Ranking != testresultsv1.TestAnalyticsRanking_TEST_ANALYTICS_RANKING_FLAKY {
			t.Fatalf("unexpected scope: %s %s %v", token, org, req)
		}
		rate := 0.5
		return &testresultsv1.ListTestAnalyticsResponse{Tests: []*testresultsv1.TestAnalytics{{TestName: "flaky test", Total: 2, Failed: 1, FlakeRate: &rate,
			LatestFailure: &testresultsv1.TestResult{OwnerId: "attempt-1", FailureMessage: "expected true"}}}, HasMore: true}, nil
	}
	cmd := NewCmdTests()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"analytics", "--repo", "acme/api"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	rows := result["tests"].([]any)
	row := rows[0].(map[string]any)
	if row["total"] != "2" || row["flakeRate"] != 0.5 || result["hasMore"] != true {
		t.Fatalf("unexpected JSON: %s", out.String())
	}
	if row["latestFailure"].(map[string]any)["failureMessage"] != "expected true" {
		t.Fatalf("missing failure: %s", out.String())
	}
}

func TestAnalyticsRejectsInvalidFlagsBeforeAuthentication(t *testing.T) {
	analyticsHooks(t)
	resolveOrgAuthFunc = func(context.Context, string) (string, error) { t.Fatal("must validate first"); return "", nil }
	for _, args := range [][]string{{"--ci", "--gha"}, {"--ranking", "newest"}, {"--limit", "101"}, {"--start-date", "2026-02-30"},
		{"--start-date", "2026-01-01", "--end-date", "2026-09-01"}, {"--start-date", "2026-09-02", "--end-date", "2026-09-01"}, {"--output", "csv"}} {
		cmd := NewCmdTests()
		cmd.SetArgs(append([]string{"analytics"}, args...))
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err == nil {
			t.Fatalf("expected invalid flags: %v", args)
		}
	}
}

func TestAnalyticsServiceFailureIsNotEmptySuccess(t *testing.T) {
	analyticsHooks(t)
	listTestAnalyticsFunc = func(context.Context, string, string, *testresultsv1.ListTestAnalyticsRequest) (*testresultsv1.ListTestAnalyticsResponse, error) {
		return nil, errors.New("unavailable")
	}
	cmd := NewCmdTests()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"analytics", "--output", "json"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected successful output: %s", out.String())
	}
}

func TestAnalyticsTableSanitizesNamesAndReportsTruncation(t *testing.T) {
	var out bytes.Buffer
	duration := 5000.0
	err := writeAnalyticsTable(&out, &testresultsv1.ListTestAnalyticsResponse{Tests: []*testresultsv1.TestAnalytics{{
		TestName: "slow\n\x1btest", Repo: "acme/api", P95DurationMs: &duration, Total: 2}}, HasMore: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "5000.0ms") || !strings.Contains(out.String(), "narrow the filters") {
		t.Fatalf("bad output: %q", out.String())
	}
}
