package ci

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

var (
	ciListArtifacts          = api.CIListArtifacts
	ciGetArtifactDownloadURL = api.CIGetArtifactDownloadURL
	ciArtifactDownloadClient = newArtifactDownloadHTTPClient()
)

const artifactDownloadHTTPTimeout = 30 * time.Minute

func NewCmdArtifacts() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifacts",
		Short: "List and download CI artifacts",
		Long:  "List CI artifact metadata and download one artifact by artifact ID.",
		Example: `  # List artifacts for a run
  depot ci artifacts list <run-id>

  # Download one artifact by ID
  depot ci artifacts download <artifact-id> --output-file coverage.zip`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCmdArtifactsList())
	cmd.AddCommand(NewCmdArtifactsDownload())
	return cmd
}

func NewCmdArtifactsList() *cobra.Command {
	var (
		orgID      string
		token      string
		output     string
		workflowID string
		jobID      string
		attemptID  string
	)

	cmd := &cobra.Command{
		Use:   "list <run-id>",
		Short: "List artifacts for a CI run",
		Long:  "List artifact metadata for a Depot CI run. Download URLs are never included in list output.",
		Example: `  # List artifacts for a run
  depot ci artifacts list <run-id>

  # List artifacts for one job as JSON
  depot ci artifacts list <run-id> --job <job-id> --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTextOrJSONOutput(output); err != nil {
				return err
			}

			ctx := cmd.Context()
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}
			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			artifacts, err := ciListArtifacts(ctx, tokenVal, orgID, args[0], api.CIListArtifactsOptions{
				WorkflowID: workflowID,
				JobID:      jobID,
				AttemptID:  attemptID,
			})
			if err != nil {
				return fmt.Errorf("failed to list artifacts: %w", err)
			}
			if outputIsJSON(output) {
				return writeJSON(buildArtifactsListJSON(artifacts))
			}
			printArtifactsTable(cmd.OutOrStdout(), artifacts)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (text, json)")
	cmd.Flags().StringVar(&workflowID, "workflow", "", "Workflow ID to filter artifacts")
	cmd.Flags().StringVar(&jobID, "job", "", "Job ID to filter artifacts")
	cmd.Flags().StringVar(&attemptID, "attempt", "", "Attempt ID to filter artifacts")
	return cmd
}

func NewCmdArtifactsDownload() *cobra.Command {
	var (
		orgID      string
		token      string
		outputPath string
	)

	cmd := &cobra.Command{
		Use:   "download <artifact-id>",
		Short: "Download one CI artifact by artifact ID",
		Long:  "Download one CI artifact by its artifact ID. Existing files are never overwritten.",
		Example: `  # Download an artifact to its default filename
  depot ci artifacts download <artifact-id>

  # Download an artifact to a specific file
  depot ci artifacts download <artifact-id> --output-file coverage.zip`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputPath == "-" {
				return fmt.Errorf("--output-file - is not supported; provide a file path")
			}

			ctx := cmd.Context()
			if orgID == "" {
				orgID = config.GetCurrentOrganization()
			}
			tokenVal, err := helpers.ResolveOrgAuth(ctx, token)
			if err != nil {
				return err
			}
			if tokenVal == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}
			if outputPath != "" {
				if err := validateArtifactOutputDoesNotExist(outputPath); err != nil {
					return err
				}
			}

			resp, err := ciGetArtifactDownloadURL(ctx, tokenVal, orgID, args[0])
			if err != nil {
				return fmt.Errorf("failed to get artifact download URL: %w", err)
			}
			artifact := resp.GetArtifact()
			if artifact == nil {
				return fmt.Errorf("artifact download response did not include artifact metadata")
			}

			destination := outputPath
			if destination == "" {
				destination = artifactLocalFilename(artifact.GetName(), artifact.GetArtifactId())
			}
			written, err := downloadArtifactToFile(ctx, resp.GetUrl(), destination)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Downloaded %s to %s (%s)\n", artifact.GetArtifactId(), destination, formatArtifactSize(written))
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&outputPath, "output-file", "", "Output file path")
	return cmd
}

type artifactsListJSON struct {
	Artifacts []artifactJSON `json:"artifacts"`
}

type artifactJSON struct {
	ArtifactID   string `json:"artifact_id"`
	RunID        string `json:"run_id"`
	WorkflowID   string `json:"workflow_id"`
	WorkflowPath string `json:"workflow_path"`
	JobID        string `json:"job_id"`
	JobKey       string `json:"job_key"`
	AttemptID    string `json:"attempt_id"`
	Attempt      int32  `json:"attempt"`
	Name         string `json:"name"`
	SizeBytes    int64  `json:"size_bytes"`
	CreatedAt    string `json:"created_at"`
}

func buildArtifactsListJSON(artifacts []*civ1.Artifact) artifactsListJSON {
	out := artifactsListJSON{Artifacts: make([]artifactJSON, 0, len(artifacts))}
	for _, artifact := range artifacts {
		out.Artifacts = append(out.Artifacts, artifactToJSON(artifact))
	}
	return out
}

func artifactToJSON(artifact *civ1.Artifact) artifactJSON {
	return artifactJSON{
		ArtifactID:   artifact.GetArtifactId(),
		RunID:        artifact.GetRunId(),
		WorkflowID:   artifact.GetWorkflowId(),
		WorkflowPath: artifact.GetWorkflowPath(),
		JobID:        artifact.GetJobId(),
		JobKey:       artifact.GetJobKey(),
		AttemptID:    artifact.GetAttemptId(),
		Attempt:      artifact.GetAttempt(),
		Name:         artifact.GetName(),
		SizeBytes:    artifact.GetSizeBytes(),
		CreatedAt:    artifact.GetCreatedAt(),
	}
}

func printArtifactsTable(w io.Writer, artifacts []*civ1.Artifact) {
	if len(artifacts) == 0 {
		fmt.Fprintln(w, "No artifacts found.")
		return
	}

	fmt.Fprintf(w, "%-22s  %-28s  %-9s  %-28s  %-20s  %-7s  %s\n", "ARTIFACT ID", "NAME", "SIZE", "WORKFLOW", "JOB", "ATTEMPT", "CREATED")
	for _, artifact := range artifacts {
		fmt.Fprintf(
			w,
			"%-22s  %-28s  %-9s  %-28s  %-20s  %-7d  %s\n",
			safeArtifactTableCell(artifact.GetArtifactId()),
			truncateForTable(safeArtifactTableCell(artifact.GetName()), 28),
			formatArtifactSize(artifact.GetSizeBytes()),
			truncateForTable(safeArtifactTableCell(artifact.GetWorkflowPath()), 28),
			truncateForTable(safeArtifactTableCell(artifact.GetJobKey()), 20),
			artifact.GetAttempt(),
			safeArtifactTableCell(artifact.GetCreatedAt()),
		)
	}
}

func validateArtifactOutputDoesNotExist(destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("%s already exists", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func downloadArtifactToFile(ctx context.Context, url, destination string) (int64, error) {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return 0, fmt.Errorf("%s already exists", destination)
		}
		return 0, err
	}

	resp, err := getArtifactURL(ctx, url)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(destination)
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = file.Close()
		_ = os.Remove(destination)
		return 0, fmt.Errorf("artifact download failed with HTTP %d", resp.StatusCode)
	}

	written, copyErr := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return written, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return written, closeErr
	}
	return written, nil
}

func getArtifactURL(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return ciArtifactDownloadClient.Do(req)
}

func newArtifactDownloadHTTPClient() *http.Client {
	return &http.Client{
		Timeout: artifactDownloadHTTPTimeout,
	}
}

func artifactLocalFilename(name, artifactID string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || unicode.IsControl(r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	sanitized := strings.TrimSpace(b.String())
	if sanitized == "" || strings.Trim(sanitized, ".") == "" {
		return "artifact-" + artifactID
	}
	return sanitized
}

func safeArtifactTableCell(value string) string {
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func formatArtifactSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	units := []string{"KB", "MB", "GB", "TB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}

func truncateForTable(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
