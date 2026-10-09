package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	cliv1 "github.com/depot/cli/pkg/proto/depot/cli/v1"
	"golang.org/x/crypto/bcrypt"
)

const (
	containerPrefix   = "depot-validation-"
	namespaceOwner    = containerPrefix + "network"
	registryContainer = containerPrefix + "registry"
	authRegistry      = containerPrefix + "registry-auth"
	runnerImage       = "docker:29-cli"

	registryPort = "41234"
	// registryHost names the registry in the shared network namespace. It
	// is not "localhost", because clients use plain HTTP for localhost.
	registryHost      = "registry.validation.internal"
	buildkitAMD64Port = "1234"
	// authRegistryPort serves a registry that requires a password, for
	// commands that read the authentication challenge of a registry.
	authRegistryPort  = "41235"
	authUser          = "validation"
	authPassword      = "validation-password"
	buildkitARM64Port = "1235"
	gitPort           = "8080"
)

// environment describes the containers that stand in for Depot builders.
//
// A network container owns a network namespace. The registries, the
// buildkitd containers, and every CLI run join it, and reach the registry as
// "registry.validation.internal:41234". The Docker daemon and the harness
// reach the same registry as "localhost:41234" through a published port.
// Other containers, such as the registry proxy that --load starts, reach
// buildkitd at the address of the namespace on the default bridge network.
type environment struct {
	buildkitImage string
	address       string
	gitAddress    string
	certificates  *certificates
	certDir       string
}

func dockerCommand(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func containerIP(ctx context.Context, name string) (string, error) {
	return dockerCommand(ctx, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name)
}

// publishedPort returns the host port that Docker published for a port of a
// container.
func publishedPort(ctx context.Context, name, port string) (string, error) {
	out, err := dockerCommand(ctx, "port", name, port+"/tcp")
	if err != nil {
		return "", err
	}
	_, hostPort, err := net.SplitHostPort(strings.Split(out, "\n")[0])
	return hostPort, err
}

func removeContainers(ctx context.Context) {
	out, err := dockerCommand(ctx, "ps", "-aq", "--filter", "name="+containerPrefix)
	if err == nil && out != "" {
		_, _ = dockerCommand(ctx, append([]string{"rm", "-f", "-v"}, strings.Fields(out)...)...)
	}
}

func startEnvironment(ctx context.Context, buildkitImage, workDir string) (*environment, error) {
	removeContainers(ctx)
	env := &environment{buildkitImage: buildkitImage}

	if _, err := dockerCommand(ctx, "run", "-d", "--name", namespaceOwner,
		"--add-host", registryHost+":127.0.0.1",
		"--add-host", "host.docker.internal:host-gateway",
		"-p", "127.0.0.1:"+registryPort+":"+registryPort,
		"-p", "127.0.0.1:"+authRegistryPort+":"+authRegistryPort,
		"-p", "127.0.0.1::"+buildkitAMD64Port, "-p", "127.0.0.1::"+buildkitARM64Port,
		"busybox:1.36", "sleep", "infinity"); err != nil {
		return nil, err
	}
	ip, err := containerIP(ctx, namespaceOwner)
	if err != nil {
		return nil, err
	}
	env.address = ip

	env.certificates, err = newCertificates(net.ParseIP(ip))
	if err != nil {
		return nil, err
	}
	certDir := filepath.Join(workDir, "certs")
	env.certDir = certDir
	if err := env.certificates.writeFiles(certDir); err != nil {
		return nil, err
	}

	if err := writeHtpasswd(filepath.Join(certDir, "htpasswd")); err != nil {
		return nil, err
	}
	for _, registry := range []struct {
		name string
		args []string
	}{
		{registryContainer, []string{"-e", "REGISTRY_HTTP_ADDR=:" + registryPort}},
		{authRegistry, []string{
			"-e", "REGISTRY_HTTP_ADDR=:" + authRegistryPort,
			"-e", "REGISTRY_AUTH=htpasswd",
			"-e", "REGISTRY_AUTH_HTPASSWD_REALM=validation",
			"-e", "REGISTRY_AUTH_HTPASSWD_PATH=/certs/htpasswd",
		}},
	} {
		args := append([]string{"create", "--name", registry.name, "--network", "container:" + namespaceOwner,
			"-e", "REGISTRY_HTTP_TLS_CERTIFICATE=/certs/registry.pem",
			"-e", "REGISTRY_HTTP_TLS_KEY=/certs/registry-key.pem"}, registry.args...)
		if _, err := dockerCommand(ctx, append(args, "registry:2")...); err != nil {
			return nil, err
		}
		if _, err := dockerCommand(ctx, "cp", certDir, registry.name+":/"); err != nil {
			return nil, err
		}
		if _, err := dockerCommand(ctx, "start", registry.name); err != nil {
			return nil, err
		}
	}

	config := fmt.Sprintf("[registry.%q]\n  http = false\n  ca = [\"/certs/ca.pem\"]\n", env.registryPushHost())
	configPath := filepath.Join(workDir, "buildkitd.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		return nil, err
	}

	for platform, port := range map[string]string{"amd64": buildkitAMD64Port, "arm64": buildkitARM64Port} {
		name := containerPrefix + "buildkitd-" + platform
		if _, err := dockerCommand(ctx, "create", "--name", name, "--privileged", "--network", "container:"+namespaceOwner,
			buildkitImage, "--addr", "tcp://0.0.0.0:"+port, "--root", "/var/lib/buildkit-"+platform, "--config", "/buildkitd.toml",
			"--tlscacert", "/certs/ca.pem", "--tlscert", "/certs/cert.pem", "--tlskey", "/certs/key.pem",
			"--allow-insecure-entitlement", "security.insecure", "--allow-insecure-entitlement", "network.host"); err != nil {
			return nil, err
		}
		if _, err := dockerCommand(ctx, "cp", configPath, name+":/buildkitd.toml"); err != nil {
			return nil, err
		}
		if _, err := dockerCommand(ctx, "cp", certDir, name+":/"); err != nil {
			return nil, err
		}
		if _, err := dockerCommand(ctx, "start", name); err != nil {
			return nil, err
		}
	}

	if err := startGitServer(ctx, env, workDir); err != nil {
		return nil, err
	}

	for _, wait := range []struct{ container, port string }{
		{namespaceOwner, buildkitAMD64Port},
		{namespaceOwner, buildkitARM64Port},
		{namespaceOwner, registryPort},
		{namespaceOwner, authRegistryPort},
		{containerPrefix + "git", gitPort},
	} {
		hostPort, err := publishedPort(ctx, wait.container, wait.port)
		if err != nil {
			return nil, err
		}
		if err := waitForPort(ctx, net.JoinHostPort("127.0.0.1", hostPort)); err != nil {
			return nil, err
		}
	}
	return env, nil
}

func waitForPort(ctx context.Context, address string) error {
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for %s", address)
}

// startGitServer serves every fixture directory as a git repository over
// the dumb HTTP protocol, so that scenarios can use remote build contexts
// and bake definitions.
func startGitServer(ctx context.Context, env *environment, workDir string) error {
	fixtures, err := fixturesDir(ctx)
	if err != nil {
		return err
	}
	repos := filepath.Join(workDir, "git")
	if err := os.RemoveAll(repos); err != nil {
		return err
	}
	entries, err := os.ReadDir(fixtures)
	if err != nil {
		return err
	}
	gitEnv := []string{
		"GIT_AUTHOR_NAME=validation", "GIT_AUTHOR_EMAIL=validation@depot.invalid",
		"GIT_COMMITTER_NAME=validation", "GIT_COMMITTER_EMAIL=validation@depot.invalid",
		"GIT_AUTHOR_DATE=2020-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2020-01-01T00:00:00Z",
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		source := filepath.Join(workDir, "git-source", entry.Name())
		_ = os.RemoveAll(source)
		if err := copyTree(filepath.Join(fixtures, entry.Name()), source); err != nil {
			return err
		}
		bare := filepath.Join(repos, entry.Name()+".git")
		for _, args := range [][]string{
			{"init", "-q", "-b", "main"},
			{"add", "-A"},
			{"commit", "-q", "--allow-empty", "-m", "fixture"},
			{"clone", "-q", "--bare", source, bare},
			{"-C", bare, "update-server-info"},
		} {
			if _, err := runCommand(ctx, source, gitEnv, "git", args...); err != nil {
				return err
			}
		}
	}

	name := containerPrefix + "git"
	if _, err := dockerCommand(ctx, "create", "--name", name, "-p", "127.0.0.1::"+gitPort, "busybox:1.36",
		"httpd", "-f", "-p", gitPort, "-h", "/git"); err != nil {
		return err
	}
	if _, err := dockerCommand(ctx, "cp", repos, name+":/"); err != nil {
		return err
	}
	if _, err := dockerCommand(ctx, "start", name); err != nil {
		return err
	}
	env.gitAddress, err = containerIP(ctx, name)
	return err
}

func fixturesDir(ctx context.Context) (string, error) {
	root, err := repositoryRoot(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "validation", "fixtures"), nil
}

// gitURL returns the URL of the git server. Append "/<fixture>.git" to
// name the repository of a fixture directory.
func (e *environment) gitURL() string {
	return "http://" + net.JoinHostPort(e.gitAddress, gitPort)
}

// registryPushHost is the registry address for buildkitd and the CLI.
func (e *environment) registryPushHost() string {
	return net.JoinHostPort(registryHost, registryPort)
}

// authRegistryHost is the address of the registry that requires a password.
func (e *environment) authRegistryHost() string {
	return net.JoinHostPort(registryHost, authRegistryPort)
}

// registryPullHost is the registry address for the Docker daemon and for the
// harness, which reach the registry through the published port.
func (e *environment) registryPullHost() string {
	return net.JoinHostPort("localhost", registryPort)
}

func (e *environment) buildkitEndpoint(platform cliv1.BuilderPlatform) (string, error) {
	switch platform {
	case cliv1.BuilderPlatform_BUILDER_PLATFORM_AMD64:
		return "tcp://" + net.JoinHostPort(e.address, buildkitAMD64Port), nil
	case cliv1.BuilderPlatform_BUILDER_PLATFORM_ARM64:
		return "tcp://" + net.JoinHostPort(e.address, buildkitARM64Port), nil
	default:
		return "", fmt.Errorf("unsupported platform %s", platform)
	}
}

func (e *environment) replacements() map[string]string {
	return map[string]string{
		e.registryPushHost():                           "<registry>",
		e.registryPullHost():                           "<registry-local>",
		e.authRegistryHost():                           "<registry-auth>",
		net.JoinHostPort(e.address, buildkitAMD64Port): "<buildkitd-amd64>",
		net.JoinHostPort(e.address, buildkitARM64Port): "<buildkitd-arm64>",
		e.gitURL(): "<git>",
	}
}

func writeHtpasswd(path string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(authPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(authUser+":"+string(hash)+"\n"), 0o644)
}

// dockerConfig returns a Docker configuration file with the credentials of
// the registry that requires a password.
func (e *environment) dockerConfig() []byte {
	auth := base64.StdEncoding.EncodeToString([]byte(authUser + ":" + authPassword))
	return []byte(fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, e.authRegistryHost(), auth))
}

// addLatency delays every packet in the network namespace of the builders,
// to measure the effect of round trips to a remote builder.
func addLatency(ctx context.Context, delay time.Duration) error {
	_, err := dockerCommand(ctx, "run", "--rm", "--cap-add", "NET_ADMIN", "--network", "container:"+namespaceOwner,
		"alpine:3.22", "sh", "-c", fmt.Sprintf("apk add -q iproute2-tc && tc qdisc add dev lo root netem delay %dms", delay.Milliseconds()))
	return err
}
