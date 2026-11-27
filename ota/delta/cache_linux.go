package delta

import "golang.org/x/sys/unix"

// dropCache asks the kernel to forget the cached pages of a device, so that
// what is read back comes from the device.
func dropCache(d Device) {
	if f, ok := d.(interface{ Fd() uintptr }); ok {
		_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
	}
}
