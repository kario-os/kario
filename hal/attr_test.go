package hal

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAttrReadAndWrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "brightness")
	if err := WriteAttr(p, []byte("1023\n")); err != nil {
		t.Fatal(err)
	}
	if err := WriteAttr(p, []byte("7")); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadAttr(p); err != nil || string(b) != "7" {
		t.Fatalf("ReadAttr = %q, %v", b, err)
	}
	if _, err := ReadAttr(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing attribute: %v", err)
	}
}

// eagainAttr is a FIFO held open for reading and writing with nothing in
// it: a read answers EAGAIN, as a sysfs attribute whose driver is not
// ready does (the PMI8998 charger's usb_type), and epoll takes it, as it
// takes sysfs attributes.
func eagainAttr(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "usb_type")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip("no FIFO:", err)
	}
	fd, err := syscall.Open(p, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Closing the last writer ends any read still waiting (with EOF).
	t.Cleanup(func() { syscall.Close(fd) })
	return p
}

// An attribute answering EAGAIN is an error at once. os.ReadFile, which
// the HAL used before, parks until the file says it is ready, which a
// sysfs attribute never does: the Shell's loop stopped there on a 6T.
func TestAnAttributeAnsweringEAGAINIsAnError(t *testing.T) {
	p := eagainAttr(t)
	done := make(chan error, 1)
	go func() {
		_, err := ReadAttr(p)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, syscall.EAGAIN) {
			t.Fatalf("ReadAttr: %v, want EAGAIN", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadAttr waited for an attribute that answers EAGAIN")
	}
	old := make(chan struct{})
	go func() {
		_, _ = os.ReadFile(p)
		close(old)
	}()
	select {
	case <-old:
		t.Log("os.ReadFile returned: this Go does not park on EAGAIN")
	case <-time.After(300 * time.Millisecond):
	}
}

// A write the driver answers with EAGAIN (a suspend a device refuses for
// now) is an error at once too; here a full FIFO answers it.
func TestAWriteAnsweredEAGAINIsAnError(t *testing.T) {
	p := eagainAttr(t)
	fd, err := syscall.Open(p, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	block := make([]byte, 4096)
	for {
		if _, err := syscall.Write(fd, block); err != nil {
			break
		}
	}
	done := make(chan error, 1)
	go func() { done <- WriteAttr(p, []byte("mem")) }()
	select {
	case err := <-done:
		if !errors.Is(err, syscall.EAGAIN) {
			t.Fatalf("WriteAttr: %v, want EAGAIN", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WriteAttr waited on a write answered EAGAIN")
	}
}
