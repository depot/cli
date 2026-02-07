package api

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

func TestRetryDialUntilSuccess(t *testing.T) {
	// "reserve" a port.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to listen on random port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Wait a a bit so that the dialer will retry a few times.
		time.Sleep(50 * time.Millisecond)

		// Start a server to listen on the reserved port.
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			cancel()
			return
		}
		t.Log("listener", listener.Addr())
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			cancel()
			return
		}
		defer conn.Close()
	}()

	dialer := RetryDialUntilSuccess(10 * time.Millisecond)
	conn, err := dialer(ctx, "tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	conn.Close()
	wg.Wait()
}

func TestRacingDialer(t *testing.T) {
	// "reserve" a port.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to listen on random port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Wait a a bit so that the dialer will retry a few times.
		time.Sleep(50 * time.Millisecond)

		// Start a server to listen on the reserved port.
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			cancel()
			return
		}
		t.Log("listener", listener.Addr())
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			cancel()
			return
		}
		defer conn.Close()
	}()

	dialer := RacingDialer(DialNoTimeout(), RetryDialUntilSuccess(10*time.Millisecond))
	conn, err := dialer(ctx, "tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	conn.Close()
	wg.Wait()
}
