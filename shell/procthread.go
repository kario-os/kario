package shell

import (
	"os/exec"
	"runtime"
	"syscall"
)

// Every child process starts from one OS thread that never exits, and an
// in-place restart execs from it. Linux sends a child its parent-death
// signal when the thread that forked it ends, not the process, and exec
// ends every thread but the caller: children forked from any other thread
// would be killed by the restart.
var procThread = func() chan func() {
	ch := make(chan func())
	go func() {
		runtime.LockOSThread()
		for f := range ch {
			f()
		}
	}()
	return ch
}()

func onProcThread(f func()) {
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
