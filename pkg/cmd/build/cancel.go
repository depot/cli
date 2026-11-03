package build

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"buf.build/gen/go/depot/api/connectrpc/go/depot/core/v1/corev1connect"
	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

const buildCancellationTimeout = 30 * time.Second
const buildCancellationConcurrency = 4

type buildCancellationResult struct {
	BuildID string                  `json:"build_id"`
	Outcome string                  `json:"outcome,omitempty"`
	Error   *buildCancellationError `json:"error,omitempty"`
}

type buildCancellationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewCmdCancel() *cobra.Command {
	var token, projectID, output string
	var allRunning, yes, dryRun bool

	cmd := &cobra.Command{
		Use:   "cancel [<build-id>...]",
		Short: "Request cancellation of container builds",
		Long: `Request cancellation of one or more container builds by ID, or select all
running builds in an explicitly specified project with --all-running --project.

Cancellation is asynchronous. A successful request does not mean execution has
stopped. Already finished builds are successful no-ops. Each selected build gets
its own result; any failed request makes the command exit with an error.

Bulk selection reads every page before requesting cancellation. It is not an
atomic snapshot; builds started after selection are excluded. Bulk cancellation
previews the selected IDs and asks for confirmation, or accepts --yes for scripts.
Use --dry-run to preview without requesting cancellation.

Requires an organization API token. Create one in Organization Settings > API
Tokens and pass it with --token or DEPOT_TOKEN. Project tokens, reader tokens,
legacy depot login tokens, and CI OIDC credentials cannot cancel builds.`,
		Example: `  # Request cancellation of two builds
  depot build cancel <build-id> <build-id>

  # Preview running builds in a project
  depot build cancel --project <project-id> --all-running --dry-run

  # Request cancellation of selected running builds without prompting
  depot build cancel --project <project-id> --all-running --yes --output json`,
		Args: func(cmd *cobra.Command, args []string) error {
			if output != "" && output != "json" {
				return fmt.Errorf("unsupported output %q; use json", output)
			}
			if allRunning {
				if len(args) != 0 {
					return fmt.Errorf("build IDs and --all-running are mutually exclusive")
				}
				if strings.TrimSpace(projectID) == "" {
					return fmt.Errorf("--all-running requires an explicit --project")
				}
			} else {
				if cmd.Flags().Changed("project") {
					return fmt.Errorf("--project requires --all-running")
				}
				if len(args) == 0 {
					return fmt.Errorf("provide at least one build ID or --all-running --project <project-id>")
				}
			}
			for _, id := range args {
				if strings.TrimSpace(id) == "" {
					return fmt.Errorf("build IDs must not be empty")
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedToken := helpers.ResolveStaticOrgAuth(token)
			if resolvedToken == "" {
				return fmt.Errorf("missing organization API token; create one in Organization Settings > API Tokens and pass it with --token or DEPOT_TOKEN")
			}

			client := api.NewCoreBuildClient()
			ids := uniqueBuildIDs(args)
			if allRunning {
				var err error
				ids, err = runningBuildIDs(cmd.Context(), client, resolvedToken, projectID)
				if err != nil {
					return err
				}
			}

			if allRunning && !dryRun && len(ids) > 0 && !yes {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Selected %d running builds in project %s:\n%s\n", len(ids), projectID, strings.Join(ids, "\n")); err != nil {
					return err
				}
				if !helpers.IsStdinTerminal() || !helpers.IsTerminal() {
					return fmt.Errorf("bulk cancellation requires --yes in non-interactive mode; use --dry-run to preview")
				}
				if _, err := fmt.Fprint(cmd.ErrOrStderr(), "Request cancellation of these builds? [y/N] "); err != nil {
					return err
				}
				answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil {
					return fmt.Errorf("read confirmation: %w", err)
				}
				answer = strings.ToLower(strings.TrimSpace(answer))
				if answer != "y" && answer != "yes" {
					_, err := fmt.Fprintln(cmd.ErrOrStderr(), "No cancellation requested.")
					return err
				}
			}

			results := make([]buildCancellationResult, len(ids))
			if dryRun {
				for i, id := range ids {
					results[i] = buildCancellationResult{BuildID: id, Outcome: "WOULD_REQUEST_CANCELLATION"}
				}
			} else {
				results = cancelBuildIDs(cmd.Context(), client, resolvedToken, ids)
			}
			if err := writeBuildCancellationResults(cmd.OutOrStdout(), results, output, dryRun); err != nil {
				return err
			}
			failed := 0
			for _, result := range results {
				if result.Error != nil {
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("failed to request cancellation for %d of %d builds", failed, len(results))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&token, "token", "", "Depot organization API token (or DEPOT_TOKEN)")
	cmd.Flags().StringVar(&projectID, "project", "", "Depot project ID (required with --all-running)")
	cmd.Flags().BoolVar(&allRunning, "all-running", false, "Select all running container builds in the project")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Confirm bulk cancellation without prompting")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview selected build IDs without requesting cancellation")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}

func uniqueBuildIDs(ids []string) []string {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	return unique
}

func runningBuildIDs(ctx context.Context, client corev1connect.BuildServiceClient, token, projectID string) ([]string, error) {
	ids := []string{}
	pageToken := ""
	seenPages := map[string]bool{}
	for {
		request := &corev1.ListBuildsRequest{ProjectId: projectID, PageSize: proto.Int32(100)}
		if pageToken != "" {
			request.PageToken = proto.String(pageToken)
		}
		requestCtx, cancel := context.WithTimeout(ctx, buildCancellationTimeout)
		response, err := client.ListBuilds(requestCtx, api.WithAuthentication(connect.NewRequest(request), token))
		cancel()
		if err != nil {
			return nil, fmt.Errorf("failed to list builds; no cancellation requested: %w", err)
		}
		for _, build := range response.Msg.GetBuilds() {
			if build.GetStatus() == corev1.Build_STATUS_RUNNING {
				if strings.TrimSpace(build.GetBuildId()) == "" {
					return nil, fmt.Errorf("API returned a running build without an ID; no cancellation requested")
				}
				ids = append(ids, build.GetBuildId())
			}
		}
		pageToken = response.Msg.GetNextPageToken()
		if pageToken == "" {
			return uniqueBuildIDs(ids), nil
		}
		if seenPages[pageToken] {
			return nil, fmt.Errorf("API returned a repeated page token; no cancellation requested")
		}
		seenPages[pageToken] = true
	}
}

func cancelBuildIDs(ctx context.Context, client corev1connect.BuildServiceClient, token string, ids []string) []buildCancellationResult {
	results := make([]buildCancellationResult, len(ids))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for i := 0; i < min(buildCancellationConcurrency, len(ids)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				result := buildCancellationResult{BuildID: ids[index]}
				err := ctx.Err()
				if err == nil {
					requestCtx, cancel := context.WithTimeout(ctx, buildCancellationTimeout)
					response, callErr := client.CancelBuild(requestCtx, api.WithAuthentication(connect.NewRequest(&corev1.CancelBuildRequest{BuildId: ids[index]}), token))
					cancel()
					err = callErr
					if err == nil {
						outcome := response.Msg.GetOutcome()
						if outcome != corev1.CancelBuildResponse_OUTCOME_CANCELLATION_REQUESTED && outcome != corev1.CancelBuildResponse_OUTCOME_ALREADY_TERMINAL {
							err = fmt.Errorf("API returned an unknown cancellation outcome (%d); cancellation is not confirmed", outcome)
						} else {
							result.Outcome = outcome.String()
						}
					}
				}
				if err != nil {
					result.Error = &buildCancellationError{Code: connect.CodeOf(err).String(), Message: err.Error()}
				}
				results[index] = result
			}
		}()
	}
	for index := range ids {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return results
}

func writeBuildCancellationResults(w io.Writer, results []buildCancellationResult, output string, dryRun bool) error {
	if output == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(struct {
			DryRun  bool                      `json:"dry_run"`
			Results []buildCancellationResult `json:"results"`
		}{DryRun: dryRun, Results: results})
	}
	if len(results) == 0 {
		_, err := fmt.Fprintln(w, "No running builds found.")
		return err
	}
	for _, result := range results {
		var message string
		switch {
		case result.Error != nil:
			message = "failed: " + result.Error.Message
		case dryRun:
			message = "would request cancellation"
		case result.Outcome == corev1.CancelBuildResponse_OUTCOME_ALREADY_TERMINAL.String():
			message = "already finished (no change)"
		default:
			message = "cancellation requested (execution may still be running)"
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", result.BuildID, message); err != nil {
			return err
		}
	}
	return nil
}
