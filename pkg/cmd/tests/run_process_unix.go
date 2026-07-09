//go:build !windows

package tests

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const processTerminationGracePeriod = 2 * time.Second

func configureShellCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func commandExitCode(exitErr *exec.ExitError) int {
	if code := exitErr.ExitCode(); code >= 0 {
		return code
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
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
		pgid := processGroupID(cmd)
		killProcessGroup(pgid, syscall.SIGTERM)
		timer := time.NewTimer(processTerminationGracePeriod)
		defer timer.Stop()
		err, commandExited := waitForCommandAndProcessGroup(pgid, done, timer.C)
		if !commandExited || processGroupExists(pgid) {
			killProcessGroup(pgid, syscall.SIGKILL)
		}
		if !commandExited {
			err = <-done
		}
		waitForProcessGroupExit(pgid)
		if err == nil {
			return ctx.Err()
		}
		return errors.Join(ctx.Err(), err)
	}
}

func processGroupID(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}

func killProcessGroup(pgid int, signal syscall.Signal) {
	if pgid <= 0 {
		return
	}
	_ = syscall.Kill(-pgid, signal)
}

func waitForCommandAndProcessGroup(pgid int, done <-chan error, timeout <-chan time.Time) (error, bool) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var waitErr error
	commandExited := false
	for {
		if commandExited && !processGroupExists(pgid) {
			return waitErr, true
		}
		select {
		case waitErr = <-done:
			commandExited = true
		case <-ticker.C:
		case <-timeout:
			return waitErr, commandExited
		}
	}
}

func waitForProcessGroupExit(pgid int) {
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !processGroupExists(pgid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func processGroupExists(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
