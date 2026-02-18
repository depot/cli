package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
)

var baseURLFunc = getBaseURL

func ciRequest[T any](ctx context.Context, token, orgID, path string, payload interface{}) (*T, error) {
	var requestBody io.Reader

	if payload != nil {
		jsonBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		requestBody = bytes.NewReader(jsonBytes)
	}

	url := baseURLFunc() + "/" + path
	client := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "POST", url, requestBody)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", "application/json")
	if token != "" {
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	}
	if orgID != "" {
		req.Header.Add("x-depot-org", orgID)
	}
	req.Header.Add("User-Agent", Agent())
	req.Header.Add("Depot-User-Agent", Agent())

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	infoMessage := resp.Header.Get("X-Depot-Info-Message")
	if infoMessage != "" {
		fmt.Println(infoStyle.Render(infoMessage))
	}

	warnMessage := resp.Header.Get("X-Depot-Warn-Message")
	if warnMessage != "" {
		fmt.Println(warnStyle.Render(warnMessage))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp ErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && strings.TrimSpace(errResp.Error) != "" {
			return nil, fmt.Errorf("%s", errResp.Error)
		}

		var connectErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &connectErr); err == nil && strings.TrimSpace(connectErr.Message) != "" {
			return nil, fmt.Errorf("%s", connectErr.Message)
		}

		return nil, fmt.Errorf("API error: status %d", resp.StatusCode)
	}

	var response T
	if len(bytes.TrimSpace(body)) == 0 {
		return &response, nil
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func newCISecretServiceClient() civ1connect.SecretServiceClient {
	baseURL := baseURLFunc()
	return civ1connect.NewSecretServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

// CIAddSecret adds a single CI secret to an organization
func CIAddSecret(ctx context.Context, token, orgID, name, value string) error {
	return CIAddSecretWithDescription(ctx, token, orgID, name, value, "")
}

// CIAddSecretWithDescription adds a single CI secret to an organization, with an optional description.
func CIAddSecretWithDescription(ctx context.Context, token, orgID, name, value, description string) error {
	client := newCISecretServiceClient()
	req := &civ1.AddSecretRequest{
		Name:  name,
		Value: value,
	}
	if description != "" {
		req.Description = &description
	}
	_, err := client.AddSecret(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	return err
}

// CIListSecrets lists all CI secrets for an organization
func CIListSecrets(ctx context.Context, token, orgID string) ([]CISecretMeta, error) {
	client := newCISecretServiceClient()
	resp, err := client.ListSecrets(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.ListSecretsRequest{}), token, orgID))
	if err != nil {
		return nil, err
	}
	secrets := make([]CISecretMeta, 0, len(resp.Msg.Secrets))
	for _, s := range resp.Msg.Secrets {
		meta := CISecretMeta{
			Name: s.Name,
		}
		if s.Description != nil {
			meta.Description = *s.Description
		}
		if s.LastModified != nil {
			meta.CreatedAt = s.LastModified.AsTime().Format(time.RFC3339)
		}
		secrets = append(secrets, meta)
	}
	return secrets, nil
}

// CIDeleteSecret deletes a CI secret from an organization
func CIDeleteSecret(ctx context.Context, token, orgID, name string) error {
	client := newCISecretServiceClient()
	_, err := client.RemoveSecret(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.RemoveSecretRequest{Name: name}), token, orgID))
	return err
}

// CIAddVariable adds a single CI variable to an organization
func CIAddVariable(ctx context.Context, token, orgID, name, value string) error {
	payload := CIVariableAddRequest{
		Name:  name,
		Value: value,
	}
	_, err := ciRequest[interface{}](ctx, token, orgID, "depot.ci.v1.VariableService/AddVariable", payload)
	return err
}

// CIListVariables lists all CI variables for an organization
func CIListVariables(ctx context.Context, token, orgID string) ([]CIVariableMeta, error) {
	resp, err := ciRequest[CIVariableListResponse](ctx, token, orgID, "depot.ci.v1.VariableService/ListVariables", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []CIVariableMeta{}, nil
	}
	return resp.Variables, nil
}

// CIDeleteVariable deletes a CI variable from an organization
func CIDeleteVariable(ctx context.Context, token, orgID, name string) error {
	payload := map[string]string{
		"name": name,
	}
	_, err := ciRequest[interface{}](ctx, token, orgID, "depot.ci.v1.VariableService/DeleteVariable", payload)
	return err
}
