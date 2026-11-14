package shell

import "syscall"

const prSetTHPDisable = 41

// DisableHugePages keeps transparent huge pages away from the Shell and,
// since the flag survives fork and exec, from every app and service it
// starts: with THP "always" a Go heap that touches a few KB of a 2 MB region
// gets the whole page, several MB per small process.
func DisableHugePages() error {
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetTHPDisable, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
