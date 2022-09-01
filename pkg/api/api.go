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

type BuildReponse struct {
	OK bool   `json:"ok"`
	ID string `json:"id"`
}

func (d *Depot) CreateBuild(projectID string) (*BuildReponse, error) {
	return apiRequest[BuildReponse](
		"POST",
		fmt.Sprintf("%s/api/internal/cli/projects/%s/builds", d.BaseURL, projectID),
		d.token,
		map[string]string{},
	)
}

type BuildHintReponse struct {
	OK bool `json:"ok"`
}

func (d *Depot) ReportBuildHint(projectID string) (*BuildHintReponse, error) {
	return apiRequest[BuildHintReponse](
		"POST",
		fmt.Sprintf("%s/api/internal/cli/projects/%s/build-hints", d.BaseURL, projectID),
		d.token,
		map[string]string{},
	)
}

type BuilderResponse struct {
	OK           bool   `json:"ok"`
	Endpoint     string `json:"endpoint"`
	AccessToken  string `json:"accessToken"`
	BuilderState string `json:"builderState"`
	PollSeconds  int    `json:"pollSeconds"`
	Platform     string `json:"platform"`

	// Version 2 uses mTLS for authentication
	Version string `json:"version"`
	CACert  string `json:"caCert"`
	Cert    string `json:"cert"`
	Key     string `json:"key"`
}

func (d *Depot) GetBuilder(buildID string, platform string) (*BuilderResponse, error) {
	return apiRequest[BuilderResponse](
		"GET",
		fmt.Sprintf("%s/api/internal/cli/builds/%s/platform/%s", d.BaseURL, buildID, platform),
		d.token,
		map[string]string{},
	)
}

type BuilderHealthResponse struct {
	OK bool `json:"ok"`
}

func (d *Depot) ReportBuilderHealth(buildID string, platform string, status string) (*BuilderHealthResponse, error) {
	return apiRequest[BuilderHealthResponse](
		"POST",
		fmt.Sprintf("%s/api/internal/cli/builds/%s/platform/%s/health", d.BaseURL, buildID, platform),
		d.token,
		map[string]string{"status": status},
	)
}

type FinishResponse struct {
	OK bool `json:"ok"`
}

func (d *Depot) FinishBuild(buildID string, buildErr error) error {
	var status string
	if buildErr != nil {
		status = "error"
	} else {
		status = "success"
	}

	_, err := apiRequest[FinishResponse](
		"DELETE",
		fmt.Sprintf("%s/api/internal/cli/builds/%s", d.BaseURL, buildID),
		d.token,
		map[string]string{"status": status},
	)
	return err
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
		fmt.Sprintf("%s/api/cli/release/%s/%s/latest", d.BaseURL, runtime.GOOS, runtime.GOARCH),
		d.token,
		nil,
	)
}

type Project struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OrgID   string `json:"orgID"`
	OrgName string `json:"orgName"`
}

type GetProjectsResponse struct {
	OK       bool       `json:"ok"`
	Projects []*Project `json:"projects"`
}

func (d *Depot) GetProjects() (*GetProjectsResponse, error) {
	return apiRequest[GetProjectsResponse](
		"GET",
		fmt.Sprintf("%s/api/internal/cli/projects", d.BaseURL),
		d.token,
		nil,
	)
}
