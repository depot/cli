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

	"github.com/bufbuild/connect-go"
	"github.com/depot/cli/pkg/api"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
)

type Machine struct {
	BuildID  string
	Token    string
	Platform string

	reportHealth sync.Once
}

// Platform can be "amd64" or "arm64".
// This reports health continually to the Depot API and waits for the buildkit
// machine to be ready.  This can be canceled by canceling the context.
func Acquire(ctx context.Context, buildID, token, platform string) (*Buildkit, error) {
	m := &Machine{
		BuildID:  buildID,
		Token:    token,
		Platform: platform,
	}
	m.reportHealth.Do(func() {
		go func() {
			err := m.ReportHealth(ctx)
			if err != nil {
				log.Printf("warning: failed to report health for %s machine: %v\n", m.Platform, err)
			}
		}()
	})

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
			return &Buildkit{
				Addr:       connection.Active.Endpoint,
				ServerName: connection.Active.ServerName,
				CACert:     connection.Active.CaCert.Cert,
				Cert:       connection.Active.Cert.Cert,
				Key:        connection.Active.Cert.Key,
			}, nil
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

func (m *Machine) ReportHealth(ctx context.Context) error {
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
		err := m.doReportHealth(ctx, client, builderPlatform)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			fmt.Printf("error reporting health: %s", err.Error())
			client = api.NewBuildClient()
		}
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return nil
		}
	}
}

func (m *Machine) doReportHealth(ctx context.Context, client cliv1connect.BuildServiceClient, builderPlatform cliv1.BuilderPlatform) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req := cliv1.ReportBuildHealthRequest{BuildId: m.BuildID, Platform: builderPlatform}
	_, err := client.ReportBuildHealth(ctx, api.WithAuthentication(connect.NewRequest(&req), m.Token))
	return err
}

type Buildkit struct {
	Addr       string
	ServerName string
	CACert     string
	Cert       string
	Key        string

	client *client.Client
}

func (b *Buildkit) Release() error {
	// TODO: stop reporting health here.
	if b.client != nil {
		return b.client.Close()
	}
	return nil
}

func (m *Buildkit) Client(ctx context.Context) (*client.Client, error) {
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

		opts = append(opts, client.WithCredentials("", caCert, cert, key))
	}

	c, err := client.New(ctx, m.Addr, opts...)
	if err != nil {
		return nil, err
	}

	m.client = c
	return c, nil
}

func (m *Buildkit) CheckReady(ctx context.Context) (*client.Client, error) {
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
func (m *Buildkit) Connect(ctx context.Context) (*client.Client, error) {
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
