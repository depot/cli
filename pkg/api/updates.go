package api

import (
	"fmt"
	"runtime"
	"time"
)

type ReleaseResponse struct {
	OK          bool      `json:"ok"`
	Version     string    `json:"version"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt"`
}

func LatestRelease() (*ReleaseResponse, error) {
	return apiRequest[ReleaseResponse](
		"GET",
		fmt.Sprintf("https://dl.depot.dev/cli/release/%s/%s/latest", runtime.GOOS, runtime.GOARCH),
		"",
		nil,
	)
}
