package builder

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/depot/cli/pkg/api"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
)

type Builder struct {
	token    string
	BuildID  string
	Platform string
}

func NewBuilder(token string, buildID string, platform string) *Builder {
	return &Builder{
		token:    token,
		BuildID:  buildID,
		Platform: platform,
	}
}

type AcquiredBuilder struct {
	Addr       string
	ServerName string
	CACert     string
	Cert       string
	Key        string

	client *client.Client
}

func (b *Builder) Acquire(ctx context.Context, reporter progress.Logger) (*AcquiredBuilder, error) {
	go func() {
		err := b.ReportHealth(ctx)
		if err != nil {
			log.Printf("warning: failed to report health for %s builder: %v\n", b.Platform, err)
		}
	}()

	var (
		err     error
		builder AcquiredBuilder
	)

	builderPlatform := cliv1.BuilderPlatform_BUILDER_PLATFORM_UNSPECIFIED
	switch b.Platform {
	case "amd64":
		builderPlatform = cliv1.BuilderPlatform_BUILDER_PLATFORM_AMD64
	case "arm64":
		builderPlatform = cliv1.BuilderPlatform_BUILDER_PLATFORM_ARM64
	default:
		return nil, errors.Errorf("unsupported platform: %s", b.Platform)
	}

	client := api.NewBuildClient()

	acquireFn := func(sub progress.SubLogger) error {
		for {
			req := cliv1.GetBuildKitConnectionRequest{
				BuildId:  b.BuildID,
				Platform: builderPlatform,
			}
			resp, err := client.GetBuildKitConnection(ctx, api.WithAuthentication(connect.NewRequest(&req), b.token))
			if err != nil {
				return err
			}

			switch connection := resp.Msg.Connection.(type) {
			case *cliv1.GetBuildKitConnectionResponse_Active:
				builder.Addr = connection.Active.Endpoint
				builder.ServerName = connection.Active.ServerName
				builder.CACert = connection.Active.CaCert.Cert
				builder.Cert = connection.Active.Cert.Cert
				builder.Key = connection.Active.Cert.Key
				return nil

			case *cliv1.GetBuildKitConnectionResponse_Pending:
				select {
				case <-time.After(time.Duration(connection.Pending.WaitMs) * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
		}
	}

	// Try to acquire builder twice
	err = progress.Wrap("[depot] launching "+b.Platform+" builder", reporter, acquireFn)
	if err != nil {
		err = progress.Wrap("[depot] launching "+b.Platform+" builder", reporter, acquireFn)
		if err != nil {
			return nil, err
		}
	}

	return &builder, nil
}

func (b *Builder) ReportHealth(ctx context.Context) error {
	var builderPlatform cliv1.BuilderPlatform
	switch b.Platform {
	case "amd64":
		builderPlatform = cliv1.BuilderPlatform_BUILDER_PLATFORM_AMD64
	case "arm64":
		builderPlatform = cliv1.BuilderPlatform_BUILDER_PLATFORM_ARM64
	default:
		return errors.Errorf("unsupported platform: %s", b.Platform)
	}

	client := api.NewBuildClient()
	for {
		err := b.doReportHealth(ctx, client, builderPlatform)
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

func (b *Builder) doReportHealth(ctx context.Context, client cliv1connect.BuildServiceClient, builderPlatform cliv1.BuilderPlatform) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req := cliv1.ReportBuildHealthRequest{BuildId: b.BuildID, Platform: builderPlatform}
	_, err := client.ReportBuildHealth(ctx, api.WithAuthentication(connect.NewRequest(&req), b.token))
	return err
}

func (b *AcquiredBuilder) Close() error {
	if b.client != nil {
		return b.client.Close()
	}
	return nil
}

func (b *AcquiredBuilder) Client(ctx context.Context) (*client.Client, error) {
	if b.client != nil {
		return b.client, nil
	}

	opts := []client.ClientOpt{
		client.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			addr = strings.TrimPrefix(addr, "tcp://")
			return net.Dial("tcp", addr)
		}),
	}

	// We create all these files as buildkit does not allow control of the gRPC client
	// without using overly restrictive private structs.
	if b.Cert != "" {
		file, err := os.CreateTemp("", "depot-cert")
		if err != nil {
			return nil, errors.Wrap(err, "failed to create temp file")
		}
		defer file.Close()
		err = os.WriteFile(file.Name(), []byte(b.Cert), 0600)
		if err != nil {
			return nil, errors.Wrap(err, "failed to write cert to temp file")
		}
		cert := file.Name()

		file, err = os.CreateTemp("", "depot-key")
		if err != nil {
			return nil, errors.Wrap(err, "failed to create temp file")
		}
		defer file.Close()
		err = os.WriteFile(file.Name(), []byte(b.Key), 0600)
		if err != nil {
			return nil, errors.Wrap(err, "failed to write key to temp file")
		}
		key := file.Name()

		file, err = os.CreateTemp("", "depot-ca-cert")
		if err != nil {
			return nil, errors.Wrap(err, "failed to create temp file")
		}
		defer file.Close()
		err = os.WriteFile(file.Name(), []byte(b.CACert), 0600)
		if err != nil {
			return nil, errors.Wrap(err, "failed to write CA cert to temp file")
		}
		caCert := file.Name()

		opts = append(opts, client.WithCredentials(b.ServerName, caCert, cert, key))
	}

	return client.New(ctx, b.Addr, opts...)
}

func (b *AcquiredBuilder) IsReady(ctx context.Context) bool {
	client, err := b.Client(ctx)
	if err != nil {
		return false
	}

	// TODO: Switch to gRPC Healthchecks after exposing the client in the client.
	_, err = client.ListWorkers(ctx)
	return err == nil
}
