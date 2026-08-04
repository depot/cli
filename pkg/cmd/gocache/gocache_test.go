package gocache

import (
	"strings"
	"testing"
)

func TestValidOutputID(t *testing.T) {
	valid := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if !validOutputID(valid) {
		t.Fatal("valid output ID rejected")
	}
	if !validOutputID("E" + valid[1:]) {
		t.Fatal("uppercase output ID rejected")
	}
	if !validOutputID(strings.Repeat("ab", 127)) {
		t.Fatal("long output ID rejected")
	}

	invalid := []string{
		"../../tmp/some/path",
		"a",
		strings.Repeat("ab", 128),
	}
	for _, outputID := range invalid {
		if validOutputID(outputID) {
			t.Errorf("invalid output ID accepted: %q", outputID)
		}
	}
}
