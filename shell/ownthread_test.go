package shell

import (
	"os"
	"runtime"
	"strconv"
	"syscall"
	"testing"
)

// The tests' main goroutine stays on the process's main thread, so TestMain
// can call ownThread from there.
func init() { runtime.LockOSThread() }

// ownThreadOnMain is the thread ownThread ran f on when called from the main
// thread (0 when TestMain was not on it).
var ownThreadOnMain int

func TestMain(m *testing.M) {
	closeInheritedOnExec()
	if syscall.Gettid() == syscall.Getpid() {
		ownThread(func() { ownThreadOnMain = syscall.Gettid() })
	}
	done := testDict()
	code := m.Run()
	done()
	removeStaged()
	os.Exit(code)
}

// closeInheritedOnExec keeps what the test process inherited from the
// processes it starts. The selftest lock is such a descriptor (flock
// /tmp/kario-selftest.lock go test ./shell): the end-to-end tests' services
// inherited it, and when a timed-out test left them behind they held the
// lock until killed by hand.
func closeInheritedOnExec() {
	ents, _ := os.ReadDir("/proc/self/fd")
	for _, e := range ents {
		if fd, err := strconv.Atoi(e.Name()); err == nil && fd > 2 {
			syscall.CloseOnExec(fd)
		}
	}
}

// A radio moved into a namespace unshared on the main thread came back
// only when the Shell exited (QEMU, 2026-11-17): Go keeps the main thread
// when its goroutine ends locked, and /proc/self/net showed the device's
// routes meanwhile.
func TestOwnThreadIsNotTheMainThread(t *testing.T) {
	if ownThreadOnMain == 0 {
		t.Skip("TestMain did not run on the main thread")
	}
	if ownThreadOnMain == syscall.Getpid() {
		t.Fatal("ownThread ran f on the main thread")
	}
}
