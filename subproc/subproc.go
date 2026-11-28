// Package subproc starts the helper processes a program runs (a decoder,
// the sound card's player, a compiler) so that they end with it. Where
// systemd runs a service it stops the service's whole cgroup, but a
// service another supervisor restarts (phone-shell --services spawn,
// phone-shell's own supervision, the end-to-end tests) left its helpers
// running with the old state: the card held, files open.
//
// A child gets a parent-death signal, SIGKILL with Command. Linux sends it
// when the thread that forked the child ends, not the process, and Go ends
// a thread whenever a goroutine locked to it returns, maybe a thread an
// unlocked goroutine forked a child from before. So every child starts
// from one thread of this package's that stays locked: it ends with the
// process, or when the process execs, which ends every thread but the
// caller. Children that have to outlive the Shell's in-place restart start
// from its main thread instead (shell.StartProcess).
package subproc

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
)

var (
	starts = make(chan func())
	thread sync.Once
)

// Command is exec.Command for a child that gets SIGKILL when this process
// ends; start it with Start.
func Command(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return cmd
}

// CommandContext is Command, killed when ctx is done.
func CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return cmd
}

// Start starts cmd from the package's thread.
func Start(cmd *exec.Cmd) error {
	thread.Do(func() {
		go func() {
			runtime.LockOSThread()
			for f := range starts {
				f()
			}
		}()
	})
	errc := make(chan error, 1)
	starts <- func() { errc <- cmd.Start() }
	return <-errc
}

// Output runs cmd, started with Start, and returns its standard output.
func Output(cmd *exec.Cmd) ([]byte, error) {
	var out bytes.Buffer
	cmd.Stdout = &out
	err := run(cmd)
	return out.Bytes(), err
}

// CombinedOutput runs cmd, started with Start, and returns its standard
// output and standard error.
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := run(cmd)
	return out.Bytes(), err
}

func run(cmd *exec.Cmd) error {
	if err := Start(cmd); err != nil {
		return err
	}
	return cmd.Wait()
}
