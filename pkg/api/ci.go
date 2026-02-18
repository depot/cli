package api

import (
	"context"
	"time"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
)

var baseURLFunc = getBaseURL

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

// CIListVariables lists all CI variables for an organization
func CIListVariables(ctx context.Context, token, orgID string) ([]CIVariableMeta, error) {
	client := newCIVariableServiceClient()
	resp, err := client.ListVariables(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.ListVariablesRequest{}), token, orgID))
	if err != nil {
		return nil, err
	}
	variables := make([]CIVariableMeta, 0, len(resp.Msg.Variables))
	for _, v := range resp.Msg.Variables {
		meta := CIVariableMeta{
			Name: v.Name,
		}
		if v.Description != nil {
			meta.Description = *v.Description
		}
		if v.LastModified != nil {
			meta.CreatedAt = v.LastModified.AsTime().Format(time.RFC3339)
		}
		variables = append(variables, meta)
	}
	return variables, nil
}

// CIDeleteVariable deletes a CI variable from an organization
func CIDeleteVariable(ctx context.Context, token, orgID, name string) error {
	client := newCIVariableServiceClient()
	_, err := client.RemoveVariable(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.RemoveVariableRequest{Name: name}), token, orgID))
	return err
}
