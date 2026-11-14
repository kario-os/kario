package usbshared

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// probe tries what the sandbox must refuse, for the Shell's selftest: each
// answer is the errno's name, "ok" if it worked. The program and the
// process tried do not exist, so a sandbox that let them through would
// see ENOENT and ESRCH rather than run or stop anything.
func probe() map[string]string {
	name := func(err error) string {
		var e unix.Errno
		switch {
		case err == nil:
			return "ok"
		case errors.As(err, &e):
			return unix.ErrnoName(e)
		}
		return err.Error()
	}
	out := map[string]string{}
	fd, err := unix.Open("/etc/passwd", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err == nil {
		unix.Close(fd)
	}
	out["open"] = name(err)
	tmp := fmt.Sprintf("/tmp/.phone-usbshared-%d", os.Getpid())
	fd, err = unix.Open(tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0o600)
	if err == nil {
		unix.Close(fd)
		unix.Unlink(tmp)
	}
	out["create"] = name(err)
	for key, domain := range map[string]int{"socket-inet": unix.AF_INET, "socket-unix": unix.AF_UNIX} {
		fd, err := unix.Socket(domain, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err == nil {
			unix.Close(fd)
		}
		out[key] = name(err)
	}
	out["exec"] = name(unix.Exec("/nonexistent/phone-usbshared-probe", []string{"probe"}, nil))
	out["ptrace"] = name(unix.PtraceAttach(0))
	return out
}
