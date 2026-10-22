package commands

import (
	"bytes"
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/depot/cli/pkg/buildxdriver"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/builder"
	"github.com/docker/buildx/util/buildflags"
	"github.com/docker/buildx/util/confutil"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli/command"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
)

type buildResult struct {
	// targets holds the response of each machine for each target.
	targets []buildxdriver.TargetResponse
	// merged holds the response that buildx returned for each target.
	merged map[string]*client.SolveResponse
}

// executeBuild runs one Depot build of the given targets on the given nodes.
func executeBuild(ctx context.Context, dockerCli command.Cli, nodes []builder.Node, opts map[string]build.Options, printer progress.Writer, linter *Linter, handler *build.Handler) (*buildResult, error) {
	opts = prepareOptions(opts)

	if linter != nil && linter.Enabled() {
		stdin, err := bufferStdin(opts)
		if err != nil {
			return nil, err
		}
		if err := linter.Run(ctx, opts, stdin); err != nil {
			return nil, err
		}
	}

	cfg, release, err := buildxConfig(dockerCli)
	if err != nil {
		return nil, err
	}
	defer release()

	// build.Build replaces the entries of the map it receives.
	merged, err := build.Build(ctx, nodes, maps.Clone(opts), dockerutil.NewClient(dockerCli), cfg, printer, handler)
	if err != nil {
		return nil, err
	}
	return &buildResult{targets: buildxdriver.TargetResponses(nodes, opts, merged), merged: merged}, nil
}

// prepareOptions sets the options that every Depot build uses.
func prepareOptions(in map[string]build.Options) map[string]build.Options {
	opts := cloneOptions(in)
	for name, opt := range opts {
		if opt.BuildArgs == nil {
			opt.BuildArgs = map[string]string{}
		}
		opt.BuildArgs["DEPOT_TARGET"] = name
		for _, e := range opt.Exports {
			if e.Type == client.ExporterImage {
				e.Attrs["depot.export.image.version"] = "2"
			}
		}
		opt.Ref = identity.NewID()
		opt.ProvenanceResponseMode = confutil.MetadataProvenanceModeDisabled
		opt.Policy = []buildflags.PolicyConfig{{Disabled: true}}
		opts[name] = opt
	}
	return opts
}

func cloneOptions(in map[string]build.Options) map[string]build.Options {
	out := make(map[string]build.Options, len(in))
	for name, opt := range in {
		opt.BuildArgs = maps.Clone(opt.BuildArgs)
		opt.Exports = slices.Clone(opt.Exports)
		for i := range opt.Exports {
			opt.Exports[i].Attrs = maps.Clone(opt.Exports[i].Attrs)
			if opt.Exports[i].Attrs == nil {
				opt.Exports[i].Attrs = map[string]string{}
			}
		}
		opt.Tags = slices.Clone(opt.Tags)
		opt.Session = slices.Clone(opt.Session)
		out[name] = opt
	}
	return out
}

// bufferStdin reads standard input once when a target reads its context or
// Dockerfile from it, so that both the linter and buildx can read it.
func bufferStdin(opts map[string]build.Options) ([]byte, error) {
	var data []byte
	for name, opt := range opts {
		if opt.Inputs.ContextPath != "-" && opt.Inputs.DockerfilePath != "-" {
			continue
		}
		if data == nil {
			r := opt.Inputs.InStream.NewReadCloser()
			var err error
			data, err = io.ReadAll(r)
			_ = r.Close()
			if err != nil {
				return nil, err
			}
		}
		opt.Inputs.InStream = build.NewSyncMultiReader(bytes.NewReader(data))
		opts[name] = opt
	}
	return data, nil
}

// buildxConfig returns a buildx configuration in a temporary directory, so
// that buildx does not record Depot builds in the Docker configuration. The
// build node identifier comes from the Docker configuration, so that the
// shared key of the build context stays the same between builds.
func buildxConfig(dockerCli command.Cli) (*confutil.Config, func(), error) {
	nodeID := confutil.NewConfig(dockerCli).TryNodeIdentifier()
	dir, err := os.MkdirTemp("", "depot-buildx-")
	if err != nil {
		return nil, nil, err
	}
	release := func() { _ = os.RemoveAll(dir) }
	if nodeID != "" {
		if err := os.WriteFile(filepath.Join(dir, ".buildNodeID"), []byte(nodeID), 0o600); err != nil {
			release()
			return nil, nil, err
		}
	}
	return confutil.NewConfig(dockerCli, confutil.WithDir(dir)), release, nil
}
