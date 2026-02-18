package api

import (
	"context"
	"encoding/json"
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

func TestCIAddVariable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		if r.URL.Path != "/depot.ci.v1.VariableService/AddVariable" {
			t.Errorf("expected path /depot.ci.v1.VariableService/AddVariable, got %s", r.URL.Path)
		}

		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected Authorization header, got %s", r.Header.Get("Authorization"))
		}

		if r.Header.Get("x-depot-org") != "test-org" {
			t.Errorf("expected x-depot-org header, got %s", r.Header.Get("x-depot-org"))
		}

		var req CIVariableAddRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Name != "MY_VAR" || req.Value != "var-value" {
			t.Errorf("expected name=MY_VAR value=var-value, got name=%s value=%s", req.Name, req.Value)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIAddVariable(context.Background(), "test-token", "test-org", "MY_VAR", "var-value")
	if err != nil {
		t.Fatalf("CIAddVariable failed: %v", err)
	}
}

func TestCIListVariables(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		if r.URL.Path != "/depot.ci.v1.VariableService/ListVariables" {
			t.Errorf("expected path /depot.ci.v1.VariableService/ListVariables, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := CIVariableListResponse{
			Variables: []CIVariableMeta{
				{Name: "VAR1", Value: "value1", CreatedAt: "2024-01-01T00:00:00Z"},
				{Name: "VAR2", Value: "value2", CreatedAt: "2024-01-02T00:00:00Z"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
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

	if variables[1].Name != "VAR2" {
		t.Errorf("expected second variable name VAR2, got %s", variables[1].Name)
	}
}

func TestCIDeleteVariable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		if r.URL.Path != "/depot.ci.v1.VariableService/DeleteVariable" {
			t.Errorf("expected path /depot.ci.v1.VariableService/DeleteVariable, got %s", r.URL.Path)
		}

		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req["name"] != "MY_VAR" {
			t.Errorf("expected name MY_VAR, got %s", req["name"])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIDeleteVariable(context.Background(), "test-token", "test-org", "MY_VAR")
	if err != nil {
		t.Fatalf("CIDeleteVariable failed: %v", err)
	}
}
