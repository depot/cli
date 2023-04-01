package commands

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/bufbuild/connect-go"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name  string
		steps []*Step
		want  []*Step
	}{
		{
			name:  "empty",
			steps: []*Step{},
			want:  []*Step{},
		},
		{
			steps: []*Step{
				{
					Name:         "[internal] load build context",
					Digest:       "sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
					StableDigest: "random:8831e066dc1584a0ff85128626b574bcb4bf68e46ab71957522169d84586768d",
					StartTime:    time.Unix(1677700406, 42807307),
					Duration:     1 * time.Millisecond,
					Cached:       false,
					Reported:     false,
				},
				{
					Name:         "[linux/arm64 1/5] FROM docker.io/library/nginx:latest@sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
					Digest:       "sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
					StableDigest: "sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
					StartTime:    time.Unix(1677700406, 42807307),
					Duration:     13377 * time.Nanosecond,
					InputDigests: []digest.Digest{},
					Cached:       true,
					Reported:     false,
				},
				{
					Name:         "[linux/arm64 2/5] ADD server.conf.tmpl /templates/",
					Digest:       "sha256:f8439fc1cdca327a9955d985988af06aa424960a446fb47570aec7bb86d53ce0",
					StableDigest: "sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b",
					StartTime:    time.Unix(1677700407, 880021803),
					Duration:     314674 * time.Nanosecond,
					InputDigests: []digest.Digest{
						"sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
						"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
					},
					Cached:   true,
					Reported: false,
				},
				{
					Name:         "[linux/arm64 3/5] ADD docker-start /usr/local/bin/",
					Digest:       "sha256:97909ca8737039d395333471a78367423d2e7c1404074e4a9328e2ad5a17e259",
					StableDigest: "sha256:f2969bd263ff6cb209be6abe3e78401f3aff99fd8ef352d8e4050037e39cd566",
					StartTime:    time.Unix(1677700408, 197343736),
					Duration:     36467 * time.Nanosecond,
					InputDigests: []digest.Digest{
						"sha256:f8439fc1cdca327a9955d985988af06aa424960a446fb47570aec7bb86d53ce0",
						"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
					},
					Cached:   true,
					Reported: false,
				},
				{
					Name:         "[linux/arm64 4/5] ADD docker-entry /usr/local/bin",
					Digest:       "sha256:abcbe96857349f5da5bf54c44351ab466151416eeda3afaf137f1920f2102156",
					StableDigest: "sha256:08ebd82d42af87517b9a0042172e7f1d30ee8e3c18f4389eacf0974800ebecd5",
					StartTime:    time.Unix(1677700408, 237218947),
					Duration:     35049 * time.Nanosecond,
					InputDigests: []digest.Digest{
						"sha256:97909ca8737039d395333471a78367423d2e7c1404074e4a9328e2ad5a17e259",
						"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
					},
					Cached:   true,
					Reported: false,
				},
				{
					Name:         "[linux/arm64 5/5] RUN chmod +x /usr/local/bin/*",
					Digest:       "sha256:ac91518ab41d8356a383de8d9eafdae2ae7c45b880ce3c13140eb50187112404",
					StableDigest: "sha256:0f1d2d3d4d5d6d7d8d9d0d1d2d3d4d5d6d7d8d9d0d1d2d3d4d5d6d7d8d9d0d1",
					StartTime:    time.Unix(1677700408, 274438691),
					Duration:     237074 * time.Nanosecond,
					InputDigests: []digest.Digest{
						"sha256:abcbe96857349f5da5bf54c44351ab466151416eeda3afaf137f1920f2102156",
						"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
					},
					Cached:   false,
					Reported: false,
				},
			},
			want: []*Step{
				{
					Name:         "[internal] load build context",
					Digest:       "sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
					StableDigest: "random:8831e066dc1584a0ff85128626b574bcb4bf68e46ab71957522169d84586768d",
					StartTime:    time.Unix(1677700406, 42807307),
					Duration:     1 * time.Millisecond,
					Cached:       false,
					Reported:     false,
				},
				{
					Name:         "[linux/arm64 1/5] FROM docker.io/library/nginx:latest@sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
					Digest:       "sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
					StableDigest: "sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
					StartTime:    time.Unix(1677700406, 42807307),
					Duration:     13377 * time.Nanosecond,
					InputDigests: []digest.Digest{},
					Cached:       true,
					Reported:     false,
				},
				{
					Name:         "[linux/arm64 2/5] ADD server.conf.tmpl /templates/",
					Digest:       "sha256:f8439fc1cdca327a9955d985988af06aa424960a446fb47570aec7bb86d53ce0",
					StableDigest: "sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b",
					StartTime:    time.Unix(1677700407, 880021803),
					Duration:     314674 * time.Nanosecond,
					InputDigests: []digest.Digest{
						"sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
						"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
					},
					Cached:             true,
					Reported:           false,
					StableInputDigests: []digest.Digest{"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2"},
					AncestorDigests: []digest.Digest{
						"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
					},
				},
				{
					Name:         "[linux/arm64 3/5] ADD docker-start /usr/local/bin/",
					Digest:       "sha256:97909ca8737039d395333471a78367423d2e7c1404074e4a9328e2ad5a17e259",
					StableDigest: "sha256:f2969bd263ff6cb209be6abe3e78401f3aff99fd8ef352d8e4050037e39cd566",
					StartTime:    time.Unix(1677700408, 197343736),
					Duration:     36467 * time.Nanosecond,
					InputDigests: []digest.Digest{
						"sha256:f8439fc1cdca327a9955d985988af06aa424960a446fb47570aec7bb86d53ce0",
						"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
					},
					Cached:             true,
					Reported:           false,
					StableInputDigests: []digest.Digest{"sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b"},
					AncestorDigests: []digest.Digest{
						"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
						"sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b",
					},
				},
				{
					Name:         "[linux/arm64 4/5] ADD docker-entry /usr/local/bin",
					Digest:       "sha256:abcbe96857349f5da5bf54c44351ab466151416eeda3afaf137f1920f2102156",
					StableDigest: "sha256:08ebd82d42af87517b9a0042172e7f1d30ee8e3c18f4389eacf0974800ebecd5",
					StartTime:    time.Unix(1677700408, 237218947),
					Duration:     35049 * time.Nanosecond,
					InputDigests: []digest.Digest{
						"sha256:97909ca8737039d395333471a78367423d2e7c1404074e4a9328e2ad5a17e259",
						"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
					},
					Cached:             true,
					Reported:           false,
					StableInputDigests: []digest.Digest{"sha256:f2969bd263ff6cb209be6abe3e78401f3aff99fd8ef352d8e4050037e39cd566"},
					AncestorDigests: []digest.Digest{
						"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
						"sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b",
						"sha256:f2969bd263ff6cb209be6abe3e78401f3aff99fd8ef352d8e4050037e39cd566",
					},
				},
				{
					Name:         "[linux/arm64 5/5] RUN chmod +x /usr/local/bin/*",
					Digest:       "sha256:ac91518ab41d8356a383de8d9eafdae2ae7c45b880ce3c13140eb50187112404",
					StableDigest: "sha256:0f1d2d3d4d5d6d7d8d9d0d1d2d3d4d5d6d7d8d9d0d1d2d3d4d5d6d7d8d9d0d1",
					StartTime:    time.Unix(1677700408, 274438691),
					Duration:     237074 * time.Nanosecond,
					InputDigests: []digest.Digest{
						"sha256:abcbe96857349f5da5bf54c44351ab466151416eeda3afaf137f1920f2102156",
						"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
					},
					Cached:             false,
					Reported:           false,
					StableInputDigests: []digest.Digest{"sha256:08ebd82d42af87517b9a0042172e7f1d30ee8e3c18f4389eacf0974800ebecd5"},
					AncestorDigests: []digest.Digest{
						"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
						"sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b",
						"sha256:f2969bd263ff6cb209be6abe3e78401f3aff99fd8ef352d8e4050037e39cd566",
						"sha256:08ebd82d42af87517b9a0042172e7f1d30ee8e3c18f4389eacf0974800ebecd5",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Analyze(tt.steps)
			assert.Equal(t, tt.want, tt.steps)
		})
	}
}

func TestNewTimingRequest(t *testing.T) {
	type args struct {
		buildID string
		steps   []*Step
	}
	tests := []struct {
		name string
		args args
		want *cliv1.ReportTimingsRequest
	}{
		{
			name: "If all steps already reported no request",
			args: args{
				buildID: "bid1",
				steps: []*Step{
					{
						Name:         "[linux/arm64 1/5] FROM docker.io/library/nginx:latest@sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
						StableDigest: "sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
						StartTime:    time.Unix(1677700406, 42807307),
						Duration:     13377 * time.Nanosecond,
						Cached:       true,
						Reported:     true,
					},
				},
			},
			want: nil,
		},
		{
			name: "If not steps no request",
			args: args{
				buildID: "bid1",
				steps:   []*Step{},
			},
			want: nil,
		},
		{
			name: "happy path",
			args: args{
				buildID: "bid1",
				steps: []*Step{
					{
						Name:         "[linux/arm64 1/5] FROM docker.io/library/nginx:latest@sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
						Digest:       "sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
						StableDigest: "sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
						StartTime:    time.Unix(1677700406, 42807307),
						Duration:     13377 * time.Nanosecond,
						InputDigests: []digest.Digest{},
						Cached:       true,
						Reported:     false,
					},
					{
						Name:         "[linux/arm64 2/5] ADD server.conf.tmpl /templates/",
						Digest:       "sha256:f8439fc1cdca327a9955d985988af06aa424960a446fb47570aec7bb86d53ce0",
						StableDigest: "sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b",
						StartTime:    time.Unix(1677700407, 880021803),
						Duration:     314674 * time.Nanosecond,
						InputDigests: []digest.Digest{
							"sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
							"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
						},
						Cached:             true,
						Reported:           false,
						StableInputDigests: []digest.Digest{"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2"},
						AncestorDigests: []digest.Digest{
							"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
						},
					},
				},
			},
			want: &cliv1.ReportTimingsRequest{
				BuildId: "bid1",
				BuildSteps: []*cliv1.BuildStep{
					{
						StartTime:       timestamppb.New(time.Unix(1677700406, 42807307)),
						DurationMs:      int32(13377 / time.Millisecond),
						Name:            "[linux/arm64 1/5] FROM docker.io/library/nginx:latest@sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
						StableDigest:    func(s string) *string { return &s }("sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2"),
						Cached:          true,
						InputDigests:    nil,
						AncestorDigests: nil,
					},
					{
						StartTime:       timestamppb.New(time.Unix(1677700407, 880021803)),
						DurationMs:      int32(314674 / time.Millisecond),
						Name:            "[linux/arm64 2/5] ADD server.conf.tmpl /templates/",
						StableDigest:    func(s string) *string { return &s }("sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b"),
						Cached:          true,
						InputDigests:    []string{"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2"},
						AncestorDigests: []string{"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2"},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NewTimingRequest(tt.args.buildID, tt.args.steps))
		})
	}
}

// Sends steps and authorization to server
func TestProgress_ReportBuildSteps(t *testing.T) {
	buildID := "bid1"
	token := "hunter2"
	steps := []*Step{
		{
			Name:         "[linux/arm64 1/5] FROM docker.io/library/nginx:latest@sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
			Digest:       "sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
			StableDigest: "sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
			StartTime:    time.Unix(1677700406, 42807307),
			Duration:     13377 * time.Nanosecond,
			InputDigests: []digest.Digest{},
			Cached:       true,
			Reported:     false,
		},
		{
			Name:         "[linux/arm64 2/5] ADD server.conf.tmpl /templates/",
			Digest:       "sha256:f8439fc1cdca327a9955d985988af06aa424960a446fb47570aec7bb86d53ce0",
			StableDigest: "sha256:0d3769130c18cb99812166d7a0d67261ec06b439b9756a7b4f3af7bea2c52d6b",
			StartTime:    time.Unix(1677700407, 880021803),
			Duration:     314674 * time.Nanosecond,
			InputDigests: []digest.Digest{
				"sha256:83fb6f3d8df633fe9b547e8fbcedc67c20434a6cb4c61fa362204f0bf02e800b",
				"sha256:8ced7971a11d2bd437ee0469091c3b32d2698bf27d11ed88cb74a5f775fa2708",
			},
			Cached:             true,
			Reported:           false,
			StableInputDigests: []digest.Digest{"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2"},
			AncestorDigests: []digest.Digest{
				"sha256:aa0afebbb3cfa473099a62c4b32e9b3fb73ed23f2a75a65ce1d4b4f55a5c2ef2",
			},
		},
	}

	wantRequest := NewTimingRequest(buildID, steps)

	wantHeaders := http.Header{}
	wantHeaders["Authorization"] = []string{"Bearer " + token}

	mockClient := new(mockBuildServiceClient)
	mockClient.On("ReportTimings", wantRequest, wantHeaders).Return(&cliv1.ReportTimingsResponse{}, nil)

	p := &Progress{
		buildID: buildID,
		token:   token,
		client:  mockClient,
	}

	p.ReportBuildSteps(context.Background(), steps)

	mockClient.AssertExpectations(t)
}

type mockBuildServiceClient struct {
	mock.Mock
}

func (m *mockBuildServiceClient) ReportTimings(ctx context.Context, req *connect.Request[cliv1.ReportTimingsRequest]) (*connect.Response[cliv1.ReportTimingsResponse], error) {
	args := m.Called(req.Msg, req.Header())

	return connect.NewResponse(
		args.Get(0).(*cliv1.ReportTimingsResponse)), args.Error(1)
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
