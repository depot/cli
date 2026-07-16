package browse

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/spf13/cobra"
)

const depotWebURL = "https://depot.dev"

type buildURLLookup func(context.Context, string) (string, error)

type entityDestination struct {
	kind string
	path string
	url  string
}

type entityLookup func(context.Context, string, string) ([]entityDestination, error)

type ciJobSummaryLookup func(context.Context, string, string, *civ1.GetJobSummaryRequest) (*civ1.GetJobSummaryResponse, error)

type ciJobMetricsLookup func(context.Context, string, string, string) (*civ1.GetJobMetricsResponse, error)

type entityLookupDependencies struct {
	resolveProjectToken func(context.Context) (string, error)
	resolveOrgToken     func(context.Context) (string, error)
	getBuild            func(context.Context, string, string) (*cliv1.GetBuildResponse, error)
	getWorkflow         func(context.Context, string, string, string) (*civ1.GetWorkflowResponse, error)
	getJobSummary       ciJobSummaryLookup
	getJobMetrics       ciJobMetricsLookup
}

type dependencies struct {
	currentOrg     func() string
	lookupBuildURL buildURLLookup
	lookupEntity   entityLookup
	openURL        func(string) error
}

func NewCmdBrowse() *cobra.Command {
	return newCmdBrowse(dependencies{
		currentOrg:     config.GetCurrentOrganization,
		lookupBuildURL: lookupBuildURL,
		lookupEntity:   lookupEntity,
		openURL:        api.OpenURL,
	})
}

func newCmdBrowse(deps dependencies) *cobra.Command {
	var orgID string
	var noBrowser bool

	cmd := &cobra.Command{
		Use:   "browse [path | URL]",
		Short: "Open Depot in your web browser",
		Long: `Open Depot in your web browser

Depot has multiple products. Common destinations are:

  workflows       Depot CI
  builds          Depot Container Builds (alias for projects)
  github-actions  Depot GitHub Action Runners
  <no path>       Organization homepage

If the intended product is unclear, ask the user which destination they want.

Relative paths are opened within the current Depot organization. Complete
https://depot.dev URLs are opened unchanged. The builds/<build-id> shorthand
looks up the build and opens its canonical project build page. An unrecognized
single-segment path is looked up as a build, Depot CI workflow or job, or
GitHub Actions job ID.`,
		Example: `  # Open Depot CI
  depot browse workflows

  # Open a Depot CI workflow
  # Find workflow IDs with: depot ci workflow list --output json
  depot browse workflows/<workflow-id>

  # Open a Depot CI job
  # Find job IDs with: depot ci workflow show <workflow-id> --output json
  depot browse workflows/<workflow-id>/jobs/<job-id>

  # Open Depot Container Builds
  depot browse builds

  # Open a container build
  # Find build IDs with: depot list builds --project <project-id> --output json
  depot browse builds/<build-id>

  # Open Depot GitHub Action Runners
  depot browse github-actions

  # Open a GitHub Actions job
  # Find job IDs with: depot browse github-actions
  depot browse github-actions/jobs/<github-job-id>

  # Open the organization homepage
  depot browse

  # Open organization usage for a specific month
  depot browse 'usage/2026/07?section=github-actions'

  # Look up a build, Depot CI workflow/job, or GitHub Actions job by ID
  depot browse <id>

  # Print the resolved URL without opening a browser
  depot browse workflows --no-browser`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			location := ""
			if len(args) == 1 {
				location = args[0]
			}

			selectedOrgID := orgID
			if selectedOrgID == "" && deps.currentOrg != nil {
				selectedOrgID = deps.currentOrg()
			}

			destination, err := resolveDestination(cmd.Context(), location, selectedOrgID, deps.lookupBuildURL, deps.lookupEntity)
			if err != nil {
				return err
			}

			if noBrowser {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), destination)
				return err
			}
			if deps.openURL == nil {
				return fmt.Errorf("browser opener is unavailable")
			}
			if err := deps.openURL(destination); err != nil {
				return fmt.Errorf("failed to open %s: %w", destination, err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&noBrowser, "no-browser", "n", false, "Print the destination URL instead of opening it")
	cmd.Flags().StringVar(&orgID, "org", "", "Depot organization ID (defaults to the current organization)")

	return cmd
}

func resolveDestination(ctx context.Context, location, orgID string, lookupBuild buildURLLookup, lookupBareID entityLookup) (string, error) {
	location = strings.TrimSpace(location)
	parsed, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("invalid browse destination %q: %w", location, err)
	}

	if parsed.IsAbs() || parsed.Host != "" {
		if !isDepotURL(parsed) {
			return "", fmt.Errorf("only https://depot.dev URLs can be opened as complete URLs")
		}
		return parsed.String(), nil
	}

	path := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if err := validateRelativePath(path); err != nil {
		return "", err
	}
	if buildID, ok := buildShorthandID(path); ok {
		if lookupBuild == nil {
			return "", fmt.Errorf("build lookup is unavailable")
		}
		canonical, err := lookupBuild(ctx, buildID)
		if err != nil {
			return "", fmt.Errorf("failed to look up build %s: %w", buildID, err)
		}
		return addQueryAndFragment(canonical, parsed.RawQuery, parsed.EscapedFragment())
	}
	if orgID == "" && path == "" {
		return addQueryAndFragment(depotWebURL, parsed.RawQuery, parsed.EscapedFragment())
	}
	if orgID == "" {
		return "", fmt.Errorf("no organization selected; run `depot org switch` or use --org")
	}

	if path == "builds" || path == "builds/" {
		path = "projects"
	} else if isBareID(path) && !isKnownOrgPath(path) {
		if lookupBareID == nil {
			return "", fmt.Errorf("entity lookup is unavailable")
		}
		matches, err := lookupBareID(ctx, path, orgID)
		if err != nil {
			return "", fmt.Errorf("failed to look up %s: %w", path, err)
		}
		return selectEntityDestination(path, matches, parsed.RawQuery, parsed.EscapedFragment())
	}

	prefix := depotWebURL + "/orgs/" + url.PathEscape(orgID) + "/"
	return prefix + relativeReference(path, parsed.RawQuery, parsed.EscapedFragment()), nil
}

func isBareID(path string) bool {
	return path != "" && !strings.Contains(path, "/")
}

func isKnownOrgPath(path string) bool {
	switch path {
	case "audit-logs-portal", "builds", "cache", "claude", "code", "compute",
		"dagger-projects", "github-actions", "home", "projects", "registry",
		"registry-v2", "settings", "sso-portal", "test-results", "usage", "workflows":
		return true
	default:
		return false
	}
}

func selectEntityDestination(id string, matches []entityDestination, rawQuery, escapedFragment string) (string, error) {
	if len(matches) == 0 {
		return "", fmt.Errorf("could not find %s as a build, Depot CI workflow or job, or GitHub Actions job; use an explicit path if it is an app destination", id)
	}
	if len(matches) > 1 {
		paths := make([]string, 0, len(matches))
		for _, match := range matches {
			paths = append(paths, fmt.Sprintf("%s (%s)", match.path, match.kind))
		}
		sort.Strings(paths)
		return "", fmt.Errorf("%s is ambiguous; choose one of: %s", id, strings.Join(paths, ", "))
	}
	return addQueryAndFragment(matches[0].url, rawQuery, escapedFragment)
}

func validateRelativePath(path string) error {
	for _, segment := range strings.Split(path, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return fmt.Errorf("invalid path segment %q: %w", segment, err)
		}
		if decoded == "." || decoded == ".." {
			return fmt.Errorf("organization-relative paths must not contain . or .. segments")
		}
	}
	return nil
}

func buildShorthandID(path string) (string, bool) {
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] != "builds" || parts[1] == "" {
		return "", false
	}
	buildID, err := url.PathUnescape(parts[1])
	return buildID, err == nil && buildID != ""
}

func relativeReference(path, rawQuery, escapedFragment string) string {
	reference := path
	if rawQuery != "" {
		reference += "?" + rawQuery
	}
	if escapedFragment != "" {
		reference += "#" + escapedFragment
	}
	return reference
}

func addQueryAndFragment(destination, rawQuery, escapedFragment string) (string, error) {
	parsed, err := url.Parse(destination)
	if err != nil {
		return "", fmt.Errorf("invalid build URL: %w", err)
	}
	if !isDepotURL(parsed) {
		return "", fmt.Errorf("lookup returned a URL outside https://depot.dev")
	}
	parsed.RawQuery = rawQuery
	parsed.Fragment, err = url.PathUnescape(escapedFragment)
	if err != nil {
		return "", fmt.Errorf("invalid URL fragment: %w", err)
	}
	return parsed.String(), nil
}

func isDepotURL(parsed *url.URL) bool {
	return parsed.Scheme == "https" && strings.EqualFold(parsed.Hostname(), "depot.dev") && parsed.Port() == "" && parsed.User == nil
}

func lookupBuildURL(ctx context.Context, buildID string) (string, error) {
	token, err := helpers.ResolveProjectAuth(ctx, "")
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("missing API token, please run `depot login`")
	}

	response, err := api.NewBuildClient().GetBuild(
		ctx,
		api.WithAuthentication(connect.NewRequest(&cliv1.GetBuildRequest{BuildId: buildID}), token),
	)
	if err != nil {
		return "", err
	}
	if response.Msg.GetBuildUrl() == "" {
		return "", fmt.Errorf("build lookup returned an empty URL")
	}
	return response.Msg.GetBuildUrl(), nil
}

func lookupEntity(ctx context.Context, id, orgID string) ([]entityDestination, error) {
	return lookupEntityWithDependencies(ctx, id, orgID, entityLookupDependencies{
		resolveProjectToken: func(ctx context.Context) (string, error) {
			return helpers.ResolveProjectAuth(ctx, "")
		},
		resolveOrgToken: func(ctx context.Context) (string, error) {
			return helpers.ResolveOrgAuth(ctx, "")
		},
		getBuild: func(ctx context.Context, token, id string) (*cliv1.GetBuildResponse, error) {
			response, err := api.NewBuildClient().GetBuild(
				ctx,
				api.WithAuthentication(connect.NewRequest(&cliv1.GetBuildRequest{BuildId: id}), token),
			)
			if err != nil {
				return nil, err
			}
			return response.Msg, nil
		},
		getWorkflow:   api.CIGetWorkflow,
		getJobSummary: api.CIGetJobSummary,
		getJobMetrics: api.CIGetJobMetrics,
	})
}

func lookupEntityWithDependencies(ctx context.Context, id, orgID string, deps entityLookupDependencies) ([]entityDestination, error) {
	if isDecimalID(id) {
		path := "github-actions/jobs/" + url.PathEscape(id)
		return []entityDestination{{kind: "GitHub Actions job", path: path, url: orgURL(orgID, path)}}, nil
	}

	type lookupResult struct {
		destination *entityDestination
		err         error
	}
	results := make(chan lookupResult, 3)
	var wg sync.WaitGroup
	wg.Add(3)

	projectToken, projectTokenErr := resolveEntityLookupToken(ctx, "build", deps.resolveProjectToken)
	orgToken, orgTokenErr := resolveEntityLookupToken(ctx, "Depot CI", deps.resolveOrgToken)

	go func() {
		defer wg.Done()
		if projectTokenErr != nil {
			results <- lookupResult{err: projectTokenErr}
			return
		}
		response, err := deps.getBuild(ctx, projectToken, id)
		if err != nil {
			results <- lookupResult{err: entityLookupError("build", err)}
			return
		}
		if response.GetBuildUrl() == "" {
			results <- lookupResult{err: fmt.Errorf("build lookup returned an empty URL")}
			return
		}
		path := "builds/" + url.PathEscape(id)
		results <- lookupResult{destination: &entityDestination{kind: "build", path: path, url: response.GetBuildUrl()}}
	}()

	go func() {
		defer wg.Done()
		if orgTokenErr != nil {
			results <- lookupResult{err: orgTokenErr}
			return
		}
		response, err := deps.getWorkflow(ctx, orgToken, orgID, id)
		if err != nil {
			results <- lookupResult{err: entityLookupError("Depot CI workflow", err)}
			return
		}
		if response.GetWorkflowId() == "" {
			results <- lookupResult{err: fmt.Errorf("Depot CI workflow lookup returned incomplete routing information")}
			return
		}
		path := "workflows/" + url.PathEscape(response.GetWorkflowId())
		results <- lookupResult{destination: &entityDestination{kind: "Depot CI workflow", path: path, url: orgURL(orgID, path)}}
	}()

	go func() {
		defer wg.Done()
		if orgTokenErr != nil {
			results <- lookupResult{err: orgTokenErr}
			return
		}
		destination, err := lookupCIJob(ctx, orgToken, orgID, id, deps.getJobSummary, deps.getJobMetrics)
		results <- lookupResult{destination: destination, err: err}
	}()

	wg.Wait()
	close(results)

	var matches []entityDestination
	var lookupErrors []string
	for result := range results {
		if result.destination != nil {
			matches = append(matches, *result.destination)
		}
		if result.err != nil {
			lookupErrors = append(lookupErrors, result.err.Error())
		}
	}
	return finishEntityLookup(matches, lookupErrors)
}

func resolveEntityLookupToken(ctx context.Context, kind string, resolve func(context.Context) (string, error)) (string, error) {
	token, err := resolve(ctx)
	if err != nil {
		return "", fmt.Errorf("%s authentication failed: %w", kind, err)
	}
	if token == "" {
		return "", fmt.Errorf("%s authentication failed: missing API token, please run `depot login`", kind)
	}
	return token, nil
}

func finishEntityLookup(matches []entityDestination, lookupErrors []string) ([]entityDestination, error) {
	if len(matches) > 0 {
		return matches, nil
	}
	if len(lookupErrors) > 0 {
		sort.Strings(lookupErrors)
		return nil, fmt.Errorf("%s", strings.Join(lookupErrors, "; "))
	}
	return nil, nil
}

func lookupCIJob(
	ctx context.Context,
	token, orgID, jobID string,
	getSummary ciJobSummaryLookup,
	getMetrics ciJobMetricsLookup,
) (*entityDestination, error) {
	response, err := getSummary(ctx, token, orgID, &civ1.GetJobSummaryRequest{JobId: jobID})
	if err != nil {
		return nil, entityLookupError("Depot CI job", err)
	}
	if response.GetJobId() == "" {
		return nil, fmt.Errorf("Depot CI job lookup returned incomplete routing information")
	}

	workflowID := response.GetWorkflowId()
	if workflowID == "" {
		metrics, err := getMetrics(ctx, token, orgID, response.GetJobId())
		if err != nil {
			return nil, fmt.Errorf("Depot CI job lookup could not resolve its workflow: %w", err)
		}
		workflowID = metrics.GetWorkflow().GetWorkflowId()
	}
	if workflowID == "" {
		return nil, fmt.Errorf("Depot CI job lookup returned incomplete routing information")
	}

	path := "workflows/" + url.PathEscape(workflowID) + "/jobs/" + url.PathEscape(response.GetJobId())
	return &entityDestination{kind: "Depot CI job", path: path, url: orgURL(orgID, path)}, nil
}

func isDecimalID(id string) bool {
	if id == "" {
		return false
	}
	for _, char := range id {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func entityLookupError(kind string, err error) error {
	if code := connect.CodeOf(err); code == connect.CodeNotFound || code == connect.CodeInvalidArgument {
		return nil
	}
	return fmt.Errorf("%s lookup failed: %w", kind, err)
}

func orgURL(orgID, path string) string {
	return depotWebURL + "/orgs/" + url.PathEscape(orgID) + "/" + path
}
