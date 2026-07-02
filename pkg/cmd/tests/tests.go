package tests

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/depot/cli/pkg/api"
	coreci "github.com/depot/cli/pkg/ci"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/spf13/cobra"
)

const maxPageSize = 500

var (
	resolveOrgAuthFunc    = helpers.ResolveOrgAuth
	resolveStaticAuthFunc = helpers.ResolveStaticOrgAuth
	currentOrgFunc        = config.GetCurrentOrganization
	listTestResultsFunc   = api.ListTestResults
	ciGetRunStatusFunc    = api.CIGetRunStatus
	isTerminalFunc        = helpers.IsTerminal
)

type options struct {
	orgID     string
	token     string
	ci        bool
	gha       bool
	job       string
	workflow  string
	statuses  []string
	suiteName string
	testName  string
	className string
	fileName  string
	pageSize  uint32
	pageToken string
	output    string
}

func NewCmdTests() *cobra.Command {
	opts := options{}

	cmd := &cobra.Command{
		Use:   "tests <id>",
		Short: "List test results",
		Long: `List parsed test results for a Depot CI attempt or GitHub Actions job.

By default, Depot tries the ID as Depot CI results or GitHub Actions results.

Use --ci to restrict lookup to Depot CI results. The ID may be a run, job, or
attempt ID; run and job IDs resolve to the latest matching attempt, matching
depot ci logs.

Use --gha to restrict lookup to GitHub Actions results. The ID may be a GitHub
Actions job ID.`,
		Example: `  # List Depot CI results for one attempt
  depot tests <attempt-id>

  # List Depot CI results for one job in a run
  depot tests <run-id> --job test

  # List failed GitHub Actions results for one job
  depot tests <github-job-id> --gha --status failed

  # Emit JSON for automation
  depot tests <attempt-id> --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, args[0], opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	flags.StringVar(&opts.token, "token", "", "Depot API token")
	flags.BoolVar(&opts.ci, "ci", false, "Restrict lookup to Depot CI test results")
	flags.BoolVar(&opts.gha, "gha", false, "Restrict lookup to GitHub Actions test results")
	flags.StringVar(&opts.job, "job", "", "Depot CI job key to select when ID is a run")
	flags.StringVar(&opts.workflow, "workflow", "", "Depot CI workflow path to filter jobs (e.g. ci.yml)")
	flags.StringArrayVar(&opts.statuses, "status", nil, "Test status to include (unknown, passed, failed, errored, skipped); repeatable")
	flags.StringVar(&opts.suiteName, "suite", "", "Test suite name to include")
	flags.StringVar(&opts.testName, "test", "", "Test case name to include")
	flags.StringVar(&opts.className, "class", "", "Test class name to include")
	flags.StringVar(&opts.fileName, "file", "", "Source filename to include")
	flags.Uint32Var(&opts.pageSize, "page-size", 100, "Number of results to return per page (max 500)")
	flags.StringVar(&opts.pageToken, "page-token", "", "Token to fetch the next page")
	flags.StringVar(&opts.output, "output", "auto", "Output format (auto, table, json)")

	cmd.AddCommand(newCmdTestsRun())
	cmd.AddCommand(newCmdTestsSplit())

	return cmd
}

func run(cmd *cobra.Command, id string, opts options) error {
	if err := validateOptions(opts); err != nil {
		return err
	}

	if opts.orgID == "" {
		opts.orgID = currentOrgFunc()
	}

	output := outputFormat(opts.output)
	token, err := resolveToken(cmd.Context(), opts.token, output)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("missing API token, please run `depot login`")
	}

	req, runLookupErr, err := requestFromOptions(cmd.Context(), token, id, opts)
	if err != nil {
		return err
	}

	resp, err := listTestResultsFunc(cmd.Context(), token, opts.orgID, req)
	if err != nil {
		if runLookupErr != nil {
			return fmt.Errorf("could not resolve %q as a CI run, job, or attempt ID:\n  as run: %v\n  as attempt: %v", id, runLookupErr, err)
		}
		return fmt.Errorf("failed to list test results: %w", err)
	}

	switch output {
	case "table":
		return writeTable(cmd.OutOrStdout(), resp)
	case "json":
		return writeJSON(cmd.OutOrStdout(), resp)
	default:
		return fmt.Errorf("unknown output format: %s. Requires auto, table, or json", opts.output)
	}
}

func validateOptions(opts options) error {
	if opts.ci && opts.gha {
		return fmt.Errorf("--ci and --gha are mutually exclusive")
	}

	if opts.job != "" || opts.workflow != "" {
		if opts.gha {
			return fmt.Errorf("--job and --workflow can only be used with Depot CI")
		}
	}

	if opts.pageSize > maxPageSize {
		return fmt.Errorf("--page-size must be <= %d", maxPageSize)
	}

	switch opts.output {
	case "auto", "table", "json", "":
		return nil
	default:
		return fmt.Errorf("unknown output format: %s. Requires auto, table, or json", opts.output)
	}
}

func outputFormat(value string) string {
	switch value {
	case "", "auto":
		if isTerminalFunc() {
			return "table"
		}
		return "json"
	default:
		return value
	}
}

func resolveToken(ctx context.Context, explicitToken, output string) (string, error) {
	if output == "json" {
		return resolveStaticAuthFunc(explicitToken), nil
	}
	return resolveOrgAuthFunc(ctx, explicitToken)
}

func requestFromOptions(ctx context.Context, token, id string, opts options) (*testresultsv1.ListTestResultsRequest, error, error) {
	statuses, err := parseStatuses(opts.statuses)
	if err != nil {
		return nil, nil, err
	}

	ownerID := id
	ownerType := testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_UNSPECIFIED
	var runLookupErr error

	switch {
	case opts.ci || opts.job != "" || opts.workflow != "":
		ownerType = testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_CI
		attemptID, lookupErr, err := resolveCIAttempt(ctx, token, opts.orgID, id, opts.job, opts.workflow)
		if err != nil {
			return nil, nil, err
		}
		ownerID = attemptID
		runLookupErr = lookupErr
	case opts.gha:
		ownerType = testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_GITHUB_ACTIONS
	default:
		attemptID, _, err := resolveCIRunOrJobAttempt(ctx, token, opts.orgID, id, "", "")
		if err != nil {
			return nil, nil, err
		}
		if attemptID != "" {
			ownerType = testresultsv1.TestResultsOwnerType_TEST_RESULTS_OWNER_TYPE_CI
			ownerID = attemptID
		}
	}

	return &testresultsv1.ListTestResultsRequest{
		OwnerType: ownerType,
		OwnerId:   ownerID,
		Status:    statuses,
		SuiteName: opts.suiteName,
		TestName:  opts.testName,
		ClassName: opts.className,
		FileName:  opts.fileName,
		PageSize:  opts.pageSize,
		PageToken: opts.pageToken,
	}, runLookupErr, nil
}

func resolveCIAttempt(ctx context.Context, token, orgID, id, job, workflow string) (string, error, error) {
	attemptID, runErr, err := resolveCIRunOrJobAttempt(ctx, token, orgID, id, job, workflow)
	if runErr == nil {
		return attemptID, nil, err
	}

	if job != "" || workflow != "" {
		return "", nil, fmt.Errorf("failed to look up run: %w", runErr)
	}

	return id, runErr, nil
}

func resolveCIRunOrJobAttempt(ctx context.Context, token, orgID, id, job, workflow string) (string, error, error) {
	resp, runErr := ciGetRunStatusFunc(ctx, token, orgID, id)
	if runErr != nil {
		return "", runErr, nil
	}

	attemptID, err := coreci.ResolveAttemptForRunStatus(resp, id, job, workflow)
	return attemptID, nil, err
}

func parseStatuses(values []string) ([]testresultsv1.TestResultStatus, error) {
	var statuses []testresultsv1.TestResultStatus
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			status, ok := statusByName(strings.TrimSpace(part))
			if !ok {
				return nil, fmt.Errorf("unknown status %q. Requires unknown, passed, failed, errored, or skipped", part)
			}
			statuses = append(statuses, status)
		}
	}
	return statuses, nil
}

func statusByName(value string) (testresultsv1.TestResultStatus, bool) {
	switch strings.ToLower(value) {
	case "":
		return testresultsv1.TestResultStatus_TEST_RESULT_STATUS_UNSPECIFIED, false
	case "unknown":
		return testresultsv1.TestResultStatus_TEST_RESULT_STATUS_UNKNOWN, true
	case "passed":
		return testresultsv1.TestResultStatus_TEST_RESULT_STATUS_PASSED, true
	case "failed":
		return testresultsv1.TestResultStatus_TEST_RESULT_STATUS_FAILED, true
	case "errored":
		return testresultsv1.TestResultStatus_TEST_RESULT_STATUS_ERRORED, true
	case "skipped":
		return testresultsv1.TestResultStatus_TEST_RESULT_STATUS_SKIPPED, true
	default:
		return testresultsv1.TestResultStatus_TEST_RESULT_STATUS_UNSPECIFIED, false
	}
}

func writeJSON(w io.Writer, resp *testresultsv1.ListTestResultsResponse) error {
	out, err := protojson.MarshalOptions{UseProtoNames: false}.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", out)
	return err
}

func writeTable(w io.Writer, resp *testresultsv1.ListTestResultsResponse) error {
	if len(resp.GetResults()) == 0 {
		_, err := fmt.Fprintln(w, "No test results found.")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSUITE\tTEST\tDURATION\tFILE\tFAILURE")
	for _, result := range resp.GetResults() {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			statusLabel(result.GetStatus()),
			sanitizeCell(result.GetSuiteName()),
			sanitizeCell(result.GetTestName()),
			formatDuration(result.GetDurationMs()),
			sanitizeCell(formatFile(result)),
			truncate(sanitizeCell(firstLine(result.GetFailureMessage())), 80),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if resp.GetNextPageToken() != "" {
		_, err := fmt.Fprintf(w, "\nNext page token: %s\n", sanitizeCell(resp.GetNextPageToken()))
		return err
	}
	return nil
}

func sanitizeCell(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}

func statusLabel(status testresultsv1.TestResultStatus) string {
	switch status {
	case testresultsv1.TestResultStatus_TEST_RESULT_STATUS_UNKNOWN:
		return "unknown"
	case testresultsv1.TestResultStatus_TEST_RESULT_STATUS_PASSED:
		return "passed"
	case testresultsv1.TestResultStatus_TEST_RESULT_STATUS_FAILED:
		return "failed"
	case testresultsv1.TestResultStatus_TEST_RESULT_STATUS_ERRORED:
		return "errored"
	case testresultsv1.TestResultStatus_TEST_RESULT_STATUS_SKIPPED:
		return "skipped"
	default:
		return "unspecified"
	}
}

func formatDuration(ms uint32) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Millisecond).String()
}

func formatFile(result *testresultsv1.TestResult) string {
	if result.GetFileName() == "" {
		return ""
	}
	if result.GetLineNumber() == 0 {
		return result.GetFileName()
	}
	return fmt.Sprintf("%s:%d", result.GetFileName(), result.GetLineNumber())
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if i := strings.IndexByte(value, '\n'); i >= 0 {
		return value[:i]
	}
	return value
}

func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
