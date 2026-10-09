package machine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/debuglog"
	"github.com/depot/cli/pkg/helpers"
	"github.com/depot/cli/pkg/keepalive"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
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

	ClientOptions []client.ClientOpt

	client           *client.Client
	conn             *grpc.ClientConn
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
	if m.conn != nil {
		_ = m.conn.Close()
	}
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// TLSConfig returns the client TLS configuration, or nil when the machine
// does not use TLS.
func (m *Machine) TLSConfig() (*tls.Config, error) {
	if m.Cert == "" {
		return nil, nil
	}
	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM([]byte(m.CACert)); !ok {
		return nil, errors.New("failed to append ca certs")
	}
	cert, err := tls.X509KeyPair([]byte(m.Cert), []byte(m.Key))
	if err != nil {
		return nil, errors.Wrap(err, "could not read certificate/key")
	}
	return &tls.Config{RootCAs: certPool, ServerName: m.ServerName, Certificates: []tls.Certificate{cert}}, nil
}

func (m *Machine) dialOptions() ([]grpc.DialOption, error) {
	opts := []grpc.DialOption{grpc.WithKeepaliveParams(keepalive.ClientParameters())}
	tlsConfig, err := m.TLSConfig()
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)), grpc.WithAuthority(m.ServerName))
	}
	if m.useGzip {
		opts = append(opts, grpc.WithDefaultCallOptions(grpc.UseCompressor(gzip.Name)))
	}
	return opts, nil
}

func dialTCP(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", strings.TrimPrefix(addr, "tcp://"))
}

func (m *Machine) Client(ctx context.Context) (*client.Client, error) {
	if m.client != nil {
		return m.client, nil
	}

	dialOpts, err := m.dialOptions()
	if err != nil {
		return nil, err
	}
	opts := []client.ClientOpt{client.WithContextDialer(dialTCP)}
	for _, o := range dialOpts {
		opts = append(opts, client.WithGRPCDialOption(o))
	}
	opts = append(opts, m.ClientOptions...)

	c, err := client.New(ctx, m.Addr, opts...)
	if err != nil {
		return nil, err
	}

	m.client = c
	return c, nil
}

// Conn returns a gRPC connection to buildkitd for the services that the
// buildkit client does not expose, such as leases.
func (m *Machine) Conn(ctx context.Context) (*grpc.ClientConn, error) {
	if m.conn != nil {
		return m.conn, nil
	}
	dialOpts, err := m.dialOptions()
	if err != nil {
		return nil, err
	}
	if m.Cert == "" {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	dialOpts = append(dialOpts, grpc.WithContextDialer(dialTCP))
	conn, err := grpc.NewClient("passthrough:///"+strings.TrimPrefix(m.Addr, "tcp://"), dialOpts...)
	if err != nil {
		return nil, err
	}
	m.conn = conn
	return conn, nil
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
