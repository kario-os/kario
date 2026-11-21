package sandbox

import "golang.org/x/sys/unix"

// The path calls AArch64 never had.
var helperArchDenied = []uintptr{
	unix.SYS_OPEN, unix.SYS_CREAT, unix.SYS_MKDIR, unix.SYS_RMDIR, unix.SYS_UNLINK, unix.SYS_RENAME,
	unix.SYS_LINK, unix.SYS_SYMLINK, unix.SYS_MKNOD, unix.SYS_CHMOD, unix.SYS_CHOWN, unix.SYS_LCHOWN,
	unix.SYS_UTIME, unix.SYS_UTIMES, unix.SYS_FUTIMESAT,
}
