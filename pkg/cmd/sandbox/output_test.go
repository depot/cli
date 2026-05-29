package sandbox

import (
	"bytes"
	"testing"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func TestWriteExecuteCommandResponseRawOutput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode, exited, err := writeExecuteCommandResponse(&civ1.ExecuteCommandResponse{
		Message:   &civ1.ExecuteCommandResponse_Stdout{Stdout: "text fallback"},
		StdoutRaw: []byte{0, 'o', 'k', 255},
		StderrRaw: []byte("raw err"),
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("writeExecuteCommandResponse returned error: %v", err)
	}
	if exited || exitCode != 0 {
		t.Fatalf("exitCode = %d, exited = %v, want no exit", exitCode, exited)
	}
	if got := stdout.Bytes(); !bytes.Equal(got, []byte{0, 'o', 'k', 255}) {
		t.Fatalf("stdout = %v, want raw bytes", got)
	}
	if got := stderr.String(); got != "raw err" {
		t.Fatalf("stderr = %q, want raw err", got)
	}
}

func TestWriteExecuteCommandResponseExitCode(t *testing.T) {
	exitCode, exited, err := writeExecuteCommandResponse(&civ1.ExecuteCommandResponse{
		Message: &civ1.ExecuteCommandResponse_ExitCode{ExitCode: 42},
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("writeExecuteCommandResponse returned error: %v", err)
	}
	if !exited || exitCode != 42 {
		t.Fatalf("exitCode = %d, exited = %v, want exit 42", exitCode, exited)
	}
}
