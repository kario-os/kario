package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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

// e2ePartMark names the part of the selftest script the cases below it
// belong to (TestSelftestScenario).
const e2ePartMark = "#part "

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
	lines := strings.Split(script, "\n")
	var parts []scenarioPart
	text := map[string][]string{}
	boot, cur := -1, ""
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if name, ok := strings.CutPrefix(t, e2ePartMark); ok {
			name = strings.TrimSpace(name)
			if boot < 0 {
				boot = i
			}
			if _, seen := text[name]; !seen {
				out := make([]string, len(lines))
				copy(out, lines[:boot])
				out[i] = "at sys.idle 5000"
				text[name] = out
				parts = append(parts, scenarioPart{name: name})
			}
			cur = name
			continue
		}
		if strings.HasPrefix(t, "== ") && cur == "" {
			return nil, fmt.Errorf("line %d: a case before the first %q line", i+1, strings.TrimSpace(e2ePartMark))
		}
		if cur != "" {
			text[cur][i] = l
		}
	}
	for i := range parts {
		parts[i].script = strings.Join(text[parts[i].name], "\n")
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
		c, _ := e2eStack(t)
		var out strings.Builder
		if !(&Runner{C: c, Out: &out}).Run(parts[i].script) {
			t.Fatalf("scenario failed:\n%s", out.String())
		}
		t.Log("\n" + out.String())
		return
	}
	t.Parallel()
	prefix := e2eStage(t, t.TempDir())
	// The parts start together whatever -parallel allows: they wait on
	// their apps and services far more than they compute.
	results := make([]chan childResult, len(parts))
	for i, p := range parts {
		results[i] = make(chan childResult, 1)
		go func() { results[i] <- runChild(t, e2ePartEnv+"="+p.name, e2ePrefixEnv+"="+prefix) }()
	}
	for i, p := range parts {
		t.Run(p.name, func(t *testing.T) { (<-results[i]).report(t) })
	}
}

// aloneEnv names the test a child process of the test binary runs (alone).
const aloneEnv = "KARIO_TEST_ALONE"

// alone runs the test again in a child process of its own, in parallel with
// the other tests, and reports whether this is that child. A Shell started
// in a process sets its time.Local, which every other Shell in it reads.
func alone(t *testing.T) bool {
	if os.Getenv(aloneEnv) == t.Name() {
		return true
	}
	t.Parallel()
	runChild(t, aloneEnv+"="+t.Name()).report(t)
	return false
}

// childResult is how a test run again in a child process of the test
// binary went (runChild).
type childResult struct {
	out []byte
	err error
}

// runChild runs the test again in a child process of the test binary, with
// env added to its environment.
func runChild(t *testing.T, env ...string) childResult {
	args := []string{"-test.run=^" + strings.Split(t.Name(), "/")[0] + "$", "-test.count=1", "-test.v"}
	if d, ok := t.Deadline(); ok && time.Until(d) > 20*time.Second {
		args = append(args, "-test.timeout="+(time.Until(d)-10*time.Second).String())
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return childResult{out, err}
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
	bin, prefix := filepath.Join(dir, "bin"), filepath.Join(dir, "usr")
	build := exec.Command("go", "build", "-o", bin+"/", "github.com/kario-os/kario/cmd/...", "github.com/kario-os/kario/apps/...", "github.com/kario-os/kario/cts/...", "github.com/kario-os/kario/examples/...")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if out, err := exec.Command("sh", "../scripts/stage.sh", bin, prefix).CombinedOutput(); err != nil {
		t.Fatalf("stage: %v\n%s", err, out)
	}
	return prefix
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
			return cmd, cmd.Start()
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
