package shell

import (
	"runtime"
	"syscall"
)

// ownThread runs f locked to an OS thread for good: f may change the
// thread's namespaces, and the thread ends with f. Never on the process's
// main thread: a namespace unshared there is /proc/self/net's too, and Go
// keeps the main thread when its goroutine ends locked, so the namespace,
// and a radio or link moved into it, would outlive f.
func ownThread(f func()) {
	runtime.LockOSThread()
	if syscall.Gettid() != syscall.Getpid() {
		f()
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Not on this thread, which stays locked meanwhile.
		ownThread(f)
	}()
	<-done
	runtime.UnlockOSThread()
}
