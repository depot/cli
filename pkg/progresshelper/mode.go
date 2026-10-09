package progresshelper

import (
	"os"

	"github.com/moby/buildkit/util/progress/progressui"
)

// DisplayMode returns the progress display mode for a --progress value. An
// "auto" value uses BUILDKIT_PROGRESS when it is set. A value that buildkit
// does not know shows plain output, as in earlier versions of the CLI.
func DisplayMode(mode string) progressui.DisplayMode {
	if v := os.Getenv("BUILDKIT_PROGRESS"); v != "" && mode == string(progressui.AutoMode) {
		mode = v
	}
	switch m := progressui.DisplayMode(mode); m {
	case progressui.AutoMode, progressui.TtyMode, progressui.PlainMode, progressui.QuietMode, progressui.RawJSONMode:
		return m
	}
	return progressui.PlainMode
}
