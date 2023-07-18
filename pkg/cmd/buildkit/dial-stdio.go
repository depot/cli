package buildkit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/depot/cli/pkg/builder"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/progress"
	"github.com/docker/buildx/build"
	"github.com/moby/buildkit/client"
	"github.com/spf13/cobra"
)

func NewCmdDial() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "dial-stdio",
		Short:  "Dial a remote buildkit instance and proxy stdin/stdout",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run()
		},
	}

	return cmd
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithSignals(ctx)

	projectID := os.Getenv("DEPOT_PROJECT_ID")
	if projectID == "" {
		return fmt.Errorf("DEPOT_PROJECT_ID is not set")
	}

	token := os.Getenv("DEPOT_TOKEN")
	if token == "" {
		return fmt.Errorf("DEPOT_TOKEN is not set")
	}

	validatedOpts := map[string]build.Options{"default": {}}
	exportPush := false
	exportLoad := false
	lint := false

	req := helpers.NewBuildRequest(
		projectID,
		validatedOpts,
		exportPush,
		exportLoad,
		lint,
	)
	build, err := helpers.BeginBuild(ctx, req, token)
	if err != nil {
		return fmt.Errorf("unable to begin build: %w", err)
	}
	var buildErr error
	defer func() { build.Finish(buildErr) }()

	ctx2, cancel := context.WithCancel(context.TODO())
	defer cancel()
	reporter, err := progress.NewProgress(ctx2, build.ID, token, "quiet")
	if err != nil {
		return err
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		reporter.Run(ctx2)
		wg.Done()
	}()
	defer wg.Wait() // Required to ensure that the reporter is stopped before the context is cancelled.
	defer cancel()

	report := func(s *client.SolveStatus) {
		reporter.Write(s)
	}
	builder, err := builder.NewBuilder(token, build.ID, "amd64").Acquire(report)
	if err != nil {
		return fmt.Errorf("unable to acquire builder: %w", err)
	}

	buildkitConn, err := tlsConn(ctx, builder)
	if err != nil {
		return fmt.Errorf("unable to connect: %w", err)
	}
	buildkit, err := BuildkitdClient(ctx, buildkitConn, builder.Addr)
	if err != nil {
		return fmt.Errorf("unable to dial: %w", err)
	}

	buildx := &StdioConn{}

	Proxy(ctx, buildx, buildkit, reporter)

	return nil
}

type StdioConn struct{}

func (s *StdioConn) Read(b []byte) (int, error) {
	return os.Stdin.Read(b)
}

func (s *StdioConn) Write(b []byte) (int, error) {
	return os.Stdout.Write(b)
}

func (s *StdioConn) Close() error {
	_ = os.Stdin.Close()
	_ = os.Stdout.Close()
	return nil
}

func (s *StdioConn) LocalAddr() net.Addr {
	return stdioAddr{}
}
func (s *StdioConn) RemoteAddr() net.Addr {
	return stdioAddr{}
}
func (s *StdioConn) SetDeadline(t time.Time) error {
	return nil
}
func (s *StdioConn) SetReadDeadline(t time.Time) error {
	return nil
}
func (s *StdioConn) SetWriteDeadline(t time.Time) error {
	return nil
}

type stdioAddr struct {
}

func (d stdioAddr) Network() string {
	return "pipe"
}

func (d stdioAddr) String() string {
	return "localhost"
}

func tlsConn(ctx context.Context, opts *builder.AcquiredBuilder) (net.Conn, error) {
	// Uses similar retry logic as the depot buildx driver.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(5)*time.Minute)
	defer cancel()

	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM([]byte(opts.CACert)); !ok {
		return nil, fmt.Errorf("failed to append ca certs")
	}

	cfg := &tls.Config{RootCAs: certPool}
	if opts.Cert != "" || opts.Key != "" {
		cert, err := tls.X509KeyPair([]byte(opts.Cert), []byte(opts.Key))
		if err != nil {
			return nil, fmt.Errorf("could not read certificate/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	dialer := &tls.Dialer{Config: cfg}
	addr := strings.TrimPrefix(opts.Addr, "tcp://")

	var (
		conn net.Conn
		err  error
	)
	for i := 0; i < 120; i++ {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			time.Sleep(1 * time.Second)
		}
	}

	return nil, err
}

// WithSignals returns a context that is canceled with SIGINT or SIGTERM.
func WithSignals(ctx context.Context) context.Context {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
			return
		}
	}()
	return ctx
}
