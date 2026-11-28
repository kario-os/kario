package sandbox

import (
	"errors"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Kernel is what the running kernel provides of what the launcher and the
// services rely on (24-sku-framework.md §7.5). ProbeKernel finds out
// without changing the caller: no filter is installed, no namespace made,
// what it opens it closes.
type Kernel struct {
	// Landlock is the ABI version, 0 without one: before Linux 5.13, or
	// built but not enabled at boot (LandlockOff).
	Landlock    int  `json:"landlock"`
	LandlockOff bool `json:"landlock_off,omitempty"`
	// SeccompFilter: seccomp filters (3.5). SeccompTSync: seccomp(2) and
	// its TSYNC (3.17); without it the launcher installs the filter with
	// prctl. KillProcess: SECCOMP_RET_KILL_PROCESS (4.14); before, a
	// foreign-architecture call ends only its thread.
	SeccompFilter bool `json:"seccomp_filter"`
	SeccompTSync  bool `json:"seccomp_tsync"`
	KillProcess   bool `json:"seccomp_kill_process"`
	NoNewPrivs    bool `json:"no_new_privs"`
	// NoNamespaces are the namespaces an app gets that the kernel lacks.
	NoNamespaces []string `json:"no_namespaces,omitempty"`
	// Pidfd: pidfd_open (5.3). MemfdSeal: memfd_create with seals (3.17),
	// which the sound rings and media surfaces need.
	Pidfd     bool `json:"pidfd"`
	MemfdSeal bool `json:"memfd_seal"`
}

// AppNamespaces are the namespaces the Shell starts apps in.
var AppNamespaces = []string{"mnt", "pid", "ipc", "uts", "net"}

// kernelCalls are the probes' system calls; tests give old kernels' errors.
type kernelCalls struct {
	landlock  func() (int, error)
	prctl     func(option int, arg2 uintptr) error
	seccomp   func(op, flags uint, args unsafe.Pointer) error
	pidfd     func() error
	memfd     func() error
	namespace func(kind string) bool
	status    func() string
}

var realCalls = kernelCalls{
	landlock: landlockVersion,
	prctl: func(option int, arg2 uintptr) error {
		_, _, errno := unix.Syscall6(unix.SYS_PRCTL, uintptr(option), arg2, 0, 0, 0, 0)
		if errno != 0 {
			return errno
		}
		return nil
	},
	seccomp: func(op, flags uint, args unsafe.Pointer) error {
		_, _, errno := unix.Syscall(unix.SYS_SECCOMP, uintptr(op), uintptr(flags), uintptr(args))
		if errno != 0 {
			return errno
		}
		return nil
	},
	pidfd: func() error {
		fd, err := unix.PidfdOpen(os.Getpid(), 0)
		if err == nil {
			unix.Close(fd)
		}
		return err
	},
	memfd: func() error {
		fd, err := unix.MemfdCreate("kario-probe", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		_, err = unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_SEAL)
		return err
	},
	namespace: func(kind string) bool {
		_, err := os.Stat("/proc/self/ns/" + kind)
		return err == nil
	},
	status: func() string {
		raw, _ := os.ReadFile("/proc/self/status")
		return string(raw)
	},
}

// ProbeKernel looks at the running kernel.
func ProbeKernel() Kernel { return probeKernel(realCalls) }

func probeKernel(k kernelCalls) Kernel {
	var out Kernel
	abi, err := k.landlock()
	if err == nil && abi > 0 {
		out.Landlock = abi
	}
	out.LandlockOff = errors.Is(err, unix.EOPNOTSUPP)
	// A process already under a filter (a service manager's) has them;
	// otherwise filter mode with no program fails with EFAULT where the
	// kernel has filters, EINVAL where it has none.
	out.SeccompFilter = strings.Contains(k.status(), "\nSeccomp:\t2") ||
		!errors.Is(k.prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER), unix.EINVAL)
	if out.SeccompFilter {
		// SECCOMP_GET_ACTION_AVAIL came in 4.14 with KILL_PROCESS; 3.17 to
		// 4.13 have seccomp(2) but not the operation (EINVAL).
		action := uint32(unix.SECCOMP_RET_KILL_PROCESS)
		err := k.seccomp(unix.SECCOMP_GET_ACTION_AVAIL, 0, unsafe.Pointer(&action))
		out.SeccompTSync = !errors.Is(err, unix.ENOSYS)
		out.KillProcess = err == nil
	}
	out.NoNewPrivs = !errors.Is(k.prctl(unix.PR_GET_NO_NEW_PRIVS, 0), unix.EINVAL)
	for _, ns := range AppNamespaces {
		if !k.namespace(ns) {
			out.NoNamespaces = append(out.NoNamespaces, ns)
		}
	}
	out.Pidfd = k.pidfd() == nil
	out.MemfdSeal = k.memfd() == nil
	return out
}
