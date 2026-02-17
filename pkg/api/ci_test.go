package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCIAddSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		if r.URL.Path != "/depot.ci.v1.SecretService/AddSecret" {
			t.Errorf("expected path /depot.ci.v1.SecretService/AddSecret, got %s", r.URL.Path)
		}

		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected Authorization header, got %s", r.Header.Get("Authorization"))
		}

		if r.Header.Get("x-depot-org") != "test-org" {
			t.Errorf("expected x-depot-org header, got %s", r.Header.Get("x-depot-org"))
		}

		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}

		if r.Header.Get("User-Agent") == "" {
			t.Error("expected User-Agent header")
		}

		var req CISecretAddRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Name != "MY_SECRET" || req.Value != "secret-value" {
			t.Errorf("expected name=MY_SECRET value=secret-value, got name=%s value=%s", req.Name, req.Value)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIAddSecret(context.Background(), "test-token", "test-org", "MY_SECRET", "secret-value")
	if err != nil {
		t.Fatalf("CIAddSecret failed: %v", err)
	}
}

func TestCIListSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		if r.URL.Path != "/depot.ci.v1.SecretService/ListSecrets" {
			t.Errorf("expected path /depot.ci.v1.SecretService/ListSecrets, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := CISecretListResponse{
			Secrets: []CISecretMeta{
				{Name: "SECRET1", Description: "First secret", CreatedAt: "2024-01-01T00:00:00Z"},
				{Name: "SECRET2", Description: "Second secret", CreatedAt: "2024-01-02T00:00:00Z"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
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

	if secrets[1].Name != "SECRET2" {
		t.Errorf("expected second secret name SECRET2, got %s", secrets[1].Name)
	}
}

func TestCIDeleteSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		if r.URL.Path != "/depot.ci.v1.SecretService/DeleteSecret" {
			t.Errorf("expected path /depot.ci.v1.SecretService/DeleteSecret, got %s", r.URL.Path)
		}

		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req["name"] != "MY_SECRET" {
			t.Errorf("expected name MY_SECRET, got %s", req["name"])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIDeleteSecret(context.Background(), "test-token", "test-org", "MY_SECRET")
	if err != nil {
		t.Fatalf("CIDeleteSecret failed: %v", err)
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

func TestCIErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{
			OK:    false,
			Error: "invalid request",
		})
	}))
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	err := CIAddSecret(context.Background(), "test-token", "test-org", "SECRET", "value")
	if err == nil {
		t.Error("expected error for non-200 response")
	}
}

func TestCIDepotOrgHeader(t *testing.T) {
	headerReceived := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerReceived = r.Header.Get("x-depot-org")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(CISecretListResponse{Secrets: []CISecretMeta{}})
	}))
	defer server.Close()

	oldBaseURLFunc := baseURLFunc
	baseURLFunc = func() string { return server.URL }
	defer func() { baseURLFunc = oldBaseURLFunc }()

	CIListSecrets(context.Background(), "test-token", "my-org-id")
	if headerReceived != "my-org-id" {
		t.Errorf("expected x-depot-org header to be my-org-id, got %s", headerReceived)
	}
}
