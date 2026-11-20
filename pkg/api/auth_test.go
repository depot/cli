package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1beta1/cliv1beta1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func TestAuthorizeDeviceSendsPollSecret(t *testing.T) {
	handler := &loginHandler{}
	setupLoginServer(t, handler)

	resp, err := AuthorizeDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if resp.GetToken() != "token-1" {
		t.Fatalf("expected token %q, got %q", "token-1", resp.GetToken())
	}
	if handler.finishRequest.GetId() != "request-1" {
		t.Fatalf("expected request ID %q, got %q", "request-1", handler.finishRequest.GetId())
	}
	if handler.finishRequest.GetPollSecret() != "poll-secret-1" {
		t.Fatalf("expected poll secret %q, got %q", "poll-secret-1", handler.finishRequest.GetPollSecret())
	}
}

func TestAuthorizeDeviceReportsExpiredLogin(t *testing.T) {
	handler := &loginHandler{finishError: connect.NewError(connect.CodeNotFound, errors.New("Authorization request not found"))}
	setupLoginServer(t, handler)

	_, err := AuthorizeDevice(context.Background())
	if !errors.Is(err, errLoginExpired) {
		t.Fatalf("expected %v, got %v", errLoginExpired, err)
	}
}

func setupLoginServer(t *testing.T, handler *loginHandler) {
	t.Helper()

	mux := http.NewServeMux()
	path, connectHandler := cliv1beta1connect.NewLoginServiceHandler(handler)
	mux.Handle(path, connectHandler)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)

	t.Setenv("DEPOT_API_URL", server.URL)
}

type loginHandler struct {
	cliv1beta1connect.UnimplementedLoginServiceHandler

	finishRequest *cliv1beta1.FinishLoginRequest
	finishError   error
}

func (h *loginHandler) StartLogin(_ context.Context, _ *connect.Request[cliv1beta1.StartLoginRequest]) (*connect.Response[cliv1beta1.StartLoginResponse], error) {
	return connect.NewResponse(&cliv1beta1.StartLoginResponse{
		Id:               "request-1",
		ApproveUrl:       "https://depot.dev/orgs/cli/approve/request-1",
		VerificationCode: "K7QH-M3XR",
		PollSecret:       "poll-secret-1",
	}), nil
}

func (h *loginHandler) FinishLogin(_ context.Context, req *connect.Request[cliv1beta1.FinishLoginRequest], stream *connect.ServerStream[cliv1beta1.FinishLoginResponse]) error {
	h.finishRequest = req.Msg
	if h.finishError != nil {
		return h.finishError
	}
	return stream.Send(&cliv1beta1.FinishLoginResponse{Token: "token-1"})
}
