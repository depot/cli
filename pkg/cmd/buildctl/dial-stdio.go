package buildctl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	depot "github.com/depot/cli/internal/build"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/machine"
	"github.com/depot/cli/pkg/progress"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/moby/buildkit/client"
	"github.com/spf13/cobra"
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
	cmd.Version = fmt.Sprintf("%s %s", depot.Version, Commit)
	return cmd
}

var Commit = func() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return ""
}()

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

	var (
		once  sync.Once
		state ProxyState

		cancelStatus   func()
		finishStatus   func()
		buildFinish    func(error)
		machineRelease func() error
	)
	defer func() {
		// Forwards remaining status messages.
		if cancelStatus != nil {
			cancelStatus()
		}

		// Waits until status has finished sending.
		if finishStatus != nil {
			finishStatus()
		}

		// Stop reporting that the CLI is running and close the buildkitd connection.
		if machineRelease != nil {
			_ = machineRelease()
		}

		// Report that the build has finished.
		if buildFinish != nil {
			buildFinish(state.Err)
		}
	}()

	status := make(chan *client.SolveStatus, 1024)
	listener := func(s *client.SolveStatus) {
		select {
		case status <- s:
		default:
		}
	}

	acquireState := func() ProxyState {
		once.Do(func() {
			req := &cliv1.CreateBuildRequest{
				ProjectId: projectID,
				Options:   []*cliv1.BuildOptions{{Command: cliv1.Command_COMMAND_BUILD}},
			}
			build, err := helpers.BeginBuild(ctx, req, token)
			if err != nil {
				state.Err = fmt.Errorf("unable to begin build: %w", err)
				return
			}

			ctx2 := context.TODO()
			ctx2, cancelStatus = context.WithCancel(ctx2)
			state.Reporter, finishStatus, _ = progress.NewProgress(ctx2, build.ID, build.Token, progress.Quiet)
			state.Reporter.AddListener(listener)

			state.SummaryURL = build.BuildURL
			buildFinish = build.Finish

			if os.Getenv("DEPOT_NO_SUMMARY_LINK") == "" {
				state.Reporter.Log("[depot] build: "+state.SummaryURL, nil)
			}

			var builder *machine.Machine
			state.Err = state.Reporter.WithLog("[depot] launching "+platform+" machine", func() error {
				for i := 0; i < 2; i++ {
					builder, state.Err = machine.Acquire(ctx, build.ID, build.Token, platform)
					if state.Err == nil {
						break
					}
				}
				return state.Err
			})
			if state.Err != nil {
				state.Err = fmt.Errorf("unable to acquire builder: %w", state.Err)
				return
			}

			machineRelease = builder.Release

			state.Err = state.Reporter.WithLog("[depot] connecting to "+platform+" machine", func() error {
				buildkitConn, err := tlsConn(ctx, builder)
				if err != nil {
					state.Err = fmt.Errorf("unable to connect: %w", err)
					return state.Err
				}

				state.Conn, err = BuildkitdClient(ctx, buildkitConn, builder.Addr)
				if err != nil {
					state.Err = fmt.Errorf("unable to dial: %w", err)
					return state.Err
				}

				return nil
			})
		})
		return state
	}

	buildx := &StdioConn{}
	Proxy(ctx, buildx, acquireState, platform, status)

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

func tlsConn(ctx context.Context, builder *machine.Machine) (net.Conn, error) {
	// Uses similar retry logic as the depot buildx driver.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM([]byte(builder.CACert)); !ok {
		return nil, fmt.Errorf("failed to append ca certs")
	}

	cfg := &tls.Config{RootCAs: certPool}
	if builder.Cert != "" || builder.Key != "" {
		cert, err := tls.X509KeyPair([]byte(builder.Cert), []byte(builder.Key))
		if err != nil {
			return nil, fmt.Errorf("could not read certificate/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	dialer := &tls.Dialer{Config: cfg}
	addr := strings.TrimPrefix(builder.Addr, "tcp://")

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
