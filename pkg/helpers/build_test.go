package helpers

import (
	"context"
	"errors"
	"strings"
	"testing"

	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
)

func TestBeginBuildReturnsClearErrorWhenProjectIDMissing(t *testing.T) {
	// Make sure we don't accidentally take the DEPOT_BUILD_ID branch.
	t.Setenv("DEPOT_BUILD_ID", "")

	cases := []struct {
		name string
		req  *cliv1.CreateBuildRequest
	}{
		{
			name: "nil ProjectId",
			req:  &cliv1.CreateBuildRequest{},
		},
		{
			name: "empty string ProjectId",
			req:  &cliv1.CreateBuildRequest{ProjectId: stringPtr("")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BeginBuild(context.Background(), tc.req, "fake-token")
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !errors.Is(err, ErrMissingProjectID) {
				t.Fatalf("expected ErrMissingProjectID, got %v", err)
			}
			// Sanity check that the message stays actionable for users.
			msg := err.Error()
			for _, want := range []string{
				"no project ID specified",
				"--project",
				"DEPOT_PROJECT_ID",
				"depot.json",
				"depot init",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("error message missing %q: %s", want, msg)
				}
			}
		})
	}
}

func stringPtr(s string) *string { return &s }
