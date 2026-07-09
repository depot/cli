package tests

import (
	"testing"

	testresultsv1 "github.com/depot/cli/pkg/proto/depot/testresults/v1"
)

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
