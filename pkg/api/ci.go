package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Add("x-depot-org", orgID)
	req.Header.Add("User-Agent", Agent())

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp ErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && !errResp.OK {
			return nil, fmt.Errorf("%s", errResp.Error)
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

// CIAddSecret adds a single CI secret to an organization
func CIAddSecret(ctx context.Context, token, orgID, name, value string) error {
	return CIAddSecretWithDescription(ctx, token, orgID, name, value, "")
}

// CIAddSecretWithDescription adds a single CI secret to an organization, with an optional description.
func CIAddSecretWithDescription(ctx context.Context, token, orgID, name, value, description string) error {
	payload := CISecretAddRequest{
		Name:        name,
		Value:       value,
		Description: description,
	}
	_, err := ciRequest[interface{}](ctx, token, orgID, "depot.ci.v1.SecretService/AddSecret", payload)
	return err
}

// CIListSecrets lists all CI secrets for an organization
func CIListSecrets(ctx context.Context, token, orgID string) ([]CISecretMeta, error) {
	resp, err := ciRequest[CISecretListResponse](ctx, token, orgID, "depot.ci.v1.SecretService/ListSecrets", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []CISecretMeta{}, nil
	}
	return resp.Secrets, nil
}

// CIDeleteSecret deletes a CI secret from an organization
func CIDeleteSecret(ctx context.Context, token, orgID, name string) error {
	payload := map[string]string{
		"name": name,
	}
	_, err := ciRequest[interface{}](ctx, token, orgID, "depot.ci.v1.SecretService/DeleteSecret", payload)
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
