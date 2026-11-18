package registry

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	v1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/registry/v1beta1"
	"github.com/depot/cli/pkg/api"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func tokenCommands(o *options) *cobra.Command {
	group := &cobra.Command{Use: "token", Aliases: []string{"tokens"}, Short: "Manage registry pull/push tokens"}
	var p pagination
	list := command("list", "List token metadata (never secrets)", 0, func(cmd *cobra.Command, args []string) error {
		if err := p.validate(); err != nil {
			return err
		}
		resp, err := call(cmd, o, &v1.ListTokensRequest{Page: p.page, PageSize: p.size, Query: p.filter(cmd)}, api.NewSDKRegistryClient().ListTokens)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})
	p.flags(list)
	var permission, expires, description string
	var scopes []string
	create := command("create <name>", "Create a token and print its one-time secret", 1, func(cmd *cobra.Command, args []string) error {
		if len(args[0]) > 100 || len(description) > 1000 {
			return fmt.Errorf("name must be at most 100 characters and description at most 1000")
		}
		req := &v1.CreateTokenRequest{Name: args[0], RepositoryScopes: scopes}
		switch permission {
		case "pull":
			req.Permission = v1.RegistryAccessTokenPermission_REGISTRY_ACCESS_TOKEN_PERMISSION_PULL
		case "push":
			req.Permission = v1.RegistryAccessTokenPermission_REGISTRY_ACCESS_TOKEN_PERMISSION_PUSH
		default:
			return fmt.Errorf("--permission must be pull or push")
		}
		if len(scopes) == 0 || len(scopes) > 50 {
			return fmt.Errorf("provide 1-50 --scope values; use '**' explicitly for all repositories")
		}
		for _, scope := range scopes {
			if err := validateRepository(scope, true); err != nil {
				return err
			}
		}
		if cmd.Flags().Changed("description") {
			req.Description = &description
		}
		if cmd.Flags().Changed("expires-at") {
			expiry, err := time.Parse(time.RFC3339, expires)
			if err != nil || !expiry.After(time.Now()) {
				return fmt.Errorf("--expires-at must be a future RFC3339 timestamp")
			}
			req.ExpiresAt = timestamppb.New(expiry)
		}
		resp, err := call(cmd, o, req, api.NewSDKRegistryClient().CreateToken)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})
	create.Flags().StringVar(&permission, "permission", "", "Token permission: pull or push (required)")
	create.Flags().StringArrayVar(&scopes, "scope", nil, "Repository scope, such as team/* (required, repeatable)")
	create.Flags().StringVar(&expires, "expires-at", "", "Expiry as an RFC3339 timestamp; omitted means no expiry")
	create.Flags().StringVar(&description, "description", "", "Token description")
	create.Long = "Create a registry token. The secret appears once in stdout; store it securely.\nOrganization-token creation must be enabled for your organization."
	revoke := destructive(command("revoke <token-id>", "Revoke a registry access token", 1, func(cmd *cobra.Command, args []string) error {
		if err := confirm(cmd); err != nil {
			return err
		}
		resp, err := call(cmd, o, &v1.RevokeTokenRequest{TokenId: args[0]}, api.NewSDKRegistryClient().RevokeToken)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	}))
	group.AddCommand(list, create, revoke)
	return group
}

func retentionCommands(o *options) *cobra.Command {
	group := &cobra.Command{Use: "retention", Short: "Manage explicit retention policies", Long: "Manage explicit retention policies by repository path or pattern. Quote '*' and '**'.\nDeleting a policy removes its override; inherited retention can still apply."}
	get := command("get <repository-or-pattern>", "Get the explicit retention policy", 1, func(cmd *cobra.Command, args []string) error {
		if err := validateRepository(args[0], true); err != nil {
			return err
		}
		resp, err := call(cmd, o, &v1.GetRetentionPolicyRequest{Repository: args[0]}, api.NewSDKRegistryClient().GetRetentionPolicy)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})
	var count, days int32
	set := destructive(command("set <repository-or-pattern>", "Set retention (can delete older images)", 1, func(cmd *cobra.Command, args []string) error {
		if err := validateRepository(args[0], true); err != nil {
			return err
		}
		if err := confirm(cmd); err != nil {
			return err
		}
		hasCount, hasDays := cmd.Flags().Changed("keep-count"), cmd.Flags().Changed("keep-days")
		if hasCount == hasDays || (hasCount && count <= 0) || (hasDays && days <= 0) {
			return fmt.Errorf("set exactly one positive --keep-count or --keep-days")
		}
		req := &v1.UpsertRetentionPolicyRequest{Repository: args[0], Enabled: true}
		if hasCount {
			req.KeepCount = &count
		} else {
			req.KeepDays = &days
		}
		resp, err := call(cmd, o, req, api.NewSDKRegistryClient().UpsertRetentionPolicy)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	}))
	set.Flags().Int32Var(&count, "keep-count", 0, "Number of images to keep")
	set.Flags().Int32Var(&days, "keep-days", 0, "Number of days to keep images")
	remove := destructive(command("delete <repository-or-pattern>", "Remove an explicit retention policy", 1, func(cmd *cobra.Command, args []string) error {
		if err := validateRepository(args[0], true); err != nil {
			return err
		}
		if err := confirm(cmd); err != nil {
			return err
		}
		resp, err := call(cmd, o, &v1.DeleteRetentionPolicyRequest{Repository: args[0]}, api.NewSDKRegistryClient().DeleteRetentionPolicy)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	}))
	group.AddCommand(get, set, remove)
	return group
}

func upstreamCommands(o *options) *cobra.Command {
	group := &cobra.Command{Use: "upstream", Aliases: []string{"upstreams"}, Short: "Manage upstream registries"}
	list := command("list", "List upstream registries (passwords are never returned)", 0, func(cmd *cobra.Command, args []string) error {
		resp, err := call(cmd, o, &v1.ListUpstreamRegistriesRequest{}, api.NewSDKRegistryClient().ListUpstreamRegistries)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})
	var url, username, authType, role string
	var fromStdin bool
	create := command("create <name>", "Create an upstream registry", 1, func(cmd *cobra.Command, args []string) error {
		if err := validateUpstreamURL(url); err != nil {
			return err
		}
		req := &v1.CreateUpstreamRegistryRequest{Name: args[0], UpstreamUrl: url, Username: username}
		switch authType {
		case "basic":
			req.AuthType = v1.UpstreamRegistryAuthType_UPSTREAM_REGISTRY_AUTH_TYPE_BASIC
			if cmd.Flags().Changed("role-arn") {
				return fmt.Errorf("--role-arn requires --auth-type ecr")
			}
		case "ecr":
			req.AuthType = v1.UpstreamRegistryAuthType_UPSTREAM_REGISTRY_AUTH_TYPE_ECR
			if strings.TrimSpace(role) == "" {
				return fmt.Errorf("--role-arn is required for ECR")
			}
			if fromStdin || cmd.Flags().Changed("username") {
				return fmt.Errorf("ECR uses a role, not username/password")
			}
			req.RoleArn = &role
		default:
			return fmt.Errorf("--auth-type must be basic or ecr")
		}
		if _, err := o.auth(); err != nil {
			return err
		}
		if fromStdin {
			var err error
			req.Password, err = password(cmd)
			if err != nil {
				return err
			}
		}
		resp, err := call(cmd, o, req, api.NewSDKRegistryClient().CreateUpstreamRegistry)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})
	create.Flags().StringVar(&url, "url", "", "Upstream registry URL (required)")
	create.Flags().StringVar(&username, "username", "", "Basic-auth username")
	create.Flags().StringVar(&authType, "auth-type", "basic", "Authentication type: basic or ecr")
	create.Flags().StringVar(&role, "role-arn", "", "AWS role ARN for ECR")
	create.Flags().BoolVar(&fromStdin, "password-stdin", false, "Read the password from stdin")
	var updateURL, updateUsername, updateRole string
	var updatePassword bool
	update := command("update <upstream-id>", "Update only the specified upstream fields", 1, func(cmd *cobra.Command, args []string) error {
		req := &v1.UpdateUpstreamRegistryRequest{UpstreamRegistryId: args[0]}
		if cmd.Flags().Changed("url") {
			if err := validateUpstreamURL(updateURL); err != nil {
				return err
			}
			req.UpstreamUrl = &updateURL
		}
		if cmd.Flags().Changed("username") {
			req.Username = &updateUsername
		}
		if cmd.Flags().Changed("role-arn") {
			req.RoleArn = &updateRole
		}
		if req.UpstreamUrl == nil && req.Username == nil && req.RoleArn == nil && !updatePassword {
			return fmt.Errorf("specify at least one field to update")
		}
		if _, err := o.auth(); err != nil {
			return err
		}
		if updatePassword {
			value, err := password(cmd)
			if err != nil {
				return err
			}
			req.Password = &value
		}
		resp, err := call(cmd, o, req, api.NewSDKRegistryClient().UpdateUpstreamRegistry)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})
	update.Flags().StringVar(&updateURL, "url", "", "New upstream URL")
	update.Flags().StringVar(&updateUsername, "username", "", "New username (empty clears it)")
	update.Flags().StringVar(&updateRole, "role-arn", "", "New AWS role ARN")
	update.Flags().BoolVar(&updatePassword, "password-stdin", false, "Read a new password from stdin (empty clears it)")
	remove := destructive(command("delete <upstream-id>", "Delete an upstream registry", 1, func(cmd *cobra.Command, args []string) error {
		if err := confirm(cmd); err != nil {
			return err
		}
		resp, err := call(cmd, o, &v1.DeleteUpstreamRegistryRequest{UpstreamRegistryId: args[0]}, api.NewSDKRegistryClient().DeleteUpstreamRegistry)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	}))
	group.AddCommand(list, create, update, remove)
	return group
}
