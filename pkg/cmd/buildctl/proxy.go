package buildctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"time"

	content "github.com/containerd/containerd/api/services/content/v1"
	"github.com/containerd/containerd/api/services/leases/v1"
	"github.com/containerd/containerd/defaults"
	"github.com/depot/cli/pkg/progress"
	"github.com/gogo/protobuf/types"
	control "github.com/moby/buildkit/api/services/control"
	worker "github.com/moby/buildkit/api/types"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/depot"
	gateway "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/session/secrets"
	"github.com/moby/buildkit/session/sshforward"
	"github.com/moby/buildkit/session/upload"
	"github.com/moby/buildkit/solver/pb"
	"github.com/opencontainers/go-digest"
	trace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	health "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

var (
	_ gateway.LLBBridgeServer  = (*GatewayProxy)(nil)
	_ control.ControlServer    = (*ControlProxy)(nil)
	_ filesync.FileSyncServer  = (*FileSyncProxy)(nil)
	_ filesync.FileSendServer  = (*FileSendProxy)(nil)
	_ auth.AuthServer          = (*AuthProxy)(nil)
	_ upload.UploadServer      = (*UploadProxy)(nil)
	_ sshforward.SSHServer     = (*SshProxy)(nil)
	_ secrets.SecretsServer    = (*SecretsProxy)(nil)
	_ trace.TraceServiceServer = (*TracesProxy)(nil)
	_ content.ContentServer    = (*ContentProxy)(nil)
	_ leases.LeasesServer      = (*LeasesProxy)(nil)
	_ health.HealthServer      = (*HealthProxy)(nil)
)

func BuildkitdClient(ctx context.Context, conn net.Conn, buildkitdAddress string) (*grpc.ClientConn, error) {
	dialContext := func(context.Context, string) (net.Conn, error) {
		return conn, nil
	}

	uri, err := url.Parse(buildkitdAddress)
	if err != nil {
		return nil, err
	}

	opts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(defaults.DefaultMaxRecvMsgSize)),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(defaults.DefaultMaxSendMsgSize)),
		grpc.WithContextDialer(dialContext),
		grpc.WithAuthority(uri.Host),
		// conn is already a TLS connection.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	return grpc.DialContext(ctx, buildkitdAddress, opts...)
}

// Proxy buildkitd server over connection. Cancel context to shutdown.
func Proxy(ctx context.Context, conn net.Conn, acquireBuilder func() (*grpc.ClientConn, error), platform string, report *progress.Progress) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	opts := []grpc.ServerOption{
		grpc.KeepaliveEnforcementPolicy(depot.LoadKeepaliveEnforcementPolicy()),
		grpc.KeepaliveParams(depot.LoadKeepaliveServerParams()),
		//grpc.StreamInterceptor(ReportStream(report)),
		//grpc.UnaryInterceptor(ReportUnary(report)),
	}
	server := grpc.NewServer(opts...)

	control.RegisterControlServer(server, &ControlProxy{conn: acquireBuilder, platform: platform, report: report, cancel: cancel})
	gateway.RegisterLLBBridgeServer(server, &GatewayProxy{conn: acquireBuilder})
	/*
		filesync.RegisterFileSyncServer(server, &FileSyncProxy{conn: buildkitdClient})
		filesync.RegisterFileSendServer(server, &FileSendProxy{conn: buildkitdClient})
		auth.RegisterAuthServer(server, &AuthProxy{conn: buildkitdClient})
		upload.RegisterUploadServer(server, &UploadProxy{conn: buildkitdClient})
		sshforward.RegisterSSHServer(server, &SshProxy{conn: buildkitdClient})
		secrets.RegisterSecretsServer(server, &SecretsProxy{conn: buildkitdClient})
		trace.RegisterTraceServiceServer(server, &TracesProxy{conn: buildkitdClient})
		content.RegisterContentServer(server, &ContentProxy{conn: buildkitdClient})
		leases.RegisterLeasesServer(server, &LeasesProxy{conn: buildkitdClient})
		health.RegisterHealthServer(server, &HealthProxy{conn: buildkitdClient})
	*/

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	(&http2.Server{}).ServeConn(conn, &http2.ServeConnOpts{Handler: server})
}

type GatewayProxy struct {
	conn func() (*grpc.ClientConn, error)
}

func (p *GatewayProxy) ResolveImageConfig(ctx context.Context, in *gateway.ResolveImageConfigRequest) (*gateway.ResolveImageConfigResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.ResolveImageConfig(ctx, in)
}

func (p *GatewayProxy) Solve(ctx context.Context, in *gateway.SolveRequest) (*gateway.SolveResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.Solve(ctx, in)
}

func (p *GatewayProxy) ReadFile(ctx context.Context, in *gateway.ReadFileRequest) (*gateway.ReadFileResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.ReadFile(ctx, in)
}

func (p *GatewayProxy) ReadDir(ctx context.Context, in *gateway.ReadDirRequest) (*gateway.ReadDirResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.ReadDir(ctx, in)
}

func (p *GatewayProxy) StatFile(ctx context.Context, in *gateway.StatFileRequest) (*gateway.StatFileResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.StatFile(ctx, in)
}

func (p *GatewayProxy) Evaluate(ctx context.Context, in *gateway.EvaluateRequest) (*gateway.EvaluateResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.Evaluate(ctx, in)
}

func (p *GatewayProxy) Ping(ctx context.Context, in *gateway.PingRequest) (*gateway.PongResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.Ping(ctx, in)
}

func (p *GatewayProxy) Return(ctx context.Context, in *gateway.ReturnRequest) (*gateway.ReturnResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.Return(ctx, in)
}

func (p *GatewayProxy) Inputs(ctx context.Context, in *gateway.InputsRequest) (*gateway.InputsResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.Inputs(ctx, in)
}

func (p *GatewayProxy) NewContainer(ctx context.Context, in *gateway.NewContainerRequest) (*gateway.NewContainerResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.NewContainer(ctx, in)
}

func (p *GatewayProxy) ReleaseContainer(ctx context.Context, in *gateway.ReleaseContainerRequest) (*gateway.ReleaseContainerResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.ReleaseContainer(ctx, in)
}

func (p *GatewayProxy) ExecProcess(buildx gateway.LLBBridge_ExecProcessServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	conn, err := p.conn()
	if err != nil {
		return err
	}

	buildkit, err := gateway.NewLLBBridgeClient(conn).ExecProcess(buildkitCtx)
	if err != nil {
		return err
	}

	buildxToBuildkit := forwardBuildxToBuildkit(buildx, buildkit)
	buildkitToBuildx := forwardBuildkitToBuildx(buildkit, buildx)
	for i := 0; i < 2; i++ {
		select {
		case err := <-buildxToBuildkit:
			if errors.Is(err, io.EOF) {
				_ = buildkit.CloseSend()
			} else {
				buildkitCancel()
				return status.Errorf(codes.Internal, "%v", err)
			}
		case err := <-buildkitToBuildx:
			buildx.SetTrailer(buildkit.Trailer())
			if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}
	}

	return status.Errorf(codes.Internal, "unreachable")
}

func (p *GatewayProxy) Warn(ctx context.Context, in *gateway.WarnRequest) (*gateway.WarnResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := gateway.NewLLBBridgeClient(conn)
	return client.Warn(ctx, in)
}

type ControlProxy struct {
	conn     func() (*grpc.ClientConn, error)
	report   *progress.Progress
	platform string
	cancel   context.CancelFunc
}

func (p *ControlProxy) scheduleShutdown() {
	go func() { p.cancel() }()
}

func (p *ControlProxy) DiskUsage(ctx context.Context, in *control.DiskUsageRequest) (*control.DiskUsageResponse, error) {
	p.scheduleShutdown()
	return &control.DiskUsageResponse{}, nil
	/*
		md, ok := metadata.FromIncomingContext(ctx)
		if ok {
			ctx = metadata.NewOutgoingContext(ctx, md)
		}
		client := control.NewControlClient(p.conn)
		return client.DiskUsage(ctx, in)
	*/
}

func (p *ControlProxy) Prune(in *control.PruneRequest, toBuildx control.Control_PruneServer) error {
	ctx := toBuildx.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := p.conn()
	if err != nil {
		return err
	}

	fromBuildkit, err := control.NewControlClient(conn).Prune(ctx, in)
	if err != nil {
		return err
	}

	for {
		msg, err := fromBuildkit.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		err = toBuildx.Send(msg)
		if err != nil {
			return err
		}
	}

	return nil
}

func (p *ControlProxy) Solve(ctx context.Context, in *control.SolveRequest) (*control.SolveResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	conn, err := p.conn()
	if err != nil {
		return nil, err
	}

	client := control.NewControlClient(conn)
	return client.Solve(ctx, in)
}

func (p *ControlProxy) Status(in *control.StatusRequest, toBuildx control.Control_StatusServer) error {
	ctx := toBuildx.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := p.conn()
	if err != nil {
		return err
	}

	fromBuildkit, err := control.NewControlClient(conn).Status(ctx, in)
	if err != nil {
		return err
	}

	for {
		msg, err := fromBuildkit.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		p.report.Write(client.NewSolveStatus(msg))

		err = toBuildx.Send(msg)
		if err != nil {
			return err
		}
	}

	return nil
}

func (p *ControlProxy) Session(buildx control.Control_SessionServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	conn, err := p.conn()
	if err != nil {
		return err
	}

	buildkit, err := control.NewControlClient(conn).Session(buildkitCtx)
	if err != nil {
		return err
	}

	buildxToBuildkit := forwardBuildxToBuildkit(buildx, buildkit)
	buildkitToBuildx := forwardBuildkitToBuildx(buildkit, buildx)
	for i := 0; i < 2; i++ {
		select {
		case err := <-buildxToBuildkit:
			if errors.Is(err, io.EOF) {
				_ = buildkit.CloseSend()
			} else {
				buildkitCancel()
				return status.Errorf(codes.Internal, "%v", err)
			}
		case err := <-buildkitToBuildx:
			buildx.SetTrailer(buildkit.Trailer())
			if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}
	}

	return status.Errorf(codes.Internal, "unreachable")
}

func (p *ControlProxy) ListWorkers(ctx context.Context, in *control.ListWorkersRequest) (*control.ListWorkersResponse, error) {
	if p.platform == "amd64" {
		return &control.ListWorkersResponse{
			Record: []*worker.WorkerRecord{
				{
					Platforms: []pb.Platform{
						{
							Architecture: "amd64",
							OS:           "linux",
						},
						{
							Architecture: "amd64",
							OS:           "linux",
							Variant:      "v2",
						},
						{
							Architecture: "amd64",
							OS:           "linux",
							Variant:      "v3",
						},
						{
							Architecture: "amd64",
							OS:           "linux",
							Variant:      "v4",
						},
						{
							Architecture: "386",
							OS:           "linux",
						},
					},
				},
			},
		}, nil
	} else if p.platform == "arm64" {
		return &control.ListWorkersResponse{
			Record: []*worker.WorkerRecord{
				{
					Platforms: []pb.Platform{
						{
							Architecture: "arm64",
							OS:           "linux",
						},
						{
							Architecture: "arm",
							OS:           "linux",
							Variant:      "v7",
						},
						{
							Architecture: "arm",
							OS:           "linux",
							Variant:      "v6",
						},
					},
				},
			},
		}, nil

	} else {
		return &control.ListWorkersResponse{
			Record: []*worker.WorkerRecord{},
		}, nil
	}
	/*
		md, ok := metadata.FromIncomingContext(ctx)
		if ok {
			ctx = metadata.NewOutgoingContext(ctx, md)
		}
		client := control.NewControlClient(p.conn)
		return client.ListWorkers(ctx, in)
	*/
}

func (p *ControlProxy) Info(ctx context.Context, in *control.InfoRequest) (*control.InfoResponse, error) {
	p.scheduleShutdown()
	return nil, status.Errorf(codes.Unimplemented, "method Info not implemented")
	/*
		md, ok := metadata.FromIncomingContext(ctx)
		if ok {
			ctx = metadata.NewOutgoingContext(ctx, md)
		}
		client := control.NewControlClient(p.conn)
		return client.Info(ctx, in)
	*/
}

func (p *ControlProxy) ListenBuildHistory(in *control.BuildHistoryRequest, toBuildx control.Control_ListenBuildHistoryServer) error {
	p.scheduleShutdown()
	return status.Errorf(codes.Unimplemented, "method ListenBuildHistory not implemented")
	/*
		ctx := toBuildx.Context()
		md, ok := metadata.FromIncomingContext(ctx)
		if ok {
			ctx = metadata.NewOutgoingContext(ctx, md)
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		fromBuildkit, err := control.NewControlClient(p.conn).ListenBuildHistory(ctx, in)
		if err != nil {
			return err
		}

		for {
			msg, err := fromBuildkit.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return err
			}

			err = toBuildx.Send(msg)
			if err != nil {
				return err
			}
		}

		return nil
	*/
}

func (p *ControlProxy) UpdateBuildHistory(ctx context.Context, in *control.UpdateBuildHistoryRequest) (*control.UpdateBuildHistoryResponse, error) {
	p.scheduleShutdown()
	return &control.UpdateBuildHistoryResponse{}, nil
	/*
		md, ok := metadata.FromIncomingContext(ctx)
		if ok {
			ctx = metadata.NewOutgoingContext(ctx, md)
		}
		client := control.NewControlClient(p.conn)
		return client.UpdateBuildHistory(ctx, in)
	*/
}

type FileSyncProxy struct{ conn *grpc.ClientConn }

func (p *FileSyncProxy) DiffCopy(buildx filesync.FileSync_DiffCopyServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	buildkit, err := filesync.NewFileSyncClient(p.conn).DiffCopy(buildkitCtx)
	if err != nil {
		return err
	}

	buildxToBuildkit := forwardBuildxToBuildkit(buildx, buildkit)
	buildkitToBuildx := forwardBuildkitToBuildx(buildkit, buildx)
	for i := 0; i < 2; i++ {
		select {
		case err := <-buildxToBuildkit:
			if errors.Is(err, io.EOF) {
				_ = buildkit.CloseSend()
			} else {
				buildkitCancel()
				return status.Errorf(codes.Internal, "%v", err)
			}
		case err := <-buildkitToBuildx:
			buildx.SetTrailer(buildkit.Trailer())
			if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}
	}

	return status.Errorf(codes.Internal, "unreachable")
}

func (p *FileSyncProxy) TarStream(buildx filesync.FileSync_TarStreamServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	buildkit, err := filesync.NewFileSyncClient(p.conn).TarStream(buildkitCtx)
	if err != nil {
		return err
	}

	buildxToBuildkit := forwardBuildxToBuildkit(buildx, buildkit)
	buildkitToBuildx := forwardBuildkitToBuildx(buildkit, buildx)
	for i := 0; i < 2; i++ {
		select {
		case err := <-buildxToBuildkit:
			if errors.Is(err, io.EOF) {
				_ = buildkit.CloseSend()
			} else {
				buildkitCancel()
				return status.Errorf(codes.Internal, "%v", err)
			}
		case err := <-buildkitToBuildx:
			buildx.SetTrailer(buildkit.Trailer())
			if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}
	}

	return status.Errorf(codes.Internal, "unreachable")
}

type FileSendProxy struct{ conn *grpc.ClientConn }

func (p *FileSendProxy) DiffCopy(buildx filesync.FileSend_DiffCopyServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	buildkit, err := filesync.NewFileSendClient(p.conn).DiffCopy(buildkitCtx)
	if err != nil {
		return err
	}

	buildxToBuildkit := forwardBuildxToBuildkit(buildx, buildkit)
	buildkitToBuildx := forwardBuildkitToBuildx(buildkit, buildx)
	for i := 0; i < 2; i++ {
		select {
		case err := <-buildxToBuildkit:
			if errors.Is(err, io.EOF) {
				_ = buildkit.CloseSend()
			} else {
				buildkitCancel()
				return status.Errorf(codes.Internal, "%v", err)
			}
		case err := <-buildkitToBuildx:
			buildx.SetTrailer(buildkit.Trailer())
			if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}
	}

	return status.Errorf(codes.Internal, "unreachable")
}

type AuthProxy struct{ conn *grpc.ClientConn }

func (p *AuthProxy) Credentials(ctx context.Context, in *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := auth.NewAuthClient(p.conn)
	return client.Credentials(ctx, in)
}

func (p *AuthProxy) FetchToken(ctx context.Context, in *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := auth.NewAuthClient(p.conn)
	return client.FetchToken(ctx, in)
}

func (p *AuthProxy) GetTokenAuthority(ctx context.Context, in *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := auth.NewAuthClient(p.conn)
	return client.GetTokenAuthority(ctx, in)
}

func (p *AuthProxy) VerifyTokenAuthority(ctx context.Context, in *auth.VerifyTokenAuthorityRequest) (*auth.VerifyTokenAuthorityResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := auth.NewAuthClient(p.conn)
	return client.VerifyTokenAuthority(ctx, in)
}

type UploadProxy struct{ conn *grpc.ClientConn }

func (p *UploadProxy) Pull(buildx upload.Upload_PullServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	buildkit, err := upload.NewUploadClient(p.conn).Pull(buildkitCtx)
	if err != nil {
		return err
	}

	buildxToBuildkit := forwardBuildxToBuildkit(buildx, buildkit)
	buildkitToBuildx := forwardBuildkitToBuildx(buildkit, buildx)
	for i := 0; i < 2; i++ {
		select {
		case err := <-buildxToBuildkit:
			if errors.Is(err, io.EOF) {
				_ = buildkit.CloseSend()
			} else {
				buildkitCancel()
				return status.Errorf(codes.Internal, "%v", err)
			}
		case err := <-buildkitToBuildx:
			buildx.SetTrailer(buildkit.Trailer())
			if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}
	}

	return status.Errorf(codes.Internal, "unreachable")
}

type SshProxy struct{ conn *grpc.ClientConn }

func (p *SshProxy) CheckAgent(ctx context.Context, in *sshforward.CheckAgentRequest) (*sshforward.CheckAgentResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := sshforward.NewSSHClient(p.conn)
	return client.CheckAgent(ctx, in)
}

func (p *SshProxy) ForwardAgent(buildx sshforward.SSH_ForwardAgentServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	buildkit, err := sshforward.NewSSHClient(p.conn).ForwardAgent(buildkitCtx)
	if err != nil {
		return err
	}

	buildxToBuildkit := forwardBuildxToBuildkit(buildx, buildkit)
	buildkitToBuildx := forwardBuildkitToBuildx(buildkit, buildx)
	for i := 0; i < 2; i++ {
		select {
		case err := <-buildxToBuildkit:
			if errors.Is(err, io.EOF) {
				_ = buildkit.CloseSend()
			} else {
				buildkitCancel()
				return status.Errorf(codes.Internal, "%v", err)
			}
		case err := <-buildkitToBuildx:
			buildx.SetTrailer(buildkit.Trailer())
			if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}
	}

	return status.Errorf(codes.Internal, "unreachable")
}

type SecretsProxy struct{ conn *grpc.ClientConn }

func (p *SecretsProxy) GetSecret(ctx context.Context, in *secrets.GetSecretRequest) (*secrets.GetSecretResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := secrets.NewSecretsClient(p.conn)
	return client.GetSecret(ctx, in)
}

type TracesProxy struct {
	conn *grpc.ClientConn
	trace.UnimplementedTraceServiceServer
}

func (p *TracesProxy) Export(ctx context.Context, in *trace.ExportTraceServiceRequest) (*trace.ExportTraceServiceResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := trace.NewTraceServiceClient(p.conn)
	return client.Export(ctx, in)
}

type ContentProxy struct{ conn *grpc.ClientConn }

func (p *ContentProxy) Info(ctx context.Context, in *content.InfoRequest) (*content.InfoResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := content.NewContentClient(p.conn)
	return client.Info(ctx, in)
}

func (p *ContentProxy) Update(ctx context.Context, in *content.UpdateRequest) (*content.UpdateResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := content.NewContentClient(p.conn)
	return client.Update(ctx, in)
}

func (p *ContentProxy) List(in *content.ListContentRequest, session content.Content_ListServer) error {
	return nil
}

func (p *ContentProxy) Delete(ctx context.Context, in *content.DeleteContentRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := content.NewContentClient(p.conn)
	return client.Delete(ctx, in)
}

func (p *ContentProxy) Read(in *content.ReadContentRequest, toBuildx content.Content_ReadServer) error {
	ctx := toBuildx.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fromBuildkit, err := content.NewContentClient(p.conn).Read(ctx, in)
	if err != nil {
		return err
	}

	for {
		msg, err := fromBuildkit.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		err = toBuildx.Send(msg)
		if err != nil {
			return err
		}
	}

	return nil
}

func (p *ContentProxy) Status(ctx context.Context, in *content.StatusRequest) (*content.StatusResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := content.NewContentClient(p.conn)
	return client.Status(ctx, in)
}

func (p *ContentProxy) ListStatuses(ctx context.Context, in *content.ListStatusesRequest) (*content.ListStatusesResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := content.NewContentClient(p.conn)
	return client.ListStatuses(ctx, in)
}

func (p *ContentProxy) Write(buildx content.Content_WriteServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	buildkit, err := content.NewContentClient(p.conn).Write(buildkitCtx)
	if err != nil {
		return err
	}

	buildxToBuildkit := forwardBuildxToBuildkit(buildx, buildkit)
	buildkitToBuildx := forwardBuildkitToBuildx(buildkit, buildx)
	for i := 0; i < 2; i++ {
		select {
		case err := <-buildxToBuildkit:
			if errors.Is(err, io.EOF) {
				_ = buildkit.CloseSend()
			} else {
				buildkitCancel()
				return status.Errorf(codes.Internal, "%v", err)
			}
		case err := <-buildkitToBuildx:
			buildx.SetTrailer(buildkit.Trailer())
			if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}
	}

	return status.Errorf(codes.Internal, "unreachable")
}

func (p *ContentProxy) Abort(ctx context.Context, in *content.AbortRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := content.NewContentClient(p.conn)
	return client.Abort(ctx, in)
}

type LeasesProxy struct{ conn *grpc.ClientConn }

func (p *LeasesProxy) Delete(ctx context.Context, in *leases.DeleteRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := leases.NewLeasesClient(p.conn)
	return client.Delete(ctx, in)
}

func (p *LeasesProxy) Create(ctx context.Context, in *leases.CreateRequest) (*leases.CreateResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := leases.NewLeasesClient(p.conn)
	return client.Create(ctx, in)
}

func (p *LeasesProxy) List(ctx context.Context, in *leases.ListRequest) (*leases.ListResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := leases.NewLeasesClient(p.conn)
	return client.List(ctx, in)
}

func (p *LeasesProxy) AddResource(ctx context.Context, in *leases.AddResourceRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := leases.NewLeasesClient(p.conn)
	return client.AddResource(ctx, in)
}

func (p *LeasesProxy) DeleteResource(ctx context.Context, in *leases.DeleteResourceRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := leases.NewLeasesClient(p.conn)
	return client.DeleteResource(ctx, in)
}

func (p *LeasesProxy) ListResources(ctx context.Context, in *leases.ListResourcesRequest) (*leases.ListResourcesResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := leases.NewLeasesClient(p.conn)
	return client.ListResources(ctx, in)
}

type HealthProxy struct{ conn *grpc.ClientConn }

func (p *HealthProxy) Check(ctx context.Context, in *health.HealthCheckRequest) (*health.HealthCheckResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	client := health.NewHealthClient(p.conn)
	return client.Check(ctx, in)
}

func (p *HealthProxy) Watch(in *health.HealthCheckRequest, toBuildx health.Health_WatchServer) error {
	ctx := toBuildx.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fromBuildkit, err := health.NewHealthClient(p.conn).Watch(ctx, in)
	if err != nil {
		return err
	}

	for {
		msg, err := fromBuildkit.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		err = toBuildx.Send(msg)
		if err != nil {
			return err
		}
	}

	return nil
}

func forwardBuildkitToBuildx(buildkit grpc.ClientStream, buildx grpc.ServerStream) chan error {
	ret := make(chan error, 1)
	setHeader := false
	go func() {
		f := &emptypb.Empty{}
		for {
			if err := buildkit.RecvMsg(f); err != nil {
				ret <- err
				break
			}

			if !setHeader {
				setHeader = true

				md, err := buildkit.Header()
				if err != nil {
					ret <- err
					break
				}
				if err := buildx.SendHeader(md); err != nil {
					ret <- err
					break
				}
			}

			if err := buildx.SendMsg(f); err != nil {
				ret <- err
				break
			}
		}
	}()

	return ret
}

func forwardBuildxToBuildkit(buildx grpc.ServerStream, buildkit grpc.ClientStream) chan error {
	ret := make(chan error, 1)
	go func() {
		f := &emptypb.Empty{}
		for {
			if err := buildx.RecvMsg(f); err != nil {
				ret <- err
				break
			}
			if err := buildkit.SendMsg(f); err != nil {
				ret <- err
				break
			}
		}
	}()
	return ret
}

func ReportUnary(reporter *progress.Progress) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		report(fmt.Sprintf("[internal] %s", info.FullMethod), reporter)
		resp, err := handler(ctx, req)
		report(fmt.Sprintf("[finished] %s", info.FullMethod), reporter)
		return resp, err
	}
}

func ReportStream(reporter *progress.Progress) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		report(fmt.Sprintf("[internal] %s", info.FullMethod), reporter)
		err := handler(srv, ss)
		report(fmt.Sprintf("[finished] %s", info.FullMethod), reporter)
		return err
	}
}

func report(log string, reporter *progress.Progress) {
	tm := time.Now()
	status := &client.SolveStatus{
		Vertexes: []*client.Vertex{
			{
				Digest:    digest.Canonical.FromString(identity.NewID()),
				Name:      log,
				Started:   &tm,
				Completed: &tm,
				Cached:    true,
			},
		},
	}

	if reporter != nil {
		reporter.Write(status)
	}
}
