package sandbox

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// denied syscalls return EPERM. The list follows what Android's app filter
// and container runtimes block: debugging other processes, changing the
// filesystem view, kernel modules and images, eBPF and perf, keyrings,
// io_uring, clock and system state.
var denied = append([]uintptr{
	unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_PIDFD_GETFD,
	unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT,
	unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_FSPICK,
	unix.SYS_MOVE_MOUNT, unix.SYS_OPEN_TREE, unix.SYS_MOUNT_SETATTR,
	unix.SYS_UNSHARE, unix.SYS_SETNS,
	unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE,
	unix.SYS_KEXEC_LOAD, unix.SYS_KEXEC_FILE_LOAD, unix.SYS_REBOOT,
	unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_USERFAULTFD,
	unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
	unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_ACCT, unix.SYS_QUOTACTL,
	unix.SYS_SETTIMEOFDAY, unix.SYS_CLOCK_SETTIME, unix.SYS_CLOCK_ADJTIME, unix.SYS_ADJTIMEX,
	unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_NAME_TO_HANDLE_AT, unix.SYS_SYSLOG,
	unix.SYS_FANOTIFY_INIT, unix.SYS_VHANGUP,
}, archDenied...)

const cloneNamespaces = unix.CLONE_NEWNS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWUTS |
	unix.CLONE_NEWIPC | unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNET

func filter() []unix.SockFilter {
	const (
		ld   = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		jeq  = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
		jset = unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K
		ret  = unix.BPF_RET | unix.BPF_K
	)
	eperm := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	// struct seccomp_data: nr at 0, arch at 4, args[0] at 16 (low half on
	// little-endian, which both supported architectures are).
	p := []unix.SockFilter{
		{Code: ld, K: 4},
		{Code: jeq, Jt: 1, K: auditArch},
		{Code: ret, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: ld, K: 0},
	}
	for _, nr := range denied {
		p = append(p, unix.SockFilter{Code: jeq, Jf: 1, K: uint32(nr)}, unix.SockFilter{Code: ret, K: eperm})
	}
	// clone3 passes its flags in memory the filter cannot read; ENOSYS makes
	// libc fall back to clone, whose flags are checked below.
	p = append(p,
		unix.SockFilter{Code: jeq, Jf: 1, K: unix.SYS_CLONE3},
		unix.SockFilter{Code: ret, K: uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS))},
		unix.SockFilter{Code: jeq, Jf: 3, K: unix.SYS_CLONE},
		unix.SockFilter{Code: ld, K: 16},
		unix.SockFilter{Code: jset, Jf: 1, K: cloneNamespaces},
		unix.SockFilter{Code: ret, K: eperm},
		unix.SockFilter{Code: ret, K: unix.SECCOMP_RET_ALLOW},
	)
	return p
}

func seccomp() error {
	f := filter()
	prog := unix.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC,
		uintptr(unsafe.Pointer(&prog)))
	if errno != 0 {
		return errno
	}
	return nil
}
