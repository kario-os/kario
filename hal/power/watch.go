package power

import (
	"bytes"
	"errors"

	"golang.org/x/sys/unix"
)

// Watch calls changed each time the kernel says a power supply changed (a
// uevent: a charger plugged in or out, the gauge's new reading), for as
// long as the process runs; an error where its uevents cannot be read.
// The kernel's own uevents (group 1), not udevd's: they come without
// udevd and before its rules run.
func Watch(changed func()) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: 1}); err != nil {
		unix.Close(fd)
		return err
	}
	go func() {
		defer unix.Close(fd)
		buf := make([]byte, 16<<10)
		for {
			n, _, err := unix.Recvfrom(fd, buf, 0)
			switch {
			case errors.Is(err, unix.EINTR):
				continue
			case errors.Is(err, unix.ENOBUFS):
				// Events were lost; the charger's may have been one.
				changed()
				continue
			case err != nil:
				return
			}
			if supplyEvent(buf[:n]) {
				changed()
			}
		}
	}()
	return nil
}

// supplyEvent: a kernel uevent ("change@/devices/…", then KEY=value
// fields, each ended by a NUL) of the power_supply subsystem.
func supplyEvent(msg []byte) bool {
	for _, f := range bytes.Split(msg, []byte{0}) {
		if string(f) == "SUBSYSTEM=power_supply" {
			return true
		}
	}
	return false
}
