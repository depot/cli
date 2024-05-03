package debuglog

import (
	"log"
	"os"
)

var Debug bool

func Log(format string, args ...interface{}) {
	if Debug {
		log.Printf(format, args...)
	}
}

func init() {
	Debug = os.Getenv("DEPOT_DEBUG") != ""
}
