package commands

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

func wrapBuildError(err error, bake bool) error {
	if err == nil {
		return nil
	}

	errMsg := err.Error()

	// Check for OpenTelemetry schema conflict errors
	if strings.Contains(errMsg, "conflicting Schema URL") || strings.Contains(errMsg, "cannot merge resource") {
		msg := fmt.Sprintf("%s\n\nThis error is usually caused by conflicting OpenTelemetry environment variables.\nTo resolve this issue, try setting DEPOT_DISABLE_OTEL=1 in your environment.", errMsg)
		return &wrapped{err, msg}
	}

	// Check for gRPC errors
	st, ok := grpcerrors.AsGRPCStatus(err)
	if ok {
		if st.Code() == codes.Unimplemented && strings.Contains(st.Message(), "unsupported frontend capability moby.buildkit.frontend.contexts") {
			msg := "current frontend does not support --build-context."
			if bake {
				msg = "current frontend does not support defining additional contexts for targets."
			}
			msg += " Named contexts are supported since Dockerfile v1.4. Use #syntax directive in Dockerfile or update to latest BuildKit."
			return &wrapped{err, msg}
		}
	}
	return err
}

type wrapped struct {
	err error
	msg string
}

func (w *wrapped) Error() string {
	return w.msg
}

func (w *wrapped) Unwrap() error {
	return w.err
}

func retryRetryableErrors(ctx context.Context, f func() error) error {
	maxRetryCountEnv := os.Getenv("DEPOT_BUILDKIT_ERROR_MAX_RETRY_COUNT")
	maxRetryCount := 5
	if maxRetryCountEnv != "" {
		maxRetryCount, _ = strconv.Atoi(maxRetryCountEnv)
	}

	retryCount := 0
	for {
		err := f()
		if !shouldRetryError(err) {
			return err
		}
		if retryCount >= maxRetryCount {
			return err
		}
		retryCount++
		fmt.Printf("\nReceived retryable BuildKit error, retrying: %v\n", err)
		fmt.Println()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func shouldRetryError(err error) bool {
	if err == nil {
		return false
	}

	if strings.Contains(err.Error(), "inconsistent graph state") {
		return true
	}

	if strings.Contains(err.Error(), "failed to get state for index") {
		return true
	}

	return false
}

func rewriteFriendlyErrors(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "header key \"exclude-patterns\" contains value with non-printable ASCII characters") {
		return errors.New(err.Error() + ". Please check your .dockerignore file for invalid characters.")
	}
	if strings.Contains(err.Error(), "failed to calculate checksum of ref") {
		pattern := `failed to solve: failed to compute cache key: failed to calculate checksum of ref [^:]+::[^:]+:`
		re := regexp.MustCompile(pattern)

		simplified := re.ReplaceAllString(err.Error(), "")
		return errors.New(simplified + ". Please check if the files exist in the context.")
	}
	if strings.Contains(err.Error(), "code = Canceled desc = grpc: the client connection is closing") {
		return errors.New("build canceled")
	}
	return err
}
