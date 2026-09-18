//go:build windows

package tests

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"time"
)

const processTerminationGracePeriod = 2 * time.Second

func configureShellCommand(cmd *exec.Cmd) {}

func commandExitCode(exitErr *exec.ExitError) int {
	if code := exitErr.ExitCode(); code >= 0 {
		return code
	}
	return 1
}

func runCancellableShellCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		killProcessTree(cmd)
		err := <-done
		if err == nil {
			return ctx.Err()
		}
		return errors.Join(ctx.Err(), err)
	}
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), processTerminationGracePeriod)
	defer cancel()

	killCmd := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	if err := killCmd.Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
