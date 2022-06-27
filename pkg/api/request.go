package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"runtime"

	"github.com/charmbracelet/lipgloss"
	"github.com/depot/cli/internal/build"
	"github.com/pkg/errors"
)

var (
	infoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

type ErrorResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func apiRequest[Response interface{}](method, url, token string, payload interface{}) (*Response, error) {
	var requestBody io.Reader

	if payload != nil {
		jsonBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		requestBody = bytes.NewReader(jsonBytes)
	} else {
		requestBody = nil
	}

	client := &http.Client{}
	req, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", "application/json")
	if token != "" {
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	}
	req.Header.Add("User-Agent", fmt.Sprintf("depot-cli/%s/%s/%s", build.Version, runtime.GOOS, runtime.GOARCH))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	infoMessage := resp.Header.Get("X-Depot-Info-Message")
	if infoMessage != "" {
		fmt.Println(infoStyle.Render(infoMessage))
	}

	warnMessage := resp.Header.Get("X-Depot-Warn-Message")
	if warnMessage != "" {
		fmt.Println(warnStyle.Render(warnMessage))
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var errorResponse ErrorResponse
	err = json.Unmarshal(body, &errorResponse)
	if err == nil && !errorResponse.OK {
		return nil, fmt.Errorf("%s", errorResponse.Error)
	}

	var response Response
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, errors.Wrap(err, string(body))
	}

	return &response, nil
}
