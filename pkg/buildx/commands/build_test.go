package commands

import (
	"errors"
	"strings"
	"testing"
)

func TestWrapTracingError(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		shouldWrap    bool
		expectedMsg   string
		expectedInMsg string
	}{
		{
			name:       "nil error",
			err:        nil,
			shouldWrap: false,
		},
		{
			name:          "conflicting Schema URL error",
			err:           errors.New("Error: cannot merge resource due to conflicting Schema URL"),
			shouldWrap:    true,
			expectedInMsg: "DEPOT_DISABLE_OTEL=1",
		},
		{
			name:          "cannot merge resource error",
			err:           errors.New("cannot merge resource with different schema"),
			shouldWrap:    true,
			expectedInMsg: "OpenTelemetry environment variables",
		},
		{
			name:       "unrelated error",
			err:        errors.New("some other error"),
			shouldWrap: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := wrapTracingError(tt.err)

			if tt.err == nil {
				if result != nil {
					t.Errorf("expected nil error, got %v", result)
				}
				return
			}

			if tt.shouldWrap {
				wrapped, ok := result.(*wrapped)
				if !ok {
					t.Errorf("expected wrapped error, got %T", result)
					return
				}

				if !strings.Contains(wrapped.Error(), tt.expectedInMsg) {
					t.Errorf("expected error message to contain %q, got %q", tt.expectedInMsg, wrapped.Error())
				}

				// Verify we can unwrap to get the original error
				if errors.Unwrap(result) != tt.err {
					t.Errorf("expected unwrapped error to be original error")
				}
			} else {
				// Should return the original error unchanged
				if result != tt.err {
					t.Errorf("expected error to be unchanged, got %v", result)
				}
			}
		})
	}
}
