package connection

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"

	content "github.com/containerd/containerd/api/services/content/v1"
	"github.com/containerd/containerd/api/services/leases/v1"
	"github.com/containerd/containerd/defaults"
	"github.com/gogo/protobuf/types"
	control "github.com/moby/buildkit/api/services/control"
	worker "github.com/moby/buildkit/api/types"
	"github.com/moby/buildkit/depot"
	gateway "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/pb"
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

func BuildkitProxy(ctx context.Context, localConn net.Conn, buildkitClient *grpc.ClientConn, platform string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	opts := []grpc.ServerOption{
		grpc.KeepaliveEnforcementPolicy(depot.LoadKeepaliveEnforcementPolicy()),
		grpc.KeepaliveParams(depot.LoadKeepaliveServerParams()),
	}
	server := grpc.NewServer(opts...)

	control.RegisterControlServer(server, &ControlProxy{BuildkitClient: buildkitClient, platform: platform})
	gateway.RegisterLLBBridgeServer(server, &GatewayProxy{BuildkitClient: buildkitClient, platform: platform})
	trace.RegisterTraceServiceServer(server, &TracesProxy{BuildkitClient: buildkitClient})
	content.RegisterContentServer(server, &ContentProxy{BuildkitClient: buildkitClient})
	leases.RegisterLeasesServer(server, &LeasesProxy{BuildkitClient: buildkitClient})
	health.RegisterHealthServer(server, &HealthProxy{BuildkitClient: buildkitClient})

	go func() {
		<-ctx.Done()
		localConn.Close()
	}()

	(&http2.Server{}).ServeConn(localConn, &http2.ServeConnOpts{Handler: server})
}

type ControlProxy struct {
	BuildkitClient *grpc.ClientConn // Conn is the connection to the buildkitd server.

	platform string
}

func (p *ControlProxy) Prune(in *control.PruneRequest, toBuildx control.Control_PruneServer) error {
	ctx := toBuildx.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fromBuildkit, err := control.NewControlClient(p.BuildkitClient).Prune(ctx, in)
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

	in.Exporter = ""

	client := control.NewControlClient(p.BuildkitClient)
	// DEPOT: stop recording the build steps and traces on the server.
	in.Internal = true
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

	fromBuildkit, err := control.NewControlClient(p.BuildkitClient).Status(ctx, in)
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

func (p *ControlProxy) Session(buildx control.Control_SessionServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	buildkit, err := control.NewControlClient(p.BuildkitClient).Session(buildkitCtx)
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
	return &control.ListWorkersResponse{
		Record: platformWorkerRecords(p.platform),
	}, nil
}

func platformWorkerRecords(platform string) []*worker.WorkerRecord {
	if platform == "amd64" {
		return []*worker.WorkerRecord{
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
		}
	} else if platform == "arm64" {
		return []*worker.WorkerRecord{
			{
				Platforms: []pb.Platform{
					{
						Architecture: "arm64",
						OS:           "linux",
					},
					{
						Architecture: "arm",
						OS:           "linux",
						Variant:      "v8",
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
		}
	} else {
		return []*worker.WorkerRecord{}
	}
}

func (p *ControlProxy) DiskUsage(ctx context.Context, in *control.DiskUsageRequest) (*control.DiskUsageResponse, error) {
	return &control.DiskUsageResponse{}, nil
}

func (p *ControlProxy) Info(ctx context.Context, in *control.InfoRequest) (*control.InfoResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Info not implemented")
}

func (p *ControlProxy) ListenBuildHistory(in *control.BuildHistoryRequest, toBuildx control.Control_ListenBuildHistoryServer) error {
	return status.Errorf(codes.Unimplemented, "method ListenBuildHistory not implemented")
}

func (p *ControlProxy) UpdateBuildHistory(ctx context.Context, in *control.UpdateBuildHistoryRequest) (*control.UpdateBuildHistoryResponse, error) {
	return &control.UpdateBuildHistoryResponse{}, nil
}

type GatewayProxy struct {
	BuildkitClient *grpc.ClientConn // Conn is the connection to the buildkitd server.
	platform       string
}

func (p *GatewayProxy) ResolveImageConfig(ctx context.Context, in *gateway.ResolveImageConfigRequest) (*gateway.ResolveImageConfigResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.ResolveImageConfig(ctx, in)
}

func (p *GatewayProxy) Solve(ctx context.Context, in *gateway.SolveRequest) (*gateway.SolveResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.Solve(ctx, in)
}

func (p *GatewayProxy) ReadFile(ctx context.Context, in *gateway.ReadFileRequest) (*gateway.ReadFileResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.ReadFile(ctx, in)
}

func (p *GatewayProxy) ReadDir(ctx context.Context, in *gateway.ReadDirRequest) (*gateway.ReadDirResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.ReadDir(ctx, in)
}

func (p *GatewayProxy) StatFile(ctx context.Context, in *gateway.StatFileRequest) (*gateway.StatFileResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.StatFile(ctx, in)
}

func (p *GatewayProxy) Evaluate(ctx context.Context, in *gateway.EvaluateRequest) (*gateway.EvaluateResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.Evaluate(ctx, in)
}

// Turns out that this only matters for `gha` and `s3`.
func (p *GatewayProxy) Ping(ctx context.Context, in *gateway.PingRequest) (*gateway.PongResponse, error) {
	return &gateway.PongResponse{
		FrontendAPICaps: gateway.Caps.All(),
		LLBCaps:         pb.Caps.All(),
		Workers:         platformWorkerRecords(p.platform),
	}, nil
}

func (p *GatewayProxy) Return(ctx context.Context, in *gateway.ReturnRequest) (*gateway.ReturnResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.Return(ctx, in)
}

func (p *GatewayProxy) Inputs(ctx context.Context, in *gateway.InputsRequest) (*gateway.InputsResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.Inputs(ctx, in)
}

func (p *GatewayProxy) NewContainer(ctx context.Context, in *gateway.NewContainerRequest) (*gateway.NewContainerResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.NewContainer(ctx, in)
}

func (p *GatewayProxy) ReleaseContainer(ctx context.Context, in *gateway.ReleaseContainerRequest) (*gateway.ReleaseContainerResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.ReleaseContainer(ctx, in)
}

func (p *GatewayProxy) ExecProcess(buildx gateway.LLBBridge_ExecProcessServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	buildkit, err := gateway.NewLLBBridgeClient(p.BuildkitClient).ExecProcess(buildkitCtx)
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
	client := gateway.NewLLBBridgeClient(p.BuildkitClient)
	return client.Warn(ctx, in)
}

type TracesProxy struct {
	BuildkitClient *grpc.ClientConn // Conn is the connection to the buildkitd server.
	trace.UnimplementedTraceServiceServer
}

func (p *TracesProxy) Export(ctx context.Context, in *trace.ExportTraceServiceRequest) (*trace.ExportTraceServiceResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := trace.NewTraceServiceClient(p.BuildkitClient)
	return client.Export(ctx, in)
}

type ContentProxy struct {
	BuildkitClient *grpc.ClientConn // Conn is the connection to the buildkitd server.
}

func (p *ContentProxy) Info(ctx context.Context, in *content.InfoRequest) (*content.InfoResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := content.NewContentClient(p.BuildkitClient)
	return client.Info(ctx, in)
}

func (p *ContentProxy) Update(ctx context.Context, in *content.UpdateRequest) (*content.UpdateResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := content.NewContentClient(p.BuildkitClient)
	return client.Update(ctx, in)
}

func (p *ContentProxy) List(in *content.ListContentRequest, toBuildx content.Content_ListServer) error {
	ctx := toBuildx.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fromBuildkit, err := content.NewContentClient(p.BuildkitClient).List(ctx, in)
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

func (p *ContentProxy) Delete(ctx context.Context, in *content.DeleteContentRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := content.NewContentClient(p.BuildkitClient)
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

	fromBuildkit, err := content.NewContentClient(p.BuildkitClient).Read(ctx, in)
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

	client := content.NewContentClient(p.BuildkitClient)
	return client.Status(ctx, in)
}

func (p *ContentProxy) ListStatuses(ctx context.Context, in *content.ListStatusesRequest) (*content.ListStatusesResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := content.NewContentClient(p.BuildkitClient)
	return client.ListStatuses(ctx, in)
}

func (p *ContentProxy) Write(buildx content.Content_WriteServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	buildkit, err := content.NewContentClient(p.BuildkitClient).Write(buildkitCtx)
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

	client := content.NewContentClient(p.BuildkitClient)
	return client.Abort(ctx, in)
}

type LeasesProxy struct {
	BuildkitClient *grpc.ClientConn // Conn is the connection to the buildkitd server.
}

func (p *LeasesProxy) Delete(ctx context.Context, in *leases.DeleteRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := leases.NewLeasesClient(p.BuildkitClient)
	return client.Delete(ctx, in)
}

func (p *LeasesProxy) Create(ctx context.Context, in *leases.CreateRequest) (*leases.CreateResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := leases.NewLeasesClient(p.BuildkitClient)
	return client.Create(ctx, in)
}

func (p *LeasesProxy) List(ctx context.Context, in *leases.ListRequest) (*leases.ListResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := leases.NewLeasesClient(p.BuildkitClient)
	return client.List(ctx, in)
}

func (p *LeasesProxy) AddResource(ctx context.Context, in *leases.AddResourceRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := leases.NewLeasesClient(p.BuildkitClient)
	return client.AddResource(ctx, in)
}

func (p *LeasesProxy) DeleteResource(ctx context.Context, in *leases.DeleteResourceRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := leases.NewLeasesClient(p.BuildkitClient)
	return client.DeleteResource(ctx, in)
}

func (p *LeasesProxy) ListResources(ctx context.Context, in *leases.ListResourcesRequest) (*leases.ListResourcesResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := leases.NewLeasesClient(p.BuildkitClient)
	return client.ListResources(ctx, in)
}

type HealthProxy struct {
	BuildkitClient *grpc.ClientConn // Conn is the connection to the buildkitd server.
}

func (p *HealthProxy) Check(ctx context.Context, in *health.HealthCheckRequest) (*health.HealthCheckResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	client := health.NewHealthClient(p.BuildkitClient)
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

	fromBuildkit, err := health.NewHealthClient(p.BuildkitClient).Watch(ctx, in)
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
