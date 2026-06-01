package sandbox

import (
	"fmt"
	"io"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func writeExecuteCommandResponse(resp *civ1.ExecuteCommandResponse, stdout, stderr io.Writer) (int32, bool, error) {
	if out := resp.GetStdoutRaw(); len(out) > 0 {
		if _, err := stdout.Write(out); err != nil {
			return 0, false, fmt.Errorf("write stdout: %w", err)
		}
	} else if out, ok := resp.GetMessage().(*civ1.ExecuteCommandResponse_Stdout); ok {
		if _, err := fmt.Fprint(stdout, out.Stdout); err != nil {
			return 0, false, fmt.Errorf("write stdout: %w", err)
		}
	}

	if errOut := resp.GetStderrRaw(); len(errOut) > 0 {
		if _, err := stderr.Write(errOut); err != nil {
			return 0, false, fmt.Errorf("write stderr: %w", err)
		}
	} else if errOut, ok := resp.GetMessage().(*civ1.ExecuteCommandResponse_Stderr); ok {
		if _, err := fmt.Fprint(stderr, errOut.Stderr); err != nil {
			return 0, false, fmt.Errorf("write stderr: %w", err)
		}
	}

	if exit, ok := resp.GetMessage().(*civ1.ExecuteCommandResponse_ExitCode); ok {
		return exit.ExitCode, true, nil
	}
	return 0, false, nil
}
