package migrate

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	secretRegex   = regexp.MustCompile(`\$\{\{\s*secrets\.([A-Za-z_][A-Za-z0-9_]*)\s*[}\s|]`)
	variableRegex = regexp.MustCompile(`\$\{\{\s*vars\.([A-Za-z_][A-Za-z0-9_]*)\s*[}\s|]`)
)

// DetectSecretsFromFile scans a file's raw content for ${{ secrets.X }} references.
func DetectSecretsFromFile(path string) ([]string, error) {
	return detectMatchesFromFile(path, secretRegex)
}

// DetectVariablesFromFile scans a file's raw content for ${{ vars.X }} references.
func DetectVariablesFromFile(path string) ([]string, error) {
	return detectMatchesFromFile(path, variableRegex)
}

// DetectSecretsFromDir scans all .yml/.yaml files in a directory for secrets.
func DetectSecretsFromDir(dir string) ([]string, error) {
	return detectMatchesFromDir(dir, secretRegex)
}

// DetectVariablesFromDir scans all .yml/.yaml files in a directory for variables.
func DetectVariablesFromDir(dir string) ([]string, error) {
	return detectMatchesFromDir(dir, variableRegex)
}

func detectMatchesFromFile(path string, re *regexp.Regexp) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	matches := re.FindAllStringSubmatch(string(content), -1)
	values := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			values = append(values, m[1])
		}
	}

	return dedupeSorted(values), nil
}

func detectMatchesFromDir(dir string, re *regexp.Regexp) ([]string, error) {
	all := make([]string, 0)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}

		matches, err := detectMatchesFromFile(path, re)
		if err != nil {
			return err
		}
		all = append(all, matches...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return dedupeSorted(all), nil
}

func dedupeSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v != "" {
			seen[v] = struct{}{}
		}
	}

	result := make([]string, 0, len(seen))
	for v := range seen {
		result = append(result, v)
	}

	sort.Strings(result)
	return result
}
