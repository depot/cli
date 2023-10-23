// Lists the latests builds for a project.
package list

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/helpers"
	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"github.com/depot/cli/pkg/proto/depot/cli/v1/cliv1connect"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func NewCmdBuilds() *cobra.Command {
	var projectID string
	var token string
	var outputFormat string

	cmd := &cobra.Command{
		Use:     "builds",
		Aliases: []string{"b"},
		Short:   "List builds for a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			projectID := helpers.ResolveProjectID(projectID, cwd)
			if projectID == "" {
				return errors.Errorf("unknown project ID (run `depot init` or use --project or $DEPOT_PROJECT_ID)")
			}

			token, err := helpers.ResolveToken(context.Background(), token)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("missing API token, please run `depot login`")
			}

			client := api.NewBuildClient()
			if outputFormat != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				depotBuilds, err := builds(ctx, projectID, token, client)
				if err != nil {
					return err
				}

				switch outputFormat {
				case "csv":
					return writeCSV(depotBuilds)
				case "json":
					return writeJSON(depotBuilds)
				}

				return errors.Errorf("unknown format: %s. Requires csv or json", outputFormat)
			}

			m := helpers.NewBuildsModel(projectID, token, client)
			_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
			return err
		},
	}

	flags := cmd.Flags()

	flags.StringVar(&projectID, "project", "", "Depot project ID")
	flags.StringVar(&token, "token", "", "Depot API token")
	flags.StringVar(&outputFormat, "output", "", "Non-interactive output format (json, csv)")

	return cmd
}

type depotBuild struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StartTime string `json:"startTime"`
	Duration  int    `json:"duration"`
}

func builds(ctx context.Context, projectID, token string, client cliv1connect.BuildServiceClient) ([]depotBuild, error) {
	req := cliv1.ListBuildsRequest{ProjectId: projectID}
	resp, err := client.ListBuilds(ctx, api.WithAuthentication(connect.NewRequest(&req), token))
	if err != nil {
		return nil, err
	}

	res := []depotBuild{}

	for _, build := range resp.Msg.Builds {
		createdAt := build.CreatedAt.AsTime()
		if build.CreatedAt == nil {
			createdAt = time.Now()
		}

		finishedAt := build.FinishedAt.AsTime()
		// This will will cause the duration to increase until the build is complete.
		if build.FinishedAt == nil {
			finishedAt = time.Now()
		}

		startTime := createdAt.Format(time.RFC3339)
		duration := int(finishedAt.Sub(createdAt).Seconds())
		status := strings.ToLower(strings.TrimPrefix(build.Status.String(), "BUILD_STATUS_"))

		res = append(res, depotBuild{
			ID:        build.Id,
			Status:    status,
			StartTime: startTime,
			Duration:  duration,
		})
	}

	return res, nil
}

func writeCSV(depotBuilds []depotBuild) error {
	w := csv.NewWriter(os.Stdout)
	if len(depotBuilds) > 0 {
		if err := w.Write([]string{"Build ID", "Status", "Started", "Duration (s)"}); err != nil {
			return err
		}
	}

	for _, build := range depotBuilds {
		row := []string{build.ID, build.Status, build.StartTime, fmt.Sprintf("%d", build.Duration)}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}

func writeJSON(depotBuilds []depotBuild) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(depotBuilds)
}
