package shell

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/tools/prebuilt"
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
			dir := filepath.Dir(string(raw))
			if left := killLeftBehind(dir, e2eRunFileEnv+"="+file, 5*time.Second); len(left) > 0 {
				t.Errorf("%d processes of the part's stack in %s outlived it (%v): %s\n%s", len(left), dir, err, strings.Join(left, "; "), out.String())
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

// A test process killed before its TestMain leaves its staged directory
// (about 190 MB with its parts' stacks); the next run removes it at its
// start, with what still runs from it. Only those: the directories of live
// runs, this one's among them, and those named for no owner stay.
func TestStaleStagedRemoved(t *testing.T) {
	tmp := t.TempDir()
	sleep := func() (*exec.Cmd, uint64) {
		cmd := exec.Command("sleep", "600")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		start, ok := procStart(cmd.Process.Pid)
		if !ok {
			t.Fatalf("no start time of %d", cmd.Process.Pid)
		}
		return cmd, start
	}
	staged := func(pid int, start uint64) string {
		d, err := os.MkdirTemp(tmp, fmt.Sprintf(e2eStagedPattern, pid, start))
		if err == nil {
			err = os.MkdirAll(filepath.Join(d, "usr", "bin"), 0o755)
		}
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	live, liveStart := sleep()
	t.Cleanup(func() { _ = live.Process.Kill(); _ = live.Wait() })
	dead, deadStart := sleep()
	_ = dead.Process.Kill()
	_ = dead.Wait()
	ownStart, ok := procStart(os.Getpid())
	if !ok {
		t.Fatal("no start time of this process")
	}
	liveDir, ownDir := staged(live.Process.Pid, liveStart), staged(os.Getpid(), ownStart)
	deadDir, reusedDir := staged(dead.Process.Pid, deadStart), staged(os.Getpid(), ownStart+1)
	nameless, err := os.MkdirTemp(tmp, "kario-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	// A service of the killed run that outlived it, running from its prefix.
	// Copied by cp: a file this process wrote may be held open for writing
	// by a child forked meanwhile, and exec then fails with ETXTBSY.
	path, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(deadDir, "usr", "bin", "sleep")
	if out, err := exec.Command("cp", path, prog).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	left := exec.Command(prog, "600")
	if err := left.Start(); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- left.Wait() }()

	removeStaleStaged(tmp)
	for _, d := range []string{liveDir, ownDir, nameless} {
		if !fileExists(d) {
			t.Errorf("%s was removed", filepath.Base(d))
		}
	}
	for _, d := range []string{deadDir, reusedDir} {
		if fileExists(d) {
			t.Errorf("%s is left", filepath.Base(d))
		}
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		_ = left.Process.Kill()
		t.Error("what ran from the killed run's prefix still runs")
	}

	_ = live.Process.Kill()
	_ = live.Wait()
	removeStaleStaged(tmp)
	if fileExists(liveDir) {
		t.Errorf("%s is left after its owner ended", filepath.Base(liveDir))
	}
}

// Staging fails, naming what to build, when the prefix lacks a program the
// scenario runs; e2eBuild builds each first.
func TestStagedLacks(t *testing.T) {
	prefix := t.TempDir()
	for _, n := range e2eNeeds {
		if !slices.Contains(e2eMakeTargets, n.target) {
			t.Errorf("e2eBuild does not make %s, for %s", n.target, n.file)
		}
		p := filepath.Join(prefix, n.file)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if n.file == "bin/phone-telephonyd" {
			continue
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	err := stagedLacks(prefix)
	if err == nil || err.Error() != "the staged prefix has no bin/phone-telephonyd (make telephonyd)" {
		t.Errorf("without telephonyd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", "phone-telephonyd"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := stagedLacks(prefix); err != nil {
		t.Error(err)
	}
}

// e2eStageEnv names the directory a child of
// TestStagingBuildEndsWithTheTest stages in.
const e2eStageEnv = "KARIO_E2E_STAGE"

// A test process killed while it stages (go test's timeout, by hand) ends
// the build it started: the make went on building in the tree, its lock
// held, and ninja's jobs, in process groups of their own, with it. The
// build of another tree, in a test process of its own, goes on.
func TestStagingBuildEndsWithTheTest(t *testing.T) {
	if dir := os.Getenv(e2eStageEnv); dir != "" {
		e2eMakeLock = filepath.Join(dir, "lock")
		t.Fatal(e2eBuild(dir))
	}
	t.Parallel()
	bin := t.TempDir()
	for name, body := range map[string]string{
		"go":   "#!/bin/sh\n",
		"make": "#!/bin/sh\nset -m\nsleep 600 &\necho $! > \"$" + e2eStageEnv + "/job.new\"\nmv \"$" + e2eStageEnv + "/job.new\" \"$" + e2eStageEnv + "/job\"\nwait\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	type stager struct {
		cmd      *exec.Cmd
		dir, job string
	}
	start := func() stager {
		dir := t.TempDir()
		cmd := childCmd(t, nil, e2eStageEnv+"="+dir, "PATH="+bin+":"+os.Getenv("PATH"), prebuilt.RequiredEnv+"=")
		if err := StartProcess(cmd); err != nil {
			t.Fatal(err)
		}
		job := filepath.Join(dir, "job")
		for end := time.Now().Add(20 * time.Second); !fileExists(job); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				_ = cmd.Process.Kill()
				t.Fatal("the build's make did not start")
			}
		}
		return stager{cmd, dir, job}
	}
	end := func(s stager) {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
		if left := killLeftBehind(s.dir, e2eStageEnv+"="+s.dir, 10*time.Second); len(left) > 0 {
			t.Errorf("%d processes of the build outlived the test: %s", len(left), strings.Join(left, "; "))
		}
	}
	killed, other := start(), start()
	end(killed)
	raw, _ := os.ReadFile(other.job)
	if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid == 0 || !processAlive(pid) {
		t.Error("the other tree's build ended with this one")
	}
	end(other)
}

// killLeftBehind waits up to d for leftBehind(dir, env) to be empty, then
// kills what is left and names it.
func killLeftBehind(dir, env string, d time.Duration) []string {
	left := leftBehind(dir, env)
	for end := time.Now().Add(d); len(left) > 0 && time.Now().Before(end); left = leftBehind(dir, env) {
		time.Sleep(50 * time.Millisecond)
	}
	names := make([]string, len(left))
	for i, pid := range left {
		args, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		names[i] = strconv.Itoa(pid) + " " + strings.ReplaceAll(strings.TrimRight(string(args), "\x00"), "\x00", " ")
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return names
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
