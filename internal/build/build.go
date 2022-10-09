package build

import "runtime/debug"

var Version = "0.0.0-dev"
var Date = ""
var SentryEnvironment = "development"

func init() {
	if Version == "0.0.0-dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" {
			Version = info.Main.Version
		}
	}
}
