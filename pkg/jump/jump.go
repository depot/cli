package jump

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

func EnsureJump(projectID string) error {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}

	containerID := "buildx_buildkit_depot-project0"

	info, err := cli.ContainerInspect(ctx, containerID)
	if err == nil {
		if info.State.Running {
			return nil
		}
		if err := cli.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{}); err != nil {
			return err
		}
	}

	// reader, err := cli.ImagePull(ctx, "ghcr.io/depot/cli:local", types.ImagePullOptions{})
	// if err != nil {
	// 	return err
	// }
	// io.Copy(os.Stdout, reader)

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: "ghcr.io/depot/cli:local",
		Cmd:   []string{"jump"},
		Env: []string{
			"DEPOT_API_KEY=xxx",
			"DEPOT_PROJECT_ID=" + projectID,
			"DEPOT_API_HOST=https://app.depot.dev",
		},
	}, &container.HostConfig{
		AutoRemove: true,
	}, nil, nil, containerID)
	if err != nil {
		return err
	}

	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}

	// statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	// select {
	// case err := <-errCh:
	// 	if err != nil {
	// 		return err
	// 	}
	// case <-statusCh:
	// }

	return nil
}
