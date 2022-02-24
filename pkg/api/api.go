package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"github.com/depot/cli/pkg/config"
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
		baseURL = "https://app.depot.dev"
		// return nil, fmt.Errorf("DEPOT_API_HOST is not set")
	}
	return NewDepot(baseURL), nil
}

type InitResponse struct {
	OK          bool   `json:"ok"`
	BaseURL     string `json:"baseURL"`
	ID          string `json:"id"`
	AccessToken string `json:"accessToken"`
	Busy        bool   `json:"busy"`
}

func (d *Depot) InitBuild(projectID string) (*InitResponse, error) {
	client := &http.Client{}
	payload := fmt.Sprintf(`{"projectID": "%s"}`, projectID)
	jsonStr := []byte(payload)

	for {
		req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/builds", d.BaseURL), bytes.NewBuffer(jsonStr))
		if err != nil {
			return nil, err
		}
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", config.GetApiToken()))
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
		err = json.Unmarshal([]byte(body), &response)
		if err != nil {
			return nil, err
		}

		if response.OK && response.Busy {
			time.Sleep(1 * time.Second)
			continue
		}

		return &response, nil
	}
}

func (d *Depot) FinishBuild(buildID string) error {
	client := &http.Client{}
	payload := fmt.Sprintf(`{"id": "%s"}`, buildID)
	jsonStr := []byte(payload)

	req, err := http.NewRequest("DELETE", fmt.Sprintf("%s/api/builds", d.BaseURL), bytes.NewBuffer(jsonStr))
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", config.GetApiToken()))
	req.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}
