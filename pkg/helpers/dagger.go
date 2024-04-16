package helpers

import (
	"fmt"
	"os/exec"
	"strings"
)

func ResolveDaggerVersion() (string, error) {
	daggerPath, err := exec.LookPath("dagger")
	if err != nil {
		return "", err
	}

	output, err := exec.Command(daggerPath, "version").Output()
	if err != nil {
		return "", err
	}

	parsed := strings.Split(string(output), " ")
	if len(parsed) < 2 {
		return "", fmt.Errorf("unable able to parse dagger version")
	}
	return parsed[1], nil
}
