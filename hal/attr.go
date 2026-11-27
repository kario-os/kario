package hal

import (
	"os"
	"syscall"
)

// Device class attributes are read and written with plain system calls,
// never through *os.File: Go puts every file epoll takes into non-blocking
// mode on its poller, epoll takes sysfs attributes, and a driver answering
// EAGAIN (the PMI8998 charger's usb_type while it still detects the
// charger) then parks the caller until the file is ready, which an
// attribute never signals. On a OnePlus 6T that stopped the Shell's loop
// for good (docs/39-oneplus-6t.md §16.10). Here EAGAIN is an error like
// any other; O_NONBLOCK has a file that would block outside a driver's own
// wait (a FIFO, as the tests use) answer EAGAIN at once too.

// ReadAttr is os.ReadFile for an attribute.
func ReadAttr(path string) ([]byte, error) {
	fd, err := openAttr(path, syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := syscall.Read(fd, buf)
		switch {
		case err == syscall.EINTR:
			continue
		case err != nil:
			return out, &os.PathError{Op: "read", Path: path, Err: err}
		case n == 0:
			return out, nil
		}
		out = append(out, buf[:n]...)
	}
}

// WriteAttr is os.WriteFile for an attribute: one write of data (a test's
// tree gets the file made; sysfs makes none).
func WriteAttr(path string, data []byte) error {
	fd, err := openAttr(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC)
	if err != nil {
		return err
	}
	for {
		_, err = syscall.Write(fd, data)
		if err != syscall.EINTR {
			break
		}
	}
	cerr := syscall.Close(fd)
	if err != nil {
		return &os.PathError{Op: "write", Path: path, Err: err}
	}
	if cerr != nil {
		return &os.PathError{Op: "close", Path: path, Err: cerr}
	}
	return nil
}

func openAttr(path string, mode int) (int, error) {
	for {
		fd, err := syscall.Open(path, mode|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0o644)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return -1, &os.PathError{Op: "open", Path: path, Err: err}
		}
		return fd, nil
	}
}
