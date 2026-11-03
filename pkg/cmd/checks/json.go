package checks

import (
	"encoding/json"

	checksv1 "github.com/depot/cli/pkg/proto/depot/checks/v1"
)

type checkJSON struct {
	CheckID        string       `json:"check_id"`
	CreatedAt      string       `json:"created_at"`
	CurrentAttempt *attemptJSON `json:"current_attempt"`
}

type attemptJSON struct {
	CheckID            string               `json:"check_id"`
	AttemptID          string               `json:"attempt_id"`
	AgentID            string               `json:"agent_id"`
	AgentName          string               `json:"agent_name"`
	Repo               *checksv1.Repository `json:"repo"`
	Branch             string               `json:"branch"`
	HeadSHA            string               `json:"head_sha"`
	BaseSHA            string               `json:"base_sha"`
	Status             string               `json:"status"`
	Outcome            string               `json:"outcome"`
	IsCurrent          bool                 `json:"is_current"`
	CreatedAt          string               `json:"created_at"`
	StartedAt          string               `json:"started_at"`
	FinishedAt         string               `json:"finished_at"`
	Result             json.RawMessage      `json:"result,omitempty"`
	ResultJSON         string               `json:"result_json,omitempty"`
	FailureCode        string               `json:"failure_code"`
	FailureMessage     string               `json:"failure_message"`
	ExecutionID        string               `json:"execution_id"`
	SandboxID          string               `json:"sandbox_id"`
	RuntimeSnapshotRef string               `json:"runtime_snapshot_ref"`
	ModelID            string               `json:"model_id"`
	HasSession         bool                 `json:"has_session"`
	TranscriptStatus   string               `json:"transcript_status"`
	Resolutions        []resolutionJSON     `json:"resolutions"`
	PreviousReview     *previousReviewJSON  `json:"previous_review,omitempty"`
}

type resolutionJSON struct {
	FindingIndex     uint32 `json:"finding_index"`
	ResolvedByUserID string `json:"resolved_by_user_id"`
	ResolvedByName   string `json:"resolved_by_name"`
	ResolvedAt       string `json:"resolved_at"`
}

type previousReviewJSON struct {
	AttemptID string                `json:"attempt_id"`
	Findings  []previousFindingJSON `json:"findings"`
}

type previousFindingJSON struct {
	FindingIndex        uint32  `json:"finding_index"`
	Outcome             string  `json:"outcome"`
	Note                string  `json:"note"`
	CurrentFindingIndex *uint32 `json:"current_finding_index,omitempty"`
}

func checkToJSON(check *checksv1.Check) checkJSON {
	return checkJSON{CheckID: check.GetId(), CreatedAt: check.GetCreatedAt(), CurrentAttempt: attemptToJSON(check.GetCurrentAttempt())}
}

func attemptToJSON(a *checksv1.CheckAttempt) *attemptJSON {
	if a == nil {
		return nil
	}
	out := &attemptJSON{
		CheckID: a.CheckId, AttemptID: a.Id, AgentID: a.AgentId, AgentName: a.AgentName, Repo: a.Repo,
		Branch: a.Branch, HeadSHA: a.HeadSha, BaseSHA: a.BaseSha, Status: a.Status, Outcome: outcome(a), IsCurrent: a.IsCurrent,
		CreatedAt: a.CreatedAt, StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, ResultJSON: a.ResultJson,
		FailureCode: a.FailureCode, FailureMessage: a.FailureMessage, ExecutionID: a.ExecutionId, SandboxID: a.SandboxId,
		RuntimeSnapshotRef: a.RuntimeSnapshotRef, ModelID: a.ModelId, HasSession: a.HasSession, TranscriptStatus: a.TranscriptStatus,
		Resolutions: make([]resolutionJSON, 0, len(a.Resolutions)),
	}
	if json.Valid([]byte(a.ResultJson)) {
		out.Result = json.RawMessage(a.ResultJson)
	}
	for _, r := range a.Resolutions {
		out.Resolutions = append(out.Resolutions, resolutionJSON{FindingIndex: r.GetFindingIndex(), ResolvedByUserID: r.GetResolvedByUserId(), ResolvedByName: r.GetResolvedByName(), ResolvedAt: r.GetResolvedAt()})
	}
	if previous := a.PreviousReview; previous != nil {
		out.PreviousReview = &previousReviewJSON{AttemptID: previous.AttemptId, Findings: make([]previousFindingJSON, 0, len(previous.Findings))}
		for _, f := range previous.Findings {
			out.PreviousReview.Findings = append(out.PreviousReview.Findings, previousFindingJSON{FindingIndex: f.GetFindingIndex(), Outcome: f.GetOutcome(), Note: f.GetNote(), CurrentFindingIndex: f.CurrentFindingIndex})
		}
	}
	return out
}

type agentJSON struct {
	AgentID      string                 `json:"agent_id"`
	Name         string                 `json:"name"`
	Instructions string                 `json:"instructions"`
	ModelID      string                 `json:"model_id"`
	Repos        []*checksv1.Repository `json:"repos"`
	Events       []string               `json:"events"`
}

func agentToJSON(a *checksv1.ReviewAgent) *agentJSON {
	if a == nil {
		return nil
	}
	return &agentJSON{
		AgentID: a.Id, Name: a.Name, Instructions: a.Instructions, ModelID: a.ModelId,
		Repos:  append([]*checksv1.Repository{}, a.GetRepos().GetItems()...),
		Events: append([]string{}, a.GetTriggers().GetEvents()...),
	}
}
