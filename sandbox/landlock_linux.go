package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

func landlockABI() int {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(v)
}

const (
	fsRead = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR
	fsExec = unix.LANDLOCK_ACCESS_FS_EXECUTE
	fsRW   = fsRead | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE
	// Rights that may be granted on a file (as opposed to a directory).
	fsFileRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE |
		unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
)

// handledFS lists every filesystem right the kernel's ABI knows; anything
// handled and not granted by a rule is denied.
func handledFS(abi int) uint64 {
	h := uint64(unix.LANDLOCK_ACCESS_FS_MAKE_SYM<<1 - 1) // ABI 1: EXECUTE … MAKE_SYM
	if abi >= 2 {
		h |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		h |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		h |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return h
}

func landlock(p Policy, abi int) error {
	handled := handledFS(abi)
	// Older kernels accept the larger struct as long as the fields they do
	// not know are zero.
	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	if abi >= 4 {
		// Apps never listen on TCP ports; without the network permission
		// they cannot connect either (on top of the empty network namespace).
		attr.Access_net = unix.LANDLOCK_ACCESS_NET_BIND_TCP
		if !p.Net {
			attr.Access_net |= unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
		}
	}
	if abi >= 6 {
		attr.Scoped = unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return errno
	}
	defer unix.Close(int(fd))
	var gpu []string
	if p.GPU {
		gpu, _ = filepath.Glob("/dev/dri/renderD*")
	}
	devices := []string{"/dev/null", "/dev/zero", "/dev/urandom", "/dev/random"}
	if p.Helper {
		devices = nil
	}
	rules := []struct {
		paths  []string
		access uint64
	}{
		{gpu, unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV},
		{p.Code, fsRead | fsExec},
		{p.ReadOnly, fsRead},
		{p.ReadFiles, unix.LANDLOCK_ACCESS_FS_READ_FILE},
		{[]string{p.DataDir}, fsRW},
		{devices, unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE},
	}
	for _, r := range rules {
		for _, path := range r.paths {
			if err := addPathRule(int(fd), path, r.access&handled); err != nil {
				return err
			}
		}
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func addPathRule(ruleset int, path string, access uint64) error {
	if path == "" {
		return nil
	}
	f, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer unix.Close(f)
	var st unix.Stat_t
	if err := unix.Fstat(f, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		access &= fsFileRights
	}
	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(f)}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
	if errno != 0 {
		return &os.PathError{Op: "landlock_add_rule", Path: path, Err: errno}
	}
	return nil
}
