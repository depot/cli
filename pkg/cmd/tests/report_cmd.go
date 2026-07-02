package tests

import (
	"encoding/json"
	"fmt"
	"io"

	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/spf13/cobra"
)

const (
	reportOutputAuto = "auto"
	reportOutputText = "text"
	reportOutputJSON = "json"
)

type reportOutput struct {
	FilesProcessed      uint32 `json:"files_processed"`
	TestsReported       uint32 `json:"tests_reported"`
	DuplicateInvocation bool   `json:"duplicate_invocation"`
	FilesSkipped        uint32 `json:"files_skipped"`
}

func newCmdTestsReport() *cobra.Command {
	opts := runOptions{output: reportOutputAuto}

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Upload JUnit XML test reports",
		Long:  "Upload JUnit XML test reports for the authenticated CI execution.",
		Example: `  depot tests report --report-path "reports/*.xml"
  depot tests report --report-path reports/junit.xml --key go-test --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTestsReport(cmd, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.reportPath, "report-path", "", "JUnit XML report path, directory, or glob")
	flags.StringVar(&opts.key, "key", "", "Test invocation key")
	flags.StringVar(&opts.output, "output", reportOutputAuto, "Output format (auto, text, json)")

	return cmd
}

func runTestsReport(cmd *cobra.Command, opts runOptions) error {
	if opts.reportPath == "" {
		return fmt.Errorf("--report-path is required")
	}

	output, err := resolveReportOutput(opts.output)
	if err != nil {
		return err
	}

	resp, err := uploadTestReportsResponse(cmd.Context(), opts)
	if err != nil {
		return err
	}

	if output == reportOutputJSON {
		return writeReportJSON(cmd.OutOrStdout(), resp)
	}
	return writeReportSummary(cmd.OutOrStdout(), resp)
}

func resolveReportOutput(value string) (string, error) {
	switch value {
	case "", reportOutputAuto:
		return reportOutputText, nil
	case reportOutputText, reportOutputJSON:
		return value, nil
	default:
		return "", fmt.Errorf("unknown output format: %s. Requires auto, text, or json", value)
	}
}

func writeReportSummary(w io.Writer, resp *testresultsv1.ReportTestResultsResponse) error {
	_, err := fmt.Fprintf(
		w,
		"Depot uploaded %d test report file(s), reported %d test(s), and skipped %d file(s).\n",
		resp.GetFilesProcessed(),
		resp.GetTestsReported(),
		resp.GetFilesSkipped(),
	)
	return err
}

func writeReportJSON(w io.Writer, resp *testresultsv1.ReportTestResultsResponse) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(reportOutput{
		FilesProcessed:      resp.GetFilesProcessed(),
		TestsReported:       resp.GetTestsReported(),
		DuplicateInvocation: resp.GetDuplicateInvocation(),
		FilesSkipped:        resp.GetFilesSkipped(),
	})
}
