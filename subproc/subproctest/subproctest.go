// Package subproctest checks that a program's helper processes end with
// it: a test runs itself again in a child process of the test binary,
// which starts a stand-in for the helper; the child is killed, and the
// stand-in has to end with it.
package subproctest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// PidEnv names the file a stand-in writes its pid to.
const PidEnv = "KARIO_STANDIN_PID"

// StandIn is a stand-in's program: it writes its pid and sleeps.
const StandIn = "#!/bin/sh\necho $$ > \"$" + PidEnv + "\"\nexec sleep 600\n"

// StandInHere is StandIn with the path of the file written in, for a
// stand-in the child writes for a helper started with an environment of
// its own.
func StandInHere() string {
	return "#!/bin/sh\necho $$ > '" + os.Getenv(PidEnv) + "'\nexec sleep 600\n"
}

const childEnv = "KARIO_SUBPROCTEST_CHILD"

// Child reports whether this process is the child Survivor runs t in.
func Child(t *testing.T) bool { return os.Getenv(childEnv) == t.Name() }

// Survivor runs t again in a child process of the test binary, with env
// added, and kills the child (SIGKILL) once a stand-in it started has
// written its pid. It returns the stand-in's pid when the stand-in is still
// running 5 s later, killing it then, and 0 when it ended with the child.
// The child's temporary files go in a directory Survivor removes: killed,
// it runs no Cleanup.
func Survivor(t *testing.T, env ...string) int {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	// Short: the child's socket paths are in it.
	tmp, err := os.MkdirTemp("", "subproctest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	var run []string
	for _, n := range strings.Split(t.Name(), "/") {
		run = append(run, "^"+regexp.QuoteMeta(n)+"$")
	}
	cmd := exec.Command(os.Args[0], "-test.run="+strings.Join(run, "/"), "-test.count=1", "-test.v")
	cmd.Env = append(append(os.Environ(), env...), childEnv+"="+t.Name(), PidEnv+"="+pidFile, "TMPDIR="+tmp)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// A stand-in that outlives the child may hold its output open.
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	pid := 0
	for end := time.Now().Add(20 * time.Second); pid == 0; time.Sleep(20 * time.Millisecond) {
		raw, _ := os.ReadFile(pidFile)
		if pid, _ = strconv.Atoi(strings.TrimSpace(string(raw))); pid != 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the child ended before the stand-in started: %v\n%s", err, out.Bytes())
		default:
		}
		if time.Now().After(end) {
			_ = cmd.Process.Kill()
			<-done
			t.Fatalf("the stand-in did not start:\n%s", out.Bytes())
		}
	}
	_ = cmd.Process.Kill()
	<-done
	if Gone(pid, 5*time.Second) {
		return 0
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return pid
}

// Gone reports whether process pid has ended (or is a zombie) within d.
func Gone(pid int, d time.Duration) bool {
	for end := time.Now().Add(d); ; time.Sleep(20 * time.Millisecond) {
		if !Alive(pid) {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}

// Alive reports whether process pid runs (is there and not a zombie).
func Alive(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	return err == nil && i > 0 && i+2 < len(s) && s[i+2] != 'Z'
}

// OnEndingThread runs f on a goroutine locked to its OS thread and returns
// once that thread has ended: a child f started from it with a
// parent-death signal has been sent the signal by then.
func OnEndingThread(t *testing.T, f func()) {
	t.Helper()
	tid := make(chan int, 1)
	var on func()
	on = func() {
		runtime.LockOSThread()
		if syscall.Gettid() != syscall.Getpid() {
			f()
			tid <- syscall.Gettid()
			return
		}
		// Go keeps the main thread when its goroutine ends locked: f goes
		// to another thread while this one is held.
		done := make(chan struct{})
		go func() {
			defer close(done)
			on()
		}()
		<-done
		runtime.UnlockOSThread()
	}
	go on()
	task := fmt.Sprintf("/proc/self/task/%d", <-tid)
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(task); os.IsNotExist(err) {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("thread %s did not end", filepath.Base(task))
		}
	}
}
