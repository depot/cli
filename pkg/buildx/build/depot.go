package build

import (
	"context"
	"encoding/base64"
	"encoding/json"

	dockerbuild "github.com/docker/buildx/build"
	"github.com/docker/buildx/builder"
	"github.com/docker/buildx/util/dockerutil"
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func DepotBuild(ctx context.Context, nodes []builder.Node, opt map[string]dockerbuild.Options, docker *dockerutil.Client, configDir string, w progress.Writer) ([]DepotBuildResponse, error) {
	return DepotBuildWithResultHandler(ctx, nodes, opt, docker, configDir, w, nil, false)
}

// DepotBuildWithResultHandler is a wrapper around BuildWithResultHandler
// that allows the caller to handle the result of each build.
//
// BuildWithResultHandler was copied from github.com/docker/buildx/build/build.go
// and modified to return multiple responses.
func DepotBuildWithResultHandler(ctx context.Context, nodes []builder.Node, opts map[string]dockerbuild.Options, docker *dockerutil.Client, configDir string, w progress.Writer, resultHandleFunc func(driverIndex int, rCtx *dockerbuild.ResultContext), allowNoOutput bool) ([]DepotBuildResponse, error) {
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
	return BuildWithResultHandler(ctx, nodes, depotopts, docker, configDir, w, depotHandleFunc, allowNoOutput)
}

// DEPOT: Replaces the docker map[string]*client.SolveResponse by returning each
// node and their response.  This has the information needed
// to appropriately load the image into the docker daemon.
type DepotBuildResponse struct {
	Name          string // For bake this is the target name and for a single build it is "default".
	NodeResponses []DepotNodeResponse
}

type DepotNodeResponse struct {
	Node             builder.Node
	SolveResponse    *client.SolveResponse
	AttestationIndex *ocispecs.Index
	ManifestConfigs  []*ManifestConfig
}

type ManifestConfig struct {
	Desc           *ocispecs.Descriptor
	Manifest       *ocispecs.Manifest
	ImageConfig    *ocispecs.Image
	RawManifest    string
	RawImageConfig string
}

func NewDepotNodeResponse(node builder.Node, resp *client.SolveResponse) DepotNodeResponse {
	nodeResp := DepotNodeResponse{
		Node:          node,
		SolveResponse: resp,
	}
	encodedDesc, ok := resp.ExporterResponse[exptypes.ExporterImageDescriptorKey]
	if !ok {
		return nodeResp
	}

	jsonImageDesc, err := base64.StdEncoding.DecodeString(encodedDesc)
	if err != nil {
		return nodeResp
	}

	var manifestDescriptor ocispecs.Descriptor
	if err := json.Unmarshal(jsonImageDesc, &manifestDescriptor); err != nil {
		return nodeResp
	}

	manifestDescriptors := []*ocispecs.Descriptor{}

	// These checks handle situations where the image does and does not have attestations.
	// If there are no attestations, then the imageDesc contains the manifest and config.
	// Otherwise the imageDesc's `depot.containerimage.index` will contain the manifest and config.

	encodedIndex, ok := manifestDescriptor.Annotations["depot.containerimage.index"]
	if !ok {
		// No attestations.
		manifestDescriptors = append(manifestDescriptors, &manifestDescriptor)
	} else {
		// With attestations.
		var index ocispecs.Index
		if err := json.Unmarshal([]byte(encodedIndex), &index); err != nil {
			return nodeResp
		}
		for i := range index.Manifests {
			manifestDescriptors = append(manifestDescriptors, &index.Manifests[i])
		}
		delete(manifestDescriptor.Annotations, "depot.containerimage.index")
		nodeResp.AttestationIndex = &index
	}

	for _, desc := range manifestDescriptors {
		manifestConfig := &ManifestConfig{}
		m, ok := desc.Annotations["depot.containerimage.manifest"]
		if !ok {
			return nodeResp
		}
		delete(desc.Annotations, "depot.containerimage.manifest")

		var manifest ocispecs.Manifest
		if err := json.Unmarshal([]byte(m), &manifest); err != nil {
			return nodeResp
		}
		manifestConfig.RawManifest = m
		manifestConfig.Manifest = &manifest

		c, ok := desc.Annotations["depot.containerimage.config"]
		if !ok {
			return nodeResp
		}
		delete(desc.Annotations, "depot.containerimage.config")

		var image ocispecs.Image
		if err := json.Unmarshal([]byte(c), &image); err != nil {
			return nodeResp
		}
		manifestConfig.RawImageConfig = c
		manifestConfig.ImageConfig = &image

		manifestConfig.Desc = desc

		nodeResp.ManifestConfigs = append(nodeResp.ManifestConfigs, manifestConfig)
	}

	return nodeResp
}
