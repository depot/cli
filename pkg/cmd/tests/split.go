package tests

import (
	"encoding/json"
	"fmt"
	"io"

	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/spf13/cobra"
)

const (
	splitOutputAuto = "auto"
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

func newCmdTestsSplit() *cobra.Command {
	opts := runOptions{splitBy: string(splitModeTimings), index: -1, output: splitOutputAuto}

	cmd := &cobra.Command{
		Use:   "split",
		Short: "Print candidates assigned to a test shard",
		Long: `Print the candidates assigned to this shard without running tests or uploading reports.

Candidates are newline-delimited runnable units read from stdin or --candidates. By default, Depot uses historical test
timings to select a balanced shard. Use --split-by name or --split-by filesize for local deterministic splitting.`,
		Example: `  # Print a timing-balanced shard from stdin candidates
  go list ./... | depot tests split --index 0 --total 4 --candidate-type classname

  # Print a deterministic shard from an explicit candidates file
  depot tests split --candidates tests.txt --index 0 --total 4 --split-by name`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTestsSplit(cmd, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.splitBy, "split-by", string(splitModeTimings), "Split mode (timings, name, filesize)")
	flags.StringVar(&opts.candidateType, "candidate-type", "", "Runnable candidate identity (filename, classname)")
	flags.StringVar(&opts.timingsType, "timings-type", "", "JUnit timing identity (filename, classname, testname)")
	flags.IntVar(&opts.index, "index", -1, "Zero-based shard index")
	flags.IntVar(&opts.total, "total", 0, "Total number of shards")
	flags.StringVar(&opts.splitKey, "split-key", "", "Identity for this logical split when one job runs multiple splits")
	flags.StringVar(&opts.candidates, "candidates", "", "Path to newline-delimited runnable test candidates instead of stdin")
	flags.StringVar(&opts.output, "output", splitOutputAuto, "Output format (auto, text, json)")

	return cmd
}

func runTestsSplit(cmd *cobra.Command, opts runOptions) error {
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
	case "", splitOutputAuto:
		return splitOutputText, nil
	case splitOutputText, splitOutputJSON:
		return value, nil
	default:
		return "", fmt.Errorf("unknown output format: %s. Requires auto, text, or json", value)
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

func writeSplitJSON(w io.Writer, mode splitMode, opts runOptions, candidates, selectedCandidates []string, resp *testresultsv1.SplitTestsResponse) error {
	output := splitOutput{
		Candidates:            selectedCandidates,
		CandidatesRequested:   len(candidates),
		CandidatesSelected:    len(selectedCandidates),
		CandidatesWithTimings: 0,
		ShardIndex:            opts.index,
		ShardTotal:            opts.total,
		SplitBy:               mode,
	}
	if resp != nil {
		output.CandidatesRequested = int(resp.GetCandidatesRequested())
		output.CandidatesSelected = int(resp.GetCandidatesSelected())
		output.CandidatesWithTimings = int(resp.GetCandidatesWithTimings())
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}
