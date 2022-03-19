package build

import "runtime/debug"

var Version = "dev"
var Date = ""

func init() {
	if Version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			Version = info.Main.Version
		}
	}
}
