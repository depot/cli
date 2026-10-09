package buildxdriver

import (
	"maps"
	"slices"
	"strings"

	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	"github.com/docker/buildx/build"
	"github.com/docker/buildx/builder"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// TargetResponse holds the solve responses of one build target.
type TargetResponse struct {
	// Name is the bake target name, or "default" for a build.
	Name string
	// NodeResponses holds one response for each machine that built the
	// target. When buildx merged the images of several machines into one
	// image index, the response for the index is the last entry.
	NodeResponses []NodeResponse
}

// NodeResponse holds the solve response of one machine.
type NodeResponse struct {
	Driver        *Driver
	SolveResponse *client.SolveResponse
}

// TargetResponses combines the response that buildx returned for each
// target with the response that each machine returned.
func TargetResponses(nodes []builder.Node, opts map[string]build.Options, merged map[string]*client.SolveResponse) []TargetResponse {
	targets := make([]TargetResponse, 0, len(opts))
	for _, name := range slices.Sorted(maps.Keys(opts)) {
		opt := opts[name]
		target := TargetResponse{Name: name}
		for _, node := range nodesByPlatformOrder(nodes, opt.Platforms) {
			d := FromNode(node)
			if d == nil {
				continue
			}
			if res, ok := d.SolveResponse(opt.Ref); ok {
				target.NodeResponses = append(target.NodeResponses, NodeResponse{Driver: d, SolveResponse: res})
			}
		}

		if index, ok := mergedIndex(target.NodeResponses, merged[name]); ok {
			if names := pushNames(opt); names != "" {
				for _, nodeRes := range target.NodeResponses {
					nodeRes.SolveResponse.ExporterResponse["image.name"] = names
				}
			}
			target.NodeResponses = append(target.NodeResponses, NodeResponse{
				Driver:        target.NodeResponses[0].Driver,
				SolveResponse: &client.SolveResponse{ExporterResponse: map[string]string{exptypes.ExporterImageDigestKey: index}},
			})
		}

		if len(target.NodeResponses) > 0 {
			targets = append(targets, target)
		}
	}
	return targets
}

// mergedIndex returns the digest of the image index that buildx pushed after
// it merged the images of several machines.
func mergedIndex(nodeResponses []NodeResponse, merged *client.SolveResponse) (string, bool) {
	if len(nodeResponses) < 2 || merged == nil {
		return "", false
	}
	index := merged.ExporterResponse[exptypes.ExporterImageDigestKey]
	if index == "" {
		return "", false
	}
	for _, nodeRes := range nodeResponses {
		if nodeRes.SolveResponse.ExporterResponse[exptypes.ExporterImageDigestKey] == index {
			return "", false
		}
	}
	return index, true
}

// pushNames returns the image names of a pushed image export, in the form
// that buildx gives to the image exporter.
func pushNames(opt build.Options) string {
	for _, e := range opt.Exports {
		if e.Type != client.ExporterImage || !strings.EqualFold(e.Attrs["push"], "true") {
			continue
		}
		if len(opt.Tags) == 0 {
			return e.Attrs["name"]
		}
		names := make([]string, 0, len(opt.Tags))
		for _, tag := range opt.Tags {
			ref, err := reference.Parse(tag)
			if err != nil {
				return e.Attrs["name"]
			}
			names = append(names, ref.String())
		}
		return strings.Join(names, ",")
	}
	return ""
}

// nodesByPlatformOrder orders the nodes by the first target platform that
// each node builds, so that the responses follow the order of --platform.
func nodesByPlatformOrder(nodes []builder.Node, targetPlatforms []ocispecs.Platform) []builder.Node {
	rank := func(node builder.Node) int {
		for i, tp := range targetPlatforms {
			want := platforms.FormatAll(platforms.Normalize(tp))
			for _, np := range node.Platforms {
				if platforms.FormatAll(platforms.Normalize(np)) == want {
					return i
				}
			}
		}
		return len(targetPlatforms)
	}
	ordered := slices.Clone(nodes)
	slices.SortStableFunc(ordered, func(a, b builder.Node) int { return rank(a) - rank(b) })
	return ordered
}
