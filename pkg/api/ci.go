package api

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/depot/cli/pkg/proto/depot/ci/v1/civ1connect"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
	"github.com/depot/cli/pkg/proto/depot/ci/v2/civ2connect"
	civ3beta2 "github.com/depot/cli/pkg/proto/depot/ci/v3beta2"
	"github.com/depot/cli/pkg/proto/depot/ci/v3beta2/civ3beta2connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var baseURLFunc = getBaseURL

const ciStreamLogDedupeSize = 4096

const (
	ciDefaultVariantName = "default"
	ciDefaultPage        = 1
	ciMaxPageSize        = 100
)

var (
	ciStreamInitialBackoff = 250 * time.Millisecond
	ciStreamMaxBackoff     = 30 * time.Second
	ciStreamSleep          = sleepWithContext
)

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

// CIGetWorkflow returns curated run/workflow/execution/job/attempt metadata for a single workflow.
func CIGetWorkflow(ctx context.Context, token, orgID, workflowID string) (*civ1.GetWorkflowResponse, error) {
	client := newCIServiceClient()
	resp, err := client.GetWorkflow(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.GetWorkflowRequest{WorkflowId: workflowID}), token, orgID))
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

// CIGetJobAttemptMetrics returns CPU and memory samples for a CI job attempt.
func CIGetJobAttemptMetrics(ctx context.Context, token, orgID, attemptID string) (*civ1.GetJobAttemptMetricsResponse, error) {
	client := newCIServiceClient()
	resp, err := client.GetJobAttemptMetrics(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.GetJobAttemptMetricsRequest{AttemptId: attemptID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CIGetJobMetrics returns per-attempt CPU and memory metric summaries for a CI job.
func CIGetJobMetrics(ctx context.Context, token, orgID, jobID string) (*civ1.GetJobMetricsResponse, error) {
	client := newCIServiceClient()
	resp, err := client.GetJobMetrics(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.GetJobMetricsRequest{JobId: jobID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CIGetRunMetrics returns workflow/job/attempt CPU and memory metric summaries for a CI run.
func CIGetRunMetrics(ctx context.Context, token, orgID, runID string) (*civ1.GetRunMetricsResponse, error) {
	client := newCIServiceClient()
	resp, err := client.GetRunMetrics(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ1.GetRunMetricsRequest{RunId: runID}), token, orgID))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// CIGetJobSummary returns authored step summary markdown for a job, a concrete attempt, or both.
func CIGetJobSummary(ctx context.Context, token, orgID string, req *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error) {
	client := newCIServiceClient()
	resp, err := client.GetJobSummary(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
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

type CILogStreamTarget struct {
	AttemptID string
	JobID     string
}

// CIExportJobAttemptLogs streams a finite exported log snapshot for a job
// attempt or the latest attempt of a job into w. The returned metadata is
// advisory; callers choose their own destination path.
func CIExportJobAttemptLogs(ctx context.Context, token, orgID string, target CILogStreamTarget, format civ1.JobAttemptLogExportFormat, w io.Writer) (*civ1.JobAttemptLogExportMetadata, error) {
	if target.AttemptID == "" && target.JobID == "" {
		return nil, fmt.Errorf("exactly one of attempt ID or job ID is required")
	}
	if target.AttemptID != "" && target.JobID != "" {
		return nil, fmt.Errorf("exactly one of attempt ID or job ID is required")
	}

	client := newCIServiceClient()
	req := &civ1.ExportJobAttemptLogsRequest{AttemptId: target.AttemptID, JobId: target.JobID, Format: format}
	stream, err := client.ExportJobAttemptLogs(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	var metadata *civ1.JobAttemptLogExportMetadata
	for stream.Receive() {
		msg := stream.Msg()
		if nextMetadata := msg.GetMetadata(); nextMetadata != nil {
			if metadata != nil {
				return nil, fmt.Errorf("log export stream sent metadata more than once")
			}
			metadata = nextMetadata
			continue
		}

		chunk := msg.GetChunk()
		if len(chunk) == 0 {
			continue
		}
		if metadata == nil {
			return nil, fmt.Errorf("log export stream sent chunk before metadata")
		}
		if _, err := w.Write(chunk); err != nil {
			return nil, err
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, fmt.Errorf("log export stream ended without metadata")
	}
	return metadata, nil
}

// CIStreamJobAttemptLogs streams log lines for a job attempt or the latest
// attempt of a job, resuming from the last cursor after transient stream errors.
// If onStatus is non-nil, it receives attempt status updates from the stream.
func CIStreamJobAttemptLogs(ctx context.Context, token, orgID string, target CILogStreamTarget, w io.Writer, onStatus func(string)) error {
	var onStatusWithError func(string) error
	if onStatus != nil {
		onStatusWithError = func(status string) error {
			onStatus(status)
			return nil
		}
	}
	return CIStreamJobAttemptLogLines(ctx, token, orgID, target, func(line *civ1.LogLine) error {
		return writeLogLine(w, line)
	}, onStatusWithError)
}

// CIStreamJobAttemptLogLines streams log line metadata for a job attempt or the
// latest attempt of a job, resuming from the last cursor after transient stream
// errors. Duplicate replayed lines are suppressed before onLine is called.
func CIStreamJobAttemptLogLines(ctx context.Context, token, orgID string, target CILogStreamTarget, onLine func(*civ1.LogLine) error, onStatus func(string) error) error {
	if target.AttemptID == "" && target.JobID == "" {
		return fmt.Errorf("exactly one of attempt ID or job ID is required")
	}
	if target.AttemptID != "" && target.JobID != "" {
		return fmt.Errorf("exactly one of attempt ID or job ID is required")
	}

	client := newCIServiceClient()
	cursor := ""
	backoff := ciStreamInitialBackoff
	seen := newLogLineDedupe(ciStreamLogDedupeSize)

	for {
		req := &civ1.StreamJobAttemptLogsRequest{AttemptId: target.AttemptID, JobId: target.JobID, Cursor: cursor}
		stream, err := client.StreamJobAttemptLogs(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
		if err != nil {
			if !isTransientConnectError(err) {
				return err
			}
			if err := ciStreamSleep(ctx, backoff); err != nil {
				return err
			}
			backoff = nextCIStreamBackoff(backoff)
			continue
		}

		for stream.Receive() {
			msg := stream.Msg()
			backoff = ciStreamInitialBackoff
			if status := msg.GetAttemptStatus(); status != "" && onStatus != nil {
				if err := onStatus(status); err != nil {
					stream.Close()
					return err
				}
			}

			line := msg.GetLine()
			if line == nil {
				continue
			}

			identity := logLineIdentity(line)
			if !seen.Contains(identity) {
				if onLine != nil {
					if err := onLine(line); err != nil {
						stream.Close()
						return err
					}
				}
				seen.Add(identity)
				if msg.GetNextCursor() != "" {
					cursor = msg.GetNextCursor()
				}
			}
		}

		err = stream.Err()
		stream.Close()
		if err == nil {
			return nil
		}
		if !isTransientConnectError(err) {
			return err
		}
		if err := ciStreamSleep(ctx, backoff); err != nil {
			return err
		}
		backoff = nextCIStreamBackoff(backoff)
	}
}

func writeLogLine(w io.Writer, line *civ1.LogLine) error {
	text := line.GetBody() + "\n"
	n, err := io.WriteString(w, text)
	if err != nil {
		return err
	}
	if n != len(text) {
		return io.ErrShortWrite
	}
	return nil
}

func isTransientConnectError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeAborted:
		return true
	default:
		return false
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextCIStreamBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > ciStreamMaxBackoff {
		return ciStreamMaxBackoff
	}
	return next
}

func logLineIdentity(line *civ1.LogLine) string {
	sum := sha256.Sum256([]byte(line.GetBody()))
	return fmt.Sprintf("%s:%d:%d:%d:%s", line.GetStepKey(), line.GetTimestampMs(), line.GetLineNumber(), line.GetStream(), hex.EncodeToString(sum[:]))
}

type logLineDedupe struct {
	capacity int
	entries  map[string]*list.Element
	order    *list.List
}

func newLogLineDedupe(capacity int) *logLineDedupe {
	return &logLineDedupe{
		capacity: capacity,
		entries:  make(map[string]*list.Element, capacity),
		order:    list.New(),
	}
}

func (d *logLineDedupe) Contains(key string) bool {
	if elem, ok := d.entries[key]; ok {
		d.order.MoveToFront(elem)
		return true
	}
	return false
}

func (d *logLineDedupe) Add(key string) {
	if elem, ok := d.entries[key]; ok {
		d.order.MoveToFront(elem)
		return
	}
	elem := d.order.PushFront(key)
	d.entries[key] = elem
	if d.order.Len() <= d.capacity {
		return
	}
	oldest := d.order.Back()
	if oldest == nil {
		return
	}
	d.order.Remove(oldest)
	delete(d.entries, oldest.Value.(string))
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
	Statuses    []string
	Limit       int32
	Repo        string
	Sha         string
	Trigger     string
	PullRequest string
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
			Trigger:   options.Trigger,
			Pr:        options.PullRequest,
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

func newCISecretServiceV3Beta2Client() civ3beta2connect.SecretServiceClient {
	baseURL := baseURLFunc()
	return civ3beta2connect.NewSecretServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

// CIVariantAttribute describes a condition that controls where a secret or variable variant applies.
type CIVariantAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CISecretGroup contains a logical CI secret and its variants.
type CISecretGroup struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Variants     []CISecretVariant `json:"variants"`
	VariantCount uint32            `json:"variantCount"`
	LastModified string            `json:"lastModified,omitempty"`
}

// CISecretVariant contains metadata for one named CI secret variant. Secret values are never returned.
type CISecretVariant struct {
	ID              string               `json:"id"`
	SecretID        string               `json:"secretId"`
	Name            string               `json:"name"`
	Description     string               `json:"description,omitempty"`
	Attributes      []CIVariantAttribute `json:"attributes,omitempty"`
	LastModified    string               `json:"lastModified,omitempty"`
	ValueGroupIndex *uint32              `json:"valueGroupIndex,omitempty"`
}

type CIListSecretVariantsOptions struct {
	Query       string
	Repo        []string
	Environment []string
	Branch      []string
	Workflow    []string
}

type CIListSecretVariantsResult struct {
	Secrets []CISecretGroup `json:"secrets"`
}

type CISetSecretVariantOptions struct {
	Name        string
	Variant     string
	Value       string
	Description string
	Repo        []string
	Environment []string
	Branch      []string
	Workflow    []string
}

type CISetSecretVariantResult struct {
	Secret         CISecretGroup   `json:"secret"`
	Variant        CISecretVariant `json:"variant"`
	CreatedSecret  bool            `json:"createdSecret"`
	CreatedVariant bool            `json:"createdVariant"`
}

func CIListSecretVariants(ctx context.Context, token, orgID string, opts CIListSecretVariantsOptions) (CIListSecretVariantsResult, error) {
	client := newCISecretServiceV3Beta2Client()

	attrs := ciAttributes(opts.Repo, opts.Environment, opts.Branch, opts.Workflow)
	result := CIListSecretVariantsResult{Secrets: []CISecretGroup{}}
	for page := uint32(ciDefaultPage); ; page++ {
		resp, err := client.ListSecrets(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ3beta2.ListSecretsRequest{
			Page:       ciPageRequest(page),
			Query:      opts.Query,
			Attributes: attrs,
		}), token, orgID))
		if err != nil {
			return CIListSecretVariantsResult{}, err
		}

		result.Secrets = append(result.Secrets, secretGroupsFromProto(resp.Msg.GetSecrets())...)
		if !resp.Msg.GetPage().GetHasMore() {
			return result, nil
		}
	}
}

func CIGetSecretVariantGroup(ctx context.Context, token, orgID, name string) (CISecretGroup, error) {
	client := newCISecretServiceV3Beta2Client()
	resp, err := client.GetSecret(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ3beta2.GetSecretRequest{
		Lookup: &civ3beta2.GetSecretRequest_Name{Name: name},
	}), token, orgID))
	if err != nil {
		return CISecretGroup{}, err
	}
	return secretGroupFromProto(resp.Msg.GetSecret()), nil
}

func CIGetSecretVariant(ctx context.Context, token, orgID, variantID string) (CISecretVariant, error) {
	client := newCISecretServiceV3Beta2Client()
	resp, err := client.GetSecretVariant(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ3beta2.GetSecretVariantRequest{
		Id: variantID,
	}), token, orgID))
	if err != nil {
		return CISecretVariant{}, err
	}
	return secretVariantFromProto(resp.Msg.GetVariant()), nil
}

func CISetSecretVariant(ctx context.Context, token, orgID string, opts CISetSecretVariantOptions) (CISetSecretVariantResult, error) {
	client := newCISecretServiceV3Beta2Client()
	req := &civ3beta2.SetSecretVariantRequest{
		SecretName:  opts.Name,
		VariantName: ciVariantName(opts.Variant),
		Value:       opts.Value,
		Attributes:  ciAttributes(opts.Repo, opts.Environment, opts.Branch, opts.Workflow),
	}
	if opts.Description != "" {
		req.Description = &opts.Description
	}

	resp, err := client.SetSecretVariant(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return CISetSecretVariantResult{}, err
	}

	return CISetSecretVariantResult{
		Secret:         secretGroupFromProto(resp.Msg.GetSecret()),
		Variant:        secretVariantFromProto(resp.Msg.GetVariant()),
		CreatedSecret:  resp.Msg.GetCreatedSecret(),
		CreatedVariant: resp.Msg.GetCreatedVariant(),
	}, nil
}

func CIDeleteSecretVariant(ctx context.Context, token, orgID, variantID string) (bool, error) {
	client := newCISecretServiceV3Beta2Client()
	resp, err := client.DeleteSecretVariant(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ3beta2.DeleteSecretVariantRequest{VariantId: variantID}), token, orgID))
	if err != nil {
		return false, err
	}
	return resp.Msg.GetDeletedSecret(), nil
}

func CIDeleteSecretGroup(ctx context.Context, token, orgID, name string) error {
	client := newCISecretServiceV3Beta2Client()
	_, err := client.DeleteSecret(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ3beta2.DeleteSecretRequest{
		Lookup: &civ3beta2.DeleteSecretRequest_Name{Name: name},
	}), token, orgID))
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

func newCIVariableServiceV3Beta2Client() civ3beta2connect.VariableServiceClient {
	baseURL := baseURLFunc()
	return civ3beta2connect.NewVariableServiceClient(getHTTPClient(baseURL), baseURL, WithUserAgent())
}

// CIVariableGroup contains a logical CI variable and its variants.
type CIVariableGroup struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Variants     []CIVariableVariant `json:"variants"`
	VariantCount uint32              `json:"variantCount"`
	LastModified string              `json:"lastModified,omitempty"`
}

// CIVariableVariant contains metadata and value for one named CI variable variant.
type CIVariableVariant struct {
	ID           string               `json:"id"`
	VariableID   string               `json:"variableId"`
	Name         string               `json:"name"`
	Value        string               `json:"value"`
	Description  string               `json:"description,omitempty"`
	Attributes   []CIVariantAttribute `json:"attributes,omitempty"`
	LastModified string               `json:"lastModified,omitempty"`
}

type CIListVariableVariantsOptions struct {
	Query       string
	Repo        []string
	Environment []string
	Branch      []string
	Workflow    []string
}

type CIListVariableVariantsResult struct {
	Variables []CIVariableGroup `json:"variables"`
}

type CISetVariableVariantOptions struct {
	Name        string
	Variant     string
	Value       string
	Description string
	Repo        []string
	Environment []string
	Branch      []string
	Workflow    []string
}

type CISetVariableVariantResult struct {
	Variable        CIVariableGroup   `json:"variable"`
	Variant         CIVariableVariant `json:"variant"`
	CreatedVariable bool              `json:"createdVariable"`
	CreatedVariant  bool              `json:"createdVariant"`
}

func CIListVariableVariants(ctx context.Context, token, orgID string, opts CIListVariableVariantsOptions) (CIListVariableVariantsResult, error) {
	client := newCIVariableServiceV3Beta2Client()

	attrs := ciAttributes(opts.Repo, opts.Environment, opts.Branch, opts.Workflow)
	result := CIListVariableVariantsResult{Variables: []CIVariableGroup{}}
	for page := uint32(ciDefaultPage); ; page++ {
		resp, err := client.ListVariables(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ3beta2.ListVariablesRequest{
			Page:       ciPageRequest(page),
			Query:      opts.Query,
			Attributes: attrs,
		}), token, orgID))
		if err != nil {
			return CIListVariableVariantsResult{}, err
		}

		result.Variables = append(result.Variables, variableGroupsFromProto(resp.Msg.GetVariables())...)
		if !resp.Msg.GetPage().GetHasMore() {
			return result, nil
		}
	}
}

func CIGetVariableVariantGroup(ctx context.Context, token, orgID, name string) (CIVariableGroup, error) {
	client := newCIVariableServiceV3Beta2Client()
	resp, err := client.GetVariable(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ3beta2.GetVariableRequest{
		Lookup: &civ3beta2.GetVariableRequest_Name{Name: name},
	}), token, orgID))
	if err != nil {
		return CIVariableGroup{}, err
	}
	return variableGroupFromProto(resp.Msg.GetVariable()), nil
}

func CISetVariableVariant(ctx context.Context, token, orgID string, opts CISetVariableVariantOptions) (CISetVariableVariantResult, error) {
	client := newCIVariableServiceV3Beta2Client()
	req := &civ3beta2.SetVariableVariantRequest{
		VariableName: opts.Name,
		VariantName:  ciVariantName(opts.Variant),
		Value:        opts.Value,
		Attributes:   ciAttributes(opts.Repo, opts.Environment, opts.Branch, opts.Workflow),
	}
	if opts.Description != "" {
		req.Description = &opts.Description
	}

	resp, err := client.SetVariableVariant(ctx, WithAuthenticationAndOrg(connect.NewRequest(req), token, orgID))
	if err != nil {
		return CISetVariableVariantResult{}, err
	}

	return CISetVariableVariantResult{
		Variable:        variableGroupFromProto(resp.Msg.GetVariable()),
		Variant:         variableVariantFromProto(resp.Msg.GetVariant()),
		CreatedVariable: resp.Msg.GetCreatedVariable(),
		CreatedVariant:  resp.Msg.GetCreatedVariant(),
	}, nil
}

func CIDeleteVariableVariant(ctx context.Context, token, orgID, variantID string) (bool, error) {
	client := newCIVariableServiceV3Beta2Client()
	resp, err := client.DeleteVariableVariant(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ3beta2.DeleteVariableVariantRequest{VariantId: variantID}), token, orgID))
	if err != nil {
		return false, err
	}
	return resp.Msg.GetDeletedVariable(), nil
}

func CIDeleteVariableGroup(ctx context.Context, token, orgID, name string) error {
	client := newCIVariableServiceV3Beta2Client()
	_, err := client.DeleteVariable(ctx, WithAuthenticationAndOrg(connect.NewRequest(&civ3beta2.DeleteVariableRequest{
		Lookup: &civ3beta2.DeleteVariableRequest_Name{Name: name},
	}), token, orgID))
	return err
}

func ciVariantName(name string) string {
	if name == "" {
		return ciDefaultVariantName
	}
	return name
}

func ciPageRequest(page uint32) *civ3beta2.PageRequest {
	if page == 0 {
		page = ciDefaultPage
	}
	return &civ3beta2.PageRequest{Page: page, PageSize: ciMaxPageSize}
}

func ciAttributes(repos, environments, branches, workflows []string) []*civ3beta2.Attribute {
	attrs := make([]*civ3beta2.Attribute, 0, len(repos)+len(environments)+len(branches)+len(workflows))
	for _, repo := range repos {
		if repo != "" {
			attrs = append(attrs, &civ3beta2.Attribute{Key: "repository", Value: repo})
		}
	}
	for _, environment := range environments {
		if environment != "" {
			attrs = append(attrs, &civ3beta2.Attribute{Key: "environment", Value: environment})
		}
	}
	for _, branch := range branches {
		if branch != "" {
			attrs = append(attrs, &civ3beta2.Attribute{Key: "branch", Value: branch})
		}
	}
	for _, workflow := range workflows {
		if workflow != "" {
			attrs = append(attrs, &civ3beta2.Attribute{Key: "workflow", Value: workflow})
		}
	}
	return attrs
}

func ciAttributesFromProto(attrs []*civ3beta2.Attribute) []CIVariantAttribute {
	result := make([]CIVariantAttribute, 0, len(attrs))
	for _, attr := range attrs {
		result = append(result, CIVariantAttribute{Key: attr.GetKey(), Value: attr.GetValue()})
	}
	return result
}

func ciTimeString(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().Format(time.RFC3339)
}

func secretGroupsFromProto(secrets []*civ3beta2.Secret) []CISecretGroup {
	result := make([]CISecretGroup, 0, len(secrets))
	for _, secret := range secrets {
		result = append(result, secretGroupFromProto(secret))
	}
	return result
}

func secretGroupFromProto(secret *civ3beta2.Secret) CISecretGroup {
	if secret == nil {
		return CISecretGroup{}
	}
	return CISecretGroup{
		ID:           secret.GetId(),
		Name:         secret.GetName(),
		Variants:     secretVariantsFromProto(secret.GetVariants()),
		VariantCount: secret.GetVariantCount(),
		LastModified: ciTimeString(secret.GetLastModified()),
	}
}

func secretVariantsFromProto(variants []*civ3beta2.SecretVariant) []CISecretVariant {
	result := make([]CISecretVariant, 0, len(variants))
	for _, variant := range variants {
		result = append(result, secretVariantFromProto(variant))
	}
	return result
}

func secretVariantFromProto(variant *civ3beta2.SecretVariant) CISecretVariant {
	if variant == nil {
		return CISecretVariant{}
	}
	return CISecretVariant{
		ID:              variant.GetId(),
		SecretID:        variant.GetSecretId(),
		Name:            variant.GetName(),
		Description:     variant.GetDescription(),
		Attributes:      ciAttributesFromProto(variant.GetAttributes()),
		LastModified:    ciTimeString(variant.GetLastModified()),
		ValueGroupIndex: variant.ValueGroupIndex,
	}
}

func variableGroupsFromProto(variables []*civ3beta2.Variable) []CIVariableGroup {
	result := make([]CIVariableGroup, 0, len(variables))
	for _, variable := range variables {
		result = append(result, variableGroupFromProto(variable))
	}
	return result
}

func variableGroupFromProto(variable *civ3beta2.Variable) CIVariableGroup {
	if variable == nil {
		return CIVariableGroup{}
	}
	return CIVariableGroup{
		ID:           variable.GetId(),
		Name:         variable.GetName(),
		Variants:     variableVariantsFromProto(variable.GetVariants()),
		VariantCount: variable.GetVariantCount(),
		LastModified: ciTimeString(variable.GetLastModified()),
	}
}

func variableVariantsFromProto(variants []*civ3beta2.VariableVariant) []CIVariableVariant {
	result := make([]CIVariableVariant, 0, len(variants))
	for _, variant := range variants {
		result = append(result, variableVariantFromProto(variant))
	}
	return result
}

func variableVariantFromProto(variant *civ3beta2.VariableVariant) CIVariableVariant {
	if variant == nil {
		return CIVariableVariant{}
	}
	return CIVariableVariant{
		ID:           variant.GetId(),
		VariableID:   variant.GetVariableId(),
		Name:         variant.GetName(),
		Value:        variant.GetValue(),
		Description:  variant.GetDescription(),
		Attributes:   ciAttributesFromProto(variant.GetAttributes()),
		LastModified: ciTimeString(variant.GetLastModified()),
	}
}
