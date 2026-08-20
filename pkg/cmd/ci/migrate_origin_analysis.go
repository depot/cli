package ci

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/ci/migrate"
	civ2 "github.com/depot/cli/pkg/proto/depot/ci/v2"
)

type cursorOriginAnalysis struct {
	response      *connect.Response[civ2.GetRepositoryMigrationAnalysisResponse]
	remote        string
	repositoryURL string
}

func cursorPreflight(ctx context.Context, opts migrateOptions) (*preflightResult, error) {
	analysis, err := analyzeCursorOriginWorkflows(ctx, opts, nil)
	if err != nil {
		return nil, err
	}

	token, orgID, err := resolveAuth(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := opts.stdout
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintf(out, "\nDetected repository: %s\n", analysis.repositoryURL)
	fmt.Fprintln(out, "Cursor Origin repository is available to this Depot organization.")
	return &preflightResult{token: token, orgID: orgID, repo: analysis.repositoryURL}, nil
}

func analyzeCursorOriginWorkflows(
	ctx context.Context,
	opts migrateOptions,
	workflows []*migrate.WorkflowFile,
) (*cursorOriginAnalysis, error) {
	token, orgID, err := resolveAuth(ctx, opts)
	if err != nil {
		return nil, err
	}

	workDir := opts.dir
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	remote, repositoryURL, err := detectMigrationRemote(ctx, workDir, migrationForgeCursor, false)
	if err != nil {
		return nil, err
	}

	workflowFiles, err := migrationWorkflowFiles(workDir, workflows)
	if err != nil {
		return nil, err
	}

	client := opts.repositoryAnalysisClient
	if client == nil {
		client = api.NewWorkflowMigrationClient()
	}
	request := api.WithAuthenticationAndOrg(connect.NewRequest(&civ2.GetRepositoryMigrationAnalysisRequest{
		Forge:         civ2.MigrationForge_MIGRATION_FORGE_CURSOR_ORIGIN,
		RepositoryUrl: repositoryURL,
		WorkflowFiles: &civ2.MigrationWorkflowFiles{Workflows: workflowFiles},
	}), token, orgID)
	response, err := client.GetRepositoryAnalysis(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze Cursor Origin compatibility for %s: %w", repositoryURL, err)
	}
	return &cursorOriginAnalysis{response: response, remote: remote, repositoryURL: repositoryURL}, nil
}

func migrationWorkflowFiles(workDir string, workflows []*migrate.WorkflowFile) ([]*civ2.MigrationWorkflowFile, error) {
	root, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve repository directory: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve repository directory: %w", err)
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve repository directory: %w", err)
	}

	files := make([]*civ2.MigrationWorkflowFile, 0, len(workflows))
	for _, workflow := range workflows {
		path, err := filepath.Abs(workflow.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workflow path %s: %w", workflow.Path, err)
		}
		requestPath, err := filepath.Rel(root, path)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workflow path %s: %w", workflow.Path, err)
		}

		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workflow path %s: %w", workflow.Path, err)
		}
		resolvedPath, err = filepath.Abs(resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workflow path %s: %w", workflow.Path, err)
		}
		resolvedRelative, err := filepath.Rel(resolvedRoot, resolvedPath)
		if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("workflow %s resolves outside the repository and cannot be sent for analysis", workflow.Path)
		}

		content, err := os.ReadFile(resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s for analysis: %w", workflow.Path, err)
		}
		files = append(files, &civ2.MigrationWorkflowFile{
			Path:    filepath.ToSlash(requestPath),
			Content: string(content),
		})
	}
	return files, nil
}
