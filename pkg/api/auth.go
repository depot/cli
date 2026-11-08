package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"charm.land/lipgloss/v2"
	"connectrpc.com/connect"
	"github.com/briandowns/spinner"
	cliv1beta1 "github.com/depot/cli/pkg/proto/depot/cli/v1beta1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1beta1/cliv1beta1connect"
	"github.com/mattn/go-isatty"
	"github.com/muesli/cancelreader"
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

var errLoginCanceled = errors.New("login canceled")

var errLoginExpired = errors.New("the login request expired or is no longer valid; run `depot login` to try again")

var (
	loginTitleStyle = lipgloss.NewStyle().Bold(true)
	loginDimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	loginCodeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
)

func printLoginInstructions(login *cliv1beta1.StartLoginResponse, interactive bool) {
	lipgloss.Println()
	lipgloss.Println(loginTitleStyle.Render("Log in to Depot"))
	lipgloss.Println()
	if login.VerificationCode != "" {
		lipgloss.Println(loginDimStyle.Render("Code  ") + loginCodeStyle.Render(login.VerificationCode))
	}
	lipgloss.Println(loginDimStyle.Render("URL   ") + login.ApproveUrl)
	lipgloss.Println()
	if login.VerificationCode != "" {
		lipgloss.Println("Check that the approval page shows the same code.")
	}
	if interactive {
		lipgloss.Println("Press " + loginTitleStyle.Render("Enter") + " to open the URL in your browser.")
	} else {
		lipgloss.Println("Open the URL to approve this login.")
	}
	lipgloss.Println()
}

func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func AuthorizeDevice(ctx context.Context) (*cliv1beta1.FinishLoginResponse, error) {
	client := NewLoginClient()
	req := cliv1beta1.StartLoginRequest{}
	response, err := client.StartLogin(ctx, connect.NewRequest(&req))
	if err != nil {
		return nil, err
	}

	results := make(chan finishLoginResult, 1)
	go func() {
		token, err := finishLogin(ctx, client, response.Msg)
		results <- finishLoginResult{token, err}
	}()

	interactive := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	printLoginInstructions(response.Msg, interactive)
	if interactive {
		if result, finished := waitForEnterOrResult(results); finished {
			return result.token, result.err
		}
		if err := openURL(response.Msg.ApproveUrl); err != nil {
			lipgloss.Println(loginDimStyle.Render("Could not open a browser. Open the URL above to continue."))
		}
	}

	spinner := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
	spinner.Suffix = " Waiting for approval..."
	spinner.Start()
	defer spinner.Stop()

	result := <-results
	return result.token, result.err
}

type finishLoginResult struct {
	token *cliv1beta1.FinishLoginResponse
	err   error
}

// On Windows, cancelreader turns off line input and Ctrl-C processing,
// so Enter arrives as '\r' and Ctrl-C arrives as a byte.
const ctrlC = 0x03

// waitForEnterOrResult stops reading standard input when the login finishes first,
// so that a later prompt receives the next key press.
func waitForEnterOrResult(results <-chan finishLoginResult) (finishLoginResult, bool) {
	reader, err := cancelreader.NewReader(os.Stdin)
	if err != nil {
		return <-results, true
	}
	defer reader.Close()

	pressed := make(chan struct{})
	interrupted := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		key := make([]byte, 1)
		for {
			if _, err := reader.Read(key); err != nil {
				return
			}
			switch key[0] {
			case '\r', '\n':
				close(pressed)
				return
			case ctrlC:
				close(interrupted)
				return
			}
		}
	}()

	select {
	case <-pressed:
		return finishLoginResult{}, false
	case <-interrupted:
		return finishLoginResult{err: errLoginCanceled}, true
	case result := <-results:
		if reader.Cancel() {
			<-readDone
		}
		return result, true
	}
}

func finishLogin(ctx context.Context, client cliv1beta1connect.LoginServiceClient, login *cliv1beta1.StartLoginResponse) (*cliv1beta1.FinishLoginResponse, error) {
	stream, err := client.FinishLogin(ctx, connect.NewRequest(&cliv1beta1.FinishLoginRequest{
		Id:         login.Id,
		PollSecret: login.PollSecret,
	}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return nil, errLoginExpired
	}
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
		if connect.CodeOf(err) == connect.CodeNotFound {
			return nil, errLoginExpired
		}
		return nil, connect.NewError(connect.CodeUnknown, err)
	}

	return nil, connect.NewError(connect.CodeUnknown, fmt.Errorf("unknown error"))
}
