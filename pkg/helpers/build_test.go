package helpers

import (
	"reflect"
	"testing"

	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
)

func TestSaveTagsFromRequest(t *testing.T) {
	req := &cliv1.CreateBuildRequest{
		Options: []*cliv1.BuildOptions{
			{SaveTags: []string{"release", "latest"}},
			nil,
			{SaveTags: []string{"latest", "", "canary"}},
		},
	}

	got := saveTagsFromRequest(req)
	want := []string{"release", "latest", "canary"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("saveTagsFromRequest() = %#v, want %#v", got, want)
	}
}

func TestSaveTagsFromRequestNil(t *testing.T) {
	if got := saveTagsFromRequest(nil); got != nil {
		t.Fatalf("saveTagsFromRequest(nil) = %#v, want nil", got)
	}
}
