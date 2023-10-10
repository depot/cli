package build

import (
	dockerbuild "github.com/docker/buildx/build"
)

func BuildxOpts(opts map[string]dockerbuild.Options) {
	for name, opt := range opts {
		for i, e := range opt.Exports {
			if e.Type == "image" {
				opts[name].Exports[i].Attrs["depot.export.image.verson"] = "2"
			}
		}
	}
}
