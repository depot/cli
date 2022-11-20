package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cli/safeexec"
	"github.com/depot/cli/pkg/api"
	"github.com/hashicorp/go-version"
	"gopkg.in/yaml.v2"
)

// Check whether the depot binary was found under the Homebrew prefix
func IsUnderHomebrew() bool {
	binary, err := os.Executable()
	if err != nil {
		return false
	}

	brewExe, err := safeexec.LookPath("brew")
	if err != nil {
		return false
	}

	brewPrefixBytes, err := exec.Command(brewExe, "--prefix").Output()
	if err != nil {
		return false
	}

	brewBinPrefix := filepath.Join(strings.TrimSpace(string(brewPrefixBytes)), "bin") + string(filepath.Separator)
	return strings.HasPrefix(binary, brewBinPrefix)
}

type StateEntry struct {
	CheckedForUpdateAt time.Time            `yaml:"checkedForUpdateAt"`
	LatestRelease      *api.ReleaseResponse `yaml:"latestRelease"`
}

func CheckForUpdate(stateFilePath, currentVersion string) (*api.ReleaseResponse, error) {
	state, _ := readStateFile(stateFilePath)
	if state != nil && time.Since(state.CheckedForUpdateAt) < time.Hour*1 {
		return nil, nil
	}

	release, err := api.LatestRelease()
	if err != nil {
		return nil, err
	}

	state = &StateEntry{CheckedForUpdateAt: time.Now(), LatestRelease: release}
	err = writeStateFile(stateFilePath, state)
	if err != nil {
		return nil, err
	}

	if versionGreaterThan(release.Version, currentVersion) {
		return release, nil
	}

	return nil, nil
}

func readStateFile(stateFilePath string) (*StateEntry, error) {
	content, err := os.ReadFile(stateFilePath)
	if err != nil {
		return nil, err
	}

	var stateEntry StateEntry
	err = yaml.Unmarshal(content, &stateEntry)
	if err != nil {
		return nil, err
	}

	return &stateEntry, nil
}

func writeStateFile(stateFilePath string, state *StateEntry) error {
	content, err := yaml.Marshal(state)
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Dir(stateFilePath), 0755)
	if err != nil {
		return err
	}

	err = os.WriteFile(stateFilePath, content, 0600)
	return err
}

func versionGreaterThan(a, b string) bool {
	versionA, err := version.NewVersion(a)
	if err != nil {
		return false
	}
	versionB, err := version.NewVersion(b)
	if err != nil {
		return false
	}
	return versionA.GreaterThan(versionB)
}
