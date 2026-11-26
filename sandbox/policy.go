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

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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

// Env is the variable the launcher sets for the app, listing what was applied
// ("net" too, when the app keeps the network).
const Env = "PHONE_SANDBOX"

// ResolvConf is the resolver's configuration. On the image it links into
// systemd-resolved's directory under /run (NetReadOnly).
const ResolvConf = "/etc/resolv.conf"

// NetReadOnly is what a process with the network reads besides /etc: the
// directory resolvConf links into, if outside /etc. The rule is on the
// directory because systemd-resolved replaces the file as the network
// changes; without it the resolver cannot read its configuration and asks
// 127.0.0.1:53, where nothing answers.
func NetReadOnly(resolvConf string) []string {
	p := resolvConf
	for i := 0; i < 8; i++ {
		t, err := os.Readlink(p)
		if err != nil {
			break
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(p), t)
		}
		p = filepath.Clean(t)
	}
	if dir := filepath.Dir(p); p != resolvConf && dir != "/etc" && !strings.HasPrefix(dir, "/etc/") {
		return []string{dir}
	}
	return nil
}

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

// LandlockABI is the Landlock ABI the launcher applied ("landlock-v<n>"),
// 0 without Landlock.
func (a Applied) LandlockABI() int {
	abi := 0
	for f := range a {
		if v, ok := strings.CutPrefix(f, "landlock-v"); ok {
			if n, err := strconv.Atoi(v); err == nil {
				abi = max(abi, n)
			}
		}
	}
	return abi
}
