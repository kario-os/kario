package sandbox

import "golang.org/x/sys/unix"

const auditArch = unix.AUDIT_ARCH_AARCH64

var archDenied = []uintptr{unix.SYS_LOOKUP_DCOOKIE}
