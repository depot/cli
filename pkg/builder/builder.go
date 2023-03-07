package builder

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/depot/cli/internal/build"
	"github.com/depot/cli/pkg/api"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/docker/buildx/util/progress"
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
}

func (b *Builder) Acquire(l progress.Logger) (*AcquiredBuilder, error) {
	var err error
	var builder AcquiredBuilder

	builderPlatform := cliv1beta1.BuilderPlatform_BUILDER_PLATFORM_UNSPECIFIED
	switch b.Platform {
	case "amd64":
		builderPlatform = cliv1beta1.BuilderPlatform_BUILDER_PLATFORM_AMD64
	case "arm64":
		builderPlatform = cliv1beta1.BuilderPlatform_BUILDER_PLATFORM_ARM64
	default:
		return nil, errors.Errorf("unsupported platform: %s", b.Platform)
	}

	client := api.NewBuildClient()
	ctx := context.Background()

	acquireFn := func(sub progress.SubLogger) error {
		req := cliv1beta1.GetBuildKitConnectionRequest{
			BuildId:  b.BuildID,
			Platform: builderPlatform,
		}
		stream, err := client.GetBuildKitConnection(ctx, api.WithAuthentication(connect.NewRequest(&req), b.token))
		if err != nil {
			return err
		}
		defer stream.Close()

		for stream.Receive() {
			resp := stream.Msg()

			switch connection := resp.Connection.(type) {
			case *cliv1beta1.GetBuildKitConnectionResponse_Active:
				builder.Addr = connection.Active.Endpoint
				builder.ServerName = connection.Active.ServerName
				builder.CACert = connection.Active.CaCert.Cert
				builder.Cert = connection.Active.Cert.Cert
				builder.Key = connection.Active.Cert.Key
				return nil

			case *cliv1beta1.GetBuildKitConnectionResponse_Pending:
				// do nothing
			}
		}

		if err := stream.Err(); err != nil {
			return connect.NewError(connect.CodeUnknown, err)
		}

		return nil
	}

	// Try to acquire builder twice
	err = progress.Wrap("[depot] launching "+b.Platform+" builder", l, acquireFn)
	if err != nil {
		err = progress.Wrap("[depot] launching "+b.Platform+" builder", l, acquireFn)
		if err != nil {
			return nil, err
		}
	}

	return &builder, nil
}

func (b *Builder) ReportHealth(ctx context.Context) error {
	for {
		err := b.doReportHealth(ctx)
		if err == nil {
			return nil
		}
		fmt.Printf("error reporting health: %s", err.Error())
	}
}

func (b *Builder) doReportHealth(ctx context.Context) error {
	client := api.NewBuildClient()

	var builderPlatform cliv1beta1.BuilderPlatform
	switch b.Platform {
	case "amd64":
		builderPlatform = cliv1beta1.BuilderPlatform_BUILDER_PLATFORM_AMD64
	case "arm64":
		builderPlatform = cliv1beta1.BuilderPlatform_BUILDER_PLATFORM_ARM64
	default:
		return errors.Errorf("unsupported platform: %s", b.Platform)
	}

	stream := client.ReportBuildHealth(ctx)
	stream.RequestHeader().Add("User-Agent", fmt.Sprintf("depot-cli/%s/%s/%s", build.Version, runtime.GOOS, runtime.GOARCH))
	stream.RequestHeader().Add("Depot-User-Agent", fmt.Sprintf("depot-cli/%s/%s/%s", build.Version, runtime.GOOS, runtime.GOARCH))
	stream.RequestHeader().Add("Authorization", "Bearer "+b.token)
	defer func() {
		_, _ = stream.CloseAndReceive()
	}()

	for {
		err := stream.Send(&cliv1beta1.ReportBuildHealthRequest{BuildId: b.BuildID, Platform: builderPlatform})
		if err != nil {
			return err
		}
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return nil
		}
	}
}
