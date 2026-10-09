package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type apiBehavior struct {
	PendingConnections int
	Gzip               bool
	LoadUsingRegistry  bool
	PullFails          bool
	CancelAfter        time.Duration
	CreateBuildCode    connect.Code
	ConnectionCode     connect.Code
	ResponseProjectID  string
}

type apiCall struct {
	Method  string          `json:"method"`
	Request json.RawMessage `json:"request"`
}

type apiSession struct {
	mu        sync.Mutex
	name      string
	project   string
	behavior  apiBehavior
	calls     []apiCall
	builds    int
	pending   map[cliv1.BuilderPlatform]int
	createdAt map[string]time.Time
	statuses  int
	stable    map[string]string
}

type fakeAPI struct {
	mu       sync.Mutex
	sessions map[string]*apiSession
	env      *environment
	server   *http.Server
	address  string
	port     int
}

// listenAddress is the address of the API. Containers reach it as
// host.docker.internal. Docker on macOS forwards that name to the loopback
// interface of the host; on Linux it is the gateway of the bridge network,
// so the API listens on every interface.
func listenAddress() string {
	if runtime.GOOS == "darwin" {
		return "127.0.0.1:0"
	}
	return "0.0.0.0:0"
}

// containerURL is the URL of the API for a container.
func (a *fakeAPI) containerURL() string {
	return fmt.Sprintf("http://host.docker.internal:%d", a.port)
}

func startFakeAPI(env *environment) (*fakeAPI, error) {
	api := &fakeAPI{sessions: map[string]*apiSession{}, env: env}
	mux := http.NewServeMux()
	mux.Handle(cliv1connect.NewBuildServiceHandler(api))
	mux.Handle(cliv1connect.NewPushServiceHandler(pushService{api}))
	listener, err := net.Listen("tcp", listenAddress())
	if err != nil {
		return nil, err
	}
	api.address = "http://" + listener.Addr().String()
	api.port = listener.Addr().(*net.TCPAddr).Port
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	api.server = &http.Server{Handler: mux, Protocols: &protocols, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = api.server.Serve(listener) }()
	return api, nil
}

func (a *fakeAPI) close() {
	_ = a.server.Close()
}

func (a *fakeAPI) openSession(name, token, project string, behavior apiBehavior) *apiSession {
	s := &apiSession{
		name:      name,
		project:   project,
		behavior:  behavior,
		pending:   map[cliv1.BuilderPlatform]int{},
		createdAt: map[string]time.Time{},
		stable:    map[string]string{},
	}
	a.mu.Lock()
	a.sessions[token] = s
	a.mu.Unlock()
	return s
}

func (a *fakeAPI) session(header http.Header) (*apiSession, error) {
	token := strings.TrimPrefix(header.Get("Authorization"), "Bearer ")
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[token]
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unknown token"))
	}
	return s, nil
}

func (a *fakeAPI) bindToken(token string, s *apiSession) {
	a.mu.Lock()
	a.sessions[token] = s
	a.mu.Unlock()
}

func (s *apiSession) record(method string, msg proto.Message) {
	data, err := protojson.Marshal(msg)
	if err != nil {
		data = []byte(`{}`)
	}
	s.mu.Lock()
	s.calls = append(s.calls, apiCall{Method: method, Request: data})
	s.mu.Unlock()
}

func (s *apiSession) snapshot() []apiCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apiCall(nil), s.calls...)
}

func (a *fakeAPI) CreateBuild(ctx context.Context, req *connect.Request[cliv1.CreateBuildRequest]) (*connect.Response[cliv1.CreateBuildResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("CreateBuild", req.Msg)
	if s.behavior.CreateBuildCode != 0 {
		return nil, connect.NewError(s.behavior.CreateBuildCode, errors.New("validation: create build rejected"))
	}

	s.mu.Lock()
	s.builds++
	buildID := fmt.Sprintf("%s-b%d", s.name, s.builds)
	s.createdAt[buildID] = time.Now()
	s.mu.Unlock()

	projectID := req.Msg.GetProjectId()
	if s.behavior.ResponseProjectID != "" {
		projectID = s.behavior.ResponseProjectID
	}
	buildToken := "build-token-" + buildID
	a.bindToken(buildToken, s)

	registryHost := a.env.registryPushHost()
	credential := base64.StdEncoding.EncodeToString([]byte("x-token:" + buildToken))
	tags := []*cliv1.CreateBuildResponse_Tag{{Tag: fmt.Sprintf("%s/%s:%s", registryHost, projectID, buildID)}}
	for _, opt := range req.Msg.GetOptions() {
		for _, saveTag := range opt.GetSaveTags() {
			tags = append(tags, &cliv1.CreateBuildResponse_Tag{Tag: fmt.Sprintf("%s/%s:%s", registryHost, projectID, saveTag)})
		}
	}

	return connect.NewResponse(&cliv1.CreateBuildResponse{
		BuildId:               buildID,
		BuildToken:            buildToken,
		BuildUrl:              "https://depot.invalid/builds/" + buildID,
		ProjectId:             projectID,
		Registry:              &cliv1.Registry{LoadUsingRegistry: s.behavior.LoadUsingRegistry},
		AdditionalCredentials: []*cliv1.CreateBuildResponse_Credential{{Host: registryHost, Token: credential}},
		AdditionalTags:        tags,
	}), nil
}

func (a *fakeAPI) GetBuild(ctx context.Context, req *connect.Request[cliv1.GetBuildRequest]) (*connect.Response[cliv1.GetBuildResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("GetBuild", req.Msg)
	return connect.NewResponse(&cliv1.GetBuildResponse{
		BuildId:   req.Msg.BuildId,
		ProjectId: s.project,
		BuildUrl:  "https://depot.invalid/builds/" + req.Msg.BuildId,
	}), nil
}

func (a *fakeAPI) FinishBuild(ctx context.Context, req *connect.Request[cliv1.FinishBuildRequest]) (*connect.Response[cliv1.FinishBuildResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("FinishBuild", req.Msg)
	return connect.NewResponse(&cliv1.FinishBuildResponse{}), nil
}

func (a *fakeAPI) GetBuildKitConnection(ctx context.Context, req *connect.Request[cliv1.GetBuildKitConnectionRequest]) (*connect.Response[cliv1.GetBuildKitConnectionResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("GetBuildKitConnection", req.Msg)
	if s.behavior.ConnectionCode != 0 {
		return nil, connect.NewError(s.behavior.ConnectionCode, errors.New("validation: no builder is available"))
	}

	s.mu.Lock()
	pendingServed := s.pending[req.Msg.Platform]
	if pendingServed < s.behavior.PendingConnections {
		s.pending[req.Msg.Platform] = pendingServed + 1
		s.mu.Unlock()
		return connect.NewResponse(&cliv1.GetBuildKitConnectionResponse{
			Connection: &cliv1.GetBuildKitConnectionResponse_Pending{Pending: &cliv1.GetBuildKitConnectionResponse_PendingConnection{WaitMs: 100}},
		}), nil
	}
	s.mu.Unlock()

	endpoint, err := a.env.buildkitEndpoint(req.Msg.Platform)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	certs := a.env.certificates
	active := &cliv1.GetBuildKitConnectionResponse_ActiveConnection{
		Endpoint:   endpoint,
		ServerName: buildkitServerName,
		CaCert:     &cliv1.Cert{Cert: certs.CA},
		Cert:       &cliv1.Cert{Cert: certs.ClientCert, Key: certs.ClientKey},
	}
	if s.behavior.Gzip {
		active.Compressor = &cliv1.GetBuildKitConnectionResponse_ActiveConnection_Gzip_{Gzip: &cliv1.GetBuildKitConnectionResponse_ActiveConnection_Gzip{}}
	}
	return connect.NewResponse(&cliv1.GetBuildKitConnectionResponse{
		Connection: &cliv1.GetBuildKitConnectionResponse_Active{Active: active},
	}), nil
}

func (a *fakeAPI) ReportBuildHealth(ctx context.Context, req *connect.Request[cliv1.ReportBuildHealthRequest]) (*connect.Response[cliv1.ReportBuildHealthResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("ReportBuildHealth", req.Msg)
	res := &cliv1.ReportBuildHealthResponse{}
	if s.behavior.CancelAfter > 0 {
		s.mu.Lock()
		created, ok := s.createdAt[req.Msg.BuildId]
		s.mu.Unlock()
		if ok {
			res.CancelsAt = timestamppb.New(created.Add(s.behavior.CancelAfter))
		}
	}
	return connect.NewResponse(res), nil
}

func (a *fakeAPI) ReportTimings(ctx context.Context, req *connect.Request[cliv1.ReportTimingsRequest]) (*connect.Response[cliv1.ReportTimingsResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("ReportTimings", req.Msg)
	return connect.NewResponse(&cliv1.ReportTimingsResponse{}), nil
}

func (a *fakeAPI) ReportStatus(ctx context.Context, req *connect.Request[cliv1.ReportStatusRequest]) (*connect.Response[cliv1.ReportStatusResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.statuses += len(req.Msg.Statuses)
	maps.Copy(s.stable, req.Msg.StableDigests)
	s.mu.Unlock()
	s.record("ReportStatus", &cliv1.ReportStatusRequest{BuildId: req.Msg.BuildId})
	return connect.NewResponse(&cliv1.ReportStatusResponse{}), nil
}

func (a *fakeAPI) ReportStatusStream(ctx context.Context, stream *connect.ClientStream[cliv1.ReportStatusStreamRequest]) (*connect.Response[cliv1.ReportStatusStreamResponse], error) {
	s, err := a.session(stream.RequestHeader())
	if err != nil {
		return nil, err
	}
	for stream.Receive() {
		msg := stream.Msg()
		s.mu.Lock()
		s.statuses += len(msg.Statuses)
		maps.Copy(s.stable, msg.StableDigests)
		s.mu.Unlock()
	}
	s.record("ReportStatusStream", &cliv1.ReportStatusStreamRequest{})
	return connect.NewResponse(&cliv1.ReportStatusStreamResponse{}), stream.Err()
}

func (a *fakeAPI) ReportBuildContext(ctx context.Context, req *connect.Request[cliv1.ReportBuildContextRequest]) (*connect.Response[cliv1.ReportBuildContextResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("ReportBuildContext", req.Msg)
	return connect.NewResponse(&cliv1.ReportBuildContextResponse{}), nil
}

func (a *fakeAPI) ListBuilds(ctx context.Context, req *connect.Request[cliv1.ListBuildsRequest]) (*connect.Response[cliv1.ListBuildsResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("ListBuilds", req.Msg)
	return connect.NewResponse(&cliv1.ListBuildsResponse{}), nil
}

func (a *fakeAPI) GetPullInfo(ctx context.Context, req *connect.Request[cliv1.GetPullInfoRequest]) (*connect.Response[cliv1.GetPullInfoResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("GetPullInfo", req.Msg)
	repository := s.project
	if s.behavior.PullFails {
		repository = "missing/" + s.project
	}
	return connect.NewResponse(&cliv1.GetPullInfoResponse{
		Reference:    fmt.Sprintf("%s/%s:%s", a.env.registryPullHost(), repository, req.Msg.BuildId),
		Username:     "x-token",
		Password:     "validation",
		RegistryHost: a.env.registryPullHost(),
	}), nil
}

func (a *fakeAPI) GetPullToken(ctx context.Context, req *connect.Request[cliv1.GetPullTokenRequest]) (*connect.Response[cliv1.GetPullTokenResponse], error) {
	s, err := a.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("GetPullToken", req.Msg)
	return connect.NewResponse(&cliv1.GetPullTokenResponse{Token: "validation", RegistryHost: a.env.registryPullHost()}), nil
}

type apiSummary struct {
	CreateBuild        []json.RawMessage `json:"createBuild,omitempty"`
	ConnectedPlatforms []string          `json:"connectedPlatforms,omitempty"`
	FinishBuild        []string          `json:"finishBuild,omitempty"`
	BuildContext       []string          `json:"buildContext,omitempty"`
	PullInfoRequests   int               `json:"pullInfoRequests,omitempty"`
	Pushes             []string          `json:"pushes,omitempty"`
	StatusReported     bool              `json:"-"`
	StableDigests      bool              `json:"stableDigests"`
	TimingsReported    bool              `json:"timingsReported"`
}

func (s *apiSession) summary() apiSummary {
	var sum apiSummary
	platforms := map[string]struct{}{}
	for _, call := range s.snapshot() {
		switch call.Method {
		case "CreateBuild":
			sum.CreateBuild = append(sum.CreateBuild, call.Request)
		case "GetBuildKitConnection":
			var req cliv1.GetBuildKitConnectionRequest
			if protojson.Unmarshal(call.Request, &req) == nil {
				platforms[req.Platform.String()] = struct{}{}
			}
		case "FinishBuild":
			var req cliv1.FinishBuildRequest
			if protojson.Unmarshal(call.Request, &req) == nil {
				sum.FinishBuild = append(sum.FinishBuild, finishResult(&req))
			}
		case "ReportBuildContext":
			var req cliv1.ReportBuildContextRequest
			if protojson.Unmarshal(call.Request, &req) == nil {
				for _, d := range req.Dockerfiles {
					hash := sha256.Sum256([]byte(d.Contents))
					sum.BuildContext = append(sum.BuildContext, fmt.Sprintf("%s %s %s", d.Target, d.Filename, hex.EncodeToString(hash[:6])))
				}
			}
		case "GetPullInfo":
			sum.PullInfoRequests++
		case "StartPush", "FinishPush":
			sum.Pushes = append(sum.Pushes, call.Method+" "+string(call.Request))
		case "ReportStatus", "ReportStatusStream":
			sum.StatusReported = true
		case "ReportTimings":
			sum.TimingsReported = true
		}
	}
	for p := range platforms {
		sum.ConnectedPlatforms = append(sum.ConnectedPlatforms, p)
	}
	sum.CreateBuild = sortCreateBuild(sum.CreateBuild)
	sort.Strings(sum.ConnectedPlatforms)
	sort.Strings(sum.FinishBuild)
	sort.Strings(sum.BuildContext)
	s.mu.Lock()
	sum.StatusReported = sum.StatusReported || s.statuses > 0
	sum.StableDigests = len(s.stable) > 0
	s.mu.Unlock()
	return sum
}

func finishResult(req *cliv1.FinishBuildRequest) string {
	switch r := req.Result.(type) {
	case *cliv1.FinishBuildRequest_Success:
		return "success"
	case *cliv1.FinishBuildRequest_Canceled:
		return "canceled"
	case *cliv1.FinishBuildRequest_Error:
		return "error: " + r.Error.Error
	default:
		return "unknown"
	}
}

// sortCreateBuild orders build requests by project and their options by
// target. Earlier versions of the CLI sent both in random order.
func sortCreateBuild(requests []json.RawMessage) []json.RawMessage {
	type request struct {
		raw     map[string]any
		project string
	}
	var parsed []request
	for _, r := range requests {
		var m map[string]any
		if err := json.Unmarshal(r, &m); err != nil {
			return requests
		}
		if options, ok := m["options"].([]any); ok {
			sort.SliceStable(options, func(i, j int) bool {
				return fmt.Sprint(options[i].(map[string]any)["targetName"]) < fmt.Sprint(options[j].(map[string]any)["targetName"])
			})
		}
		parsed = append(parsed, request{raw: m, project: fmt.Sprint(m["projectId"])})
	}
	sort.SliceStable(parsed, func(i, j int) bool { return parsed[i].project < parsed[j].project })
	out := make([]json.RawMessage, len(parsed))
	for i, p := range parsed {
		data, err := json.Marshal(p.raw)
		if err != nil {
			return requests
		}
		out[i] = data
	}
	return out
}

type pushService struct {
	api *fakeAPI
}

func (p pushService) StartPush(ctx context.Context, req *connect.Request[cliv1.StartPushRequest]) (*connect.Response[cliv1.StartPushResponse], error) {
	s, err := p.api.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("StartPush", req.Msg)
	return connect.NewResponse(&cliv1.StartPushResponse{PushId: "push-" + req.Msg.BuildId}), nil
}

func (p pushService) FinishPush(ctx context.Context, req *connect.Request[cliv1.FinishPushRequest]) (*connect.Response[cliv1.FinishPushResponse], error) {
	s, err := p.api.session(req.Header())
	if err != nil {
		return nil, err
	}
	s.record("FinishPush", req.Msg)
	return connect.NewResponse(&cliv1.FinishPushResponse{}), nil
}
