//go:build !windows

package gocache

import "os"

// platformNoReplaceOps tests writeOutputNoReplace with stand-ins for the
// Windows operations. They only behave like those on Unix: on Windows, Link,
// Remove and Rename fail while another writer has the file open.
var platformNoReplaceOps = map[string]noReplaceOps{
	"hard link": {renameNoReplace: linkNoReplace, replace: os.Rename, lock: mutexLock},
}
