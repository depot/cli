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
		want    []string
	}{
		{
			name:    "patch release falls back to major.minor and major",
			version: "2.101.69",
			want:    []string{repo + "2.101.69", repo + "2.101", repo + "2"},
		},
		{
			name:    "zero patch still yields distinct floating tags",
			version: "2.101.0",
			want:    []string{repo + "2.101.0", repo + "2.101", repo + "2"},
		},
		{
			name:    "major.minor.0 with zero minor",
			version: "3.0.0",
			want:    []string{repo + "3.0.0", repo + "3.0", repo + "3"},
		},
		{
			name:    "prerelease build tries exact tag only",
			version: "0.0.0-dev",
			want:    []string{repo + "0.0.0-dev"},
		},
		{
			name:    "non-semver tries exact tag only",
			version: "latest",
			want:    []string{repo + "latest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := driverImageCandidates(tt.version)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("driverImageCandidates(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestSelectDriverImage(t *testing.T) {
	candidates := []string{"repo:1.2.3", "repo:1.2", "repo:1"}

	t.Run("returns exact when it pulls, without trying fallbacks", func(t *testing.T) {
		var tried []string
		got, err := selectDriverImage(candidates, func(image string) error {
			tried = append(tried, image)
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "repo:1.2.3" {
			t.Errorf("got %q, want repo:1.2.3", got)
		}
		if !reflect.DeepEqual(tried, []string{"repo:1.2.3"}) {
			t.Errorf("tried %v, want only the exact tag", tried)
		}
	})

	t.Run("falls back in order to the first candidate that pulls", func(t *testing.T) {
		var tried []string
		got, err := selectDriverImage(candidates, func(image string) error {
			tried = append(tried, image)
			if image == "repo:1.2.3" {
				return errors.New("not found")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "repo:1.2" {
			t.Errorf("got %q, want repo:1.2", got)
		}
		if !reflect.DeepEqual(tried, []string{"repo:1.2.3", "repo:1.2"}) {
			t.Errorf("tried %v, want exact then major.minor", tried)
		}
	})

	t.Run("returns the last error when every candidate fails", func(t *testing.T) {
		wantErr := errors.New("major failed")
		got, err := selectDriverImage(candidates, func(image string) error {
			if image == "repo:1" {
				return wantErr
			}
			return errors.New("not found")
		})
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
		if !errors.Is(err, wantErr) {
			t.Errorf("got err %v, want last error %v", err, wantErr)
		}
	})
}
