package profiler

import (
	"os"
	"runtime"

	"github.com/depot/cli/internal/build"
	"github.com/pyroscope-io/client/pyroscope"
)

// StartProfiler starts a profiler if DEPOT_ENABLE_DEBUG_PROFILING is set.
func StartProfiler(buildID string) {
	profileToken := os.Getenv("DEPOT_ENABLE_DEBUG_PROFILING")
	if profileToken != "" {
		runtime.SetMutexProfileFraction(5)
		runtime.SetBlockProfileRate(10000)
		_, _ = pyroscope.Start(pyroscope.Config{
			ApplicationName: "depot-cli",
			ServerAddress:   "https://ingest.pyroscope.cloud",
			Logger:          nil,
			Tags:            map[string]string{"version": build.Version, "buildID": buildID},
			AuthToken:       profileToken,

			ProfileTypes: []pyroscope.ProfileType{
				pyroscope.ProfileCPU,
				// pyroscope.ProfileAllocObjects,
				// pyroscope.ProfileAllocSpace,
				// pyroscope.ProfileInuseObjects,
				// pyroscope.ProfileInuseSpace,
				pyroscope.ProfileGoroutines,
				pyroscope.ProfileMutexCount,
				pyroscope.ProfileMutexDuration,
				pyroscope.ProfileBlockCount,
				pyroscope.ProfileBlockDuration,
			},
		})
	}
}
