package build

import (
	"context"

	dockerbuild "github.com/docker/buildx/build"
	"github.com/docker/buildx/builder"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/buildx/util/progress"
)

func DepotBuild(ctx context.Context, nodes []builder.Node, opt map[string]dockerbuild.Options, docker *dockerutil.Client, configDir string, w progress.Writer, dockerfileCallback DockerfileCallback) ([]DepotBuildResponse, error) {
	return DepotBuildWithResultHandler(ctx, nodes, opt, docker, configDir, w, dockerfileCallback, nil, false)
}

// DepotBuildWithResultHandler is a wrapper around BuildWithResultHandler
// that allows the caller to handle the result of each build.
//
// BuildWithResultHandler was copied from github.com/docker/buildx/build/build.go
// and modified to return multiple responses.
func DepotBuildWithResultHandler(ctx context.Context, nodes []builder.Node, opts map[string]dockerbuild.Options, docker *dockerutil.Client, configDir string, w progress.Writer, dockerfileCallback DockerfileCallback, resultHandleFunc func(driverIndex int, rCtx *dockerbuild.ResultContext), allowNoOutput bool) ([]DepotBuildResponse, error) {
	depotopts := BuildxOpts(opts)

	var depotHandleFunc func(driverIndex int, rCtx *ResultContext)
	if resultHandleFunc != nil {
		depotHandleFunc = func(driverIndex int, rCtx *ResultContext) {
			var dockerResultContext dockerbuild.ResultContext
			if rCtx != nil {
				dockerResultContext = dockerbuild.ResultContext{
					Client: rCtx.Client,
					Res:    rCtx.Res,
				}
			}
			resultHandleFunc(driverIndex, &dockerResultContext)
		}

	}
	return BuildWithResultHandler(ctx, nodes, depotopts, docker, configDir, w, dockerfileCallback, depotHandleFunc, allowNoOutput)
}

func BuildxOpts(opts map[string]dockerbuild.Options) map[string]Options {
	var depotopts map[string]Options
	if opts != nil {
		depotopts = make(map[string]Options, len(opts))
		for k, opt := range opts {
			var printFunc PrintFunc
			if opt.PrintFunc != nil {
				printFunc = PrintFunc{
					Name:   opt.PrintFunc.Name,
					Format: opt.PrintFunc.Format,
				}
			}
			var printFuncPtr *PrintFunc
			if opt.PrintFunc != nil {
				printFuncPtr = &printFunc
			}

			var namedContexts map[string]NamedContext
			if opt.Inputs.NamedContexts != nil {
				namedContexts = make(map[string]NamedContext, len(opt.Inputs.NamedContexts))
				for k, v := range opt.Inputs.NamedContexts {
					namedContexts[k] = NamedContext{
						Path:  v.Path,
						State: v.State,
					}
				}
			}
			opt.BuildArgs["DEPOT_TARGET"] = k

			for _, e := range opt.Exports {
				if e.Type == "image" {
					e.Attrs["depot.export.image.version"] = "2"
				}
			}

			depotopts[k] = Options{
				Inputs: Inputs{
					ContextPath:      opt.Inputs.ContextPath,
					DockerfilePath:   opt.Inputs.DockerfilePath,
					InStream:         opt.Inputs.InStream,
					ContextState:     opt.Inputs.ContextState,
					DockerfileInline: opt.Inputs.DockerfileInline,
					NamedContexts:    namedContexts,
				},
				Allow:         opt.Allow,
				Attests:       opt.Attests,
				BuildArgs:     opt.BuildArgs,
				CacheFrom:     opt.CacheFrom,
				CacheTo:       opt.CacheTo,
				CgroupParent:  opt.CgroupParent,
				Exports:       opt.Exports,
				ExtraHosts:    opt.ExtraHosts,
				ImageIDFile:   opt.ImageIDFile,
				Labels:        opt.Labels,
				NetworkMode:   opt.NetworkMode,
				NoCache:       opt.NoCache,
				NoCacheFilter: opt.NoCacheFilter,
				Platforms:     opt.Platforms,
				Pull:          opt.Pull,
				Session:       opt.Session,
				ShmSize:       opt.ShmSize,
				Tags:          opt.Tags,
				Target:        opt.Target,
				Ulimits:       opt.Ulimits,
				Linked:        opt.Linked,
				PrintFunc:     printFuncPtr,
			}
		}
	}
	return depotopts
}
