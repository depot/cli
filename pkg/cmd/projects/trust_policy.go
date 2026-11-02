package projects

import (
	"fmt"
	"strings"

	corev1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/core/v1"
	"connectrpc.com/connect"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

type trustPolicyOptions struct {
	projectID string
	token     string
	output    string
}

func newCmdTrustPolicy() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust-policy",
		Short: "Manage project OIDC trust policies",
		Long: `Manage the CI providers allowed to authenticate to a Depot project.

Requires an organization API token, passed with --token or DEPOT_TOKEN.
Select the Depot project with --project-id, DEPOT_PROJECT_ID, or depot.json.`,
	}
	cmd.AddCommand(newCmdTrustPolicyList(), newCmdTrustPolicyAdd(), newCmdTrustPolicyUpdate(), newCmdTrustPolicyRemove())
	return cmd
}

func (o *trustPolicyOptions) flags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&o.projectID, "project-id", "p", "", "Depot project ID (or DEPOT_PROJECT_ID/depot.json)")
	cmd.Flags().StringVar(&o.token, "token", "", "Depot organization API token (or DEPOT_TOKEN)")
	cmd.Flags().StringVarP(&o.output, "output", "o", "", "Output format (json)")
}

func (o *trustPolicyOptions) resolve() (string, string, error) {
	if err := validateProjectOutput(o.output); err != nil {
		return "", "", err
	}
	projectID, err := resolveProjectID(nil, o.projectID)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(projectID) == "" {
		return "", "", fmt.Errorf("project ID must not be empty")
	}
	token := helpers.ResolveStaticOrgAuth(o.token)
	if token == "" {
		return "", "", fmt.Errorf("missing organization API token; create one in Organization Settings > API Tokens and pass it with --token or DEPOT_TOKEN")
	}
	return projectID, token, nil
}

func trustPolicyProvider(name string) (corev1.TrustPolicyProvider, error) {
	switch name {
	case "github":
		return corev1.TrustPolicyProvider_TRUST_POLICY_PROVIDER_GITHUB, nil
	case "gitlab":
		return corev1.TrustPolicyProvider_TRUST_POLICY_PROVIDER_GITLAB, nil
	case "circleci":
		return corev1.TrustPolicyProvider_TRUST_POLICY_PROVIDER_CIRCLE_CI, nil
	case "buildkite":
		return corev1.TrustPolicyProvider_TRUST_POLICY_PROVIDER_BUILDKITE, nil
	default:
		return 0, fmt.Errorf("unsupported provider %q (valid: github, gitlab, circleci, buildkite)", name)
	}
}

func newCmdTrustPolicyList() *cobra.Command {
	var o trustPolicyOptions
	var provider, pageToken string
	var pageSize int32
	cmd := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List a page of OIDC trust policies", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &corev1.ListTrustPoliciesRequest{PageSize: proto.Int32(pageSize)}
			if cmd.Flags().Changed("provider") {
				value, err := trustPolicyProvider(provider)
				if err != nil {
					return err
				}
				req.Provider = &value
			}
			if pageSize < 1 || pageSize > 250 {
				return fmt.Errorf("--page-size must be between 1 and 250")
			}
			if pageToken != "" {
				req.PageToken = &pageToken
			}
			projectID, token, err := o.resolve()
			if err != nil {
				return err
			}
			req.ProjectId = projectID
			resp, err := api.NewSDKProjectsClient().ListTrustPolicies(cmd.Context(), api.WithAuthentication(connect.NewRequest(req), token))
			if err != nil {
				return trustPolicyError("list", err)
			}
			if o.output == "json" {
				return writeTrustPolicyJSON(cmd.OutOrStdout(), resp.Msg)
			}
			if err := writeTrustPolicies(cmd.OutOrStdout(), resp.Msg.TrustPolicies); err != nil {
				return err
			}
			if next := resp.Msg.GetNextPageToken(); next != "" {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Next page token: %s\nPass it with --page-token and keep --project-id and --provider unchanged.\n", trustPolicyCell(next))
			}
			return err
		},
	}
	o.flags(cmd)
	cmd.Flags().StringVar(&provider, "provider", "", "Filter by provider (github, gitlab, circleci, buildkite)")
	cmd.Flags().Int32Var(&pageSize, "page-size", 250, "Policies per page (1-250)")
	cmd.Flags().StringVar(&pageToken, "page-token", "", "Next page token from a previous response")
	return cmd
}

func newCmdTrustPolicyAdd() *cobra.Command {
	var o trustPolicyOptions
	var provider, owner, repository, namespaceID, gitlabProjectID, organizationUUID, projectUUID, organizationSlug, pipelineSlug string
	var publicPRs, protectedRefs bool
	cmd := &cobra.Command{
		Use: "add", Short: "Add an OIDC trust policy", Args: cobra.NoArgs,
		Long: `Add a policy for github, gitlab, circleci, or buildkite.

Adding an existing policy returns its current settings without changing its
restrictions. Use update to change those settings. Output shows the saved policy.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := trustPolicyProvider(provider); err != nil {
				return err
			}
			providerFlags := map[string][]string{
				"github":    {"repository-owner", "repository", "allow-public-pull-requests"},
				"gitlab":    {"namespace-id", "gitlab-project-id", "protected-refs-only"},
				"circleci":  {"organization-uuid", "project-uuid"},
				"buildkite": {"organization-slug", "pipeline-slug"},
			}
			for _, p := range []string{"github", "gitlab", "circleci", "buildkite"} {
				for _, flag := range providerFlags[p] {
					if p != provider && cmd.Flags().Changed(flag) {
						return fmt.Errorf("--%s is only valid with --provider %s", flag, p)
					}
				}
			}
			req := &corev1.AddTrustPolicyRequest{}
			switch provider {
			case "github":
				req.Provider = &corev1.AddTrustPolicyRequest_Github{Github: &corev1.TrustPolicy_GitHub{RepositoryOwner: owner, Repository: repository, AllowPublicPullRequests: publicPRs}}
			case "gitlab":
				req.Provider = &corev1.AddTrustPolicyRequest_Gitlab{Gitlab: &corev1.TrustPolicy_GitLab{NamespaceId: namespaceID, ProjectId: gitlabProjectID, ProtectedRefsOnly: protectedRefs}}
			case "circleci":
				req.Provider = &corev1.AddTrustPolicyRequest_Circleci{Circleci: &corev1.TrustPolicy_CircleCI{OrganizationUuid: organizationUUID, ProjectUuid: projectUUID}}
			case "buildkite":
				req.Provider = &corev1.AddTrustPolicyRequest_Buildkite{Buildkite: &corev1.TrustPolicy_Buildkite{OrganizationSlug: organizationSlug, PipelineSlug: pipelineSlug}}
			}
			// Each provider's first two flags identify the external organization and resource.
			for _, flag := range providerFlags[provider][:2] {
				value, _ := cmd.Flags().GetString(flag)
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("--%s is required for --provider %s", flag, provider)
				}
			}
			projectID, token, err := o.resolve()
			if err != nil {
				return err
			}
			req.ProjectId = projectID
			resp, err := api.NewSDKProjectsClient().AddTrustPolicy(cmd.Context(), api.WithAuthentication(connect.NewRequest(req), token))
			if err != nil {
				return trustPolicyError("add", err)
			}
			return writeTrustPolicy(cmd.OutOrStdout(), resp.Msg.GetTrustPolicy(), o.output)
		},
	}
	o.flags(cmd)
	cmd.Flags().StringVar(&provider, "provider", "", "Provider (github, gitlab, circleci, buildkite; required)")
	cmd.Flags().StringVar(&owner, "repository-owner", "", "GitHub repository owner")
	cmd.Flags().StringVar(&repository, "repository", "", "GitHub repository name")
	cmd.Flags().BoolVar(&publicPRs, "allow-public-pull-requests", false, "Allow public GitHub pull requests from forks")
	cmd.Flags().StringVar(&namespaceID, "namespace-id", "", "Numeric GitLab namespace ID")
	cmd.Flags().StringVar(&gitlabProjectID, "gitlab-project-id", "", "Numeric GitLab project ID (not the Depot project ID)")
	cmd.Flags().BoolVar(&protectedRefs, "protected-refs-only", false, "Restrict GitLab authentication to protected refs")
	cmd.Flags().StringVar(&organizationUUID, "organization-uuid", "", "CircleCI organization UUID")
	cmd.Flags().StringVar(&projectUUID, "project-uuid", "", "CircleCI project UUID")
	cmd.Flags().StringVar(&organizationSlug, "organization-slug", "", "Buildkite organization slug")
	cmd.Flags().StringVar(&pipelineSlug, "pipeline-slug", "", "Buildkite pipeline slug")
	return cmd
}

func newCmdTrustPolicyUpdate() *cobra.Command {
	var o trustPolicyOptions
	var publicPRs, protectedRefs bool
	cmd := &cobra.Command{
		Use: "update <trust-policy-id>", Short: "Update a GitHub or GitLab policy's restrictions", Args: cobra.ExactArgs(1),
		Long: `Update a GitHub or GitLab policy's restrictions. Only explicitly supplied
flags are changed. Use --protected-refs-only=false to allow all GitLab refs, or
--allow-public-pull-requests=false to disallow public GitHub pull requests.

Provider identities cannot be edited; add a new policy and remove the old one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("trust policy ID must not be empty")
			}
			req := &corev1.UpdateTrustPolicyRequest{TrustPolicyId: args[0]}
			if cmd.Flags().Changed("allow-public-pull-requests") {
				req.AllowPublicPullRequests = &publicPRs
			}
			if cmd.Flags().Changed("protected-refs-only") {
				req.ProtectedRefsOnly = &protectedRefs
			}
			if req.AllowPublicPullRequests == nil && req.ProtectedRefsOnly == nil {
				return fmt.Errorf("--allow-public-pull-requests or --protected-refs-only is required")
			}
			projectID, token, err := o.resolve()
			if err != nil {
				return err
			}
			req.ProjectId = projectID
			resp, err := api.NewSDKProjectsClient().UpdateTrustPolicy(cmd.Context(), api.WithAuthentication(connect.NewRequest(req), token))
			if err != nil {
				return trustPolicyError("update", err)
			}
			return writeTrustPolicy(cmd.OutOrStdout(), resp.Msg.GetTrustPolicy(), o.output)
		},
	}
	o.flags(cmd)
	cmd.Flags().BoolVar(&publicPRs, "allow-public-pull-requests", false, "Allow public GitHub pull requests from forks (explicit true or false)")
	cmd.Flags().BoolVar(&protectedRefs, "protected-refs-only", false, "Restrict GitLab authentication to protected refs (explicit true or false)")
	cmd.MarkFlagsMutuallyExclusive("allow-public-pull-requests", "protected-refs-only")
	return cmd
}

func newCmdTrustPolicyRemove() *cobra.Command {
	var o trustPolicyOptions
	cmd := &cobra.Command{
		Use: "remove <trust-policy-id>", Aliases: []string{"rm"}, Short: "Remove an OIDC trust policy", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("trust policy ID must not be empty")
			}
			projectID, token, err := o.resolve()
			if err != nil {
				return err
			}
			req := &corev1.RemoveTrustPolicyRequest{ProjectId: projectID, TrustPolicyId: args[0]}
			resp, err := api.NewSDKProjectsClient().RemoveTrustPolicy(cmd.Context(), api.WithAuthentication(connect.NewRequest(req), token))
			if err != nil {
				return trustPolicyError("remove", err)
			}
			if o.output == "json" {
				return writeTrustPolicyJSON(cmd.OutOrStdout(), resp.Msg)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed trust policy %s from project %s.\n", trustPolicyCell(args[0]), trustPolicyCell(projectID))
			return err
		},
	}
	o.flags(cmd)
	return cmd
}

func trustPolicyError(action string, err error) error {
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		return fmt.Errorf("the API does not support this trust-policy operation; use an API deployment with OIDC policy management support: %w", err)
	}
	return fmt.Errorf("failed to %s trust policy: %w", action, err)
}
