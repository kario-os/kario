package shell

import (
	"os/exec"
	"runtime"
	"sync"
	"syscall"
)

// Every child process starts from one OS thread, and an in-place restart
// execs from it. Linux sends a child its parent-death signal when the thread
// that forked it ends, not the process, and exec ends every thread but the
// caller, which becomes the new program's main thread. So that thread has to
// be the main thread: any other one is a different thread after the first
// restart, and the second restart would kill every child the first program
// started.
func init() { runtime.LockOSThread() }

var (
	procThread = make(chan func())
	procServer sync.Once
)

// ServeProcesses runs main, starting child processes from the calling
// goroutine meanwhile: call it from the program's main goroutine, which
// stays on the main thread.
func ServeProcesses(main func()) {
	serving := false
	procServer.Do(func() { serving = true })
	if !serving {
		main()
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		main()
	}()
	for {
		select {
		case f := <-procThread:
			f()
		case <-done:
			return
		}
	}
}

func onProcThread(f func()) {
	// Without ServeProcesses (tests, other programs) a thread of its own that
	// never exits starts them.
	procServer.Do(func() {
		go func() {
			runtime.LockOSThread()
			for f := range procThread {
				f()
			}
		}()
	})
	done := make(chan struct{})
	procThread <- func() {
		defer close(done)
		f()
	}
	<-done
}

// StartProcess starts cmd from the process thread.
func StartProcess(cmd *exec.Cmd) (err error) {
	onProcThread(func() { err = cmd.Start() })
	return err
}

func execOnProcThread(exe string, args, env []string) (err error) {
	onProcThread(func() { err = syscall.Exec(exe, args, env) })
	return err
}
