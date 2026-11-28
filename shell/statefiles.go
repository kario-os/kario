package shell

import (
	"errors"

	"github.com/kario-os/kario/statefile"
)

// The Shell's state files (11-platform-architecture.md §17) are written
// whole with statefile.Write; those whose loss would weaken the phone's
// protections with statefile.WriteKeep, which keeps a copy to read when
// the file is damaged. A damaged file without a usable copy is said once
// in the log when it is read, and the Shell goes on from the defaults,
// except where that would hold back less than before (lock.go, minor.go,
// broker.go, install.go, antitheft.go, sandbox.go): there it holds back
// more.

// readState decodes the state file at path into v (statefile.ReadJSON) and
// logs a damaged one. It reports whether v was decoded, from path or from
// its copy; damaged: neither was whole.
func readState(logf func(string, ...any), path string, v any) (ok, damaged bool) {
	bak, err := statefile.ReadJSON(path, v)
	switch {
	case err == nil && bak:
		logf("state: %s was damaged; read the copy kept of it", path)
	case errors.Is(err, statefile.ErrDamaged):
		logf("state: %s: %v; starting from the defaults", path, err)
		return false, true
	}
	return err == nil, false
}
