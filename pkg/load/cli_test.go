package load

import (
	"io"
	"reflect"
	"testing"

	"github.com/docker/buildx/build"
	"github.com/moby/buildkit/client"
)

func Test_defaultImageName(t *testing.T) {
	type args struct {
		loadOpts DepotLoadOptions
		target   string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Uppercase project name is converted to lowercase",
			args: args{
				loadOpts: DepotLoadOptions{
					Project: "FOO",
					BuildID: "bar",
				},
			},
			want: "depot-project-foo:build-bar",
		},
		{
			name: "Uppercase build id name is converted to lowercase",
			args: args{
				loadOpts: DepotLoadOptions{
					Project: "foo",
					BuildID: "BAR",
				},
			},
			want: "depot-project-foo:build-bar",
		},
		{
			name: "Removes invalid characters from project name",
			args: args{
				loadOpts: DepotLoadOptions{
					Project: "FOO!@#$%^&*()",
					BuildID: "BAR",
				},
			},
			want: "depot-project-foo:build-bar",
		},
		{
			name: "Removes invalid characters from build id",
			args: args{
				loadOpts: DepotLoadOptions{
					Project: "FOO._-",
					BuildID: "BAR!@#$%^&*()_",
					IsBake:  true,
				},
				target: "HOWDY#_",
			},
			want: "depot-project-foo._-:build-bar_-howdy_",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultImageName(tt.args.loadOpts, tt.args.target); got != tt.want {
				t.Errorf("defaultImageName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDockerLoadPreservesOtherOutputs(t *testing.T) {
	tarOutput := func(map[string]string) (io.WriteCloser, error) { return nil, nil }
	cases := []struct {
		name    string
		exports []client.ExportEntry
		want    []string
	}{
		{"default", nil, []string{"docker"}},
		{"local output", []client.ExportEntry{{Type: "local", OutputDir: "out"}}, []string{"local", "docker"}},
		{"push", []client.ExportEntry{{Type: "image", Attrs: map[string]string{"push": "true"}}}, []string{"image", "docker"}},
		{"docker tar", []client.ExportEntry{{Type: "docker", Output: tarOutput}}, []string{"docker", "docker"}},
		{"already loading", []client.ExportEntry{{Type: "docker"}}, []string{"docker"}},
		{"cache only", []client.ExportEntry{{Type: "cacheonly"}}, []string{"cacheonly"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := map[string]build.Options{"app": {Exports: tc.exports, Tags: []string{"example:latest"}}}
			got := WithDockerLoadOutput(opts, nil)["app"]
			var kinds []string
			for _, e := range got.Exports {
				kinds = append(kinds, e.Type)
			}
			if !reflect.DeepEqual(kinds, tc.want) {
				t.Fatalf("exports = %v, want %v", kinds, tc.want)
			}
			if !reflect.DeepEqual(got.Tags, []string{"example:latest"}) {
				t.Fatalf("tags changed: %v", got.Tags)
			}
			for i, e := range tc.exports {
				if !reflect.DeepEqual(got.Exports[i].Attrs, e.Attrs) || got.Exports[i].OutputDir != e.OutputDir || (got.Exports[i].Output == nil) != (e.Output == nil) {
					t.Fatalf("original output %d changed: %+v", i, got.Exports[i])
				}
			}
		})
	}
}

func TestDockerLoadOnlySelectedBuildTargets(t *testing.T) {
	for _, selected := range [][]string{{"app", "outline"}, {}} {
		opts := map[string]build.Options{
			"app":        {},
			"dependency": {},
			"outline":    {CallFunc: &build.CallFunc{Name: "outline"}},
		}
		got := WithDockerLoadOutput(opts, selected)
		if len(got["dependency"].Exports) != 0 || len(got["outline"].Exports) != 0 {
			t.Fatal("loaded an unrequested target or call target")
		}
		want := 0
		if len(selected) != 0 {
			want = 1
		}
		if len(got["app"].Exports) != want {
			t.Fatalf("app exports = %v", got["app"].Exports)
		}
	}
}
