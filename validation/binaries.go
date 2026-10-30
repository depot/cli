package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type binary struct {
	Label string
	// LinuxPath is the CLI for the Linux architecture of the Docker daemon.
	LinuxPath string
	Version   string
}

const proxyDockerfile = `FROM ubuntu:24.04
RUN apt-get update && apt-get install -y ca-certificates curl && rm -rf /var/lib/apt/lists/*
COPY entrypoint.sh /usr/bin/entrypoint.sh
COPY depot buildkitd buildctl /usr/bin/
ENTRYPOINT ["/usr/bin/entrypoint.sh"]
`

func runCommand(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func repositoryRoot(ctx context.Context) (string, error) {
	return runCommand(ctx, "", nil, "git", "rev-parse", "--show-toplevel")
}

func checkoutRevision(ctx context.Context, root, workDir, revision string) (string, string, error) {
	commit, err := runCommand(ctx, root, nil, "git", "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", "", err
	}
	source := filepath.Join(workDir, "source-"+commit[:12])
	if _, err := os.Stat(filepath.Join(source, "go.mod")); err == nil {
		return source, commit, nil
	}
	_, _ = runCommand(ctx, root, nil, "git", "worktree", "prune")
	if _, err := runCommand(ctx, root, nil, "git", "worktree", "add", "--detach", source, commit); err != nil {
		return "", "", err
	}
	return source, commit, nil
}

func buildBinary(ctx context.Context, source, output, version, goos, goarch, pkg string) error {
	ldflags := "-X github.com/depot/cli/internal/build.Version=" + version
	env := []string{"CGO_ENABLED=0"}
	if goos != "" {
		env = append(env, "GOOS="+goos, "GOARCH="+goarch)
	}
	_, err := runCommand(ctx, source, env, "go", "build", "-ldflags", ldflags, "-o", output, pkg)
	return err
}

func dockerBuilder(ctx context.Context) (string, error) {
	return dockerCommand(ctx, "context", "show")
}

func prepareBinary(ctx context.Context, label, source, workDir, cacheKey string) (*binary, error) {
	version := "0.0.0-validation-" + label
	dir := filepath.Join(workDir, "bin-"+label+"-"+cacheKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	arch, err := dockerCommand(ctx, "version", "-f", "{{.Server.Arch}}")
	if err != nil {
		return nil, err
	}
	imageDir := filepath.Join(dir, "image")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return nil, err
	}
	for _, name := range []string{"depot", "buildkitd", "buildctl"} {
		if err := buildBinary(ctx, source, filepath.Join(imageDir, name), version, "linux", arch, "./cmd/"+name); err != nil {
			return nil, err
		}
	}
	entrypoint, err := os.ReadFile(filepath.Join(source, "entrypoint.sh"))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(imageDir, "entrypoint.sh"), entrypoint, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(imageDir, "Dockerfile"), []byte(proxyDockerfile), 0o644); err != nil {
		return nil, err
	}
	builder, err := dockerBuilder(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := dockerCommand(ctx, "buildx", "build", "--builder", builder, "--load", "-t", "public.ecr.aws/depot/cli:"+version, imageDir); err != nil {
		return nil, err
	}

	return &binary{Label: label, LinuxPath: filepath.Join(imageDir, "depot"), Version: version}, nil
}
