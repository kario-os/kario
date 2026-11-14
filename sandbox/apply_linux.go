package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// Apply confines the calling thread, which must be locked and must exec next.
// It returns the names of the layers that took effect.
func Apply(p Policy) ([]string, error) {
	var done []string
	if p.Namespaces {
		if err := privateProc(); err != nil {
			return nil, fmt.Errorf("mounts: %w", err)
		}
		done = append(done, "ns")
		if !p.Net {
			done = append(done, "nonet")
		}
	}
	limits := map[int]uint64{unix.RLIMIT_CORE: 0, unix.RLIMIT_NOFILE: 1024}
	if p.UID != 0 {
		limits[unix.RLIMIT_NPROC] = 256
	}
	for res, v := range limits {
		if err := unix.Setrlimit(res, &unix.Rlimit{Cur: v, Max: v}); err != nil {
			return nil, fmt.Errorf("rlimit %d: %w", res, err)
		}
	}
	done = append(done, "rlimit")
	if p.UID != 0 {
		if err := syscall.Setgroups(nil); err != nil {
			return nil, err
		}
		if err := syscall.Setresgid(int(p.UID), int(p.UID), int(p.UID)); err != nil {
			return nil, err
		}
		if err := syscall.Setresuid(int(p.UID), int(p.UID), int(p.UID)); err != nil {
			return nil, err
		}
		// Changing credentials clears the parent-death signal.
		if !p.Survive {
			if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
				return nil, err
			}
		}
		done = append(done, "uid")
	}
	if !p.Helper {
		if err := os.MkdirAll(TempDir(p), 0o700); err != nil {
			return nil, err
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return nil, fmt.Errorf("no_new_privs: %w", err)
	}
	if abi := landlockABI(); abi > 0 {
		if err := landlock(p, abi); err != nil {
			return nil, fmt.Errorf("landlock: %w", err)
		}
		done = append(done, "landlock", "landlock-v"+strconv.Itoa(abi))
	}
	if err := seccomp(); err != nil {
		return nil, fmt.Errorf("seccomp: %w", err)
	}
	return append(done, "seccomp"), nil
}

// privateProc runs as root inside the new mount and pid namespaces: a /proc
// that shows only this app's processes.
func privateProc() error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	return unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "")
}

// TempDir is the app's own temporary directory; there is no shared /tmp.
func TempDir(p Policy) string { return filepath.Join(p.DataDir, "tmp") }
