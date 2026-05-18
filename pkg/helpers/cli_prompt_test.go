package helpers

import (
	"os"
	"strings"
	"testing"
)

func TestSecretValueFromInputRejectsEmptyNonTerminalStdin(t *testing.T) {
	oldStdin := os.Stdin
	t.Cleanup(func() {
		os.Stdin = oldStdin
	})

	file, err := os.CreateTemp(t.TempDir(), "empty-stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	os.Stdin = file

	_, err = SecretValueFromInput("Enter secret: ")
	if err == nil {
		t.Fatal("expected error for empty stdin")
	}
	if !strings.Contains(err.Error(), "no secret value provided on stdin") {
		t.Fatalf("error = %q", err)
	}
}

func TestSecretValueFromInputReadsNonTerminalStdin(t *testing.T) {
	oldStdin := os.Stdin
	t.Cleanup(func() {
		os.Stdin = oldStdin
	})

	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("secret\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	os.Stdin = file

	got, err := SecretValueFromInput("Enter secret: ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "secret" {
		t.Fatalf("SecretValueFromInput() = %q, want %q", got, "secret")
	}
}
