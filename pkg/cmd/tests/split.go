package tests

import (
	"encoding/json"
	"fmt"
	"io"

	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/spf13/cobra"
)

const (
	splitOutputText = "text"
	splitOutputJSON = "json"
)

type splitOutput struct {
	Candidates            []string  `json:"candidates"`
	CandidatesRequested   int       `json:"candidates_requested"`
	CandidatesSelected    int       `json:"candidates_selected"`
	CandidatesWithTimings int       `json:"candidates_with_timings"`
	ShardIndex            int       `json:"shard_index"`
	ShardTotal            int       `json:"shard_total"`
	SplitBy               splitMode `json:"split_by"`
}

type splitOptions struct {
	candidateType  string
	timingsType    string
	index          int
	total          int
	candidatesFile string
	key            string
	output         string
}

func newCmdTestsSplit() *cobra.Command {
	opts := splitOptions{index: -1, output: splitOutputText}

	cmd := &cobra.Command{
		Use:          "split",
		Short:        "Print candidates assigned to a test shard",
		SilenceUsage: true,
		Long: `Print the candidates assigned to this shard without running tests or uploading reports.

Candidates are newline-delimited runnable units read from stdin or --candidates-file. By default, Depot uses historical test
timings to select a balanced shard. If no timings are available for filename candidates, Depot falls back to file-size
splitting.`,
		Example: `  # Print a timing-balanced shard from stdin candidates
  go list ./... | depot tests split --index 0 --total 4 --candidate-type classname

  # Print a shard from an explicit candidates file
  depot tests split --candidates-file tests.txt --index 0 --total 4 --candidate-type filename`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTestsSplit(cmd, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.candidateType, "candidate-type", "", "Runnable candidate identity (filename, classname)")
	flags.StringVar(&opts.timingsType, "timings-type", "", "JUnit timing identity (filename, classname, testname)")
	flags.IntVar(&opts.index, "index", -1, "Zero-based shard index")
	flags.IntVar(&opts.total, "total", 0, "Total number of shards")
	flags.StringVar(&opts.key, "key", "", "Test split key")
	flags.StringVar(&opts.candidatesFile, "candidates-file", "", "Path to newline-delimited runnable test candidates instead of stdin")
	flags.StringVar(&opts.output, "output", splitOutputText, "Output format (text, json)")

	return cmd
}

func runTestsSplit(cmd *cobra.Command, opts splitOptions) error {
	output, err := resolveSplitOutput(opts.output)
	if err != nil {
		return err
	}

	mode, candidates, selectedCandidates, splitResponse, err := selectTestCandidates(cmd, opts)
	if err != nil {
		return err
	}

	if output == splitOutputJSON {
		return writeSplitJSON(cmd.OutOrStdout(), mode, opts, candidates, selectedCandidates, splitResponse)
	}

	writeSplitSummary(cmd.ErrOrStderr(), mode, opts, len(candidates), selectedCandidates, splitResponse)
	return writeCandidates(cmd.OutOrStdout(), selectedCandidates)
}

func resolveSplitOutput(value string) (string, error) {
	switch value {
	case "", splitOutputText:
		return splitOutputText, nil
	case splitOutputJSON:
		return value, nil
	default:
		return "", fmt.Errorf("unknown output format: %s. Requires text or json", value)
	}
}

func writeCandidates(w io.Writer, candidates []string) error {
	for _, candidate := range candidates {
		if _, err := fmt.Fprintln(w, candidate); err != nil {
			return err
		}
	}
	return nil
}

func writeSplitSummary(w io.Writer, mode splitMode, opts splitOptions, totalCandidates int, selected []string, resp *testresultsv1.SplitTestsResponse) {
	if opts.total == 1 {
		fmt.Fprintf(w, "Depot selected all %d candidate(s) for a single shard.\n", len(selected))
		return
	}
	fmt.Fprintf(w, "Depot selected %d of %d candidate(s) for shard %d/%d using %s.\n", len(selected), totalCandidates, opts.index, opts.total, mode)
	if resp != nil && mode == splitModeTimings {
		fmt.Fprintf(w, "Depot found timings for %d candidate(s).\n", resp.GetCandidatesWithTimings())
	}
}

func writeSplitJSON(w io.Writer, mode splitMode, opts splitOptions, candidates, selectedCandidates []string, resp *testresultsv1.SplitTestsResponse) error {
	output := splitOutput{
		Candidates:            selectedCandidates,
		CandidatesRequested:   len(candidates),
		CandidatesSelected:    len(selectedCandidates),
		CandidatesWithTimings: 0,
		ShardIndex:            opts.index,
		ShardTotal:            opts.total,
		SplitBy:               mode,
	}
	if resp != nil && mode == splitModeTimings {
		output.CandidatesRequested = int(resp.GetCandidatesRequested())
		output.CandidatesSelected = int(resp.GetCandidatesSelected())
		output.CandidatesWithTimings = int(resp.GetCandidatesWithTimings())
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}
