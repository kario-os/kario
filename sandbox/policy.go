// Package sandbox confines one app process. The shell decides a Policy per
// launch; phone-sandbox applies it to itself and then execs the app, so every
// restriction is inherited and cannot be undone (no_new_privs).
//
// Layers, outermost first:
//
//	namespaces  private /proc, own pid space, no network unless granted
//	uid         one uid per app (the Android model); drops all capabilities
//	rlimits     no core dumps, bounded fds and processes
//	Landlock    files: read system code, read+write only the data dir (which
//	            holds the app's TMPDIR; there is no shared /tmp), never
//	            execute anything the app can write (W^X); no TCP listening;
//	            no signals or abstract sockets outside the sandbox
//	seccomp     kernel attack surface: ptrace, mount, modules, bpf, keyrings,
//	            io_uring, new namespaces … return EPERM
package sandbox

import "strings"

type Policy struct {
	UID        uint32   `json:"uid,omitempty"` // 0 keeps the caller's uid (developer machine)
	Namespaces bool     `json:"namespaces"`    // the shell created mount/pid/ipc/uts namespaces
	DataDir    string   `json:"data_dir"`
	Code       []string `json:"code"`      // readable and executable
	ReadOnly   []string `json:"read_only"` // readable
	ReadFiles  []string `json:"read_files"`
	Net        bool     `json:"net"`
	// GPU opens the GPU's render nodes (/dev/dri/renderD*): rendering and
	// buffer sharing only, never the display (the renderer owns that).
	GPU bool `json:"gpu,omitempty"`
	// Survive: the app outlives the Shell (its connection waits in systemd's
	// fd store for the next one), so no parent-death signal.
	Survive bool `json:"survive,omitempty"`
	// Helper is a system helper that works only on the descriptors it was
	// handed (phone-usbshared): Landlock lets it read and run Code (its
	// own program) and nothing else, no data directory, no device.
	Helper bool `json:"helper,omitempty"`
}

// Env is the variable the launcher sets for the app, listing what was applied.
const Env = "PHONE_SANDBOX"

// Applied parses the PHONE_SANDBOX value.
type Applied map[string]bool

func ParseApplied(s string) Applied {
	a := Applied{}
	for _, f := range strings.Split(s, ",") {
		if f != "" {
			a[f] = true
		}
	}
	return a
}
