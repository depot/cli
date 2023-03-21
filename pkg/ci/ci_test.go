package ci

import (
	"os"
	"testing"
)

func TestProvider(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{
			name: "Check Jenkins",
			env:  map[string]string{"BUILD_ID": "1", "JENKINS_URL": "1"},
			want: true,
		},
		{
			name: "Check dsari",
			env:  map[string]string{"DSARI": "1"},
			want: true,
		},
		{
			name: "Check GitHub Actions",
			env:  map[string]string{"GITHUB_ACTIONS": "1"},
			want: true,
		},
		{
			name: "Check Travis CI",
			env:  map[string]string{"TRAVIS": "1"},
			want: true,
		},
		{
			name: "not CI by default",
			want: false,
		},
		{
			name: "Check codeship",
			env:  map[string]string{"CI_NAME": "codeship"},
			want: true,
		},
		{
			name: "Check TaskCluster",
			env:  map[string]string{"TASK_ID": "1", "RUN_ID": "1"},
			want: true,
		},
		{
			name: "Heroku is a special case heuristic that checks NODE",
			env:  map[string]string{"NODE": "/app/.heroku/node/bin/node"},
			want: true,
		},
		{
			name: "Check sourcehut",
			env:  map[string]string{"CI_NAME": "sourcehut"},
			want: true,
		},
		{
			name: "Check Woodpecker",
			env:  map[string]string{"CI": "woodpecker"},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Clearenv()
			for k, v := range tt.env {
				os.Setenv(k, v)
			}

			if _, got := Provider(); got != tt.want {
				t.Errorf("IsCI() = %v, want %v", got, tt.want)
			}
		})
	}
}
