package docker

import (
	"errors"
	"reflect"
	"testing"
)

func TestDriverImageCandidates(t *testing.T) {
	repo := "public.ecr.aws/depot/cli:"

	tests := []struct {
		name    string
		version string
		want    []driverImageCandidate
	}{
		{
			name:    "patch release falls back to major.minor and major",
			version: "2.101.69",
			want: []driverImageCandidate{
				{ref: repo + "2.101.69", mutable: false},
				{ref: repo + "2.101", mutable: true},
				{ref: repo + "2", mutable: true},
			},
		},
		{
			name:    "zero patch still yields distinct floating tags",
			version: "2.101.0",
			want: []driverImageCandidate{
				{ref: repo + "2.101.0", mutable: false},
				{ref: repo + "2.101", mutable: true},
				{ref: repo + "2", mutable: true},
			},
		},
		{
			name:    "major.minor.0 with zero minor",
			version: "3.0.0",
			want: []driverImageCandidate{
				{ref: repo + "3.0.0", mutable: false},
				{ref: repo + "3.0", mutable: true},
				{ref: repo + "3", mutable: true},
			},
		},
		{
			name:    "prerelease build is a single mutable exact tag, no fallbacks",
			version: "0.0.0-dev",
			want:    []driverImageCandidate{{ref: repo + "0.0.0-dev", mutable: true}},
		},
		{
			name:    "non-semver (latest) is a single mutable exact tag, no fallbacks",
			version: "latest",
			want:    []driverImageCandidate{{ref: repo + "latest", mutable: true}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := driverImageCandidates(tt.version)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("driverImageCandidates(%q) = %+v, want %+v", tt.version, got, tt.want)
			}
		})
	}
}

func TestSelectDriverImage(t *testing.T) {
	candidates := []driverImageCandidate{
		{ref: "repo:1.2.3", mutable: false},
		{ref: "repo:1.2", mutable: true},
		{ref: "repo:1", mutable: true},
	}

	t.Run("returns exact when it pulls, without trying fallbacks", func(t *testing.T) {
		var tried []string
		got, err := selectDriverImage(candidates, func(c driverImageCandidate) error {
			tried = append(tried, c.ref)
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != candidates[0] {
			t.Errorf("got %+v, want %+v", got, candidates[0])
		}
		if !reflect.DeepEqual(tried, []string{"repo:1.2.3"}) {
			t.Errorf("tried %v, want only the exact tag", tried)
		}
	})

	t.Run("falls back in order to the first candidate that pulls, carrying its mutable flag", func(t *testing.T) {
		var tried []string
		got, err := selectDriverImage(candidates, func(c driverImageCandidate) error {
			tried = append(tried, c.ref)
			if c.ref == "repo:1.2.3" {
				return errors.New("not found")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != candidates[1] {
			t.Errorf("got %+v, want %+v (the mutable major.minor tag)", got, candidates[1])
		}
		if !got.mutable {
			t.Errorf("expected chosen fallback to be marked mutable so the caller pins it to a digest")
		}
		if !reflect.DeepEqual(tried, []string{"repo:1.2.3", "repo:1.2"}) {
			t.Errorf("tried %v, want exact then major.minor", tried)
		}
	})

	t.Run("returns the last error when every candidate fails", func(t *testing.T) {
		wantErr := errors.New("major failed")
		got, err := selectDriverImage(candidates, func(c driverImageCandidate) error {
			if c.ref == "repo:1" {
				return wantErr
			}
			return errors.New("not found")
		})
		if got != (driverImageCandidate{}) {
			t.Errorf("got %+v, want zero value", got)
		}
		if !errors.Is(err, wantErr) {
			t.Errorf("got err %v, want last error %v", err, wantErr)
		}
	})
}
