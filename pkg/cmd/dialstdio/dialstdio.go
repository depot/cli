package cmd

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/builder"
	"github.com/spf13/cobra"
)

func NewCmdDialStdio() *cobra.Command {

	cmd := &cobra.Command{
		Use:    "dial-stdio",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			depot, err := api.NewDepotFromEnv()
			if err != nil {
				return err
			}

			projectID := os.Getenv("DEPOT_PROJECT_ID")
			if projectID == "" {
				return fmt.Errorf("DEPOT_PROJECT_ID is not set")
			}

			build, err := depot.InitBuild(projectID)
			if err != nil {
				return err
			}

			if !build.OK {
				return fmt.Errorf("failed to init build")
			}

			err = waitForReady(build)
			if err != nil {
				return err
			}

			// TODO: attempt to run this on CTRL+C
			defer func() {
				_ = depot.FinishBuild(build.ID)
			}()

			err = builder.NewProxy(build.BaseURL, build.AccessToken, build.ID)
			if err != nil {
				return err
			}

			return nil
		},
	}
	return cmd
}

func waitForReady(build *api.InitResponse) error {
	client := &http.Client{}

	count := 0

	for {
		req, err := http.NewRequest("GET", fmt.Sprintf("%s/ready-%s/", build.BaseURL, build.ID), nil)
		if err != nil {
			return err
		}
		req.Header.Add("Authorization", fmt.Sprintf("bearer %s", build.AccessToken))

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}

		count++
		if count > 30 {
			return fmt.Errorf("timed out waiting for build to be ready")
		}

		time.Sleep(time.Second)
	}
}
