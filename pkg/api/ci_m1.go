package api

// M1 uses bounded JSON until the public CI RPC generated clients include these
// responses. Only fixed product endpoints are exposed, never an arbitrary RPC.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
)

const (
	CIEventBodyMaxBytes  = 64 * 1024
	ciM1RequestMaxBytes  = 128 * 1024
	ciM1ResponseMaxBytes = 1024 * 1024
	ciM1ServicePath      = "/depot.ci.v1.CIService/"
	ciSourcesPath        = "/v1/ci/sources"
)

var ciM1Identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// CIControlResponse is the server-authoritative pause/resume state. Revision is
// raw because protobuf JSON serializes 64-bit integers as strings.
type CIControlResponse struct {
	RunID       string          `json:"runId"`
	Paused      bool            `json:"paused"`
	WakePending bool            `json:"wakePending"`
	Revision    json.RawMessage `json:"revision"`
	Raw         json.RawMessage `json:"-"`
}

type CISubmitJobInputResponse struct {
	Pending       bool            `json:"pending"`
	Completed     bool            `json:"completed"`
	ApprovalCount int             `json:"approvalCount"`
	Raw           json.RawMessage `json:"-"`
}

type CICreateEventSourceRequest struct {
	Repo       string `json:"repo"`
	Slug       string `json:"slug"`
	Definition string `json:"definition"`
}

type CIEventSourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// URL is a credential. Callers must not write it into a repository or logs.
type CICreateEventSourceResponse struct {
	ID         string            `json:"id"`
	URL        string            `json:"url"`
	SourceFile CIEventSourceFile `json:"sourceFile"`
	Raw        json.RawMessage   `json:"-"`
}

type CIRotateEventSourceResponse struct {
	URL string          `json:"url"`
	Raw json.RawMessage `json:"-"`
}

type CIRecatchEventSourceRequest struct {
	Body     json.RawMessage `json:"body"`
	Evidence string          `json:"evidence"`
}

func CIPauseRun(ctx context.Context, token, orgID, runID string) (*CIControlResponse, error) {
	return ciControlRun(ctx, token, orgID, runID, "PauseRun")
}
func CIResumeRun(ctx context.Context, token, orgID, runID string) (*CIControlResponse, error) {
	return ciControlRun(ctx, token, orgID, runID, "ResumeRun")
}
func ciControlRun(ctx context.Context, token, orgID, runID, method string) (*CIControlResponse, error) {
	if !ciM1Identifier.MatchString(runID) {
		return nil, errors.New("invalid run ID")
	}
	body, err := ciM1RequestJSON(ctx, token, orgID, http.MethodPost, ciM1ServicePath+method, struct {
		RunID string `json:"runId"`
	}{runID})
	if err != nil {
		return nil, err
	}
	var resp CIControlResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errors.New("invalid CI control response")
	}
	if resp.RunID != runID {
		return nil, errors.New("invalid CI control run ID")
	}
	if len(resp.Revision) == 0 {
		// Protobuf JSON may omit a zero-valued scalar.
		resp.Revision = json.RawMessage(`0`)
	} else {
		revision := string(resp.Revision)
		if resp.Revision[0] == '"' {
			if err := json.Unmarshal(resp.Revision, &revision); err != nil {
				return nil, errors.New("invalid CI control revision")
			}
		}
		if _, err := strconv.ParseUint(revision, 10, 64); err != nil {
			return nil, errors.New("invalid CI control revision")
		}
	}
	resp.Raw = body
	return &resp, nil
}

// CIGetJobPublicJSON preserves the server's whitelisted public input metadata,
// including approval metadataJson, without depending on newer generated types.
func CIGetJobPublicJSON(ctx context.Context, token, orgID, jobID string) (json.RawMessage, error) {
	if !ciM1Identifier.MatchString(jobID) {
		return nil, errors.New("invalid job ID")
	}
	return ciM1RequestJSON(ctx, token, orgID, http.MethodPost, ciM1ServicePath+"GetJob", struct {
		JobID string `json:"jobId"`
	}{jobID})
}

func CISubmitJobInput(ctx context.Context, token, orgID, jobID, attemptID string, fields map[string]string) (*CISubmitJobInputResponse, error) {
	if !ciM1Identifier.MatchString(jobID) || !ciM1Identifier.MatchString(attemptID) {
		return nil, errors.New("invalid job or attempt ID")
	}
	if len(fields) == 0 {
		return nil, errors.New("input fields are required")
	}
	body, err := ciM1RequestJSON(ctx, token, orgID, http.MethodPost, ciM1ServicePath+"SubmitJobInput", struct {
		JobID     string            `json:"jobId"`
		AttemptID string            `json:"attemptId"`
		Fields    map[string]string `json:"fields"`
	}{jobID, attemptID, fields})
	if err != nil {
		return nil, err
	}
	var resp CISubmitJobInputResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errors.New("invalid CI input response")
	}
	resp.Raw = body
	return &resp, nil
}

func CICreateEventSource(ctx context.Context, token, orgID string, req CICreateEventSourceRequest) (*CICreateEventSourceResponse, error) {
	body, err := ciM1RequestJSON(ctx, token, orgID, http.MethodPost, ciSourcesPath, req)
	if err != nil {
		return nil, err
	}
	var resp CICreateEventSourceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errors.New("invalid event source response")
	}
	if resp.ID == "" || resp.URL == "" || resp.SourceFile.Path == "" || resp.SourceFile.Content == "" {
		return nil, errors.New("incomplete event source response")
	}
	resp.Raw = body
	return &resp, nil
}
func CIRotateEventSource(ctx context.Context, token, orgID, id string) (*CIRotateEventSourceResponse, error) {
	path, err := ciEventSourcePath(id)
	if err != nil {
		return nil, err
	}
	body, err := ciM1RequestJSON(ctx, token, orgID, http.MethodPost, path+"/token", struct{}{})
	if err != nil {
		return nil, err
	}
	var resp CIRotateEventSourceResponse
	if err := json.Unmarshal(body, &resp); err != nil || resp.URL == "" {
		return nil, errors.New("invalid event source token response")
	}
	resp.Raw = body
	return &resp, nil
}
func CIRevokeEventSource(ctx context.Context, token, orgID, id string) (json.RawMessage, error) {
	path, err := ciEventSourcePath(id)
	if err != nil {
		return nil, err
	}
	return ciM1RequestJSON(ctx, token, orgID, http.MethodDelete, path, nil)
}
func CIListEventSources(ctx context.Context, token, orgID, repo string) (json.RawMessage, error) {
	if strings.TrimSpace(repo) == "" || len(repo) > 512 {
		return nil, errors.New("invalid repository")
	}
	return ciM1RequestJSON(ctx, token, orgID, http.MethodGet, ciSourcesPath+"?"+url.Values{"repo": {repo}}.Encode(), nil)
}
func CIRecatchEventSource(ctx context.Context, token, orgID, id string, req CIRecatchEventSourceRequest) (json.RawMessage, error) {
	path, err := ciEventSourcePath(id)
	if err != nil {
		return nil, err
	}
	if len(req.Body) > CIEventBodyMaxBytes || !json.Valid(req.Body) {
		return nil, errors.New("event body must be valid JSON of at most 64 KiB")
	}
	if strings.TrimSpace(req.Evidence) == "" || len(req.Evidence) > CIEventBodyMaxBytes {
		return nil, errors.New("evidence must be nonempty and at most 64 KiB")
	}
	return ciM1RequestJSON(ctx, token, orgID, http.MethodPost, path+"/recatch", req)
}
func ciEventSourcePath(id string) (string, error) {
	if !ciM1Identifier.MatchString(id) {
		return "", errors.New("invalid source ID")
	}
	return ciSourcesPath + "/" + id, nil
}

// Never echo a URL, response body, input value, or transport error: each may
// contain credentials. Preserve context errors for cancellation-aware callers.
func ciM1RequestJSON(ctx context.Context, token, orgID, method, path string, payload any) (json.RawMessage, error) {
	var data []byte
	if payload != nil {
		var err error
		data, err = json.Marshal(payload)
		if err != nil {
			return nil, errors.New("cannot encode CI request")
		}
		if len(data) > ciM1RequestMaxBytes {
			return nil, errors.New("CI request exceeds 128 KiB")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	baseURL := strings.TrimRight(baseURLFunc(), "/")
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("cannot construct CI request")
	}
	req.Header = WithAuthenticationAndOrg(connect.NewRequest(&struct{}{}), token, orgID).Header().Clone()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", Agent())
	if strings.HasPrefix(path, ciM1ServicePath) {
		req.Header.Set("Connect-Protocol-Version", "1")
	}
	client := *getHTTPClient(baseURL)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("CI request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("CI request failed (HTTP %d)", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ciM1ResponseMaxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("cannot read CI response")
	}
	if len(body) > ciM1ResponseMaxBytes {
		return nil, errors.New("CI response exceeds 1 MiB")
	}
	if resp.StatusCode == http.StatusNoContent && len(body) == 0 {
		return json.RawMessage(`{}`), nil
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' || !json.Valid(body) {
		return nil, errors.New("invalid CI JSON response")
	}
	return json.RawMessage(body), nil
}
