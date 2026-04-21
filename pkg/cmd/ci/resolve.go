package ci

import (
	"context"
	"fmt"

	"github.com/depot/cli/pkg/api"
)

// resolveSingleWorkflow returns the single workflow in the run, or an error
// listing available workflow IDs if the run contains zero or multiple workflows.
// Use when the user has not supplied --workflow and the command's RPC needs a
// workflow_id.
func resolveSingleWorkflow(ctx context.Context, token, orgID, runID string) (string, error) {
	resp, err := api.CIGetRunStatus(ctx, token, orgID, runID)
	if err != nil {
		return "", fmt.Errorf("failed to look up run %s: %w", runID, err)
	}
	switch len(resp.Workflows) {
	case 0:
		return "", fmt.Errorf("run %s has no workflows", runID)
	case 1:
		return resp.Workflows[0].WorkflowId, nil
	default:
		ids := make([]string, 0, len(resp.Workflows))
		for _, w := range resp.Workflows {
			ids = append(ids, w.WorkflowId)
		}
		return "", fmt.Errorf("run %s contains multiple workflows; specify --workflow=<id> (available: %v)", runID, ids)
	}
}

// findWorkflowForJob walks the run's workflows to locate the one that contains
// the given job_id. Used by commands that accept --job without --workflow.
func findWorkflowForJob(ctx context.Context, token, orgID, runID, jobID string) (string, error) {
	resp, err := api.CIGetRunStatus(ctx, token, orgID, runID)
	if err != nil {
		return "", fmt.Errorf("failed to look up run %s: %w", runID, err)
	}
	for _, w := range resp.Workflows {
		for _, j := range w.Jobs {
			if j.JobId == jobID {
				return w.WorkflowId, nil
			}
		}
	}
	return "", fmt.Errorf("job %s not found in run %s", jobID, runID)
}
