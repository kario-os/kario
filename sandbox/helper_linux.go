package sandbox

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// FDOnly is the layer DescriptorsOnly adds, as a helper reports it.
const FDOnly = "fd-only"

// helperDenied is what a helper working only on handed descriptors never
// needs, on top of what apps are denied: running a program, a socket of
// its own (or binding and connecting one), and any file by path: opening,
// making, removing, renaming or linking it, changing its mode, owner,
// times or attributes, watching it. Reading metadata (stat) stays.
var helperDenied = []uintptr{
	unix.SYS_EXECVE, unix.SYS_EXECVEAT,
	unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_BIND, unix.SYS_CONNECT, unix.SYS_LISTEN,
	unix.SYS_OPENAT, unix.SYS_OPENAT2, unix.SYS_MKDIRAT, unix.SYS_MKNODAT, unix.SYS_UNLINKAT,
	unix.SYS_RENAMEAT, unix.SYS_RENAMEAT2, unix.SYS_LINKAT, unix.SYS_SYMLINKAT,
	unix.SYS_TRUNCATE, unix.SYS_FCHMOD, unix.SYS_FCHMODAT, unix.SYS_FCHMODAT2,
	unix.SYS_FCHOWN, unix.SYS_FCHOWNAT, unix.SYS_UTIMENSAT,
	unix.SYS_SETXATTR, unix.SYS_LSETXATTR, unix.SYS_FSETXATTR,
	unix.SYS_REMOVEXATTR, unix.SYS_LREMOVEXATTR, unix.SYS_FREMOVEXATTR,
	unix.SYS_INOTIFY_ADD_WATCH, unix.SYS_FANOTIFY_MARK,
}

// DescriptorsOnly is the last layer of a helper that works only on the
// descriptors it was handed (phone-usbshared), applied by the helper to
// itself once it holds them: from then on no thread of the process opens
// or changes a file by path, makes a socket or runs a program; each fails
// with EPERM, and so does everything the app filter denies. It needs
// neither root nor Landlock, so it holds on a developer machine too.
func DescriptorsOnly() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	nrs := append(append(append([]uintptr{}, denied...), archDenied...), helperDenied...)
	f := denyFilter(append(nrs, helperArchDenied...))
	prog := unix.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC,
		uintptr(unsafe.Pointer(&prog)))
	if errno != 0 {
		return fmt.Errorf("seccomp: %w", errno)
	}
	return nil
}

// denyFilter makes nrs fail with EPERM, and clone with namespace flags as
// the app filter does; anything else is allowed (the filters stack: the
// launcher's still applies).
func denyFilter(nrs []uintptr) []unix.SockFilter {
	const (
		ld   = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		jeq  = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
		jset = unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K
		ret  = unix.BPF_RET | unix.BPF_K
	)
	eperm := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	p := []unix.SockFilter{
		{Code: ld, K: 4},
		{Code: jeq, Jt: 1, K: auditArch},
		{Code: ret, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: ld, K: 0},
	}
	for _, nr := range nrs {
		p = append(p, unix.SockFilter{Code: jeq, Jf: 1, K: uint32(nr)}, unix.SockFilter{Code: ret, K: eperm})
	}
	return append(p,
		unix.SockFilter{Code: jeq, Jf: 1, K: unix.SYS_CLONE3},
		unix.SockFilter{Code: ret, K: uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS))},
		unix.SockFilter{Code: jeq, Jf: 3, K: unix.SYS_CLONE},
		unix.SockFilter{Code: ld, K: 16},
		unix.SockFilter{Code: jset, Jf: 1, K: cloneNamespaces},
		unix.SockFilter{Code: ret, K: eperm},
		unix.SockFilter{Code: ret, K: unix.SECCOMP_RET_ALLOW},
	)
}
