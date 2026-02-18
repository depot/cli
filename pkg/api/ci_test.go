package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Secret service test helpers

type testSecretHandler struct {
	civ1connect.UnimplementedSecretServiceHandler
	addSecretFn    func(context.Context, *connect.Request[civ1.AddSecretRequest]) (*connect.Response[civ1.AddSecretResponse], error)
	removeSecretFn func(context.Context, *connect.Request[civ1.RemoveSecretRequest]) (*connect.Response[civ1.RemoveSecretResponse], error)
	listSecretsFn  func(context.Context, *connect.Request[civ1.ListSecretsRequest]) (*connect.Response[civ1.ListSecretsResponse], error)
}

func (h *testSecretHandler) AddSecret(ctx context.Context, req *connect.Request[civ1.AddSecretRequest]) (*connect.Response[civ1.AddSecretResponse], error) {
	if h.addSecretFn != nil {
		return h.addSecretFn(ctx, req)
	}
	return h.UnimplementedSecretServiceHandler.AddSecret(ctx, req)
}

func (h *testSecretHandler) RemoveSecret(ctx context.Context, req *connect.Request[civ1.RemoveSecretRequest]) (*connect.Response[civ1.RemoveSecretResponse], error) {
	if h.removeSecretFn != nil {
		return h.removeSecretFn(ctx, req)
	}
	return h.UnimplementedSecretServiceHandler.RemoveSecret(ctx, req)
}

func (h *testSecretHandler) ListSecrets(ctx context.Context, req *connect.Request[civ1.ListSecretsRequest]) (*connect.Response[civ1.ListSecretsResponse], error) {
	if h.listSecretsFn != nil {
		return h.listSecretsFn(ctx, req)
	}
	return h.UnimplementedSecretServiceHandler.ListSecrets(ctx, req)
}

func newTestSecretServer(handler *testSecretHandler) *httptest.Server {
	mux := http.NewServeMux()
	path, h := civ1connect.NewSecretServiceHandler(handler)
	mux.Handle(path, h)
	return httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
}

// Variable service test helpers

type testVariableHandler struct {
	civ1connect.UnimplementedVariableServiceHandler
	addVariableFn    func(context.Context, *connect.Request[civ1.AddVariableRequest]) (*connect.Response[civ1.AddVariableResponse], error)
	removeVariableFn func(context.Context, *connect.Request[civ1.RemoveVariableRequest]) (*connect.Response[civ1.RemoveVariableResponse], error)
	listVariablesFn  func(context.Context, *connect.Request[civ1.ListVariablesRequest]) (*connect.Response[civ1.ListVariablesResponse], error)
}

func (h *testVariableHandler) AddVariable(ctx context.Context, req *connect.Request[civ1.AddVariableRequest]) (*connect.Response[civ1.AddVariableResponse], error) {
	if h.addVariableFn != nil {
		return h.addVariableFn(ctx, req)
	}
	return h.UnimplementedVariableServiceHandler.AddVariable(ctx, req)
}

func (h *testVariableHandler) RemoveVariable(ctx context.Context, req *connect.Request[civ1.RemoveVariableRequest]) (*connect.Response[civ1.RemoveVariableResponse], error) {
	if h.removeVariableFn != nil {
		return h.removeVariableFn(ctx, req)
	}
	return h.UnimplementedVariableServiceHandler.RemoveVariable(ctx, req)
}

func (h *testVariableHandler) ListVariables(ctx context.Context, req *connect.Request[civ1.ListVariablesRequest]) (*connect.Response[civ1.ListVariablesResponse], error) {
	if h.listVariablesFn != nil {
		return h.listVariablesFn(ctx, req)
	}
	return h.UnimplementedVariableServiceHandler.ListVariables(ctx, req)
}

func newTestVariableServer(handler *testVariableHandler) *httptest.Server {
	mux := http.NewServeMux()
	path, h := civ1connect.NewVariableServiceHandler(handler)
	mux.Handle(path, h)
	return httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
}

// Secret tests

func TestCIAddSecret(t *testing.T) {
	var (
		receivedName  string
		receivedValue string
		receivedAuth  string
		receivedOrg   string
	)

	server := newTestSecretServer(&testSecretHandler{
		addSecretFn: func(_ context.Context, req *connect.Request[civ1.AddSecretRequest]) (*connect.Response[civ1.AddSecretResponse], error) {
			receivedName = req.Msg.Name
			receivedValue = req.Msg.Value
			receivedAuth = req.Header().Get("Authorization")
			receivedOrg = req.Header().Get("x-depot-org")
			return connect.NewResponse(&civ1.AddSecretResponse{}), nil
		},
	})
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIAddSecret(context.Background(), "test-token", "test-org", "MY_SECRET", "secret-value")
	if err != nil {
		t.Fatalf("CIAddSecret failed: %v", err)
	}

	if receivedName != "MY_SECRET" {
		t.Errorf("expected name MY_SECRET, got %s", receivedName)
	}
	if receivedValue != "secret-value" {
		t.Errorf("expected value secret-value, got %s", receivedValue)
	}
	if receivedAuth != "Bearer test-token" {
		t.Errorf("expected Authorization header, got %s", receivedAuth)
	}
	if receivedOrg != "test-org" {
		t.Errorf("expected x-depot-org header, got %s", receivedOrg)
	}
}

func TestCIAddSecretWithDescription(t *testing.T) {
	var receivedDescription string

	server := newTestSecretServer(&testSecretHandler{
		addSecretFn: func(_ context.Context, req *connect.Request[civ1.AddSecretRequest]) (*connect.Response[civ1.AddSecretResponse], error) {
			receivedDescription = req.Msg.GetDescription()
			return connect.NewResponse(&civ1.AddSecretResponse{}), nil
		},
	})
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIAddSecretWithDescription(context.Background(), "test-token", "test-org", "MY_SECRET", "secret-value", "secret description")
	if err != nil {
		t.Fatalf("CIAddSecretWithDescription failed: %v", err)
	}

	if receivedDescription != "secret description" {
		t.Errorf("expected description to be set, got %q", receivedDescription)
	}
}

func TestCIListSecrets(t *testing.T) {
	desc := "First secret"
	server := newTestSecretServer(&testSecretHandler{
		listSecretsFn: func(_ context.Context, _ *connect.Request[civ1.ListSecretsRequest]) (*connect.Response[civ1.ListSecretsResponse], error) {
			return connect.NewResponse(&civ1.ListSecretsResponse{
				Secrets: []*civ1.Secret{
					{Name: "SECRET1", Description: &desc, LastModified: timestamppb.Now()},
					{Name: "SECRET2"},
				},
			}), nil
		},
	})
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	secrets, err := CIListSecrets(context.Background(), "test-token", "test-org")
	if err != nil {
		t.Fatalf("CIListSecrets failed: %v", err)
	}

	if len(secrets) != 2 {
		t.Errorf("expected 2 secrets, got %d", len(secrets))
	}

	if secrets[0].Name != "SECRET1" {
		t.Errorf("expected first secret name SECRET1, got %s", secrets[0].Name)
	}
	if secrets[0].Description != "First secret" {
		t.Errorf("expected first secret description, got %s", secrets[0].Description)
	}

	if secrets[1].Name != "SECRET2" {
		t.Errorf("expected second secret name SECRET2, got %s", secrets[1].Name)
	}
}

func TestCIDeleteSecret(t *testing.T) {
	var receivedName string

	server := newTestSecretServer(&testSecretHandler{
		removeSecretFn: func(_ context.Context, req *connect.Request[civ1.RemoveSecretRequest]) (*connect.Response[civ1.RemoveSecretResponse], error) {
			receivedName = req.Msg.Name
			return connect.NewResponse(&civ1.RemoveSecretResponse{}), nil
		},
	})
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIDeleteSecret(context.Background(), "test-token", "test-org", "MY_SECRET")
	if err != nil {
		t.Fatalf("CIDeleteSecret failed: %v", err)
	}

	if receivedName != "MY_SECRET" {
		t.Errorf("expected name MY_SECRET, got %s", receivedName)
	}
}

func TestCIErrorHandling(t *testing.T) {
	server := newTestSecretServer(&testSecretHandler{
		addSecretFn: func(_ context.Context, _ *connect.Request[civ1.AddSecretRequest]) (*connect.Response[civ1.AddSecretResponse], error) {
			return nil, connect.NewError(connect.CodeInvalidArgument, nil)
		},
	})
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIAddSecret(context.Background(), "test-token", "test-org", "SECRET", "value")
	if err == nil {
		t.Error("expected error for invalid argument response")
	}
}

func TestCIErrorHandlingConnectMessage(t *testing.T) {
	server := newTestSecretServer(&testSecretHandler{
		addSecretFn: func(_ context.Context, _ *connect.Request[civ1.AddSecretRequest]) (*connect.Response[civ1.AddSecretResponse], error) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid secret value"))
		},
	})
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIAddSecret(context.Background(), "test-token", "test-org", "SECRET", "value")
	if err == nil {
		t.Fatal("expected error for invalid argument response")
	}
	if !strings.Contains(err.Error(), "invalid secret value") {
		t.Fatalf("expected connect message in error, got %q", err.Error())
	}
}

func TestCIDepotOrgHeader(t *testing.T) {
	var headerReceived string

	server := newTestSecretServer(&testSecretHandler{
		listSecretsFn: func(_ context.Context, req *connect.Request[civ1.ListSecretsRequest]) (*connect.Response[civ1.ListSecretsResponse], error) {
			headerReceived = req.Header().Get("x-depot-org")
			return connect.NewResponse(&civ1.ListSecretsResponse{}), nil
		},
	})
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	CIListSecrets(context.Background(), "test-token", "my-org-id")
	if headerReceived != "my-org-id" {
		t.Errorf("expected x-depot-org header to be my-org-id, got %s", headerReceived)
	}
}

// Variable tests

func TestCIAddVariable(t *testing.T) {
	var (
		receivedName  string
		receivedValue string
		receivedAuth  string
		receivedOrg   string
	)

	server := newTestVariableServer(&testVariableHandler{
		addVariableFn: func(_ context.Context, req *connect.Request[civ1.AddVariableRequest]) (*connect.Response[civ1.AddVariableResponse], error) {
			receivedName = req.Msg.Name
			receivedValue = req.Msg.Value
			receivedAuth = req.Header().Get("Authorization")
			receivedOrg = req.Header().Get("x-depot-org")
			return connect.NewResponse(&civ1.AddVariableResponse{}), nil
		},
	})
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIAddVariable(context.Background(), "test-token", "test-org", "MY_VAR", "var-value")
	if err != nil {
		t.Fatalf("CIAddVariable failed: %v", err)
	}

	if receivedName != "MY_VAR" {
		t.Errorf("expected name MY_VAR, got %s", receivedName)
	}
	if receivedValue != "var-value" {
		t.Errorf("expected value var-value, got %s", receivedValue)
	}
	if receivedAuth != "Bearer test-token" {
		t.Errorf("expected Authorization header, got %s", receivedAuth)
	}
	if receivedOrg != "test-org" {
		t.Errorf("expected x-depot-org header, got %s", receivedOrg)
	}
}

func TestCIListVariables(t *testing.T) {
	desc := "First variable"
	server := newTestVariableServer(&testVariableHandler{
		listVariablesFn: func(_ context.Context, _ *connect.Request[civ1.ListVariablesRequest]) (*connect.Response[civ1.ListVariablesResponse], error) {
			return connect.NewResponse(&civ1.ListVariablesResponse{
				Variables: []*civ1.Variable{
					{Name: "VAR1", Description: &desc, LastModified: timestamppb.Now()},
					{Name: "VAR2"},
				},
			}), nil
		},
	})
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	variables, err := CIListVariables(context.Background(), "test-token", "test-org")
	if err != nil {
		t.Fatalf("CIListVariables failed: %v", err)
	}

	if len(variables) != 2 {
		t.Errorf("expected 2 variables, got %d", len(variables))
	}

	if variables[0].Name != "VAR1" {
		t.Errorf("expected first variable name VAR1, got %s", variables[0].Name)
	}
	if variables[0].Description != "First variable" {
		t.Errorf("expected first variable description, got %s", variables[0].Description)
	}

	if variables[1].Name != "VAR2" {
		t.Errorf("expected second variable name VAR2, got %s", variables[1].Name)
	}
}

func TestCIDeleteVariable(t *testing.T) {
	var receivedName string

	server := newTestVariableServer(&testVariableHandler{
		removeVariableFn: func(_ context.Context, req *connect.Request[civ1.RemoveVariableRequest]) (*connect.Response[civ1.RemoveVariableResponse], error) {
			receivedName = req.Msg.Name
			return connect.NewResponse(&civ1.RemoveVariableResponse{}), nil
		},
	})
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIDeleteVariable(context.Background(), "test-token", "test-org", "MY_VAR")
	if err != nil {
		t.Fatalf("CIDeleteVariable failed: %v", err)
	}

	if receivedName != "MY_VAR" {
		t.Errorf("expected name MY_VAR, got %s", receivedName)
	}
}
