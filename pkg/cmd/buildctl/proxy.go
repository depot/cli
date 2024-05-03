package buildctl

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	content "github.com/containerd/containerd/api/services/content/v1"
	"github.com/containerd/containerd/api/services/leases/v1"
	"github.com/containerd/containerd/defaults"
	"github.com/depot/cli/pkg/progresshelper"
	"github.com/docker/buildx/util/progress"
	"github.com/gogo/protobuf/types"
	control "github.com/moby/buildkit/api/services/control"
	worker "github.com/moby/buildkit/api/types"
	"github.com/moby/buildkit/client"
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

var (
	_ gateway.LLBBridgeServer  = (*GatewayProxy)(nil)
	_ control.ControlServer    = (*ControlProxy)(nil)
	_ trace.TraceServiceServer = (*TracesProxy)(nil)
	_ content.ContentServer    = (*ContentProxy)(nil)
	_ leases.LeasesServer      = (*LeasesProxy)(nil)
	_ health.HealthServer      = (*HealthProxy)(nil)
)

// builds counts the number of build requests. We do this because the second
// build request is the "real" one.  ListWorkers is the only function we have
// that does not have a timeout.  We wait until the second ListWorkers call
// to start the buildkitd instance as that eliminates all the _OTHER_ calls
// buildx uses to get metadata like disk usage and build history.
var builds atomic.Int64

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
func Proxy(ctx context.Context, conn net.Conn, acquireState func() *ProxyState, platform string, status chan *client.SolveStatus) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	opts := []grpc.ServerOption{
		grpc.KeepaliveEnforcementPolicy(depot.LoadKeepaliveEnforcementPolicy()),
		grpc.KeepaliveParams(depot.LoadKeepaliveServerParams()),
	}
	server := grpc.NewServer(opts...)

	control.RegisterControlServer(server, &ControlProxy{state: acquireState, platform: platform, cancel: cancel, status: status})
	gateway.RegisterLLBBridgeServer(server, &GatewayProxy{state: acquireState, platform: platform})
	trace.RegisterTraceServiceServer(server, &TracesProxy{state: acquireState})
	content.RegisterContentServer(server, &ContentProxy{state: acquireState})
	leases.RegisterLeasesServer(server, &LeasesProxy{state: acquireState})
	health.RegisterHealthServer(server, &HealthProxy{state: acquireState})

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	(&http2.Server{}).ServeConn(conn, &http2.ServeConnOpts{Handler: server})
}

// ProxyState is created once usually during a Status API call.
type ProxyState struct {
	Conn       *grpc.ClientConn // Conn is the connection to the buildkitd server.
	SummaryURL string           // SummaryURL is the UI summary page.
	Reporter   progress.Writer  // Reporter forwards status events to the API.
	Err        error            // Err is set when the connection cannot be established or the build fails.
}

type ControlProxy struct {
	state    func() *ProxyState
	status   chan *client.SolveStatus
	platform string
	cancel   context.CancelFunc
}

func (p *ControlProxy) Prune(in *control.PruneRequest, toBuildx control.Control_PruneServer) error {
	ctx := toBuildx.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := p.state()
	if state.Err != nil {
		return state.Err
	}

	fromBuildkit, err := control.NewControlClient(state.Conn).Prune(ctx, in)
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
	if builds.Load() == 1 {
		return &control.SolveResponse{}, nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := control.NewControlClient(state.Conn)
	// DEPOT: stop recording the build steps and traces on the server.
	in.Internal = true
	res, err := client.Solve(ctx, in)
	if err != nil {
		state.Err = err
	}
	return res, err
}

func (p *ControlProxy) Status(in *control.StatusRequest, toBuildx control.Control_StatusServer) error {
	if builds.Load() == 1 {
		return nil
	}

	ctx := toBuildx.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := p.state()
	if state.Err != nil {
		return state.Err
	}

	fromBuildkit, err := control.NewControlClient(state.Conn).Status(ctx, in)
	if err != nil {
		return err
	}

	buildkitErr := make(chan error, 1)

	go func() {
		for {
			msg, err := fromBuildkit.Recv()
			if err != nil {
				if os.Getenv("DEPOT_NO_SUMMARY_LINK") == "" {
					progresshelper.Log(state.Reporter, "Build summary: "+state.SummaryURL, nil)
				}

				if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
					buildkitErr <- nil
					break
				}
				buildkitErr <- err
				return
			}

			// Drop if the buffer is backed up.
			select {
			case p.status <- client.NewSolveStatus(msg):
			default:
			}
		}
	}()

	for {
		select {
		case message := <-p.status:
			for _, response := range message.Marshal() {
				err := toBuildx.Send(response)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return nil
					}
					return err
				}
			}
		case err := <-buildkitErr:
			for {
				select {
				case message := <-p.status:
					for _, response := range message.Marshal() {
						err := toBuildx.Send(response)
						if err != nil {
							if errors.Is(err, context.Canceled) {
								return nil
							}
							return err
						}
					}
				default:
					return err
				}
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (p *ControlProxy) Session(buildx control.Control_SessionServer) error {
	if builds.Load() == 1 {
		return nil
	}

	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	state := p.state()
	if state.Err != nil {
		return state.Err
	}

	buildkit, err := control.NewControlClient(state.Conn).Session(buildkitCtx)
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

// Use hard-coded list because we don't want to start an ephemeral builder until
// we get a status.
//
// Specifically, buildkit runs disk-usage and build-history continually.
// Those API calls would keep the builder alive, even if the user is not using it.
// ListWorkers call is common among builds and those commands.
func (p *ControlProxy) ListWorkers(ctx context.Context, in *control.ListWorkersRequest) (*control.ListWorkersResponse, error) {
	num := builds.Add(1)
	// When we get a second build request we know it is not an buildx metadata call such as disk usage.
	if num > 1 {
		state := p.state()
		if state.Err != nil {
			return nil, state.Err
		}
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if ok && !isOlderThanBuildx013(md.Get("user-agent")) {
		builds.Add(1)
	}

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

func (p *ControlProxy) scheduleShutdown() {
	go func() { p.cancel() }()
}

// Used by desktop.  We ignore and shutdown.
func (p *ControlProxy) DiskUsage(ctx context.Context, in *control.DiskUsageRequest) (*control.DiskUsageResponse, error) {
	p.scheduleShutdown()
	return &control.DiskUsageResponse{}, nil
}

// Used by desktop.  We ignore and shutdown.
func (p *ControlProxy) Info(ctx context.Context, in *control.InfoRequest) (*control.InfoResponse, error) {
	p.scheduleShutdown()
	return nil, status.Errorf(codes.Unimplemented, "method Info not implemented")
}

// Used by desktop.  We ignore and shutdown.
func (p *ControlProxy) ListenBuildHistory(in *control.BuildHistoryRequest, toBuildx control.Control_ListenBuildHistoryServer) error {
	return status.Errorf(codes.Unimplemented, "method ListenBuildHistory not implemented")
}

// Used by desktop.  We ignore and shutdown.
func (p *ControlProxy) UpdateBuildHistory(ctx context.Context, in *control.UpdateBuildHistoryRequest) (*control.UpdateBuildHistoryResponse, error) {
	p.scheduleShutdown()
	return &control.UpdateBuildHistoryResponse{}, nil
}

type GatewayProxy struct {
	state    func() *ProxyState
	platform string
}

func (p *GatewayProxy) ResolveImageConfig(ctx context.Context, in *gateway.ResolveImageConfigRequest) (*gateway.ResolveImageConfigResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
	return client.ResolveImageConfig(ctx, in)
}

func (p *GatewayProxy) Solve(ctx context.Context, in *gateway.SolveRequest) (*gateway.SolveResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
	return client.Solve(ctx, in)
}

func (p *GatewayProxy) ReadFile(ctx context.Context, in *gateway.ReadFileRequest) (*gateway.ReadFileResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
	return client.ReadFile(ctx, in)
}

func (p *GatewayProxy) ReadDir(ctx context.Context, in *gateway.ReadDirRequest) (*gateway.ReadDirResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
	return client.ReadDir(ctx, in)
}

func (p *GatewayProxy) StatFile(ctx context.Context, in *gateway.StatFileRequest) (*gateway.StatFileResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
	return client.StatFile(ctx, in)
}

func (p *GatewayProxy) Evaluate(ctx context.Context, in *gateway.EvaluateRequest) (*gateway.EvaluateResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
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

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
	return client.Return(ctx, in)
}

func (p *GatewayProxy) Inputs(ctx context.Context, in *gateway.InputsRequest) (*gateway.InputsResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
	return client.Inputs(ctx, in)
}

func (p *GatewayProxy) NewContainer(ctx context.Context, in *gateway.NewContainerRequest) (*gateway.NewContainerResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
	return client.NewContainer(ctx, in)
}

func (p *GatewayProxy) ReleaseContainer(ctx context.Context, in *gateway.ReleaseContainerRequest) (*gateway.ReleaseContainerResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
	return client.ReleaseContainer(ctx, in)
}

func (p *GatewayProxy) ExecProcess(buildx gateway.LLBBridge_ExecProcessServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	state := p.state()
	if state.Err != nil {
		return state.Err
	}

	buildkit, err := gateway.NewLLBBridgeClient(state.Conn).ExecProcess(buildkitCtx)
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

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := gateway.NewLLBBridgeClient(state.Conn)
	return client.Warn(ctx, in)
}

type TracesProxy struct {
	state func() *ProxyState
	trace.UnimplementedTraceServiceServer
}

func (p *TracesProxy) Export(ctx context.Context, in *trace.ExportTraceServiceRequest) (*trace.ExportTraceServiceResponse, error) {
	if builds.Load() == 1 {
		return &trace.ExportTraceServiceResponse{}, nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := trace.NewTraceServiceClient(state.Conn)
	return client.Export(ctx, in)
}

type ContentProxy struct {
	state func() *ProxyState
}

func (p *ContentProxy) Info(ctx context.Context, in *content.InfoRequest) (*content.InfoResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := content.NewContentClient(state.Conn)
	return client.Info(ctx, in)
}

func (p *ContentProxy) Update(ctx context.Context, in *content.UpdateRequest) (*content.UpdateResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := content.NewContentClient(state.Conn)
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

	state := p.state()
	if state.Err != nil {
		return state.Err
	}

	fromBuildkit, err := content.NewContentClient(state.Conn).List(ctx, in)
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

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := content.NewContentClient(state.Conn)
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

	state := p.state()
	if state.Err != nil {
		return state.Err
	}

	fromBuildkit, err := content.NewContentClient(state.Conn).Read(ctx, in)
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

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := content.NewContentClient(state.Conn)
	return client.Status(ctx, in)
}

func (p *ContentProxy) ListStatuses(ctx context.Context, in *content.ListStatusesRequest) (*content.ListStatusesResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := content.NewContentClient(state.Conn)
	return client.ListStatuses(ctx, in)
}

func (p *ContentProxy) Write(buildx content.Content_WriteServer) error {
	md, _ := metadata.FromIncomingContext(buildx.Context())
	buildkitCtx := metadata.NewOutgoingContext(buildx.Context(), md.Copy())
	buildkitCtx, buildkitCancel := context.WithCancel(buildkitCtx)
	defer buildkitCancel()

	state := p.state()
	if state.Err != nil {
		return state.Err
	}

	buildkit, err := content.NewContentClient(state.Conn).Write(buildkitCtx)
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

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := content.NewContentClient(state.Conn)
	return client.Abort(ctx, in)
}

type LeasesProxy struct {
	state func() *ProxyState
}

func (p *LeasesProxy) Delete(ctx context.Context, in *leases.DeleteRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := leases.NewLeasesClient(state.Conn)
	return client.Delete(ctx, in)
}

func (p *LeasesProxy) Create(ctx context.Context, in *leases.CreateRequest) (*leases.CreateResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := leases.NewLeasesClient(state.Conn)
	return client.Create(ctx, in)
}

func (p *LeasesProxy) List(ctx context.Context, in *leases.ListRequest) (*leases.ListResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := leases.NewLeasesClient(state.Conn)
	return client.List(ctx, in)
}

func (p *LeasesProxy) AddResource(ctx context.Context, in *leases.AddResourceRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := leases.NewLeasesClient(state.Conn)
	return client.AddResource(ctx, in)
}

func (p *LeasesProxy) DeleteResource(ctx context.Context, in *leases.DeleteResourceRequest) (*types.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := leases.NewLeasesClient(state.Conn)
	return client.DeleteResource(ctx, in)
}

func (p *LeasesProxy) ListResources(ctx context.Context, in *leases.ListResourcesRequest) (*leases.ListResourcesResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := leases.NewLeasesClient(state.Conn)
	return client.ListResources(ctx, in)
}

type HealthProxy struct {
	state func() *ProxyState
}

func (p *HealthProxy) Check(ctx context.Context, in *health.HealthCheckRequest) (*health.HealthCheckResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	state := p.state()
	if state.Err != nil {
		return nil, state.Err
	}

	client := health.NewHealthClient(state.Conn)
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

	state := p.state()
	if state.Err != nil {
		return state.Err
	}

	fromBuildkit, err := health.NewHealthClient(state.Conn).Watch(ctx, in)
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

func isOlderThanBuildx013(userAgent []string) bool {
	for _, ua := range userAgent {
		parts := strings.Split(ua, "/")
		if len(parts) < 2 {
			continue
		}

		if parts[0] != "grpc-go" {
			continue
		}

		parts = strings.Split(parts[1], ".")
		if len(parts) < 2 {
			continue
		}

		if parts[0] != "1" {
			continue
		}

		minor, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}

		if minor < 59 {
			return true
		}
	}

	return false
}
