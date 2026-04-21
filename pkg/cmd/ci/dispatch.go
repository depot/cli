package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	"github.com/depot/cli/pkg/helpers"
	civ1 "github.com/depot/cli/pkg/proto/depot/ci/v1"
	"github.com/spf13/cobra"
)

func NewCmdDispatch() *cobra.Command {
	var (
		orgID    string
		token    string
		repo     string
		workflow string
		ref      string
		inputs   []string
		output   string
	)

	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Dispatch a workflow via workflow_dispatch [beta]",
		Long: `Trigger a single workflow via workflow_dispatch. Inputs are validated against the workflow's
declared input schema.

--workflow takes the workflow file's basename (e.g. deploy.yml), not the full repo path.
This matches GitHub's workflow_dispatch API convention.`,
		Example: `  # Dispatch a workflow on the main branch (use the workflow file's basename)
  depot ci dispatch --repo depot/cli --workflow deploy.yml --ref main

  # Pass inputs (repeatable)
  depot ci dispatch --repo depot/cli --workflow deploy.yml --ref main \
    --input environment=staging --input dry_run=true

  # Output the RPC response as JSON
  depot ci dispatch --repo depot/cli --workflow deploy.yml --ref main --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			inputMap, err := parseDispatchInputs(inputs)
			if err != nil {
				return err
			}

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

			resp, err := api.CIDispatchWorkflow(ctx, tokenVal, orgID, &civ1.DispatchWorkflowRequest{
				OrgId:    orgID,
				Repo:     repo,
				Workflow: workflow,
				Ref:      ref,
				Inputs:   inputMap,
			})
			if err != nil {
				return fmt.Errorf("failed to dispatch workflow: %w", err)
			}

			if output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}

			fmt.Printf("Dispatched workflow; run %s queued\n", resp.RunId)
			fmt.Printf("  View: https://depot.dev/orgs/%s/runs/%s\n", resp.OrgId, resp.RunId)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID (required when user is a member of multiple organizations)")
	cmd.Flags().StringVar(&token, "token", "", "Depot API token")
	cmd.Flags().StringVar(&repo, "repo", "", "Target GitHub repository in {org}/{repo} format (required)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow file basename, e.g. deploy.yml — NOT the full path (required)")
	cmd.Flags().StringVar(&ref, "ref", "", "Branch or tag name to run the workflow on (required)")
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "Workflow input as key=value; repeat for multiple inputs")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")

	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("workflow")
	_ = cmd.MarkFlagRequired("ref")

	return cmd
}

// parseDispatchInputs turns a slice of "key=value" strings into a map, rejecting
// entries missing an '=' separator or with an empty key.
func parseDispatchInputs(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, entry := range raw {
		idx := strings.Index(entry, "=")
		if idx <= 0 {
			return nil, fmt.Errorf("invalid --input %q: expected key=value", entry)
		}
		key := entry[:idx]
		value := entry[idx+1:]
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("duplicate --input key %q", key)
		}
		out[key] = value
	}
	return out, nil
}
