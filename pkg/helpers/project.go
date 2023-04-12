package helpers

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/depot/cli/pkg/project"
	"github.com/sirupsen/logrus"
)

// Returns the project ID from the environment or config file.
// Searches from the directory of each of the files.
func ResolveProjectID(id string, files ...string) string {
	if id != "" {
		return id
	}

	id = os.Getenv("DEPOT_PROJECT_ID")
	if id != "" {
		return id
	}

	dirs, err := WorkingDirectories(files...)
	if err != nil {
		return ""
	}

	// Only a single project ID is allowed.
	uniqueIDs := make(map[string]struct{})

	for _, dir := range dirs {
		cwd, _ := filepath.Abs(dir)
		config, _, err := project.ReadConfig(cwd)
		if err == nil {
			id = config.ID
			uniqueIDs[id] = struct{}{}
		}
	}

	// TODO: Warn for multiple project IDs. Is this an error?
	if len(uniqueIDs) > 1 {
		ids := []string{}
		for id := range uniqueIDs {
			ids = append(ids, id)
		}

		logrus.Warnf("More than one project ID discovered: %s.  Using project: %s", strings.Join(ids, ", "), id)
	}

	return id
}

// Returns all directories for any files.  If no files are specified then
// the current working directory is returned.  Special handling for stdin
// is also included by assuming the current working directory.
func WorkingDirectories(files ...string) ([]string, error) {
	directories := []string{}
	if len(files) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		directories = append(directories, cwd)
	}

	for _, file := range files {
		if file == "-" || file == "" {
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
