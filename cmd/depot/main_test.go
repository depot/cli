package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/docker/cli/cli"
)

func TestStatusCodeFromErrorPreservesStatusErrorCode(t *testing.T) {
	var stderr bytes.Buffer

	code := statusCodeFromError(cli.StatusError{Status: "exit status 7", StatusCode: 7}, &stderr)
	if code != 7 {
		t.Fatalf("expected status code 7, got %d", code)
	}
	if strings.TrimSpace(stderr.String()) != "exit status 7" {
		t.Fatalf("expected status message, got %q", stderr.String())
	}
}

func TestStatusCodeFromErrorDefaultsZeroStatusErrorCodeToOne(t *testing.T) {
	var stderr bytes.Buffer

	code := statusCodeFromError(cli.StatusError{Status: "failed"}, &stderr)
	if code != 1 {
		t.Fatalf("expected status code 1, got %d", code)
	}
	if strings.TrimSpace(stderr.String()) != "failed" {
		t.Fatalf("expected status message, got %q", stderr.String())
	}
}

func TestStatusCodeFromErrorPrintsGenericErrors(t *testing.T) {
	var stderr bytes.Buffer

	code := statusCodeFromError(errors.New("failed"), &stderr)
	if code != 1 {
		t.Fatalf("expected status code 1, got %d", code)
	}
	if strings.TrimSpace(stderr.String()) != "failed" {
		t.Fatalf("expected generic error message, got %q", stderr.String())
	}
}
