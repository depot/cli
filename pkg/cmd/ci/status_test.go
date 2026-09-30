package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
)

func TestStatusWaitPollsUntilTerminal(t *testing.T) {
	for _, output := range []string{"", "json"} {
		for _, finalStatus := range []string{"finished", "failed", "cancelled"} {
			t.Run(output+"/"+finalStatus, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					original := ciGetRunStatus
					t.Cleanup(func() { ciGetRunStatus = original })
					statuses := []string{"queued", "running", "running", finalStatus}
					calls := 0
					start := time.Now()
					ciGetRunStatus = func(ctx context.Context, token, orgID, runID string) (*civ1.GetRunStatusResponse, error) {
						if calls >= len(statuses) {
							t.Fatal("polled after terminal status")
						}
						if token != "token-123" || orgID != "org-123" || runID != "run-1" {
							t.Fatalf("unexpected request: %q %q %q", token, orgID, runID)
						}
						if elapsed := time.Since(start); elapsed != time.Duration(calls)*5*time.Second {
							t.Fatalf("poll %d at %s, want five-second intervals", calls, elapsed)
						}
						response := testStatusResponse()
						response.Status = statuses[calls]
						calls++
						return response, nil
					}

					cmd := NewCmdStatus()
					cmd.SetArgs([]string{"run-1", "--wait", "--token", "token-123", "--org", "org-123", "--output", output})
					var progress bytes.Buffer
					cmd.SetErr(&progress)
					cmd.SilenceErrors = true
					cmd.SilenceUsage = true
					stdout, err := captureStdout(t, cmd.Execute)
					if finalStatus == "finished" && err != nil {
						t.Fatal(err)
					}
					if finalStatus != "finished" && (err == nil || !strings.Contains(err.Error(), finalStatus)) {
						t.Fatalf("want %s error, got %v", finalStatus, err)
					}
					if calls != len(statuses) {
						t.Fatalf("made %d polls, want %d", calls, len(statuses))
					}
					if !strings.Contains(progress.String(), "Run: run-1 (queued)") || !strings.Contains(progress.String(), "Run: run-1 (running)") {
						t.Fatalf("missing progress updates: %s", &progress)
					}
					if output == "json" {
						var got statusJSON
						if err := json.Unmarshal([]byte(stdout), &got); err != nil {
							t.Fatalf("expected a single JSON document: %v\n%s", err, stdout)
						}
						if got.Status != finalStatus || len(got.Workflows) != 1 {
							t.Fatalf("unexpected final status: %+v", got)
						}
					} else if !strings.Contains(stdout, "Run: run-1 ("+finalStatus+")") || !strings.Contains(stdout, "Workflow: workflow-1") || strings.Contains(stdout, "Run: run-1 (running)") {
						t.Fatalf("unexpected final output: %s", stdout)
					}
				})
			})
		}
	}
}

func TestStatusWaitStopsOnAPIError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := ciGetRunStatus
		t.Cleanup(func() { ciGetRunStatus = original })
		wantErr := errors.New("service unavailable")
		calls := 0
		ciGetRunStatus = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
			calls++
			if calls == 1 {
				return testStatusResponse(), nil
			}
			return nil, wantErr
		}
		cmd := NewCmdStatus()
		cmd.SetArgs([]string{"run-1", "--wait", "--token", "token-123", "--org", "org-123"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		stdout, err := captureStdout(t, cmd.Execute)
		if !errors.Is(err, wantErr) || calls != 2 || stdout != "" {
			t.Fatalf("output=%q, err=%v, calls=%d", stdout, err, calls)
		}
	})
}

func TestStatusTerminalRunReturnsImmediately(t *testing.T) {
	for _, status := range []string{"finished", "failed", "cancelled"} {
		for _, wait := range []string{"false", "true"} {
			t.Run(status+"/wait="+wait, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					original := ciGetRunStatus
					t.Cleanup(func() { ciGetRunStatus = original })
					calls := 0
					ciGetRunStatus = func(context.Context, string, string, string) (*civ1.GetRunStatusResponse, error) {
						calls++
						if calls > 1 {
							t.Fatal("polled an already terminal run")
						}
						return &civ1.GetRunStatusResponse{OrgId: "org-123", RunId: "run-1", Status: status}, nil
					}
					cmd := NewCmdStatus()
					cmd.SetArgs([]string{"run-1", "--wait=" + wait, "--token", "token-123", "--org", "org-123"})
					cmd.SilenceErrors = true
					cmd.SilenceUsage = true
					var progress bytes.Buffer
					cmd.SetErr(&progress)
					start := time.Now()
					stdout, err := captureStdout(t, cmd.Execute)
					wantError := wait == "true" && status != "finished"
					if (err != nil) != wantError {
						t.Fatalf("err=%v, want error=%v", err, wantError)
					}
					if time.Since(start) != 0 || calls != 1 || progress.Len() != 0 {
						t.Fatalf("elapsed=%s, calls=%d, progress=%q", time.Since(start), calls, &progress)
					}
					if !strings.Contains(stdout, "Run: run-1 ("+status+")") {
						t.Fatalf("missing final status: %s", stdout)
					}
				})
			})
		}
	}
}

func TestStatusWaitCancellation(t *testing.T) {
	for _, duringRequest := range []bool{false, true} {
		name := "during sleep"
		if duringRequest {
			name = "during request"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				original := ciGetRunStatus
				t.Cleanup(func() { ciGetRunStatus = original })
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				ciGetRunStatus = func(ctx context.Context, _, _, _ string) (*civ1.GetRunStatusResponse, error) {
					calls++
					if duringRequest {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return testStatusResponse(), nil
				}
				go func() {
					select {
					case <-time.After(time.Second):
						cancel()
					case <-ctx.Done():
					}
				}()
				cmd := NewCmdStatus()
				cmd.SetArgs([]string{"run-1", "--wait", "--token", "token-123", "--org", "org-123"})
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				start := time.Now()
				stdout, err := captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
				if !errors.Is(err, context.Canceled) || calls != 1 || stdout != "" || time.Since(start) != time.Second {
					t.Fatalf("output=%q, err=%v, calls=%d, elapsed=%s", stdout, err, calls, time.Since(start))
				}
			})
		})
	}
}

func testStatusResponse() *civ1.GetRunStatusResponse {
	return &civ1.GetRunStatusResponse{
		OrgId:  "org-123",
		RunId:  "run-1",
		Status: "running",
		Workflows: []*civ1.WorkflowStatus{
			{
				WorkflowId:   "workflow-1",
				Status:       "running",
				WorkflowPath: ".depot/workflows/ci.yml",
				Jobs: []*civ1.JobStatus{
					{
						JobId:  "job-1",
						JobKey: "ci.yml:build",
						Status: "running",
						Attempts: []*civ1.AttemptStatus{
							{AttemptId: "att-finished", Attempt: 1, Status: "finished"},
							{AttemptId: "att-running", Attempt: 2, Status: "running", SandboxId: "sandbox-1", SessionId: "session-1"},
							{AttemptId: "att-failed", Attempt: 3, Status: "failed"},
						},
					},
				},
			},
		},
	}
}

func TestStatusHumanOutputShowsDownloadForFinishedAttemptsOnly(t *testing.T) {
	for _, name := range []string{"api-package / checks", ""} {
		t.Run("display_name="+name, func(t *testing.T) {
			originalGetRunStatus := ciGetRunStatus
			t.Cleanup(func() { ciGetRunStatus = originalGetRunStatus })

			var capturedToken string
			var capturedOrgID string
			var capturedRunID string
			ciGetRunStatus = func(ctx context.Context, token, orgID, runID string) (*civ1.GetRunStatusResponse, error) {
				capturedToken = token
				capturedOrgID = orgID
				capturedRunID = runID
				response := testStatusResponse()
				response.Workflows[0].Jobs[0].JobDisplayName = name
				return response, nil
			}

			cmd := NewCmdStatus()
			cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "run-1"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			stdout, err := captureStdout(t, cmd.Execute)
			if err != nil {
				t.Fatal(err)
			}

			if capturedToken != "token-123" {
				t.Fatalf("token = %q, want token-123", capturedToken)
			}
			if capturedOrgID != "org-123" {
				t.Fatalf("orgID = %q, want org-123", capturedOrgID)
			}
			if capturedRunID != "run-1" {
				t.Fatalf("runID = %q, want run-1", capturedRunID)
			}

			wantName := name
			if wantName == "" {
				wantName = "ci.yml:build"
			}
			for _, want := range []string{
				"Job: job-1 [" + wantName + "] (running)",
				"SSH:  depot ci ssh run-1 --job ci.yml:build --org org-123",
				"Logs: depot ci logs att-finished --org org-123",
				"Download: depot ci logs att-finished --output-file logs.txt --org org-123",
				"View: https://depot.dev/orgs/org-123/workflows/workflow-1?job=job-1&attempt=att-finished",
				"Logs: depot ci logs att-running --org org-123",
				"Logs: depot ci logs att-failed --org org-123",
			} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("status output missing %q:\n%s", want, stdout)
				}
			}
			for _, notWant := range []string{
				"Download: depot ci logs att-running",
				"Download: depot ci logs att-failed",
			} {
				if strings.Contains(stdout, notWant) {
					t.Fatalf("status output advertised download for non-finished attempt %q:\n%s", notWant, stdout)
				}
			}
		})
	}
}

func TestStatusJSONOutput(t *testing.T) {
	for _, name := range []string{"api-package / checks", ""} {
		t.Run("display_name="+name, func(t *testing.T) {
			originalGetRunStatus := ciGetRunStatus
			t.Cleanup(func() { ciGetRunStatus = originalGetRunStatus })

			ciGetRunStatus = func(ctx context.Context, token, orgID, runID string) (*civ1.GetRunStatusResponse, error) {
				response := testStatusResponse()
				response.Workflows[0].Jobs[0].JobDisplayName = name
				return response, nil
			}

			cmd := NewCmdStatus()
			cmd.SetArgs([]string{"--org", "org-123", "--token", "token-123", "--output", "json", "run-1"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			stdout, err := captureStdout(t, cmd.Execute)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stdout, "Logs: depot ci logs") {
				t.Fatalf("json output included human log hints:\n%s", stdout)
			}

			var got struct {
				OrgID     string `json:"org_id"`
				RunID     string `json:"run_id"`
				Status    string `json:"status"`
				Workflows []struct {
					WorkflowID   string `json:"workflow_id"`
					WorkflowPath string `json:"workflow_path"`
					Jobs         []struct {
						JobDisplayName string `json:"job_display_name"`
						JobID          string `json:"job_id"`
						JobKey         string `json:"job_key"`
						Attempts       []struct {
							AttemptID         string `json:"attempt_id"`
							Status            string `json:"status"`
							SandboxID         string `json:"sandbox_id"`
							SessionID         string `json:"session_id"`
							LogsCommand       string `json:"logs_command"`
							DownloadAvailable bool   `json:"download_available"`
							DownloadCommand   string `json:"download_command"`
							ViewURL           string `json:"view_url"`
							SSHAvailable      bool   `json:"ssh_available"`
							SSHCommand        string `json:"ssh_command"`
						} `json:"attempts"`
					} `json:"jobs"`
				} `json:"workflows"`
			}
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
			}
			if got.OrgID != "org-123" || got.RunID != "run-1" || got.Status != "running" {
				t.Fatalf("unexpected run JSON: %+v", got)
			}
			if len(got.Workflows) != 1 || got.Workflows[0].WorkflowID != "workflow-1" || got.Workflows[0].WorkflowPath != ".depot/workflows/ci.yml" {
				t.Fatalf("unexpected workflow JSON: %+v", got.Workflows)
			}
			if len(got.Workflows[0].Jobs) != 1 || got.Workflows[0].Jobs[0].JobID != "job-1" || got.Workflows[0].Jobs[0].JobKey != "ci.yml:build" {
				t.Fatalf("unexpected job JSON: %+v", got.Workflows[0].Jobs)
			}
			attempts := got.Workflows[0].Jobs[0].Attempts
			wantName := name
			if wantName == "" {
				wantName = "ci.yml:build"
			}
			if got.Workflows[0].Jobs[0].JobDisplayName != wantName {
				t.Fatalf("job_display_name = %q, want %q", got.Workflows[0].Jobs[0].JobDisplayName, wantName)
			}
			if len(attempts) != 3 || attempts[1].AttemptID != "att-running" || attempts[1].SandboxID != "sandbox-1" || attempts[1].SessionID != "session-1" {
				t.Fatalf("unexpected attempts JSON: %+v", attempts)
			}
			if attempts[0].LogsCommand != "depot ci logs att-finished --org org-123" {
				t.Fatalf("finished logs command = %q", attempts[0].LogsCommand)
			}
			if !attempts[0].DownloadAvailable || attempts[0].DownloadCommand != "depot ci logs att-finished --output-file logs.txt --org org-123" {
				t.Fatalf("unexpected finished download affordance: %+v", attempts[0])
			}
			if attempts[0].ViewURL != "https://depot.dev/orgs/org-123/workflows/workflow-1?job=job-1&attempt=att-finished" {
				t.Fatalf("finished view url = %q", attempts[0].ViewURL)
			}
			if !attempts[1].SSHAvailable || attempts[1].SSHCommand != "depot ci ssh run-1 --job ci.yml:build --org org-123" {
				t.Fatalf("unexpected running ssh affordance: %+v", attempts[1])
			}
			if attempts[1].DownloadAvailable || attempts[1].DownloadCommand != "" {
				t.Fatalf("running attempt should not expose download affordance: %+v", attempts[1])
			}
			if attempts[2].DownloadAvailable || attempts[2].SSHAvailable || attempts[2].DownloadCommand != "" || attempts[2].SSHCommand != "" {
				t.Fatalf("failed attempt exposed unavailable affordances: %+v", attempts[2])
			}
		})
	}
}

func TestStatusRejectsUnsupportedOutput(t *testing.T) {
	originalGetRunStatus := ciGetRunStatus
	t.Cleanup(func() { ciGetRunStatus = originalGetRunStatus })

	ciGetRunStatus = func(ctx context.Context, token, orgID, runID string) (*civ1.GetRunStatusResponse, error) {
		t.Fatal("ciGetRunStatus should not be called for invalid output")
		return nil, nil
	}

	cmd := NewCmdStatus()
	cmd.SetArgs([]string{"--token", "token-123", "--output", "yaml", "run-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	_, err := captureStdout(t, cmd.Execute)
	if err == nil || !strings.Contains(err.Error(), `unsupported output "yaml"`) {
		t.Fatalf("expected unsupported output error, got %v", err)
	}
}
