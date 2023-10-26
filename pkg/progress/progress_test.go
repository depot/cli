package progress

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/bufbuild/connect-go"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Sends steps and authorization to server
func TestProgress_ReportBuildSteps(t *testing.T) {
	buildID := "bid1"
	token := "hunter2"

	reqVertexes := []*client.Vertex{
		{
			Name:         "[linux/arm64 1/5] FROM docker.io/library/nginx:latest@sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
			Digest:       "sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
			StableDigest: "sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
			Started:      func() *time.Time { t := time.Unix(1677700406, 42807307); return &t }(),
			Completed:    func() *time.Time { t := time.Unix(1677700407, 42807307); return &t }(),
			Inputs:       []digest.Digest{},
			Cached:       true,
		},
		{
			Name:         "[linux/arm64 2/5] ADD server.conf.tmpl /templates/",
			Digest:       "sha256:f8439fc1cdca327a9955d985988af06aa424960a446fb47570aec7bb86d53ce0",
			StableDigest: "sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b",
			Started:      func() *time.Time { t := time.Unix(1677700407, 880021803); return &t }(),
			Completed:    func() *time.Time { t := time.Unix(1677700408, 880021803); return &t }(),
			Inputs: []digest.Digest{
				"sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
				"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
			},
			Cached: true,
		},
	}

	// These are identical to reqVertexes; just a different type.
	wantVertexes := []*controlapi.Vertex{
		{
			Name:         "[linux/arm64 1/5] FROM docker.io/library/nginx:latest@sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
			Digest:       "sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
			StableDigest: "sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
			Started:      func() *time.Time { t := time.Unix(1677700406, 42807307); return &t }(),
			Completed:    func() *time.Time { t := time.Unix(1677700407, 42807307); return &t }(),
			Inputs:       []digest.Digest{},
			Cached:       true,
		},
		{
			Name:         "[linux/arm64 2/5] ADD server.conf.tmpl /templates/",
			Digest:       "sha256:f8439fc1cdca327a9955d985988af06aa424960a446fb47570aec7bb86d53ce0",
			StableDigest: "sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b",
			Started:      func() *time.Time { t := time.Unix(1677700407, 880021803); return &t }(),
			Completed:    func() *time.Time { t := time.Unix(1677700408, 880021803); return &t }(),
			Inputs: []digest.Digest{
				"sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
				"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
			},
			Cached: true,
		},
	}
	wantStableDigests := map[string]string{
		"sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b": "sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
		"sha256:f8439fc1cdca327a9955d985988af06aa424960a446fb47570aec7bb86d53ce0": "sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b",
	}

	wantRequest := &cliv1.ReportStatusRequest{
		BuildId:       buildID,
		Statuses:      []*controlapi.StatusResponse{{Vertexes: wantVertexes}},
		StableDigests: wantStableDigests,
	}

	wantHeaders := http.Header{}
	wantHeaders["Authorization"] = []string{"Bearer " + token}

	mockClient := new(mockBuildServiceClient)
	mockClient.On("ReportStatus",
		mock.MatchedBy(func(gotRequest *cliv1.ReportStatusRequest) bool {
			return assert.Equal(t, wantRequest.Statuses, gotRequest.Statuses) &&
				assert.Equal(t, wantRequest.BuildId, gotRequest.BuildId) &&
				assert.Equal(t, wantRequest.StableDigests, gotRequest.StableDigests)
		}),
		wantHeaders).Return(&cliv1.ReportStatusResponse{},
		nil,
	)

	p := &Progress{
		buildID: buildID,
		token:   token,
		client:  mockClient,
	}

	err := p.ReportStatus(context.Background(), []*client.SolveStatus{{Vertexes: reqVertexes}})
	assert.NoError(t, err)

	mockClient.AssertExpectations(t)
}

type mockBuildServiceClient struct {
	mock.Mock
}

func (m *mockBuildServiceClient) ReportStatus(ctx context.Context, req *connect.Request[cliv1.ReportStatusRequest]) (*connect.Response[cliv1.ReportStatusResponse], error) {
	args := m.Called(req.Msg, req.Header())

	return connect.NewResponse(
		args.Get(0).(*cliv1.ReportStatusResponse)), args.Error(1)
}

func (m *mockBuildServiceClient) ReportTimings(ctx context.Context, req *connect.Request[cliv1.ReportTimingsRequest]) (*connect.Response[cliv1.ReportTimingsResponse], error) {
	return nil, nil
}

func (m *mockBuildServiceClient) CreateBuild(context.Context, *connect.Request[cliv1.CreateBuildRequest]) (*connect.Response[cliv1.CreateBuildResponse], error) {
	return nil, nil
}

func (m *mockBuildServiceClient) FinishBuild(context.Context, *connect.Request[cliv1.FinishBuildRequest]) (*connect.Response[cliv1.FinishBuildResponse], error) {
	return nil, nil
}

func (m *mockBuildServiceClient) GetBuildKitConnection(context.Context, *connect.Request[cliv1.GetBuildKitConnectionRequest]) (*connect.Response[cliv1.GetBuildKitConnectionResponse], error) {
	return nil, nil
}

func (m *mockBuildServiceClient) ReportBuildHealth(context.Context, *connect.Request[cliv1.ReportBuildHealthRequest]) (*connect.Response[cliv1.ReportBuildHealthResponse], error) {
	return nil, nil
}

func (m *mockBuildServiceClient) ListBuilds(context.Context, *connect.Request[cliv1.ListBuildsRequest]) (*connect.Response[cliv1.ListBuildsResponse], error) {
	return nil, nil
}

func (m *mockBuildServiceClient) ReportBuildContext(context.Context, *connect.Request[cliv1.ReportBuildContextRequest]) (*connect.Response[cliv1.ReportBuildContextResponse], error) {
	return nil, nil
}

func (m *mockBuildServiceClient) GetPullInfo(context.Context, *connect.Request[cliv1.GetPullInfoRequest]) (*connect.Response[cliv1.GetPullInfoResponse], error) {
	return nil, nil
}
