package tests

import (
	"encoding/json"
	"fmt"
	"io"

	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/spf13/cobra"
)

const (
	reportOutputText = "text"
	reportOutputJSON = "json"
)

type reportOptions struct {
	reportPaths []string
	key         string
	output      string
}

type reportOutput struct {
	FilesUploaded       uint32 `json:"files_uploaded"`
	FilesProcessed      uint32 `json:"files_processed"`
	TestsReported       uint32 `json:"tests_reported"`
	DuplicateInvocation bool   `json:"duplicate_invocation"`
	FilesSkipped        uint32 `json:"files_skipped"`
}

func newCmdTestsReport() *cobra.Command {
	opts := reportOptions{output: reportOutputText}

	cmd := &cobra.Command{
		Use:          "report [path ...]",
		Short:        "Upload JUnit XML test reports",
		SilenceUsage: true,
		Long:         "Upload JUnit XML test reports for the authenticated CI execution.",
		Example: `  depot tests report reports/junit.xml
  depot tests report --report-path "reports/*.xml"
  depot tests report --report-path reports/junit.xml --key go-test --output json`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.reportPaths = append(opts.reportPaths, args...)
			return runTestsReport(cmd, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringArrayVar(&opts.reportPaths, "report-path", nil, "JUnit XML report path, directory, or glob; repeatable")
	flags.StringVar(&opts.key, "key", "", "Test key")
	flags.StringVar(&opts.output, "output", reportOutputText, "Output format (text, json)")

	return cmd
}

func runTestsReport(cmd *cobra.Command, opts reportOptions) error {
	opts.reportPaths = splitReportPathInputs(opts.reportPaths)
	if len(opts.reportPaths) == 0 {
		return fmt.Errorf("at least one report path is required; pass --report-path or one or more positional paths")
	}

	output, err := resolveReportOutput(opts.output)
	if err != nil {
		return err
	}

	filesUploaded, resp, err := uploadTestReportsResponse(cmd, opts)
	if err != nil {
		return err
	}

	if output == reportOutputJSON {
		return writeReportJSON(cmd.OutOrStdout(), filesUploaded, resp)
	}
	return writeReportSummary(cmd.OutOrStdout(), filesUploaded, resp)
}

func resolveReportOutput(value string) (string, error) {
	switch value {
	case "", reportOutputText:
		return reportOutputText, nil
	case reportOutputJSON:
		return value, nil
	default:
		return "", fmt.Errorf("unknown output format: %s. Requires text or json", value)
	}
}

func writeReportSummary(w io.Writer, filesUploaded int, resp *testresultsv1.ReportTestResultsResponse) error {
	if _, err := fmt.Fprintf(
		w,
		"Depot uploaded %d test report file(s), processed %d file(s), reported %d test(s), and skipped %d file(s).\n",
		filesUploaded,
		resp.GetFilesProcessed(),
		resp.GetTestsReported(),
		resp.GetFilesSkipped(),
	); err != nil {
		return err
	}
	if resp.GetDuplicateInvocation() {
		_, err := fmt.Fprintln(w, "Depot ignored this upload because this invocation was already reported.")
		return err
	}
	return nil
}

func writeReportJSON(w io.Writer, filesUploaded int, resp *testresultsv1.ReportTestResultsResponse) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(reportOutput{
		FilesUploaded:       uint32(filesUploaded),
		FilesProcessed:      resp.GetFilesProcessed(),
		TestsReported:       resp.GetTestsReported(),
		DuplicateInvocation: resp.GetDuplicateInvocation(),
		FilesSkipped:        resp.GetFilesSkipped(),
	})
}
