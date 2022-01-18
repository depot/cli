package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

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

func AuthorizeDevice() error {
	requestPayload := DeviceAuthorizationRequest{
		ClientID: "cli",
	}

	requestBody, err := json.Marshal(requestPayload)
	if err != nil {
		return err
	}

	res, err := http.Post("http://localhost:3000/api/cli/auth/device", "application/json", bytes.NewBuffer(requestBody))
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

	return nil
}

func waitForEnter(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Scan()
	return scanner.Err()
}
