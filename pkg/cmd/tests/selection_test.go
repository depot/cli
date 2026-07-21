package tests

import (
	"testing"

	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
	"github.com/spf13/cobra"
)

func TestResolveSplitOptionsUsesMatrixEnvironmentDefaults(t *testing.T) {
	t.Setenv("DEPOT_MATRIX_JOB_INDEX", "1")
	t.Setenv("DEPOT_MATRIX_JOB_TOTAL", "2")

	cmd, opts := newSplitOptionsCommand(t)
	resolved, err := resolveSplitOptions(cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.index != 1 || resolved.total != 2 {
		t.Fatalf("expected matrix shard 1/2, got %d/%d", resolved.index, resolved.total)
	}
}

func TestResolveSplitOptionsPrefersExplicitFlags(t *testing.T) {
	t.Setenv("DEPOT_MATRIX_JOB_INDEX", "1")
	t.Setenv("DEPOT_MATRIX_JOB_TOTAL", "4")

	cmd, opts := newSplitOptionsCommand(t, "--index", "0", "--total", "2")
	resolved, err := resolveSplitOptions(cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.index != 0 || resolved.total != 2 {
		t.Fatalf("expected explicit shard 0/2, got %d/%d", resolved.index, resolved.total)
	}
}

func TestResolveSplitOptionsCombinesExplicitAndMatrixValues(t *testing.T) {
	t.Setenv("DEPOT_MATRIX_JOB_INDEX", "1")
	t.Setenv("DEPOT_MATRIX_JOB_TOTAL", "4")

	cmd, opts := newSplitOptionsCommand(t, "--index", "0")
	resolved, err := resolveSplitOptions(cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.index != 0 || resolved.total != 4 {
		t.Fatalf("expected explicit index with matrix total 0/4, got %d/%d", resolved.index, resolved.total)
	}
}

func TestResolveSplitOptionsUsesMatrixIndexWithExplicitTotal(t *testing.T) {
	t.Setenv("DEPOT_MATRIX_JOB_INDEX", "1")
	t.Setenv("DEPOT_MATRIX_JOB_TOTAL", "4")

	cmd, opts := newSplitOptionsCommand(t, "--total", "2")
	resolved, err := resolveSplitOptions(cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.index != 1 || resolved.total != 2 {
		t.Fatalf("expected matrix index with explicit total 1/2, got %d/%d", resolved.index, resolved.total)
	}
}

func TestResolveSplitOptionsTotalOneOverridesMatrixIndex(t *testing.T) {
	t.Setenv("DEPOT_MATRIX_JOB_INDEX", "3")
	t.Setenv("DEPOT_MATRIX_JOB_TOTAL", "4")

	cmd, opts := newSplitOptionsCommand(t, "--total", "1")
	resolved, err := resolveSplitOptions(cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.index != 0 || resolved.total != 1 {
		t.Fatalf("expected explicit total one to resolve to shard 0/1, got %d/%d", resolved.index, resolved.total)
	}
}

func TestResolveSplitOptionsPreservesExplicitInvalidFlags(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "zero total", args: []string{"--index", "-1", "--total", "0"}, want: "--total must be greater than 0"},
		{name: "invalid index with total one", args: []string{"--index", "-1", "--total", "1"}, want: "--index must be greater than or equal to 0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DEPOT_MATRIX_JOB_INDEX", "0")
			t.Setenv("DEPOT_MATRIX_JOB_TOTAL", "2")

			cmd, opts := newSplitOptionsCommand(t, tt.args...)
			_, err := resolveSplitOptions(cmd, opts)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("expected explicit invalid shard values to fail validation, got %v", err)
			}
		})
	}
}

func TestResolveSplitOptionsAttributesMixedValidationErrorsToFlags(t *testing.T) {
	t.Setenv("DEPOT_MATRIX_JOB_INDEX", "0")
	t.Setenv("DEPOT_MATRIX_JOB_TOTAL", "2")

	cmd, opts := newSplitOptionsCommand(t, "--total", "0")
	_, err := resolveSplitOptions(cmd, opts)
	if err == nil || err.Error() != "--total must be greater than 0" {
		t.Fatalf("expected explicit total error without matrix attribution, got %v", err)
	}
}

func TestResolveSplitOptionsRejectsInvalidMatrixEnvironment(t *testing.T) {
	t.Run("malformed index", func(t *testing.T) {
		t.Setenv("DEPOT_MATRIX_JOB_INDEX", "one")
		t.Setenv("DEPOT_MATRIX_JOB_TOTAL", "2")

		cmd, opts := newSplitOptionsCommand(t)
		_, err := resolveSplitOptions(cmd, opts)
		if err == nil || err.Error() != `invalid DEPOT_MATRIX_JOB_INDEX value "one": must be an integer` {
			t.Fatalf("expected matrix index parse error, got %v", err)
		}
	})

	t.Run("incomplete pair", func(t *testing.T) {
		unsetEnv(t, matrixJobTotalEnv)
		t.Setenv("DEPOT_MATRIX_JOB_INDEX", "0")

		cmd, opts := newSplitOptionsCommand(t)
		_, err := resolveSplitOptions(cmd, opts)
		if err == nil || err.Error() != "DEPOT_MATRIX_JOB_TOTAL must be set when DEPOT_MATRIX_JOB_INDEX is set" {
			t.Fatalf("expected incomplete matrix environment error, got %v", err)
		}
	})

	t.Run("index outside single shard", func(t *testing.T) {
		t.Setenv("DEPOT_MATRIX_JOB_INDEX", "3")
		t.Setenv("DEPOT_MATRIX_JOB_TOTAL", "1")

		cmd, opts := newSplitOptionsCommand(t)
		_, err := resolveSplitOptions(cmd, opts)
		if err == nil || err.Error() != "--index must be less than --total" {
			t.Fatalf("expected invalid matrix shard values to fail validation, got %v", err)
		}
	})
}

func newSplitOptionsCommand(t *testing.T, args ...string) (*cobra.Command, splitOptions) {
	t.Helper()
	opts := splitOptions{index: -1}
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().IntVar(&opts.index, "index", -1, "")
	cmd.Flags().IntVar(&opts.total, "total", 0, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return cmd, opts
}

func TestResolveCandidateIdentityInfersFilenamesFromSourceExtensions(t *testing.T) {
	identity, err := resolveCandidateIdentity("", []string{
		"src/example.test.ts",
		"src/example.spec.mjs",
		"tests/test_example.py",
		"spec/example_spec.rb",
		"test-fixtures/foo.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity != testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_FILENAME {
		t.Fatalf("expected filename identity, got %v", identity)
	}
}

func TestResolveCandidateIdentityDoesNotTreatImportPathAsFilename(t *testing.T) {
	identity, err := resolveCandidateIdentity("", []string{"github.com/depot/cli/pkg/cmd/tests"})
	if err != nil {
		t.Fatal(err)
	}
	if identity != testresultsv1.TestCandidateIdentity_TEST_CANDIDATE_IDENTITY_CLASSNAME {
		t.Fatalf("expected classname identity, got %v", identity)
	}
}
