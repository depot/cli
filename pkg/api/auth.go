package api

import (
	"fmt"
	"time"

	"github.com/briandowns/spinner"
)

type CLIAuthenticationResponse struct {
	RequestID  string `json:"requestID"`
	ExpiresIn  int    `json:"expiresIn"`
	Interval   int    `json:"interval"`
	ApproveURL string `json:"approveURL"`
	TokenURL   string `json:"tokenURL"`
}

type TokenRequest struct {
	RequestID string `json:"requestID"`
}

type TokenResponse struct {
	Token string `json:"token"`
}

func (d *Depot) AuthorizeDevice() (*TokenResponse, error) {
	response, err := apiRequest[CLIAuthenticationResponse](
		"POST",
		fmt.Sprintf("%s/api/internal/cli/auth-request", d.BaseURL),
		"",
		map[string]string{},
	)
	if err != nil {
		return nil, err
	}

	if response.Interval == 0 {
		response.Interval = 5
	}

	fmt.Printf("Please visit the following URL in your browser to authenticate the CLI:\n\n    %s\n\n", response.ApproveURL)

	spinner := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
	spinner.Prefix = "Waiting for approval "
	spinner.Start()
	defer spinner.Stop()

	checkInterval := time.Duration(response.Interval) * time.Second
	for {
		response, err := apiRequest[TokenResponse](
			"POST",
			response.TokenURL,
			"",
			TokenRequest{RequestID: response.RequestID},
		)
		if err != nil {
			if err.Error() == "authorization_pending" {
				time.Sleep(checkInterval)
				continue
			}
			return nil, err
		}
		return response, nil
	}
}
