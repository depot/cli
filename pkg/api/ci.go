package api

import (
	"context"
	"time"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
)

var baseURLFunc = getBaseURL

func newCIServiceClient() civ1connect.CIServiceClient {
	baseURL := baseURLFunc()
	return civ1connect.NewCIServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

// CIGetRunStatus returns the current status of a CI run including its workflows, jobs, and attempts.
func CIGetRunStatus(ctx context.Context, token, runID string) (*civ1.GetRunStatusResponse, error) {
	client := newCIServiceClient()
	resp, err := client.GetRunStatus(ctx, WithAuthentication(connect.NewRequest(&civ1.GetRunStatusRequest{RunId: runID}), token))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CIGetJobAttemptLogs returns all log lines for a job attempt, paginating through all pages.
func CIGetJobAttemptLogs(ctx context.Context, token, attemptID string) ([]*civ1.LogLine, error) {
	client := newCIServiceClient()
	var allLines []*civ1.LogLine
	var pageToken string

	for {
		req := &civ1.GetJobAttemptLogsRequest{AttemptId: attemptID, PageToken: pageToken}
		resp, err := client.GetJobAttemptLogs(ctx, WithAuthentication(connect.NewRequest(req), token))
		if err != nil {
			return nil, err
		}
		allLines = append(allLines, resp.Msg.Lines...)
		if resp.Msg.NextPageToken == "" {
			break
		}
		pageToken = resp.Msg.NextPageToken
	}

	return allLines, nil
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

// CISecret contains metadata about a CI secret
type CISecret struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

// CIListSecrets lists all CI secrets for an organization
func CIListSecrets(ctx context.Context, token, orgID string) ([]CISecret, error) {
	client := newCISecretServiceClient()
	resp, err := client.ListSecrets(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.ListSecretsRequest{}), token, orgID))
	if err != nil {
		return nil, err
	}
	secrets := make([]CISecret, 0, len(resp.Msg.Secrets))
	for _, s := range resp.Msg.Secrets {
		cs := CISecret{
			Name: s.Name,
		}
		if s.Description != nil {
			cs.Description = *s.Description
		}
		if s.LastModified != nil {
			cs.CreatedAt = s.LastModified.AsTime().Format(time.RFC3339)
		}
		secrets = append(secrets, cs)
	}
	return secrets, nil
}

// CIDeleteSecret deletes a CI secret from an organization
func CIDeleteSecret(ctx context.Context, token, orgID, name string) error {
	client := newCISecretServiceClient()
	_, err := client.RemoveSecret(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.RemoveSecretRequest{Name: name}), token, orgID))
	return err
}

func newCIVariableServiceClient() civ1connect.VariableServiceClient {
	baseURL := baseURLFunc()
	return civ1connect.NewVariableServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

// CIAddVariable adds a single CI variable to an organization
func CIAddVariable(ctx context.Context, token, orgID, name, value string) error {
	client := newCIVariableServiceClient()
	_, err := client.AddVariable(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.AddVariableRequest{
		Name:  name,
		Value: value,
	}), token, orgID))
	return err
}

// CIVariable contains metadata about a CI variable
type CIVariable struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

// CIListVariables lists all CI variables for an organization
func CIListVariables(ctx context.Context, token, orgID string) ([]CIVariable, error) {
	client := newCIVariableServiceClient()
	resp, err := client.ListVariables(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.ListVariablesRequest{}), token, orgID))
	if err != nil {
		return nil, err
	}
	variables := make([]CIVariable, 0, len(resp.Msg.Variables))
	for _, v := range resp.Msg.Variables {
		cv := CIVariable{
			Name: v.Name,
		}
		if v.Description != nil {
			cv.Description = *v.Description
		}
		if v.LastModified != nil {
			cv.CreatedAt = v.LastModified.AsTime().Format(time.RFC3339)
		}
		variables = append(variables, cv)
	}
	return variables, nil
}

// CIDeleteVariable deletes a CI variable from an organization
func CIDeleteVariable(ctx context.Context, token, orgID, name string) error {
	client := newCIVariableServiceClient()
	_, err := client.RemoveVariable(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.RemoveVariableRequest{Name: name}), token, orgID))
	return err
}
