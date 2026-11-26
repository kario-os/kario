package shell

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// e2eEndEnv tells a child of TestScenarioPartEnds how its scenario part
// ends.
const e2eEndEnv = "KARIO_E2E_END"

// However the process running a scenario part ends, nothing it started is
// left: the part fails (the test's Cleanups run), a panic on another
// goroutine ends it (go test's timeout panics on its timer's), it is killed
// (by the context of the test that started it, or by hand), or the test
// process that started it is killed. The services of the stacks outlived
// all but the first, by the thousand on a shared machine (2026-11-26).
func TestScenarioPartEnds(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end stack skipped in -short mode")
	}
	if end := os.Getenv(e2eEndEnv); end != "" {
		partEnds(t, end)
		return
	}
	go stagedPrefix()
	t.Parallel()
	prefix, err := stagedPrefix()
	if err != nil {
		t.Fatal(err)
	}
	for _, end := range []string{"fail", "timeout", "kill", "parent"} {
		t.Run(end, func(t *testing.T) {
			t.Parallel()
			file := filepath.Join(t.TempDir(), "run-dir")
			cmd := childCmd(t, nil, e2eEndEnv+"="+end, e2eRunFileEnv+"="+file, e2ePrefixEnv+"="+prefix)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			// A grandchild left behind would hold the output open.
			cmd.WaitDelay = 5 * time.Second
			if err := StartProcess(cmd); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			if end == "kill" || end == "parent" {
				// The last service up, then into the part's boot.
				for t0 := time.Now(); time.Since(t0) < time.Minute; time.Sleep(50 * time.Millisecond) {
					if raw, _ := os.ReadFile(file); len(raw) > 0 && fileExists(filepath.Join(string(raw), "storesrv.addr")) {
						break
					}
				}
				time.Sleep(time.Second)
				_ = cmd.Process.Kill()
			}
			err := <-exited
			defer reapChild(file)
			raw, rerr := os.ReadFile(file)
			if rerr != nil || len(raw) == 0 {
				t.Fatalf("the part's stack did not come up (%v):\n%s", err, out.String())
			}
			if err == nil {
				t.Errorf("the part's process ended well:\n%s", out.String())
			}
			dir, env := filepath.Dir(string(raw)), e2eRunFileEnv+"="+file
			left := leftBehind(dir, env)
			for end := time.Now().Add(5 * time.Second); len(left) > 0 && time.Now().Before(end); left = leftBehind(dir, env) {
				time.Sleep(50 * time.Millisecond)
			}
			for _, pid := range left {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			if len(left) > 0 {
				t.Errorf("%d processes of the part's stack in %s outlived it (%v): %v\n%s", len(left), dir, err, left, out.String())
			}
			reapChild(file)
			if fileExists(dir) {
				t.Errorf("%s is left", dir)
			}
		})
	}
}

// partEnds is TestScenarioPartEnds in its child: the boot of a scenario
// part, then a case that fails, or one that hangs until a panic on a
// timer's goroutine ends the process, as go test's timeout does, or until
// the parent kills it; or (parent) the same hanging part in a child of its
// own, as TestSelftestScenario runs its parts.
func partEnds(t *testing.T, end string) {
	if end == "parent" {
		cmd := childCmd(t, nil, e2eEndEnv+"=hang")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := StartProcess(cmd); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		return
	}
	c, _ := e2eStack(t)
	boot, _, _ := strings.Cut(SelftestScript, "\n== ")
	script := boot + "\n== hangs\nsleep 600000\n"
	switch end {
	case "fail":
		script = boot + "\n== fails\nat sys.nowhere 100\n"
	case "timeout":
		time.AfterFunc(time.Second, func() { panic("test timed out after 1s") })
	}
	var out strings.Builder
	if !(&Runner{C: c, Out: &out}).Run(script) {
		t.Fatalf("scenario failed:\n%s", out.String())
	}
}

// leftBehind lists the processes that run a program under dir, have dir or
// a path under it on their command line, or env in their environment.
func leftBehind(dir, env string) []int {
	pids := running(dir)
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() || slices.Contains(pids, pid) {
			continue
		}
		args, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		environ, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if slices.ContainsFunc(strings.Split(string(args), "\x00"), func(a string) bool {
			return a == dir || strings.Contains(a, dir+string(filepath.Separator))
		}) || slices.Contains(strings.Split(string(environ), "\x00"), env) {
			pids = append(pids, pid)
		}
	}
	return pids
}
