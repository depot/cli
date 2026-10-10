package buildxdriver

import (
	"context"
	"maps"
	"net/url"
	"strings"
	"sync"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	solveMethod              = "/moby.buildkit.v1.Control/Solve"
	listWorkersMethod        = "/moby.buildkit.v1.Control/ListWorkers"
	sessionMethod            = "/moby.buildkit.v1.Control/Session"
	listenBuildHistoryMethod = "/moby.buildkit.v1.Control/ListenBuildHistory"

	sessionIDHeader        = "x-docker-expose-session-uuid"
	sharedKeyHeader        = "x-docker-expose-session-sharedkey"
	sharedKeyEncodedHeader = "x-docker-expose-session-sharedkey-encoded"

	targetBuildArg = "build-arg:DEPOT_TARGET"

	// sessionTargetTimeout limits how long a session waits for the solve
	// request that names its target.
	sessionTargetTimeout = 10 * time.Second
)

// interceptor observes and adjusts the gRPC calls of the buildkit client of
// one machine.
//
// It keeps the exporter response of every solve. buildx merges the
// responses of a multi-machine build into one, but image loading, SBOM
// download, and lease cleanup need the response of each machine.
//
// It keeps the worker list of the machine after the first successful
// request, because the buildkit client lists the workers before every
// build and the list does not change while the CLI runs.
//
// It also adds the target name to the shared key of each session. buildkitd
// stores the build context of a session under its shared key. buildx uses
// one key for every target that has the same context directory, and two
// targets that sync that directory at the same time with different ignore
// files can then read each other's files.
type interceptor struct {
	mu        sync.Mutex
	responses map[string]map[string]string
	sessions  map[string]*sessionTarget
	workers   *controlapi.ListWorkersResponse
}

// sessionTarget is the target of a session. ready closes when the solve
// request of the session is seen.
type sessionTarget struct {
	ready  chan struct{}
	target string
}

func newInterceptor() *interceptor {
	return &interceptor{
		responses: map[string]map[string]string{},
		sessions:  map[string]*sessionTarget{},
	}
}

func (r *interceptor) clientOptions() []client.ClientOpt {
	return []client.ClientOpt{
		client.WithGRPCDialOption(grpc.WithChainUnaryInterceptor(r.interceptUnary)),
		client.WithGRPCDialOption(grpc.WithChainStreamInterceptor(r.interceptStream)),
	}
}

func (r *interceptor) session(id string) *sessionTarget {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		s = &sessionTarget{ready: make(chan struct{})}
		r.sessions[id] = s
	}
	return s
}

func (r *interceptor) interceptUnary(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	switch method {
	case listWorkersMethod:
		return r.listWorkers(ctx, method, req, reply, cc, invoker, opts...)
	case solveMethod:
		return r.solve(ctx, method, req, reply, cc, invoker, opts...)
	default:
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func (r *interceptor) solve(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	solveRequest, ok := req.(*controlapi.SolveRequest)
	if ok && solveRequest.Session != "" {
		s := r.session(solveRequest.Session)
		r.mu.Lock()
		select {
		case <-s.ready:
		default:
			s.target = solveRequest.FrontendAttrs[targetBuildArg]
			close(s.ready)
		}
		r.mu.Unlock()
	}

	err := invoker(ctx, method, req, reply, cc, opts...)
	if err != nil || !ok || solveRequest.Ref == "" {
		return err
	}
	if solveResponse, ok := reply.(*controlapi.SolveResponse); ok {
		r.mu.Lock()
		r.responses[solveRequest.Ref] = maps.Clone(solveResponse.ExporterResponse)
		r.mu.Unlock()
	}
	return nil
}

func (r *interceptor) listWorkers(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	request, ok := req.(*controlapi.ListWorkersRequest)
	response, ok2 := reply.(*controlapi.ListWorkersResponse)
	if !ok || !ok2 || len(request.Filter) > 0 {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	r.mu.Lock()
	cached := r.workers
	r.mu.Unlock()
	if cached != nil {
		proto.Merge(response, cached)
		return nil
	}
	if err := invoker(ctx, method, req, reply, cc, opts...); err != nil {
		return err
	}
	r.mu.Lock()
	r.workers = proto.Clone(response).(*controlapi.ListWorkersResponse)
	r.mu.Unlock()
	return nil
}

func (r *interceptor) interceptStream(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	switch method {
	case listenBuildHistoryMethod:
		// Depot builds do not create history records, so the history API
		// is reported as unavailable without a round trip to the machine.
		return nil, status.Error(codes.Unimplemented, "build history is not available on depot builders")
	case sessionMethod:
		ctx = r.sessionContext(ctx)
	}
	return streamer(ctx, desc, cc, method, opts...)
}

// sessionContext adds the target name to the shared key in the outgoing
// metadata of a session, in the form "<directory>:<target>:<node>".
func (r *interceptor) sessionContext(ctx context.Context) context.Context {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return ctx
	}
	ids := md.Get(sessionIDHeader)
	keys := md.Get(sharedKeyHeader)
	if len(ids) == 0 || len(keys) == 0 || keys[0] == "" {
		return ctx
	}

	s := r.session(ids[0])
	timer := time.NewTimer(sessionTargetTimeout)
	defer timer.Stop()
	select {
	case <-s.ready:
	case <-timer.C:
		return ctx
	case <-ctx.Done():
		return ctx
	}
	r.mu.Lock()
	target := s.target
	r.mu.Unlock()
	if target == "" {
		return ctx
	}

	encoded := len(md.Get(sharedKeyEncodedHeader)) > 0
	key := keys[0]
	if encoded {
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
	}
	if i := strings.LastIndex(key, ":"); i >= 0 {
		key = key[:i] + ":" + target + key[i:]
	} else {
		key = key + ":" + target
	}
	if encoded {
		key = url.QueryEscape(key)
	}

	md = md.Copy()
	md.Set(sharedKeyHeader, key)
	return metadata.NewOutgoingContext(ctx, md)
}

func (r *interceptor) response(ref string) (*client.SolveResponse, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.responses[ref]
	if !ok {
		return nil, false
	}
	exporterResponse := maps.Clone(res)
	if exporterResponse == nil {
		exporterResponse = map[string]string{}
	}
	return &client.SolveResponse{ExporterResponse: exporterResponse}, true
}
