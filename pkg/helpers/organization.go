package helpers

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
)

type Organization struct {
	OrgId string
	Name  string
}

func RetrieveOrganizations() ([]*Organization, error) {
	// Fake organizations
	return []*Organization{
		{
			OrgId: "1",
			Name:  "Organization 1",
		},
		{
			OrgId: "2",
			Name:  "Organization 2",
		},
	}, nil
}

func SelectOrganization() (*Organization, error) {
	organizations, err := RetrieveOrganizations()
	if err != nil {
		return nil, err
	}

	if len(organizations) == 1 {
		return organizations[0], nil
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
