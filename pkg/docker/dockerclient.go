package docker

import (
	"os"
	"path/filepath"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/config"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/tlsconfig"
)

// Copied from github.com/docker/cli/cli/flags/common.go

var (
	dockerCertPath  = os.Getenv(client.EnvOverrideCertPath)
	dockerTLSVerify = os.Getenv(client.EnvTLSVerify) != ""
	dockerTLS       = os.Getenv("DOCKER_TLS") != ""
)

func NewDockerCLI() (*command.DockerCli, error) {
	dockerCli, err := command.NewDockerCli()
	if err != nil {
		return nil, err
	}

	// Construct options with TLS
	opts := cliflags.NewClientOptions()
	if dockerCertPath == "" {
		dockerCertPath = config.Dir()
	}

	opts.TLS = dockerTLS
	opts.TLSVerify = dockerTLSVerify
	if opts.TLSVerify {
		opts.TLS = true
	}
	if opts.TLS {
		opts.TLSOptions = &tlsconfig.Options{
			CAFile:             filepath.Join(dockerCertPath, cliflags.DefaultCaFile),
			CertFile:           filepath.Join(dockerCertPath, cliflags.DefaultCertFile),
			KeyFile:            filepath.Join(dockerCertPath, cliflags.DefaultKeyFile),
			InsecureSkipVerify: !opts.TLSVerify,
		}
		// Reset CertFile and KeyFile to empty string if the respective default files were not found.
		if _, err := os.Stat(opts.TLSOptions.CertFile); os.IsNotExist(err) {
			opts.TLSOptions.CertFile = ""
		}
		if _, err := os.Stat(opts.TLSOptions.KeyFile); os.IsNotExist(err) {
			opts.TLSOptions.KeyFile = ""
		}
	}

	err = dockerCli.Initialize(cliflags.NewClientOptions())
	if err != nil {
		return nil, err
	}

	return dockerCli, err
}
