package migrate

import "testing"

func TestClassifyLabel(t *testing.T) {
	tests := []struct {
		label string
		want  LabelClass
	}{
		// Depot native
		{"depot-ubuntu-latest", LabelDepotNative},
		{"depot-ubuntu-22.04", LabelDepotNative},
		{"Depot-Ubuntu-Latest", LabelDepotNative},

		// Standard GitHub
		{"ubuntu-latest", LabelStandardGitHub},
		{"ubuntu-24.04", LabelStandardGitHub},
		{"ubuntu-22.04", LabelStandardGitHub},
		{"ubuntu-20.04", LabelStandardGitHub},
		{"Ubuntu-Latest", LabelStandardGitHub},

		// Expression
		{"${{ matrix.os }}", LabelExpression},
		{"${{ inputs.runner }}", LabelExpression},

		// Nonstandard
		{"self-hosted", LabelNonstandard},
		{"blacksmith-4vcpu-ubuntu-2204", LabelNonstandard},
		{"buildjet-4vcpu-ubuntu-2204", LabelNonstandard},
		{"macos-latest", LabelNonstandard},
		{"windows-latest", LabelNonstandard},
		{"custom-runner", LabelNonstandard},
		{"", LabelNonstandard},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			got := ClassifyLabel(tt.label)
			if got != tt.want {
				t.Errorf("ClassifyLabel(%q) = %d, want %d", tt.label, got, tt.want)
			}
		})
	}
}

func TestMapLabel(t *testing.T) {
	tests := []struct {
		label      string
		wantLabel  string
		wantChange bool
		wantReason bool // just check non-empty
	}{
		// Depot native — no change
		{"depot-ubuntu-latest", "depot-ubuntu-latest", false, false},
		// Standard GitHub — mapped
		{"ubuntu-latest", "depot-ubuntu-latest", true, true},
		{"ubuntu-22.04", "depot-ubuntu-22.04", true, true},
		{"ubuntu-24.04", "depot-ubuntu-24.04", true, true},
		{"ubuntu-20.04", "depot-ubuntu-20.04", true, true},

		// Expression — no change
		{"${{ matrix.os }}", "${{ matrix.os }}", false, false},

		// Nonstandard — default depot label
		{"blacksmith-4vcpu-ubuntu-2204", "depot-ubuntu-latest", true, true},
		{"self-hosted", "depot-ubuntu-latest", true, true},
		{"macos-latest", "depot-ubuntu-latest", true, true},
		{"custom-runner", "depot-ubuntu-latest", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			newLabel, changed, reason := MapLabel(tt.label)
			if newLabel != tt.wantLabel {
				t.Errorf("MapLabel(%q) label = %q, want %q", tt.label, newLabel, tt.wantLabel)
			}
			if changed != tt.wantChange {
				t.Errorf("MapLabel(%q) changed = %v, want %v", tt.label, changed, tt.wantChange)
			}
			if tt.wantReason && reason == "" {
				t.Errorf("MapLabel(%q) reason is empty, want non-empty", tt.label)
			}
			if !tt.wantReason && reason != "" {
				t.Errorf("MapLabel(%q) reason = %q, want empty", tt.label, reason)
			}
		})
	}
}
