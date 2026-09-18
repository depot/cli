package helpers

import (
	"context"
	"fmt"

	"charm.land/huh/v2"
)

// OnboardProject initializes a depot.json and saves it at the CWD if the user confirms.
func OnboardProject(ctx context.Context, token string) (*SelectedProject, error) {
	selectedProject, err := InitializeProject(ctx, token, "")
	if err != nil {
		return nil, err
	}

	if ConfirmSaveProject(selectedProject) {
		err = selectedProject.Save()
		if err != nil {
			return nil, err
		}
	}

	return selectedProject, nil
}

// ConfirmSaveProject prompts the user to save the project ID.
// If the user is not in a terminal, this will return false because we require confirmation.
func ConfirmSaveProject(p *SelectedProject) bool {
	if !IsTerminal() {
		return false
	}

	prompt := fmt.Sprintf("Selected project %s (%s)\nCreate a depot.json file to remember this project for future builds?", p.Name, p.ID)

	shouldSave := true
	if err := huh.NewConfirm().
		Title(prompt).
		Value(&shouldSave).
		Run(); err != nil {
		return false
	}

	return shouldSave
}
