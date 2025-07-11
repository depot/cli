package machine

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/cleanup"
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
		resp, err := client.GetBuildKitConnection(ctx, api.WithAuthentication(connect.NewRequest(&req), m.Token))
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
			fmt.Printf("error reporting health: %s", err.Error())
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

func (m *Machine) Client(ctx context.Context) (*client.Client, error) {
	if m.client != nil {
		return m.client, nil
	}

	opts := []client.ClientOpt{
		client.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			addr = strings.TrimPrefix(addr, "tcp://")
			return net.Dial("tcp", addr)
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
