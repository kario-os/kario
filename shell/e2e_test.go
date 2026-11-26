package shell

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/tools/prebuilt"
)

// running lists the processes still running a program under dir.
func running(dir string) []int {
	var pids []int
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		if exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe")); err == nil && strings.HasPrefix(exe, dir+string(filepath.Separator)) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// noneLeft fails the test if a program under dir outlives it (a frozen app
// the Shell left behind stays stopped forever), and kills what is left.
func noneLeft(t *testing.T, dir string) {
	t.Cleanup(func() {
		left := running(dir)
		for end := time.Now().Add(3 * time.Second); len(left) > 0 && time.Now().Before(end); left = running(dir) {
			time.Sleep(50 * time.Millisecond)
		}
		for _, pid := range left {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		if len(left) > 0 {
			t.Errorf("%d processes outlived the test: %v", len(left), left)
		}
	})
}

// The scenario's parts run in child processes of the test binary: the part
// a child runs, and the prefix its parent staged for all of them.
const (
	e2ePartEnv   = "KARIO_E2E_PART"
	e2ePrefixEnv = "KARIO_E2E_PREFIX"
)

// scenarioPart is the script a part runs: its cases, in the script's order
// and at the script's line numbers, after the boot (the lines before the
// first case), and from the idle screen the boot leaves.
type scenarioPart struct {
	name   string
	script string
}

// scenarioParts splits a selftest script by its "#part <name>" lines: a case
// belongs to the part the nearest one above it names.
func scenarioParts(script string) ([]scenarioPart, error) {
	names, err := ScriptParts(script)
	if err != nil {
		return nil, err
	}
	var parts []scenarioPart
	for _, name := range names {
		text, err := SelectParts(script, []string{name})
		if err != nil {
			return nil, err
		}
		parts = append(parts, scenarioPart{name, text})
	}
	return parts, nil
}

// Every case of the selftest script is in exactly one part, at its own line
// number, after the boot; a case above the first part's line is refused.
func TestScenarioParts(t *testing.T) {
	parts, err := scenarioParts(SelftestScript)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(SelftestScript, "\n")
	seen := map[int]string{}
	for _, p := range parts {
		pl := strings.Split(p.script, "\n")
		if len(pl) != len(lines) || !slices.Contains(pl, "snap 00-boot") {
			t.Fatalf("part %s: %d lines, boot %v", p.name, len(pl), slices.Contains(pl, "snap 00-boot"))
		}
		for i, l := range pl {
			if strings.HasPrefix(l, "== ") {
				if l != lines[i] || seen[i] != "" {
					t.Errorf("line %d: %q in part %s (and %q)", i+1, l, p.name, seen[i])
				}
				seen[i] = p.name
			}
		}
	}
	for i, l := range lines {
		if strings.HasPrefix(l, "== ") && seen[i] == "" {
			t.Errorf("line %d: %q is in no part", i+1, l)
		}
	}
	if len(parts) < 2 {
		t.Fatalf("%d parts", len(parts))
	}
	if _, err := scenarioParts("sleep 1\n== a case\nkey f4\n#part x\n== another\n"); err == nil {
		t.Error("a case before the first part line was taken")
	}
}

// TestSelftestScenario builds every binary, starts private services and runs
// the built-in end-to-end scenario through the real process boundaries:
// each part of the script on a stack of its own, the parts in parallel
// (phone-shell --selftest runs the script whole, in order).
func TestSelftestScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end scenario skipped in -short mode")
	}
	parts, err := scenarioParts(SelftestScript)
	if err != nil {
		t.Fatal(err)
	}
	if name := os.Getenv(e2ePartEnv); name != "" {
		i := slices.IndexFunc(parts, func(p scenarioPart) bool { return p.name == name })
		if i < 0 {
			t.Fatalf("%s=%s: the script has no such part", e2ePartEnv, name)
		}
		// A sound switch as the 6T's: the case for it moves it (sound-switch)
		// and puts it back where no renderer reported one.
		c, _ := e2eStackWith(t, func(_ string, cfg *Config) { cfg.SKU.SoundSwitch = "Alert slider" })
		var out strings.Builder
		if !(&Runner{C: c, Out: &out}).Run(parts[i].script) {
			t.Fatalf("scenario failed:\n%s", out.String())
		}
		t.Log("\n" + out.String())
		return
	}
	// Staging starts while the tests that are not parallel still run, below
	// them in the scheduler: many of them are timed.
	go stagedPrefix()
	t.Parallel()
	prefix, err := stagedPrefix()
	if err != nil {
		t.Fatal(err)
	}
	// The parts start together whatever -parallel allows: they wait on
	// their apps and services far more than they compute.
	results := make([]chan childResult, len(parts))
	files := t.TempDir()
	for i, p := range parts {
		results[i] = make(chan childResult, 1)
		go func() {
			file := filepath.Join(files, p.name)
			r := runChild(t, nil, e2ePartEnv+"="+p.name, e2ePrefixEnv+"="+prefix, e2eRunFileEnv+"="+file)
			reapChild(file)
			results[i] <- r
		}()
	}
	for i, p := range parts {
		t.Run(p.name, func(t *testing.T) { (<-results[i]).report(t) })
	}
}

// aloneEnv names the test a child process of the test binary runs (alone).
const aloneEnv = "KARIO_TEST_ALONE"

// imeDictEnv names the input method dictionary the test process built for
// its children (testDict).
const imeDictEnv = "KARIO_TEST_IME_DICT"

// testDict maps one input method dictionary, built once, in the test
// process and its children, as phone-shell maps the image's: built in each
// child, it takes seconds under -race, inside the waits of the first test
// to spell a name. It returns what removes the file.
func testDict() func() {
	if p := os.Getenv(imeDictEnv); p != "" {
		ime.SetDictPath(p)
		return func() {}
	}
	dir, err := os.MkdirTemp("", "kario-ime-")
	if err != nil {
		return func() {}
	}
	p := filepath.Join(dir, "ime.dict")
	if err := os.WriteFile(p, ime.BuildDict(), 0o644); err != nil {
		os.RemoveAll(dir)
		return func() {}
	}
	os.Setenv(imeDictEnv, p)
	ime.SetDictPath(p)
	return func() { os.RemoveAll(dir) }
}

// aloneSlots bounds the children running at once: the parallel tests all
// start when the others end, and a hundred children starting together
// starve the timed ones among them (under -race most).
var aloneSlots = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)/4))

// alone runs the test again in a child process of its own, in parallel with
// the other tests, and reports whether this is that child. A Shell started
// in a process sets the process's language (i18n.SetLang), which every
// other Shell in it reads, and tests set package variables (clocks, hooks,
// device roots) for the Shell they start. Under -cover the child writes its
// counters where go test merges this process's (-test.gocoverdir).
func alone(t *testing.T) bool {
	if os.Getenv(aloneEnv) == t.Name() {
		return true
	}
	t.Parallel()
	aloneSlots <- struct{}{}
	defer func() { <-aloneSlots }()
	var flags []string
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		flags = append(flags, "-test.gocoverdir="+f.Value.String())
	}
	r := runChild(t, flags, aloneEnv+"="+t.Name())
	if r.err == nil && bytes.Contains(r.out, []byte("--- SKIP: "+t.Name()+" ")) {
		t.Skip("\n" + string(r.out))
	}
	r.report(t)
	return false
}

// childResult is how a test run again in a child process of the test
// binary went (runChild).
type childResult struct {
	out []byte
	err error
}

// runChild runs the test again in a child process of the test binary, with
// flags added to its command line and env to its environment.
func runChild(t *testing.T, flags []string, env ...string) childResult {
	cmd := childCmd(t, flags, env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := StartProcess(cmd)
	if err == nil {
		err = cmd.Wait()
	}
	return childResult{out.Bytes(), err}
}

// e2eRunFileEnv names the file a child of the test binary writes the run
// directory of its end-to-end stack to (e2eStackWith), for reapChild.
const e2eRunFileEnv = "KARIO_E2E_RUN_FILE"

// reapChild kills what still runs from the temporary directory of the
// stack a child started, which it named in file, and removes the
// directory: a child that ends without its Cleanups (go test's timeout, a
// kill) leaves its copy of the prefix behind.
func reapChild(file string) {
	raw, err := os.ReadFile(file)
	if err != nil || len(raw) == 0 {
		return
	}
	dir := filepath.Dir(string(raw))
	if !strings.HasPrefix(dir, os.TempDir()+string(filepath.Separator)) {
		return
	}
	for _, pid := range running(dir) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	_ = os.RemoveAll(dir)
	// t.TempDir's parent, empty when dir was its only directory.
	_ = os.Remove(filepath.Dir(dir))
}

// childCmd is runChild's command, to be started with StartProcess. The
// child dies with this process: killed by go test's timeout or by hand,
// it runs no Cleanup that would end the child.
func childCmd(t *testing.T, flags []string, env ...string) *exec.Cmd {
	args := append([]string{"-test.run=^" + strings.Split(t.Name(), "/")[0] + "$", "-test.count=1", "-test.v"}, flags...)
	if d, ok := t.Deadline(); ok && time.Until(d) > 20*time.Second {
		args = append(args, "-test.timeout="+(time.Until(d)-10*time.Second).String())
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return cmd
}

// report fails the test with the child's output if the child failed, and
// logs it otherwise.
func (r childResult) report(t *testing.T) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("%v:\n%s", r.err, r.out)
	}
	t.Log("\n" + string(r.out))
}

// e2eStage builds every program and stages them in dir/usr, as make build
// does.
func e2eStage(t *testing.T, dir string) string {
	if err := e2eBuild(dir); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "usr")
}

// e2eBuild is e2eStage's work, each command run after the words of with
// (nice, say).
func e2eBuild(dir string, with ...string) error {
	bin, prefix := filepath.Join(dir, "bin"), filepath.Join(dir, "usr")
	run := func(what string, args ...string) error {
		args = slices.Concat(with, args)
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v\n%s", what, err, out)
		}
		return nil
	}
	if err := run("build", "go", "build", "-o", bin+"/", "github.com/kario-os/kario/cmd/...", "github.com/kario-os/kario/apps/...", "github.com/kario-os/kario/cts/...", "github.com/kario-os/kario/examples/..."); err != nil {
		return err
	}
	if os.Getenv(prebuilt.RequiredEnv) == "" {
		// One make at a time in the tree: test processes stage at once.
		lock := filepath.Join(prebuilt.Root(), "build", ".e2e-make.lock")
		if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
			return err
		}
		args := append([]string{"flock", lock, "make", "-s", "-C", ".."}, e2eMakeTargets...)
		if err := run("make "+strings.Join(e2eMakeTargets, " "), args...); err != nil {
			return err
		}
	}
	return run("stage", "sh", "../scripts/stage.sh", bin, prefix)
}

// e2eMakeTargets build what stage.sh takes from build/ when it is there:
// kario-jsrt, without which the TypeScript apps (the calculator and the
// calendar, system apps, and the sample) are staged without a module and
// do not start; the C++ sample app; the native example's library. make
// test builds them before the Go tests and sets prebuilt.RequiredEnv, and
// e2eBuild leaves them alone then.
var e2eMakeTargets = []string{"jsrt", "capi", "native-example"}

// e2eStaged is the prefix stagedPrefix stages once in a test process, for
// the tests that run their stacks in child processes; TestMain removes it.
var e2eStaged struct {
	once   sync.Once
	dir    string
	prefix string
	err    error
}

// stagedPrefix builds every program and stages them, once in the process.
func stagedPrefix() (string, error) {
	s := &e2eStaged
	s.once.Do(func() {
		if s.dir, s.err = os.MkdirTemp("", "kario-e2e-"); s.err != nil {
			return
		}
		if s.err = e2eBuild(s.dir, "nice", "-n", "19"); s.err == nil {
			s.prefix = filepath.Join(s.dir, "usr")
		}
	})
	return s.prefix, s.err
}

// removeStaged removes what stagedPrefix staged.
func removeStaged() {
	e2eStaged.once.Do(func() {})
	if e2eStaged.dir != "" {
		_ = os.RemoveAll(e2eStaged.dir)
	}
}

// e2eServices are the private services of an end-to-end run, restarted
// when they exit.
type e2eServices struct {
	mu   sync.Mutex
	cmds map[string]*exec.Cmd
	// restarts counts each service's restarts.
	restarts map[string]int
}

// pids are the services' current processes by program name.
func (s *e2eServices) pids() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for name, cmd := range s.cmds {
		if cmd.Process != nil {
			out[name] = cmd.Process.Pid
		}
	}
	return out
}

// e2eStack builds every binary into a temporary prefix, starts the private
// services and a Shell on them; all of it ends with the test.
func e2eStack(t *testing.T) (*Core, *e2eServices) { return e2eStackWith(t, nil) }

// e2eStackWith is e2eStack with a hook that may change the staged prefix
// and the Shell's configuration before the Shell starts.
func e2eStackWith(t *testing.T, hook func(prefix string, cfg *Config)) (*Core, *e2eServices) {
	tmp := t.TempDir()
	noneLeft(t, tmp)
	// A parent's prefix is linked, not copied: its programs run from this
	// test's directory, which noneLeft watches, and parts starting together
	// do not each write 190 MB.
	prefix := filepath.Join(tmp, "usr")
	if staged := os.Getenv(e2ePrefixEnv); staged != "" {
		if out, err := exec.Command("cp", "-a", "--link", staged, prefix).CombinedOutput(); err != nil {
			t.Fatalf("link the staged prefix: %v\n%s", err, out)
		}
	} else {
		e2eStage(t, tmp)
	}
	run, state := filepath.Join(tmp, "run"), filepath.Join(tmp, "state")
	for _, d := range []string{run, state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if f := os.Getenv(e2eRunFileEnv); f != "" {
		if err := os.WriteFile(f, []byte(run), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	services := [][]string{
		{"phone-mediad", "--run-dir", run, "--test-codes"},
		{"phone-keystored", "--run-dir", run, "--state-dir", state},
		{"phone-audiod", "--run-dir", run, "--sink", "null"},
		{"phone-imed", "--run-dir", run, "--state-dir", state},
		{"phone-nfcd", "--run-dir", run, "--state-dir", state},
		{"phone-smsd", "--run-dir", run, "--state-dir", state},
		{"phone-displayd", "--run-dir", run, "--panel", "sim"},
		{"phone-pushgw", "--listen", "127.0.0.1:0", "--addr-file", filepath.Join(run, "pushgw.addr")},
		{"phone-pushd", "--run-dir", run, "--state-dir", state, "--gateway-file", filepath.Join(run, "pushgw.addr")},
		{"phone-chatsrv", "--listen", "127.0.0.1:0", "--addr-file", filepath.Join(run, "chatsrv.addr"), "--gateway-file", filepath.Join(run, "pushgw.addr")},
		{"phone-storesrv", "--listen", "127.0.0.1:0", "--addr-file", filepath.Join(run, "storesrv.addr"), "--data", filepath.Join(tmp, "storesrv"),
			"--root-out", filepath.Join(state, "factory", "stores", "store.pub"), "--admin-token-file", filepath.Join(run, "storesrv-admin.token")},
	}
	socks := []string{"mediad.sock", "mediad-frames.sock", "keystored.sock", "audiod.sock", "imed.sock", "nfcd.sock", "smsd.sock", "displayd.sock", "pushd.sock", "chatsrv.addr", "storesrv.addr"}
	if _, err := os.Stat(filepath.Join(prefix, "bin", "phone-telephonyd")); err == nil {
		services = append(services, []string{"phone-telephonyd", "--run-dir", run, "--state-dir", state})
		socks = append(socks, "telephonyd.sock")
	}
	// Restarted when they exit, as systemd does on the device (the input
	// method case kills phone-imed, a messages case phone-smsd).
	svcs := &e2eServices{cmds: map[string]*exec.Cmd{}, restarts: map[string]int{}}
	mu := &svcs.mu
	stopping := false
	for _, svc := range services {
		start := func() (*exec.Cmd, error) {
			cmd := exec.Command(filepath.Join(prefix, "bin", svc[0]), svc[1:]...)
			// They die with this process however it ends: go test's
			// timeout, a panic off the test's goroutine or a kill run no
			// Cleanup.
			cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
			return cmd, StartProcess(cmd)
		}
		cmd, err := start()
		if err != nil {
			t.Fatal(err)
		}
		svcs.cmds[svc[0]] = cmd
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				_ = cmd.Wait()
				mu.Lock()
				stop := stopping
				mu.Unlock()
				if stop {
					return
				}
				time.Sleep(500 * time.Millisecond)
				next, err := start()
				if err != nil {
					return
				}
				mu.Lock()
				cmd = next
				svcs.cmds[svc[0]] = next
				svcs.restarts[svc[0]]++
				mu.Unlock()
			}
		}()
		t.Cleanup(func() {
			mu.Lock()
			stopping = true
			_ = cmd.Process.Kill()
			mu.Unlock()
			<-done
		})
	}
	for _, s := range socks {
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(run, s)); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not appear", s)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	chatURL, _ := os.ReadFile(filepath.Join(run, "chatsrv.addr"))
	storeURL, _ := os.ReadFile(filepath.Join(run, "storesrv.addr"))
	cfg := Config{W: 40, H: 24, AppsDir: filepath.Join(prefix, "share", "phone", "apps"),
		AppBinDir: filepath.Join(prefix, "lib", "phone", "bin"), RunDir: run, StateDir: state,
		AppEnv: []string{"PHONE_CHAT_SERVER=" + strings.TrimSpace(string(chatURL)), "PHONE_STORE_SERVER=" + strings.TrimSpace(string(storeURL))}}
	if hook != nil {
		hook(prefix, &cfg)
	}
	// The compiled dictionary, as phone-shell maps it: built in memory, the
	// first name the Shell spells holds its loop for seconds under -race,
	// and the guard then holds back the app that asked four times as long.
	ime.SetDictPath(filepath.Join(prefix, "share", "phone", "ime.dict"))
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Shutdown)
	// Phone migration on a free port: other Shells on this machine (go test
	// ./shell, make selftest) listen on switchrecv.Port too.
	c.sw.anyPort = true
	return c, svcs
}
