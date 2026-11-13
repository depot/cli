package mr

import (
	"fmt"

	"github.com/depot/cli/pkg/api"
	code "github.com/depot/cli/pkg/proto/depot/code/v1beta2"
	"github.com/spf13/cobra"
)

// baseFlag returns the validated --base value, or nil when the flag was not passed.
func baseFlag(cmd *cobra.Command, base string) (*string, error) {
	if !cmd.Flags().Changed("base") {
		return nil, nil
	}
	name, err := branch(cmd, []string{base})
	if err != nil {
		return nil, err
	}
	return &name, nil
}

// create opens a merge request.
// An explicit base goes through CreateMergeRequestWithBase,
// which an older server refuses instead of silently using the default branch.
func (o *options) create(cmd *cobra.Command, req *code.CreateMergeRequestRequest) (*code.CreateMergeRequestResponse, error) {
	client := api.NewMergeRequestClient()
	out := &code.CreateMergeRequestResponse{}
	if req.BaseRef == nil {
		resp, err := client.CreateMergeRequest(cmd.Context(), request(o, req))
		if err != nil {
			return nil, err
		}
		out = resp.Msg
	} else {
		resp, err := client.CreateMergeRequestWithBase(cmd.Context(), request(o, &code.CreateMergeRequestWithBaseRequest{
			RepositoryId: req.RepositoryId, HeadRef: req.HeadRef, Title: req.Title, Body: req.Body, BaseRef: req.BaseRef,
		}))
		if err != nil {
			return nil, err
		}
		out.MergeRequest = resp.Msg.MergeRequest
	}
	mr := out.MergeRequest
	if mr == nil || mr.Id == "" {
		return nil, fmt.Errorf("API returned no merge request")
	}
	// Creating for a branch that already has an open merge request returns that one unchanged.
	if req.BaseRef != nil && mr.BaseRef != *req.BaseRef {
		return nil, fmt.Errorf("branch %q already has open merge request %s into %q; change its base with `depot mr edit %s --base %s`",
			mr.HeadRef, mr.Id, mr.BaseRef, mr.HeadRef, *req.BaseRef)
	}
	return out, nil
}
