package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/depot/cli/pkg/api"
	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/spf13/cobra"
)

var (
	splitTestsFunc            = api.SplitTests
	reportTestResultsFunc     = api.ReportTestResults
	resolveOIDCCredentialFunc = resolveOIDCCredential
)

const splitTestsRequestTimeout = 2 * time.Minute

func selectTestCandidates(cmd *cobra.Command, opts splitOptions) (splitMode, []string, []string, *testresultsv1.SplitTestsResponse, error) {
	if err := validateShard(opts.index, opts.total); err != nil {
		return "", nil, nil, nil, err
	}
	if err := validateExplicitSplitIdentityFlags(opts); err != nil {
		return "", nil, nil, nil, err
	}
	candidates, err := loadRequiredCandidates(cmd, opts.candidatesFile)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if opts.total == 1 {
		return splitModePassthrough, candidates, candidates, nil, nil
	}
	mode := splitModeTimings
	if err := validateSplitIdentityFlags(opts, candidates); err != nil {
		return "", nil, nil, nil, err
	}

	var splitResponse *testresultsv1.SplitTestsResponse
	selectedCandidates, splitResponse, err := splitCandidatesByTimings(cmd.Context(), candidates, opts)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if shouldFallbackToFileSize(splitResponse, opts, candidates) {
		selectedCandidates, err = partitionCandidatesByFileSize(candidates, opts.index, opts.total)
		if err != nil {
			return "", nil, nil, nil, fmt.Errorf("failed to split by filesize fallback: %w", err)
		}
		mode = splitModeFileSize
	}

	return mode, candidates, selectedCandidates, splitResponse, nil
}

func loadRequiredCandidates(cmd *cobra.Command, candidatesFile string) ([]string, error) {
	if stdin, ok := cmd.InOrStdin().(*os.File); candidatesFile == "" && ok && stdin == os.Stdin && isStdinTerminalFunc() {
		return nil, fmt.Errorf("no candidates provided; pipe newline-delimited candidates to stdin or pass --candidates-file")
	}
	candidates, err := loadCandidates(cmd.InOrStdin(), candidatesFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load candidates: %w", err)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no candidates provided; pipe newline-delimited candidates to stdin or pass --candidates-file")
	}
	return candidates, nil
}

func validateSplitIdentityFlags(opts splitOptions, candidates []string) error {
	candidateIdentity, err := resolveCandidateIdentity(opts.candidateType, candidates)
	if err != nil {
		return err
	}
	timingIdentity, err := resolveTimingIdentity(opts.timingsType, candidateIdentity)
	if err != nil {
		return err
	}
	if timingIdentity != testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_TESTNAME &&
		timingIdentity != timingIdentityForCandidate(candidateIdentity) {
		return fmt.Errorf("--timings-type must match --candidate-type unless --timings-type testname is used")
	}
	return nil
}

func shouldFallbackToFileSize(resp *testresultsv1.SplitTestsResponse, opts splitOptions, candidates []string) bool {
	if resp.GetCandidatesWithTimings() > 0 {
		return false
	}
	candidateIdentity, err := resolveCandidateIdentity(opts.candidateType, candidates)
	if err != nil {
		return false
	}
	return candidateIdentity == testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME
}

func splitCandidatesByTimings(ctx context.Context, candidates []string, opts splitOptions) ([]string, *testresultsv1.SplitTestsResponse, error) {
	candidateIdentity, err := resolveCandidateIdentity(opts.candidateType, candidates)
	if err != nil {
		return nil, nil, err
	}
	timingIdentity, err := resolveTimingIdentity(opts.timingsType, candidateIdentity)
	if err != nil {
		return nil, nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, splitTestsRequestTimeout)
	defer cancel()

	token, err := resolveOIDCCredentialFunc(requestCtx)
	if err != nil {
		return nil, nil, err
	}

	resp, err := splitTestsFunc(requestCtx, token, &testresultsv1.SplitTestsRequest{
		Candidates:        candidates,
		CandidateIdentity: candidateIdentity,
		TimingIdentity:    timingIdentity,
		ShardIndex:        uint32(opts.index),
		ShardTotal:        uint32(opts.total),
		SplitKey:          testKey(opts.key),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to split tests by timings: %w", err)
	}
	return resp.GetCandidates(), resp, nil
}

func testKey(key string) string {
	if strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	if action := strings.TrimSpace(os.Getenv("GITHUB_ACTION")); action != "" {
		return action
	}
	return "default"
}

func validateExplicitSplitIdentityFlags(opts splitOptions) error {
	switch strings.ToLower(strings.TrimSpace(opts.candidateType)) {
	case "", "filename", "classname":
	default:
		return fmt.Errorf("unknown candidate type %q. Requires filename or classname", opts.candidateType)
	}

	switch strings.ToLower(strings.TrimSpace(opts.timingsType)) {
	case "", "filename", "classname", "testname":
	default:
		return fmt.Errorf("unknown timings type %q. Requires filename, classname, or testname", opts.timingsType)
	}
	return nil
}

func resolveCandidateIdentity(value string, candidates []string) (testresultsv1.TestCandidateIdentity, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		if inferFilenameCandidates(candidates) {
			return testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME, nil
		}
		return testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME, nil
	case "filename":
		return testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME, nil
	case "classname":
		return testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME, nil
	default:
		return testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_UNSPECIFIED, fmt.Errorf("unknown candidate type %q. Requires filename or classname", value)
	}
}

func resolveTimingIdentity(value string, candidateIdentity testresultsv1.TestCandidateIdentity) (testresultsv1.TestTimingIdentity, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		switch candidateIdentity {
		case testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME:
			return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_FILENAME, nil
		case testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME:
			return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_CLASSNAME, nil
		default:
			return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_UNSPECIFIED, fmt.Errorf("candidate type is required")
		}
	case "filename":
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_FILENAME, nil
	case "classname":
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_CLASSNAME, nil
	case "testname":
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_TESTNAME, nil
	default:
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_UNSPECIFIED, fmt.Errorf("unknown timings type %q. Requires filename, classname, or testname", value)
	}
}

func timingIdentityForCandidate(candidateIdentity testresultsv1.TestCandidateIdentity) testresultsv1.TestTimingIdentity {
	switch candidateIdentity {
	case testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME:
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_FILENAME
	case testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME:
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_CLASSNAME
	default:
		return testresultsv1.TestTimingIdentity_TEST_TIMING_IDENTITY_UNSPECIFIED
	}
}

func inferFilenameCandidates(candidates []string) bool {
	hasCandidate := false
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		hasCandidate = true
		if !looksLikeFilenameCandidate(candidate) {
			return false
		}
	}
	return hasCandidate
}

func looksLikeFilenameCandidate(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(candidate), "."))
	if ext == "test" {
		return strings.ContainsAny(candidate, `/\`) || regularFileExists(candidate)
	}
	switch ext {
	case "go", "js", "jsx", "ts", "tsx", "mjs", "cjs", "mts", "cts", "rb", "py", "php", "rs", "java", "kt", "cs", "cpp", "cc", "c", "h", "hpp", "m", "mm", "swift", "scala", "clj", "ex", "exs":
		return true
	default:
		return false
	}
}

func regularFileExists(candidate string) bool {
	info, err := os.Stat(candidate)
	return err == nil && info.Mode().IsRegular()
}
