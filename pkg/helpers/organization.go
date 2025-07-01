package helpers

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/charmbracelet/huh"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/config"
	corev1 "github.com/depot/cli/pkg/proto/depot/core/v1"
)

type Organization struct {
	OrgId string
	Name  string
}

func RetrieveOrganizations() ([]*Organization, error) {
	client := api.NewOrganizationsClient()
	req := corev1.ListOrganizationRequest{}
	resp, err := client.ListOrganizations(
		context.Background(),
		api.WithAuthentication(connect.NewRequest(&req), config.GetApiToken()),
	)
	if err != nil {
		return nil, err
	}

	organizations := []*Organization{}
	for _, org := range resp.Msg.Organizations {
		organizations = append(organizations, &Organization{
			OrgId: org.OrgId,
			Name:  org.Name,
		})
	}

	return organizations, nil
}

func SelectOrganization() (*Organization, error) {
	organizations, err := RetrieveOrganizations()
	if err != nil {
		return nil, err
	}

	huhOptions := make([]huh.Option[*Organization], len(organizations))
	for i, org := range organizations {
		huhOptions[i] = huh.NewOption(fmt.Sprintf("%s (%s)", org.Name, org.OrgId), org)
	}

	var selectedOrg *Organization

	err = huh.NewSelect[*Organization]().
		Title("Select an organization").
		Options(huhOptions...).
		Value(&selectedOrg).
		Run()
	if err != nil {
		return nil, err
	}

	if selectedOrg == nil {
		return nil, errors.New("no organization selected")
	}

	return selectedOrg, nil
}
