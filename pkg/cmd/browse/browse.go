package browse

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/spf13/cobra"
)

const depotWebURL = "https://depot.dev"

type buildURLLookup func(context.Context, string) (string, error)

type dependencies struct {
	currentOrg     func() string
	lookupBuildURL buildURLLookup
	openURL        func(string) error
}

func NewCmdBrowse() *cobra.Command {
	return newCmdBrowse(dependencies{
		currentOrg:     config.GetCurrentOrganization,
		lookupBuildURL: lookupBuildURL,
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
looks up the build and opens its canonical project build page.`,
		Example: `  # Open Depot CI
  depot browse workflows

  # Open a Depot CI workflow
  # Find workflow IDs with: depot ci workflow list --output json
  depot browse workflows/<workflow-id>

  # Open Depot Container Builds
  depot browse builds

  # Open a container build
  # Find build IDs with: depot list builds --project <project-id> --output json
  depot browse builds/<build-id>

  # Open Depot GitHub Action Runners
  depot browse github-actions

  # Open a GitHub Actions job
  # Find run IDs with: gh run list --json databaseId
  # Find job IDs with: gh run view <run-id> --json jobs
  depot browse github-actions/jobs/<github-job-id>

  # Open the organization homepage
  depot browse

  # Open the registry
  depot browse registry

  # Open organization usage for a specific month
  depot browse 'usage/2026/07?section=github-actions'

  # Open a complete Depot URL unchanged
  depot browse 'https://depot.dev/orgs/<org-id>/usage/2026/07?section=github-actions'

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

			destination, err := resolveDestination(cmd.Context(), location, selectedOrgID, deps.lookupBuildURL)
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

func resolveDestination(ctx context.Context, location, orgID string, lookup buildURLLookup) (string, error) {
	location = strings.TrimSpace(location)
	parsed, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("invalid browse destination %q: %w", location, err)
	}

	if parsed.IsAbs() || parsed.Host != "" {
		if parsed.Scheme != "https" || parsed.Host != "depot.dev" || parsed.User != nil {
			return "", fmt.Errorf("only https://depot.dev URLs can be opened as complete URLs")
		}
		return parsed.String(), nil
	}

	path := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if err := validateRelativePath(path); err != nil {
		return "", err
	}
	if orgID == "" {
		return "", fmt.Errorf("no organization selected; run `depot org switch` or use --org")
	}

	if path == "builds" {
		path = "projects"
	} else if buildID, ok := buildShorthandID(path); ok {
		if lookup == nil {
			return "", fmt.Errorf("build lookup is unavailable")
		}
		canonical, err := lookup(ctx, buildID)
		if err != nil {
			return "", fmt.Errorf("failed to look up build %s: %w", buildID, err)
		}
		return addQueryAndFragment(canonical, parsed.RawQuery, parsed.EscapedFragment())
	}

	prefix := depotWebURL + "/orgs/" + url.PathEscape(orgID) + "/"
	return prefix + relativeReference(path, parsed.RawQuery, parsed.EscapedFragment()), nil
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
	if parsed.Scheme != "https" || parsed.Host != "depot.dev" || parsed.User != nil {
		return "", fmt.Errorf("build lookup returned a URL outside https://depot.dev")
	}
	parsed.RawQuery = rawQuery
	parsed.Fragment, err = url.PathUnescape(escapedFragment)
	if err != nil {
		return "", fmt.Errorf("invalid URL fragment: %w", err)
	}
	return parsed.String(), nil
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
