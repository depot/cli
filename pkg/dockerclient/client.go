package dockerclient

import (
	"github.com/depot/cli/pkg/docker"
	"github.com/docker/cli/cli/command"
)

var dockerCli *command.DockerCli

func NewDockerCLI() (*command.DockerCli, error) {
	if dockerCli != nil {
		return dockerCli, nil
	}

	var err error
	cli, err := docker.NewDockerCLI()
	if err != nil {
		return nil, err
	}

	dockerCli = cli
	return dockerCli, nil
}
