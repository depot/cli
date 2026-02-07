package api

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// DialContextFunc is a function that dials a context, network, and address.
type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

// RetryDialUntilSuccess will retry every `retryTimeout` until it succeeds.
func RetryDialUntilSuccess(retryTimeout time.Duration) DialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		for {
			dialer := &net.Dialer{
				Timeout:   retryTimeout,
				KeepAlive: 30 * time.Second, // Similar to the default HTTP dialer.
			}
			c, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					continue
				}
				if errors.Is(err, os.ErrDeadlineExceeded) {
					continue
				}
				// Testing hook.
				if testing.Testing() && strings.Contains(err.Error(), "connection refused") {
					continue
				}
			}
			return c, err
		}
	}
}

// DialNoTimeout will block with no timeout or until the context is canceled.
func DialNoTimeout() DialContextFunc {
	dialer := &net.Dialer{}
	return dialer.DialContext
}

// DefaultHTTPDialer has the same options as the default HTTP dialer.
func DefaultHTTPDialer() DialContextFunc {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return dialer.DialContext
}

// RacingDialer is a custom dialer that attempts to connect to a given address.
//
// It uses two different dialers.
// The dialer connects first is returned, and the other is canceled.
//
// The first has a short timeout (200 ms) and continues to retry until it succeeds.
// The second dialer has no timeout and will block until it either succeeds or fails.
//
// We are doing this because we see connection timeouts perhaps caused by some competing network routes.
// Our workaround is to use a short timeout dialer that will retry until it succeeds.
func RacingDialer(dialers ...DialContextFunc) DialContextFunc {
	if len(dialers) == 0 {
		return DialNoTimeout()
	}

	return func(ctx context.Context, network, address string) (net.Conn, error) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		type dialResult struct {
			conn net.Conn
			err  error
		}
		resultCh := make(chan dialResult, len(dialers))
		for _, dialer := range dialers {
			go func(d DialContextFunc) {
				c, err := d(ctx, network, address)
				resultCh <- dialResult{conn: c, err: err}
			}(dialer)
		}

		var connError error
		for range len(dialers) {
			res := <-resultCh
			if res.err == nil {
				cancel()
				return res.conn, nil
			} else {
				connError = res.err
			}
		}
		return nil, connError
	}
}
