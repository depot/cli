package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
)

type Depot struct {
	BaseURL string
}

func NewDepot(baseURL string) *Depot {
	return &Depot{BaseURL: baseURL}
}

func NewDepotFromEnv() (*Depot, error) {
	baseURL := os.Getenv("DEPOT_API_HOST")
	if baseURL == "" {
		// TODO: don't default to localhost
		baseURL = "http://localhost:3000"
		// return nil, fmt.Errorf("DEPOT_API_HOST is not set")
	}
	return NewDepot(baseURL), nil
}

type InitResponse struct {
	OK          bool   `json:"ok"`
	ID          string `json:"id"`
	AccessToken string `json:"accessToken"`
}

// TODO: use access token fetched from `depot auth`
const SimpleAuthToken = "4a4bd8307b37497b906d7b92574ccac4"

func (d *Depot) InitBuild(projectID string) (*InitResponse, error) {
	client := &http.Client{}
	payload := fmt.Sprintf(`{"projectID": "%s"}`, projectID)
	jsonStr := []byte(payload)

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/builds", d.BaseURL), bytes.NewBuffer(jsonStr))
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", SimpleAuthToken)
	req.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var response InitResponse
	json.Unmarshal([]byte(body), &response)
	return &response, nil
}
