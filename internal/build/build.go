package build

import "runtime/debug"

var Version = "dev"
var Date = ""
var SentryEnvironment = "development"

func init() {
	if Version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			Version = info.Main.Version
		}
	}
}
