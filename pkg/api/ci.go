package api

import (
	"context"
	"time"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
	"github.com/depot/cli/pkg/proto/depot/ci/v2/civ2connect"
)

var baseURLFunc = getBaseURL

func newCIServiceClient() civ1connect.CIServiceClient {
	baseURL := baseURLFunc()
	return civ1connect.NewCIServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

// CIGetRunStatus returns the current status of a CI run including its workflows, jobs, and attempts.
func CIGetRunStatus(ctx context.Context, token, orgID, runID string) (*civ1.GetRunStatusResponse, error) {
	client := newCIServiceClient()
	resp, err := client.GetRunStatus(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.GetRunStatusRequest{RunId: runID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CIGetRun returns a flat CI run record.
func CIGetRun(ctx context.Context, token, orgID, runID string) (*civ1.GetRunResponse, error) {
	client := newCIServiceClient()
	resp, err := client.GetRun(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.GetRunRequest{RunId: runID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CIGetJobAttemptLogs returns all log lines for a job attempt, paginating through all pages.
func CIGetJobAttemptLogs(ctx context.Context, token, orgID, attemptID string) ([]*civ1.LogLine, error) {
	client := newCIServiceClient()
	var allLines []*civ1.LogLine
	var pageToken string

	for {
		req := &civ1.GetJobAttemptLogsRequest{AttemptId: attemptID, PageToken: pageToken}
		resp, err := client.GetJobAttemptLogs(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
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

// CIRun triggers a CI run.
func CIRun(ctx context.Context, token, orgID string, req *civ1.RunRequest) (*civ1.RunResponse, error) {
	client := newCIServiceClient()
	resp, err := client.Run(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CIDispatchWorkflow triggers a single workflow via workflow_dispatch.
func CIDispatchWorkflow(ctx context.Context, token, orgID string, req *civ1.DispatchWorkflowRequest) (*civ1.DispatchWorkflowResponse, error) {
	client := newCIServiceClient()
	resp, err := client.DispatchWorkflow(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CICancelRun cancels a queued or running CI run and all active child work.
func CICancelRun(ctx context.Context, token, orgID, runID string) (*civ1.CancelRunResponse, error) {
	client := newCIServiceClient()
	resp, err := client.CancelRun(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.CancelRunRequest{RunId: runID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CICancelWorkflow cancels a queued or running workflow and all its child jobs.
func CICancelWorkflow(ctx context.Context, token, orgID, workflowID string) (*civ1.CancelWorkflowResponse, error) {
	client := newCIServiceClient()
	resp, err := client.CancelWorkflow(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.CancelWorkflowRequest{WorkflowId: workflowID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CICancelJob cancels a queued or running job within a workflow.
func CICancelJob(ctx context.Context, token, orgID, workflowID, jobID string) (*civ1.CancelJobResponse, error) {
	client := newCIServiceClient()
	resp, err := client.CancelJob(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.CancelJobRequest{WorkflowId: workflowID, JobId: jobID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CIRerunWorkflow resets and re-runs all jobs in a finished workflow.
func CIRerunWorkflow(ctx context.Context, token, orgID, workflowID string) (*civ1.RerunWorkflowResponse, error) {
	client := newCIServiceClient()
	resp, err := client.RerunWorkflow(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.RerunWorkflowRequest{WorkflowId: workflowID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CIRetryFailedJobs retries only failed/cancelled jobs in a finished workflow.
func CIRetryFailedJobs(ctx context.Context, token, orgID, workflowID string) (*civ1.RetryFailedJobsResponse, error) {
	client := newCIServiceClient()
	resp, err := client.RetryFailedJobs(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.RetryFailedJobsRequest{WorkflowId: workflowID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CIRetryJob retries a single failed job within a workflow.
func CIRetryJob(ctx context.Context, token, orgID, workflowID, jobID string) (*civ1.RetryJobResponse, error) {
	client := newCIServiceClient()
	resp, err := client.RetryJob(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.RetryJobRequest{WorkflowId: workflowID, JobId: jobID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

type CIListRunsOptions struct {
	Statuses []string
	Limit    int32
	Repo     string
	Sha      string
}

// CIListRuns returns CI runs, paginating as needed to collect up to `Limit` results.
// If Limit is 0, all results are returned.
func CIListRuns(ctx context.Context, token, orgID string, options CIListRunsOptions) ([]*civ1.ListRunsResponseRun, error) {
	client := newCIServiceClient()
	var allRuns []*civ1.ListRunsResponseRun
	var pageToken string

	for {
		pageSize := options.Limit
		if options.Limit > 0 {
			remaining := options.Limit - int32(len(allRuns))
			if remaining <= 0 {
				break
			}
			pageSize = remaining
		}

		req := &civ1.ListRunsRequest{
			Status:    options.Statuses,
			PageSize:  pageSize,
			PageToken: pageToken,
			Repo:      options.Repo,
			Sha:       options.Sha,
		}
		resp, err := client.ListRuns(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
		if err != nil {
			return nil, err
		}

		allRuns = append(allRuns, resp.Msg.Runs...)

		if options.Limit > 0 && int32(len(allRuns)) >= options.Limit {
			allRuns = allRuns[:options.Limit]
			break
		}

		if resp.Msg.NextPageToken == "" {
			break
		}
		pageToken = resp.Msg.NextPageToken
	}

	return allRuns, nil
}

type CIListWorkflowsOptions struct {
	Limit       int32
	Name        string
	Repo        string
	Statuses    []string
	Trigger     string
	Sha         string
	PullRequest string
}

// CIListWorkflows returns one newest-first page of recent CI workflows.
// If Limit is 0, the API default is used.
func CIListWorkflows(ctx context.Context, token, orgID string, options CIListWorkflowsOptions) ([]*civ1.ListWorkflowsResponseWorkflow, error) {
	client := newCIServiceClient()
	req := &civ1.ListWorkflowsRequest{
		PageSize: options.Limit,
		Name:     options.Name,
		Repo:     options.Repo,
		Status:   options.Statuses,
		Trigger:  options.Trigger,
		Sha:      options.Sha,
		Pr:       options.PullRequest,
	}
	resp, err := client.ListWorkflows(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, err
	}

	return resp.Msg.Workflows, nil
}

func newCISecretServiceV2Client() civ2connect.SecretServiceClient {
	baseURL := baseURLFunc()
	return civ2connect.NewSecretServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

// CIAddSecret adds a single CI secret to an organization (org-wide).
func CIAddSecret(ctx context.Context, token, orgID, name, value string) error {
	return CIAddSecretWithDescription(ctx, token, orgID, name, value, "", "")
}

// CIAddSecretWithDescription adds a CI secret, optionally scoped to a repo.
func CIAddSecretWithDescription(ctx context.Context, token, orgID, name, value, description, repo string) error {
	client := newCISecretServiceV2Client()
	if repo != "" {
		req := &civ2.AddRepoSecretRequest{Repo: repo, Name: name, Value: value}
		if description != "" {
			req.Description = &description
		}
		_, err := client.AddRepoSecret(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
		return err
	}
	req := &civ2.AddOrgSecretRequest{Name: name, Value: value}
	if description != "" {
		req.Description = &description
	}
	_, err := client.AddOrgSecret(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	return err
}

// CISecret contains metadata about a CI secret.
type CISecret struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	Scope       string `json:"scope"`
}

func secretFromProto(s *civ2.Secret, scope string) CISecret {
	cs := CISecret{Name: s.Name, Scope: scope}
	if s.Description != nil {
		cs.Description = *s.Description
	}
	if s.LastModified != nil {
		cs.CreatedAt = s.LastModified.AsTime().Format(time.RFC3339)
	}
	return cs
}

// CIListSecrets lists CI secrets. When repo is non-empty it returns both
// org-wide and repo-specific secrets so the caller can see effective state.
func CIListSecrets(ctx context.Context, token, orgID, repo string) ([]CISecret, error) {
	client := newCISecretServiceV2Client()

	orgResp, err := client.ListOrgSecrets(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.ListOrgSecretsRequest{}), token, orgID))
	if err != nil {
		return nil, err
	}

	secrets := make([]CISecret, 0, len(orgResp.Msg.Secrets))
	for _, s := range orgResp.Msg.Secrets {
		secrets = append(secrets, secretFromProto(s, "org"))
	}

	if repo != "" {
		repoResp, err := client.ListRepoSecrets(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.ListRepoSecretsRequest{Repo: repo}), token, orgID))
		if err != nil {
			return nil, err
		}
		for _, s := range repoResp.Msg.Secrets {
			secrets = append(secrets, secretFromProto(s, repo))
		}
	}

	return secrets, nil
}

// CIBatchAddSecrets adds multiple CI secrets in a single request, optionally scoped to a repo.
func CIBatchAddSecrets(ctx context.Context, token, orgID string, secrets []*civ2.SecretInput, repo string) error {
	client := newCISecretServiceV2Client()
	if repo != "" {
		_, err := client.BatchAddRepoSecrets(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.BatchAddRepoSecretsRequest{Repo: repo, Secrets: secrets}), token, orgID))
		return err
	}
	_, err := client.BatchAddOrgSecrets(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.BatchAddOrgSecretsRequest{Secrets: secrets}), token, orgID))
	return err
}

// CIDeleteSecret deletes a CI secret, optionally scoped to a repo.
func CIDeleteSecret(ctx context.Context, token, orgID, name, repo string) error {
	client := newCISecretServiceV2Client()
	if repo != "" {
		_, err := client.RemoveRepoSecret(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.RemoveRepoSecretRequest{Repo: repo, Name: name}), token, orgID))
		return err
	}
	_, err := client.RemoveOrgSecret(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.RemoveOrgSecretRequest{Name: name}), token, orgID))
	return err
}

func newCIVariableServiceV2Client() civ2connect.VariableServiceClient {
	baseURL := baseURLFunc()
	return civ2connect.NewVariableServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

// CIAddVariable adds a CI variable, optionally scoped to a repo.
func CIAddVariable(ctx context.Context, token, orgID, name, value, repo string) error {
	client := newCIVariableServiceV2Client()
	if repo != "" {
		_, err := client.AddRepoVariable(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.AddRepoVariableRequest{Repo: repo, Name: name, Value: value}), token, orgID))
		return err
	}
	_, err := client.AddOrgVariable(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.AddOrgVariableRequest{Name: name, Value: value}), token, orgID))
	return err
}

// CIVariable contains metadata about a CI variable.
type CIVariable struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	Scope       string `json:"scope"`
}

func variableFromProto(v *civ2.Variable, scope string) CIVariable {
	cv := CIVariable{Name: v.Name, Scope: scope}
	if v.Description != nil {
		cv.Description = *v.Description
	}
	if v.LastModified != nil {
		cv.CreatedAt = v.LastModified.AsTime().Format(time.RFC3339)
	}
	return cv
}

// CIListVariables lists CI variables. When repo is non-empty it returns both
// org-wide and repo-specific variables.
func CIListVariables(ctx context.Context, token, orgID, repo string) ([]CIVariable, error) {
	client := newCIVariableServiceV2Client()

	orgResp, err := client.ListOrgVariables(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.ListOrgVariablesRequest{}), token, orgID))
	if err != nil {
		return nil, err
	}

	variables := make([]CIVariable, 0, len(orgResp.Msg.Variables))
	for _, v := range orgResp.Msg.Variables {
		variables = append(variables, variableFromProto(v, "org"))
	}

	if repo != "" {
		repoResp, err := client.ListRepoVariables(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.ListRepoVariablesRequest{Repo: repo}), token, orgID))
		if err != nil {
			return nil, err
		}
		for _, v := range repoResp.Msg.Variables {
			variables = append(variables, variableFromProto(v, repo))
		}
	}

	return variables, nil
}

// CIBatchAddVariables adds multiple CI variables in a single request, optionally scoped to a repo.
func CIBatchAddVariables(ctx context.Context, token, orgID string, variables []*civ2.VariableInput, repo string) error {
	client := newCIVariableServiceV2Client()
	if repo != "" {
		_, err := client.BatchAddRepoVariables(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.BatchAddRepoVariablesRequest{Repo: repo, Variables: variables}), token, orgID))
		return err
	}
	_, err := client.BatchAddOrgVariables(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.BatchAddOrgVariablesRequest{Variables: variables}), token, orgID))
	return err
}

// CIDeleteVariable deletes a CI variable, optionally scoped to a repo.
func CIDeleteVariable(ctx context.Context, token, orgID, name, repo string) error {
	client := newCIVariableServiceV2Client()
	if repo != "" {
		_, err := client.RemoveRepoVariable(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.RemoveRepoVariableRequest{Repo: repo, Name: name}), token, orgID))
		return err
	}
	_, err := client.RemoveOrgVariable(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ2.RemoveOrgVariableRequest{Name: name}), token, orgID))
	return err
}
