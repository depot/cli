package registry

import (
	"fmt"

	v1 "buf.build/gen/go/depot/api/protocolbuffers/go/depot/registry/v1beta1"
	"github.com/depot/cli/pkg/api"
	"github.com/spf13/cobra"
)

func repositoryCommands(o *options) *cobra.Command {
	group := &cobra.Command{Use: "repository", Aliases: []string{"repositories"}, Short: "Manage repositories"}
	var p pagination
	list := command("list", "List repositories", 0, func(cmd *cobra.Command, args []string) error {
		if err := p.validate(); err != nil {
			return err
		}
		resp, err := call(cmd, o, &v1.ListRepositoriesRequest{Page: p.page, PageSize: p.size, Query: p.filter(cmd)}, api.NewSDKRegistryClient().ListRepositories)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})
	p.flags(list)
	var upstreamID, upstreamRepository string
	create := command("create <repository>", "Create a repository", 1, func(cmd *cobra.Command, args []string) error {
		if err := validateRepository(args[0], false); err != nil {
			return err
		}
		req := &v1.CreateRepositoryRequest{Repository: args[0]}
		if cmd.Flags().Changed("upstream-id") || cmd.Flags().Changed("upstream-repository") {
			if upstreamID == "" || upstreamRepository == "" {
				return fmt.Errorf("--upstream-id and --upstream-repository must both be nonempty")
			}
			if err := validateRepository(upstreamRepository, false); err != nil {
				return err
			}
			req.Upstream = &v1.RepositoryUpstream{UpstreamRegistryId: upstreamID, UpstreamRepository: upstreamRepository}
		}
		resp, err := call(cmd, o, req, api.NewSDKRegistryClient().CreateRepository)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})
	create.Flags().StringVar(&upstreamID, "upstream-id", "", "Upstream registry ID for a pull-through repository")
	create.Flags().StringVar(&upstreamRepository, "upstream-repository", "", "Repository path at the upstream registry")
	remove := destructive(command("delete <repository>", "Delete a repository and its images", 1, func(cmd *cobra.Command, args []string) error {
		if err := validateRepository(args[0], false); err != nil {
			return err
		}
		if err := confirm(cmd); err != nil {
			return err
		}
		resp, err := call(cmd, o, &v1.DeleteRepositoryRequest{Repository: args[0]}, api.NewSDKRegistryClient().DeleteRepository)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	}))
	group.AddCommand(list, create, remove)
	return group
}

func imageCommands(o *options) *cobra.Command {
	group := &cobra.Command{Use: "image", Aliases: []string{"images"}, Short: "Inspect and delete repository images"}
	var p pagination
	var status string
	list := command("list <repository>", "List images in one repository", 1, func(cmd *cobra.Command, args []string) error {
		if err := validateRepository(args[0], false); err != nil {
			return err
		}
		if err := p.validate(); err != nil {
			return err
		}
		statuses := map[string]v1.ImageTagStatus{"all": v1.ImageTagStatus_IMAGE_TAG_STATUS_UNSPECIFIED, "tagged": v1.ImageTagStatus_IMAGE_TAG_STATUS_TAGGED, "untagged": v1.ImageTagStatus_IMAGE_TAG_STATUS_UNTAGGED}
		value, ok := statuses[status]
		if !ok {
			return fmt.Errorf("--tag-status must be all, tagged, or untagged")
		}
		resp, err := call(cmd, o, &v1.ListImagesRequest{Repository: args[0], Page: p.page, PageSize: p.size, TagQuery: p.filter(cmd), TagStatus: value}, api.NewSDKRegistryClient().ListImages)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})
	p.flags(list)
	list.Flags().Lookup("query").Usage = "Filter image tags"
	list.Flags().StringVar(&status, "tag-status", "all", "Filter: all, tagged, or untagged")
	inspect := command("inspect <repository> <tag-or-digest>", "Inspect an image (manifest is base64 in JSON)", 2, func(cmd *cobra.Command, args []string) error {
		if err := validateRepository(args[0], false); err != nil {
			return err
		}
		resp, err := call(cmd, o, &v1.GetImageDetailRequest{Repository: args[0], Reference: args[1]}, api.NewSDKRegistryClient().GetImageDetail)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})
	var digests, tags []string
	remove := destructive(command("delete <repository>", "Delete images by digest or tag", 1, func(cmd *cobra.Command, args []string) error {
		if err := validateRepository(args[0], false); err != nil {
			return err
		}
		if err := confirm(cmd); err != nil {
			return err
		}
		if len(digests)+len(tags) == 0 {
			return fmt.Errorf("at least one --digest or --tag is required")
		}
		req := &v1.BatchDeleteImageRequest{Repository: args[0]}
		for _, value := range digests {
			if !digestPattern.MatchString(value) {
				return fmt.Errorf("--digest must be a sha256 digest")
			}
			value := value
			req.ImageIds = append(req.ImageIds, &v1.ImageIdentifier{ImageDigest: &value})
		}
		for _, value := range tags {
			if !tagPattern.MatchString(value) {
				return fmt.Errorf("invalid --tag")
			}
			value := value
			req.ImageIds = append(req.ImageIds, &v1.ImageIdentifier{ImageTag: &value})
		}
		resp, err := call(cmd, o, req, api.NewSDKRegistryClient().BatchDeleteImage)
		if err != nil {
			return err
		}
		if err := write(cmd, resp); err != nil {
			return err
		}
		if len(resp.Failures) > 0 {
			return fmt.Errorf("%d image deletion(s) failed; see failures in JSON output", len(resp.Failures))
		}
		return nil
	}))
	remove.Flags().StringArrayVar(&digests, "digest", nil, "Image digest to delete (repeatable; removes all associated tags)")
	remove.Flags().StringArrayVar(&tags, "tag", nil, "Image tag to delete (repeatable)")
	group.AddCommand(list, inspect, remove)
	return group
}

func tagCommands(o *options) *cobra.Command {
	group := &cobra.Command{Use: "tag", Short: "Manage image tags"}
	group.AddCommand(destructive(command("delete <repository> <tag>", "Remove a tag from an image", 2, func(cmd *cobra.Command, args []string) error {
		if err := validateRepository(args[0], false); err != nil {
			return err
		}
		if !tagPattern.MatchString(args[1]) {
			return fmt.Errorf("invalid tag")
		}
		if err := confirm(cmd); err != nil {
			return err
		}
		resp, err := call(cmd, o, &v1.DeleteTagRequest{Repository: args[0], Tag: args[1]}, api.NewSDKRegistryClient().DeleteTag)
		if err != nil {
			return err
		}
		return write(cmd, resp)
	})))
	return group
}
