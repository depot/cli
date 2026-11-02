package load

import "testing"

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
