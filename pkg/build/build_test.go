package build

import (
	"reflect"
	"testing"

	"connectrpc.com/connect"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
)

func TestAdditionalTagsExistingBuildIncludesSaveTags(t *testing.T) {
	build := Build{
		ID:        "build123",
		projectID: "project123",
		saveTags:  []string{"release", "latest"},
	}

	got := build.AdditionalTags()
	want := []string{
		"registry.depot.dev/project123:build123",
		"registry.depot.dev/project123:release",
		"registry.depot.dev/project123:latest",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AdditionalTags() = %#v, want %#v", got, want)
	}
}

func TestAdditionalTagsExistingBuildSkipsEmptyAndDuplicateSaveTags(t *testing.T) {
	build := Build{
		ID:        "build123",
		projectID: "project123",
		saveTags:  []string{"release", "", "build123", "release"},
	}

	got := build.AdditionalTags()
	want := []string{
		"registry.depot.dev/project123:build123",
		"registry.depot.dev/project123:release",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AdditionalTags() = %#v, want %#v", got, want)
	}
}

func TestAdditionalTagsUsesCreateBuildResponseWhenAvailable(t *testing.T) {
	build := Build{
		ID:        "build123",
		projectID: "project123",
		saveTags:  []string{"release"},
		Response: connect.NewResponse(&cliv1.CreateBuildResponse{
			AdditionalTags: []*cliv1.CreateBuildResponse_Tag{
				{Tag: "registry.depot.dev/project123:server-tag"},
				nil,
			},
		}),
	}

	got := build.AdditionalTags()
	want := []string{"registry.depot.dev/project123:server-tag"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AdditionalTags() = %#v, want %#v", got, want)
	}
}
