package checks

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	checksv1 "github.com/depot/cli/pkg/proto/depot/checks/v1"
	"github.com/spf13/cobra"
)

type reviewResult struct {
	Version  int    `json:"version"`
	Decision string `json:"decision"`
	Summary  string `json:"summary"`
	Findings []struct {
		Title    string `json:"title"`
		Severity string `json:"severity"`
		Path     string `json:"path"`
		Line     int    `json:"line"`
		Body     string `json:"body"`
	} `json:"findings"`
}

func readResult(attempt *checksv1.CheckAttempt) (reviewResult, error) {
	var result reviewResult
	if attempt.ResultJson == "" {
		return result, nil
	}
	if err := json.Unmarshal([]byte(attempt.ResultJson), &result); err != nil {
		return result, fmt.Errorf("invalid stored review result: %w", err)
	}
	if result.Version == 1 {
		result.Decision = "pass"
	}
	if result.Version < 1 || result.Version > 3 || (result.Decision != "pass" && result.Decision != "fail") {
		return result, fmt.Errorf("unsupported stored review result; use --output json to inspect the original")
	}
	return result, nil
}

func outcome(attempt *checksv1.CheckAttempt) string {
	if attempt == nil {
		return "unknown"
	}
	if attempt.Status != "succeeded" {
		return attempt.Status
	}
	result, err := readResult(attempt)
	if err != nil || result.Decision == "" {
		return "unknown"
	}
	if result.Decision == "fail" {
		resolved := map[uint32]bool{}
		for _, resolution := range attempt.Resolutions {
			if int(resolution.FindingIndex) < len(result.Findings) {
				resolved[resolution.FindingIndex] = true
			}
		}
		if len(result.Findings) > 0 && len(resolved) == len(result.Findings) {
			return "resolved"
		}
		return "rejected"
	}
	if len(result.Findings) > 0 {
		return "findings"
	}
	return "passed"
}

func repoName(repo *checksv1.Repository) string {
	if repo == nil {
		return ""
	}
	return repo.Forge + ":" + repo.Name
}

func printAttempts(cmd *cobra.Command, attempts []*checksv1.CheckAttempt) error {
	var b strings.Builder
	if len(attempts) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "No matching checks.")
		return err
	}
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "CHECK ID\tATTEMPT ID\tAGENT\tREPOSITORY\tBRANCH\tHEAD\tSTATE\tOUTCOME\tCURRENT\tCREATED")
	for _, a := range attempts {
		if a == nil {
			return fmt.Errorf("API returned a check without an attempt")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%t\t%s\n", a.CheckId, a.Id, a.AgentName, repoName(a.Repo), a.Branch, a.HeadSha, a.Status, outcome(a), a.IsCurrent, a.CreatedAt)
	}
	w.Flush()
	_, err := io.WriteString(cmd.OutOrStdout(), b.String())
	return err
}

func printAttempt(cmd *cobra.Command, a *checksv1.CheckAttempt) error {
	if a == nil {
		return fmt.Errorf("API returned no attempt")
	}
	result, err := readResult(a)
	if err != nil {
		return err
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	for _, row := range [][2]string{
		{"Check", a.CheckId}, {"Attempt", a.Id}, {"Current", fmt.Sprint(a.IsCurrent)}, {"Agent", a.AgentName + " (" + a.AgentId + ")"},
		{"Repository", repoName(a.Repo)}, {"Branch", a.Branch}, {"Head", a.HeadSha}, {"Base", a.BaseSha},
		{"State", a.Status}, {"Outcome", outcome(a)}, {"Decision", result.Decision}, {"Created", a.CreatedAt}, {"Started", a.StartedAt}, {"Finished", a.FinishedAt},
		{"Model", a.ModelId}, {"Execution", a.ExecutionId}, {"Sandbox", a.SandboxId}, {"Runtime snapshot", a.RuntimeSnapshotRef}, {"Transcript", a.TranscriptStatus},
		{"Failure code", a.FailureCode}, {"Failure", a.FailureMessage},
	} {
		if row[1] != "" {
			fmt.Fprintf(w, "%s\t%s\n", row[0], row[1])
		}
	}
	w.Flush()
	if result.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n", result.Summary)
	}
	resolved := map[uint32]*checksv1.FindingResolution{}
	for _, r := range a.Resolutions {
		resolved[r.FindingIndex] = r
	}
	for i, finding := range result.Findings {
		state := "open"
		if r := resolved[uint32(i)]; r != nil {
			state = "resolved at " + r.ResolvedAt
			if r.ResolvedByName != "" {
				state += " by " + r.ResolvedByName
			}
		}
		fmt.Fprintf(&b, "\n[%d] %s (%s; %s)\n%s:%d\n%s\n", i, finding.Title, finding.Severity, state, finding.Path, finding.Line, finding.Body)
	}
	if previous := a.PreviousReview; previous != nil {
		fmt.Fprintf(&b, "\nPrevious attempt: %s\n", previous.AttemptId)
		for _, f := range previous.Findings {
			fmt.Fprintf(&b, "[%d] %s %s\n", f.FindingIndex, f.Outcome, f.Note)
		}
	}
	_, err = io.WriteString(cmd.OutOrStdout(), b.String())
	return err
}

func printAgents(cmd *cobra.Command, agents []*checksv1.ReviewAgent) error {
	var b strings.Builder
	if len(agents) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "No review agents.")
		return err
	}
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT ID\tNAME\tMODEL\tREPOSITORIES\tEVENTS")
	for _, agent := range agents {
		model := agent.ModelId
		if model == "" {
			model = "default"
		}
		repos := []string{}
		for _, repo := range agent.GetRepos().GetItems() {
			repos = append(repos, repoName(repo))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", agent.Id, agent.Name, model, strings.Join(repos, ", "), strings.Join(agent.GetTriggers().GetEvents(), ", "))
	}
	w.Flush()
	_, err := io.WriteString(cmd.OutOrStdout(), b.String())
	return err
}

func printAgent(cmd *cobra.Command, a *checksv1.ReviewAgent) error {
	if a == nil {
		return fmt.Errorf("API returned no review agent")
	}
	if err := printAgents(cmd, []*checksv1.ReviewAgent{a}); err != nil {
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "\nInstructions:\n%s\n", a.Instructions)
	return err
}
