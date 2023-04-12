package helpers

import (
	"os"
	"path/filepath"

	"github.com/depot/cli/pkg/project"
)

func ResolveProjectID(id string, cwd string) string {
	if id != "" {
		return id
	}

	id = os.Getenv("DEPOT_PROJECT_ID")

	if id == "" {
		cwd, _ := filepath.Abs(cwd)
		config, _, err := project.ReadConfig(cwd)
		if err == nil {
			id = config.ID
		}
	}

	return id
}

// Returns all directories for any files.  If no files are specified then
// the current working directory is returned.  Special handling for stdin
// is also included by assuming the current working directory.
func WorkingDirectories(files []string) ([]string, error) {
	directories := []string{}
	if len(files) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		directories = append(directories, cwd)
	}

	for _, file := range files {
		if file == "-" {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			directories = append(directories, cwd)
			continue
		}
		directories = append(directories, filepath.Dir(file))
	}

	return directories, nil
}
