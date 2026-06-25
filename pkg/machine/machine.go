package machine

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/cleanup"
	"github.com/depot/cli/pkg/debuglog"
	"github.com/depot/cli/pkg/helpers"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Machine struct {
	BuildID  string
	Token    string
	Platform string

	Addr       string
	ServerName string
	CACert     string
	Cert       string
	Key        string

	client           *client.Client
	useGzip          bool
	reportHealthDone chan struct{}
}

// Platform can be "amd64" or "arm64".
// This reports health continually to the Depot API and waits for the buildkit
// machine to be ready.  This can be canceled by canceling the context.
func Acquire(ctx context.Context, buildID, token, platform string) (*Machine, error) {
	m := &Machine{
		BuildID:          buildID,
		Token:            token,
		Platform:         platform,
		reportHealthDone: make(chan struct{}),
	}

	go func() {
		err := m.ReportHealth()
		if err != nil {
			log.Printf("warning: failed to report health for %s machine: %v\n", m.Platform, err)
		}
	}()

	var builderPlatform cliv1.BuilderPlatform
	switch m.Platform {
	case "amd64":
		builderPlatform = cliv1.BuilderPlatform_BUILDER_PLATFORM_AMD64
	case "arm64":
		builderPlatform = cliv1.BuilderPlatform_BUILDER_PLATFORM_ARM64
	default:
		return nil, errors.Errorf("unsupported platform: %s", m.Platform)
	}

	client := api.NewBuildClient()

	for {
		req := cliv1.GetBuildKitConnectionRequest{
			BuildId:  m.BuildID,
			Platform: builderPlatform,
		}
		// Bound each poll with a per-call deadline so a half-open API connection
		// can't wedge acquisition forever; an error aborts so the build surfaces
		// it instead of hanging.
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		resp, err := client.GetBuildKitConnection(callCtx, api.WithAuthentication(connect.NewRequest(&req), m.Token))
		cancel()
		if err != nil {
			return nil, err
		}

		switch connection := resp.Msg.Connection.(type) {
		case *cliv1.GetBuildKitConnectionResponse_Active:
			m.Addr = connection.Active.Endpoint
			m.ServerName = connection.Active.ServerName

			if helpers.IsDepotGitHubActionsRunner() {
				// if this is failing, we can check what the actual issue is by ssh'ing into the GHA runner machine
				_ = AllowBuilderIPViaHTTP(ctx, m.Addr)
			}

			// When testing locally, we don't have TLS certs.
			if connection.Active.CaCert == nil || connection.Active.Cert == nil {
				return m, nil
			}
			m.CACert = connection.Active.CaCert.Cert
			m.Cert = connection.Active.Cert.Cert
			m.Key = connection.Active.Cert.Key
			if connection.Active.Compressor != nil {
				m.useGzip = connection.Active.GetGzip() != nil
			}
			return m, nil
		case *cliv1.GetBuildKitConnectionResponse_Pending:
			select {
			case <-time.After(time.Duration(connection.Pending.WaitMs) * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
	}
}

func (m *Machine) ReportHealth() error {
	var builderPlatform cliv1.BuilderPlatform
	switch m.Platform {
	case "amd64":
		builderPlatform = cliv1.BuilderPlatform_BUILDER_PLATFORM_AMD64
	case "arm64":
		builderPlatform = cliv1.BuilderPlatform_BUILDER_PLATFORM_ARM64
	default:
		return errors.Errorf("unsupported platform: %s", m.Platform)
	}

	client := api.NewBuildClient()
	for {
		cancelAt, err := m.doReportHealth(context.Background(), client, builderPlatform)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			debuglog.Log("ReportHealth() error reporting health: %s", err.Error())
			client = api.NewBuildClient()
		}

		// If canceling the build was requested, release the machine to interrupt the build step.
		if cancelAt != nil && time.Now().After(cancelAt.AsTime()) {
			_ = m.Release()
		}
		select {
		case <-time.After(5 * time.Second):
		case <-m.reportHealthDone:
			return nil
		}
	}
}

func (m *Machine) doReportHealth(ctx context.Context, client cliv1connect.BuildServiceClient, builderPlatform cliv1.BuilderPlatform) (*timestamppb.Timestamp, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req := cliv1.ReportBuildHealthRequest{BuildId: m.BuildID, Platform: builderPlatform}
	res, err := client.ReportBuildHealth(ctx, api.WithAuthentication(connect.NewRequest(&req), m.Token))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetCancelsAt(), nil
}

func (m *Machine) Release() error {
	close(m.reportHealthDone)
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// buildKitKeepaliveOnce guards the one-time keepalive env defaults below.
var buildKitKeepaliveOnce sync.Once

// applyBuildKitKeepaliveDefaults sets the DEPOT_KEEPALIVE_CLIENT_* values the
// depot/buildkit fork reads when it builds the gRPC client, unless the operator
// already set them. Time is the idle interval between keepalive pings and
// Timeout is how long to wait for a ping ack before closing the connection, so a
// dead builder is detected in roughly Time+Timeout while a solve stream is
// active. PermitWithoutStream is left off on purpose: the builder's keepalive
// enforcement policy can GOAWAY a client that pings on an idle, stream-less
// connection, and the hang we care about (DEP-5143) happens during an active
// solve where pings flow regardless.
func applyBuildKitKeepaliveDefaults() {
	buildKitKeepaliveOnce.Do(func() {
		setEnvDefault("DEPOT_KEEPALIVE_CLIENT_TIME_MS", "30000")
		setEnvDefault("DEPOT_KEEPALIVE_CLIENT_TIMEOUT_MS", "10000")
	})
}

func setEnvDefault(key, value string) {
	if os.Getenv(key) == "" {
		_ = os.Setenv(key, value)
	}
}

func (m *Machine) Client(ctx context.Context) (*client.Client, error) {
	if m.client != nil {
		return m.client, nil
	}

	// Enable gRPC keepalive on the BuildKit connection. buildkit's client.New
	// already wires grpc.WithKeepaliveParams, but the depot fork sources those
	// values from DEPOT_KEEPALIVE_CLIENT_* env vars and leaves them zero (i.e.
	// disabled) when unset — so a wedged solve stream to a dead builder blocks
	// forever and the build is pinned `running` (DEP-5143). Set sane defaults so
	// a half-open connection is detected in seconds.
	applyBuildKitKeepaliveDefaults()

	opts := []client.ClientOpt{
		client.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			addr = strings.TrimPrefix(addr, "tcp://")
			// A bare net.Dial has no connect timeout and ignores the context, so a
			// builder that accepts the TCP connection but never responds, or a DNS
			// hang, blocks indefinitely. Bound the dial and turn on TCP keepalive
			// as an OS-level backstop to the gRPC pings.
			dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
			return dialer.DialContext(ctx, "tcp", addr)
		}),
	}

	// We create all these files as buildkit does not allow control of the gRPC client
	// without using overly restrictive private structs.
	if m.Cert != "" {
		file, err := os.CreateTemp("", "depot-cert")
		if err != nil {
			return nil, errors.Wrap(err, "failed to create temp file")
		}
		defer file.Close()
		err = os.WriteFile(file.Name(), []byte(m.Cert), 0600)
		if err != nil {
			return nil, errors.Wrap(err, "failed to write cert to temp file")
		}
		cert := file.Name()
		cleanup.RegisterTmpfile(cert)

		file, err = os.CreateTemp("", "depot-key")
		if err != nil {
			return nil, errors.Wrap(err, "failed to create temp file")
		}
		defer file.Close()
		err = os.WriteFile(file.Name(), []byte(m.Key), 0600)
		if err != nil {
			return nil, errors.Wrap(err, "failed to write key to temp file")
		}
		key := file.Name()
		cleanup.RegisterTmpfile(key)

		file, err = os.CreateTemp("", "depot-ca-cert")
		if err != nil {
			return nil, errors.Wrap(err, "failed to create temp file")
		}
		defer file.Close()
		err = os.WriteFile(file.Name(), []byte(m.CACert), 0600)
		if err != nil {
			return nil, errors.Wrap(err, "failed to write CA cert to temp file")
		}
		caCert := file.Name()
		cleanup.RegisterTmpfile(caCert)

		opts = append(opts, client.WithCredentials(m.ServerName, caCert, cert, key))
	}

	if m.useGzip {
		useGzip := grpc.WithDefaultCallOptions(grpc.UseCompressor(gzip.Name))
		opts = append(opts, useGzip)
	}

	c, err := client.New(ctx, m.Addr, opts...)
	if err != nil {
		return nil, err
	}

	m.client = c
	return c, nil
}

func (m *Machine) CheckReady(ctx context.Context) (*client.Client, error) {
	client, err := m.Client(ctx)
	if err != nil {
		return client, err
	}

	// TODO: Switch to gRPC Healthchecks after exposing the client in the client.
	_, err = client.ListWorkers(ctx)
	return client, err
}

// Connect waits until the buildkitd is ready to accept connections.
// It tries to connect to the buildkitd every one second until it succeeds or
// the context is canceled.
func (m *Machine) Connect(ctx context.Context) (*client.Client, error) {
	var (
		client *client.Client
		err    error
	)
	client, err = m.CheckReady(ctx)
	if err == nil {
		return client, nil
	}

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("timed out connecting to machine: %w", err)
			}
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}

		client, err = m.CheckReady(ctx)
		if err == nil {
			return client, nil
		}
	}
}
