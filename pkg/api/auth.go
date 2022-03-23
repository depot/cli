package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
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

type TokenErrorResponse struct {
	Error string `json:"error"`
}

func (d *Depot) AuthorizeDevice() (*TokenResponse, error) {
	resp, err := http.Post(fmt.Sprintf("%s/api/internal/cli/auth-request", d.BaseURL), "application/json", nil)
	if err != nil {
		return nil, err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	errorResponse, _ := tryParseErrorResponse(body)
	if errorResponse != nil {
		return nil, fmt.Errorf("%s", errorResponse.Error)
	}

	var response CLIAuthenticationResponse
	err = json.Unmarshal(body, &response)
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
		time.Sleep(checkInterval)

		tokenRequestPayload := TokenRequest{
			RequestID: response.RequestID,
		}

		tokenRequestBody, err := json.Marshal(tokenRequestPayload)
		if err != nil {
			return nil, err
		}

		resp, err := http.Post(response.TokenURL, "application/json", bytes.NewBuffer(tokenRequestBody))
		if err != nil {
			return nil, err
		}

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		errorResponse, _ := tryParseErrorResponse(body)
		if errorResponse != nil {
			if errorResponse.Error == "authorization_pending" {
				continue
			}

			return nil, fmt.Errorf("error getting access token: %s", errorResponse.Error)
		}

		var response TokenResponse
		err = json.Unmarshal(body, &response)
		if err != nil {
			return nil, err
		}
		return &response, nil
	}
}
