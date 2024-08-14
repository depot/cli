package cleanup

import "os"

var tmpfiles = []string{}

func RegisterTmpfile(filename string) {
	tmpfiles = append(tmpfiles, filename)
}

func CleanupTmpfiles() {
	for _, filename := range tmpfiles {
		_ = os.Remove(filename)
	}
}
