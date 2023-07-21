package buildctl

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

	depot "github.com/depot/cli/internal/build"
	"github.com/depot/cli/pkg/builder"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/progress"
	"github.com/docker/buildx/build"
	"github.com/moby/buildkit/client"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

func NewBuildctl() *cobra.Command {
	var cmd = &cobra.Command{
		Use:   "buildctl <command> [flags]",
		Short: "Forwards buildctl dial-stdio to depot",
	}
	cmd.AddCommand(NewCmdDial())
	cmd.AddCommand(&cobra.Command{
		Use:    "debug",
		Short:  "Mimics buildctl debug workers for buildx container drivers",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	})

	cmd.SetVersionTemplate(`{{with .Name}}{{printf "%s github.com/depot/cli " .}}{{end}}{{printf "%s\n" .Version}}`)
	cmd.Version = fmt.Sprintf("%s 2951a28cd7085eb18979b1f710678623d94ed578", depot.Version)
	return cmd
}

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
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	projectID := os.Getenv("DEPOT_PROJECT_ID")
	if projectID == "" {
		return fmt.Errorf("DEPOT_PROJECT_ID is not set")
	}

	token := os.Getenv("DEPOT_TOKEN")
	if token == "" {
		return fmt.Errorf("DEPOT_TOKEN is not set")
	}

	platform := os.Getenv("DEPOT_PLATFORM")
	if token == "" {
		return fmt.Errorf("DEPOT_PLATFORM is not set")
	}

	ctx2, cancel := context.WithCancel(context.TODO())
	defer cancel()
	reporter, err := progress.NewProgress(ctx2, "", token, "quiet")
	if err != nil {
		return err
	}
	wg := &sync.WaitGroup{}

	defer wg.Wait() // Required to ensure that the reporter is stopped before the context is cancelled.
	defer cancel()

	var (
		once       sync.Once
		finish     func(error)
		builderErr error
		buildkit   *grpc.ClientConn
	)
	defer func() {
		if finish != nil {
			finish(builderErr)
		}
	}()

	acquireBuilder := func() (*grpc.ClientConn, error) {
		once.Do(func() {
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
				builderErr = fmt.Errorf("unable to begin build: %w", err)
				return
			}

			reporter.SetBuildID(build.ID)
			wg.Add(1)
			go func() {
				reporter.Run(ctx2)
				wg.Done()
			}()

			finish = build.Finish
			report := func(s *client.SolveStatus) {
				for _, v := range s.Vertexes {
					if v.Completed == nil {
						fmt.Fprintln(os.Stderr, v.Name)
					} else if v.Started != nil {
						fmt.Fprintf(os.Stderr, "%s %.[3]*[2]f done\n", v.Name, v.Completed.Sub(*v.Started).Seconds(), 2)
					}
				}
				reporter.Write(s)
			}
			builder, err := builder.NewBuilder(token, build.ID, platform).Acquire(report)
			if err != nil {
				builderErr = fmt.Errorf("unable to acquire builder: %w", err)
				return
			}

			buildkitConn, err := tlsConn(ctx, builder)
			if err != nil {
				builderErr = fmt.Errorf("unable to connect: %w", err)
				return
			}

			buildkit, err = BuildkitdClient(ctx, buildkitConn, builder.Addr)
			if err != nil {
				builderErr = fmt.Errorf("unable to dial: %w", err)
				return
			}
		})
		return buildkit, builderErr
	}

	buildx := &StdioConn{}
	Proxy(ctx, buildx, acquireBuilder, platform, reporter)

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
