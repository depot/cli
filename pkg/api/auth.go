package api

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"connectrpc.com/connect"
	"github.com/briandowns/spinner"
	"github.com/charmbracelet/huh"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
)

// openURL opens the specified URL in the user's default browser.
// It handles different operating systems appropriately.
func openURL(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start", url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	default:
		return fmt.Errorf("unsupported platform")
	}

	return exec.Command(cmd, args...).Start()
}

func AuthorizeDevice(ctx context.Context) (*cliv1beta1.FinishLoginResponse, error) {
	client := NewLoginClient()
	req := cliv1beta1.StartLoginRequest{}
	response, err := client.StartLogin(ctx, connect.NewRequest(&req))
	if err != nil {
		return nil, err
	}

	// Show the URL first
	fmt.Printf("Please visit the following URL to authenticate the CLI:\n\n    %s\n\n", response.Msg.ApproveUrl)

	// Ask user if they want to open the browser automatically
	var openBrowser bool
	prompt := huh.NewConfirm().
		Title("Open link in browser?").
		Affirmative("Yes").
		Negative("No").
		Value(&openBrowser)

	if err := prompt.Run(); err != nil {
		// If prompt fails, continue without opening browser
		fmt.Printf("Continuing without opening browser...\n")
	} else if openBrowser {
		// User chose to open browser
		fmt.Printf("Opening your browser...\n")
		if err := openURL(response.Msg.ApproveUrl); err != nil {
			fmt.Printf("Could not open browser: %v\n", err)
		}
	}

	spinner := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
	spinner.Prefix = "Waiting for approval "
	spinner.Start()
	defer spinner.Stop()

	stream, err := client.FinishLogin(ctx, connect.NewRequest(&cliv1beta1.FinishLoginRequest{
		Id: response.Msg.Id,
	}))
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	for stream.Receive() {
		response := stream.Msg()
		stream.Close()
		return response, nil
	}

	if err := stream.Err(); err != nil {
		return nil, connect.NewError(connect.CodeUnknown, err)
	}

	return nil, connect.NewError(connect.CodeUnknown, fmt.Errorf("unknown error"))
}
