package mr

import (
	"connectrpc.com/connect"
	"fmt"
	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
	"strings"
)

func newViewer(o *options) *cobra.Command {
	return &cobra.Command{
		Use: "viewer", Short: "Show the authenticated user in the selected organization",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.authenticateUser(cmd); err != nil {
				return err
			}
			response, err := api.NewMergeRequestClient().GetViewer(cmd.Context(), request(o, &code.GetViewerRequest{}))
			if err != nil {
				if connect.CodeOf(err) == connect.CodeUnimplemented || (connect.CodeOf(err) == connect.CodeUnknown && strings.Contains(err.Error(), "Route POST:/depot.code.v1beta2.MergeRequestService/GetViewer not found")) {
					return fmt.Errorf("this Depot server does not support viewer identity yet")
				}
				return err
			}
			result := response.Msg
			if result.OrgId == "" || result.Viewer == nil || result.Viewer.Id == "" || result.Viewer.Kind != code.MergeRequestActor_KIND_USER {
				return fmt.Errorf("API returned no authenticated user")
			}
			if o.org != "" && result.OrgId != o.org {
				return fmt.Errorf("API returned a viewer from another organization")
			}
			if o.json {
				return o.writeJSON(cmd, result)
			}
			user := printable(result.Viewer.Id)
			if name := printable(result.Viewer.DisplayName); name != "" {
				user += " (" + name + ")"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "User: %s\nOrganization: %s\n", user, printable(result.OrgId))
			return err
		},
	}
}
