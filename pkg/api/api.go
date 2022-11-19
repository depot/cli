package api

import (
	"fmt"
	"os"
	"runtime"
	"time"
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

type ReleaseResponse struct {
	OK          bool      `json:"ok"`
	Version     string    `json:"version"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt"`
}

func (d *Depot) LatestRelease() (*ReleaseResponse, error) {
	return apiRequest[ReleaseResponse](
		"GET",
		fmt.Sprintf("https://dl.depot.dev/cli/release/%s/%s/latest", runtime.GOOS, runtime.GOARCH),
		d.token,
		nil,
	)
}
