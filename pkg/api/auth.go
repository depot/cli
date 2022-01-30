package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/cli/browser"
)

type DeviceAuthorizationRequest struct {
	ClientID string `json:"client_id"`
	Scopes   string `json:"scopes"`
}

type DeviceAuthorizationResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type DeviceAccessTokenRequest struct {
	GrantType  string `json:"grant_type"`
	ClientID   string `json:"client_id"`
	DeviceCode string `json:"device_code"`
}

type DeviceAccessTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	RefreshToken string `json:"refresh_token"`
}

type DeviceAccessTokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (d *Depot) AuthorizeDevice() error {
	requestPayload := DeviceAuthorizationRequest{
		ClientID: "cli",
	}

	requestBody, err := json.Marshal(requestPayload)
	if err != nil {
		return err
	}

	res, err := http.Post(fmt.Sprintf("%s/api/cli/auth/device", d.BaseURL), "application/json", bytes.NewBuffer(requestBody))
	if err != nil {
		return err
	}

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", res.StatusCode)
	}

	var response DeviceAuthorizationResponse
	err = json.NewDecoder(res.Body).Decode(&response)
	if err != nil {
		return err
	}

	if response.Interval == 0 {
		response.Interval = 5
	}

	fmt.Printf("First, copy your one-time code: %s\n", response.UserCode)
	fmt.Printf("Then press [Enter] to continue in the web browser... ")

	_ = waitForEnter(os.Stdin)

	err = browser.OpenURL(response.VerificationURI)
	if err != nil {
		return fmt.Errorf("error opening the web browser: %w", err)
	}

	checkInterval := time.Duration(response.Interval) * time.Second
	for {
		time.Sleep(checkInterval)

		tokenRequestPayload := DeviceAccessTokenRequest{
			GrantType:  "urn:ietf:params:oauth:grant-type:device_code",
			ClientID:   "cli",
			DeviceCode: response.DeviceCode,
		}

		tokenRequestBody, err := json.Marshal(tokenRequestPayload)
		if err != nil {
			return err
		}

		res, err := http.Post(fmt.Sprintf("%s/api/cli/auth/token", d.BaseURL), "application/json", bytes.NewBuffer(tokenRequestBody))
		if err != nil {
			return err
		}

		if res.StatusCode == http.StatusOK {
			var tokenResponse DeviceAccessTokenResponse
			err = json.NewDecoder(res.Body).Decode(&tokenResponse)
			if err != nil {
				return err
			}
			fmt.Printf("Successfully authorized device! %v\n", tokenResponse)
			return nil
		}

		var errorResponse DeviceAccessTokenErrorResponse
		err = json.NewDecoder(res.Body).Decode(&errorResponse)
		if err != nil {
			return err
		}

		if errorResponse.Error == "authorization_pending" {
			fmt.Printf("Waiting for authorization...\n")
			continue
		}

		return fmt.Errorf("error getting device access token: %s, %s", errorResponse.Error, errorResponse.ErrorDescription)
	}
}

func waitForEnter(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Scan()
	return scanner.Err()
}
