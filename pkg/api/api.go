package api

import (
	"fmt"
	"os"
)

type Depot struct {
	BaseURL string
	token   string
}

func NewDepot(baseURL string, token string) *Depot {
	return &Depot{BaseURL: baseURL, token: token}
}

func NewDepotFromEnv(token string) (*Depot, error) {
	baseURL := os.Getenv("DEPOT_API_HOST")
	if baseURL == "" {
		baseURL = "https://depot.dev"
	}
	return NewDepot(baseURL, token), nil
}

type InitResponse struct {
	OK          bool   `json:"ok"`
	BaseURL     string `json:"baseURL"`
	ID          string `json:"id"`
	AccessToken string `json:"accessToken"`
	Busy        bool   `json:"busy"`
}

func (d *Depot) InitBuild(projectID string) (*InitResponse, error) {
	return apiRequest[InitResponse](
		"POST",
		fmt.Sprintf("%s/api/builds", d.BaseURL),
		d.token,
		map[string]string{"projectID": projectID},
	)
}

type FinishResponse struct {
	OK bool `json:"ok"`
}

func (d *Depot) FinishBuild(buildID string) error {
	_, err := apiRequest[FinishResponse](
		"DELETE",
		fmt.Sprintf("%s/api/builds", d.BaseURL),
		d.token,
		map[string]string{"id": buildID},
	)
	return err
}
