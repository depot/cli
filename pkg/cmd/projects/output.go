package projects

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"github.com/depot/cli/pkg/helpers"
)

const bytesPerGigabyte int64 = 1024 * 1024 * 1024

type projectOutput struct {
	ProjectID      string              `json:"project_id"`
	OrganizationID string              `json:"organization_id"`
	Name           string              `json:"name"`
	RegionID       string              `json:"region_id"`
	CreatedAt      string              `json:"created_at,omitempty"`
	CachePolicy    *corev1.CachePolicy `json:"cache_policy,omitempty"`
}

func newProjectOutput(project *corev1.Project) (*projectOutput, error) {
	if project == nil {
		return nil, fmt.Errorf("API returned no project")
	}

	var createdAt string
	if project.GetCreatedAt() != nil {
		createdAt = project.GetCreatedAt().AsTime().UTC().Format(time.RFC3339Nano)
	}

	return &projectOutput{
		ProjectID:      project.GetProjectId(),
		OrganizationID: project.GetOrganizationId(),
		Name:           project.GetName(),
		RegionID:       project.GetRegionId(),
		CreatedAt:      createdAt,
		CachePolicy:    project.GetCachePolicy(),
	}, nil
}

func writeProjectOutput(w io.Writer, project *corev1.Project, output string) error {
	view, err := newProjectOutput(project)
	if err != nil {
		return err
	}

	if output == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(view)
	}

	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(table, "Project ID:\t%s\n", view.ProjectID)
	fmt.Fprintf(table, "Organization ID:\t%s\n", view.OrganizationID)
	fmt.Fprintf(table, "Name:\t%s\n", view.Name)
	fmt.Fprintf(table, "Region:\t%s\n", view.RegionID)
	fmt.Fprintf(table, "Created:\t%s\n", valueOrDash(view.CreatedAt))
	if view.CachePolicy != nil {
		fmt.Fprintf(table, "Cache storage:\t%s\n", formatCacheStorage(view.CachePolicy.GetKeepBytes()))
		fmt.Fprintf(table, "Cache retention:\t%s\n", formatCacheRetention(view.CachePolicy.GetKeepDays()))
	}
	return table.Flush()
}

func validateProjectOutput(output string) error {
	switch output {
	case "", "json":
		return nil
	default:
		return fmt.Errorf("unsupported output %q (valid: json)", output)
	}
}

func resolveProjectID(args []string, projectID string) (string, error) {
	if len(args) > 0 {
		if projectID != "" {
			return "", fmt.Errorf("project ID must be specified as either an argument or --project-id, not both")
		}
		projectID = args[0]
	}

	projectID = helpers.ResolveProjectID(projectID)
	if projectID == "" {
		return "", fmt.Errorf("unknown project ID (run `depot init`, pass a project ID, use --project-id, or set $DEPOT_PROJECT_ID)")
	}
	return projectID, nil
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func formatCacheStorage(bytes int64) string {
	if bytes == 0 {
		return "No limit"
	}
	if bytes%bytesPerGigabyte == 0 {
		return fmt.Sprintf("%d GB", bytes/bytesPerGigabyte)
	}
	return fmt.Sprintf("%d bytes", bytes)
}

func formatCacheRetention(days int32) string {
	if days == 0 {
		return "No limit"
	}
	return fmt.Sprintf("%d days", days)
}
