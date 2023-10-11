package helpers

import (
	"context"

	"github.com/erikgeiser/promptkit/confirmation"
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

	input := confirmation.New("Remember this project for future builds?", confirmation.NewValue(true))
	input.Template = confirmation.TemplateArrow
	input.ResultTemplate = confirmation.ResultTemplateArrow

	// vim
	input.KeyMap.SelectYes = append(input.KeyMap.SelectYes, "h")
	input.KeyMap.SelectNo = append(input.KeyMap.SelectNo, "l")

	shouldSave, err := input.RunPrompt()
	if err != nil {
		return false
	}

	return shouldSave
}
