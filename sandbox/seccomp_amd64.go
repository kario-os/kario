package sandbox

import "golang.org/x/sys/unix"

const auditArch = unix.AUDIT_ARCH_X86_64

var archDenied = []uintptr{unix.SYS_IOPL, unix.SYS_IOPERM, unix.SYS_USELIB, unix.SYS_MODIFY_LDT, unix.SYS_LOOKUP_DCOOKIE}
