package debuglog

import (
	"log"
	"os"

	depotprogress "github.com/depot/cli/pkg/progress"
)

var Debug bool

func Log(format string, args ...interface{}) {
	if Debug {
		log.Printf(format, args...)
	}
}

func LogProgress(printer *depotprogress.Progress, message string, err error) {
	if Debug {
		printer.Log(message, err)
	}
}

func init() {
	Debug = os.Getenv("DEPOT_DEBUG") != ""
}
