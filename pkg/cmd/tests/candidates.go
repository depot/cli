package tests

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

var runCandidatesCommandFunc = runCandidatesCommand

func loadCandidates(ctx context.Context, r io.Reader, candidatesFile, candidatesCommand string, stderr io.Writer) ([]string, error) {
	if candidatesFile != "" && candidatesCommand != "" {
		return nil, fmt.Errorf("--candidates-file and --candidates-command are mutually exclusive")
	}
	if candidatesFile != "" {
		file, err := os.Open(candidatesFile)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return readCandidates(file)
	}
	if candidatesCommand != "" {
		var stdout bytes.Buffer
		if err := runCandidatesCommandFunc(ctx, candidatesCommand, &stdout, stderr); err != nil {
			return nil, fmt.Errorf("candidate command failed: %w", err)
		}
		return readCandidates(&stdout)
	}

	return readCandidates(r)
}

func runCandidatesCommand(ctx context.Context, command string, stdout, stderr io.Writer) error {
	shell, args := shellCommand(command)
	subCmd := exec.Command(shell, args...)
	configureShellCommand(subCmd)
	subCmd.Stdout = stdout
	subCmd.Stderr = stderr
	return runCancellableShellCommand(ctx, subCmd)
}

func readCandidates(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	var candidates []string
	for scanner.Scan() {
		candidate := strings.TrimSpace(scanner.Text())
		if candidate == "" {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}
