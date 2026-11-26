//go:build kariocover

package sandbox

import "os"

// coverDirs: in a coverage build (make coverage) an app writes its counters
// into the Shell's $GOCOVERDIR, which it may read and write but not run.
func coverDirs() []string {
	if d := os.Getenv("GOCOVERDIR"); d != "" {
		return []string{d}
	}
	return nil
}
