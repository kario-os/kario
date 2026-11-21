package shell

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/cts"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/sandbox"
)

// fakeGroups is what a kernel gives the delegated group, as cgroupfs files.
type fakeGroups struct {
	controllers                []string
	freezer, kill, clone3, psi bool
}

// fakeCgroupfs lays out a delegated group the way systemd hands it over,
// with the files a kernel with k makes, and the host that sees it.
func fakeCgroupfs(t *testing.T, k fakeGroups) (cgHost, string) {
	t.Helper()
	root := t.TempDir()
	rel := "/system.slice/phone-selftest.service"
	base := filepath.Join(root, rel)
	files := map[string]string{"cgroup.controllers": strings.Join(k.controllers, " "), "cgroup.subtree_control": "",
		"cgroup.procs": "", "shell/cgroup.procs": ""}
	perController := map[string][]string{"memory": {"memory.max", "memory.high", "memory.swap.max", "memory.events"},
		"pids": {"pids.max"}, "cpu": {"cpu.weight"}}
	for _, ctl := range k.controllers {
		for _, f := range perController[ctl] {
			files["shell/"+f] = ""
		}
	}
	for _, opt := range []struct {
		on    bool
		names []string
	}{{k.freezer, []string{"cgroup.freeze", "shell/cgroup.freeze"}}, {k.kill, []string{"cgroup.kill"}}, {k.psi, []string{"memory.pressure"}}} {
		for _, f := range opt.names {
			if opt.on {
				files[f] = ""
			}
		}
	}
	for name, body := range files {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := cgHost{root: root, self: "0::" + rel, euid: 0, delegated: func(string) bool { return true },
		cloneInto: func(*os.File) error {
			if k.clone3 {
				return nil
			}
			return syscall.ENOSYS
		}}
	return h, base
}

var mainline = sandbox.Kernel{Landlock: 7, SeccompFilter: true, SeccompTSync: true, KillProcess: true, NoNewPrivs: true, Pidfd: true, MemfdSeal: true}

func TestCgroupsOnOldKernels(t *testing.T) {
	all := []string{"memory", "pids", "cpu"}
	for _, c := range []struct {
		name         string
		k            fakeGroups
		want         cgFeatures
		missing, alt []string
	}{
		{"4.19", fakeGroups{controllers: all},
			cgFeatures{Memory: true, MemoryHigh: true, Pids: true, CPU: true},
			[]string{"cgroup-freezer"}, []string{"cgroup-kill", "clone-into-cgroup", "psi"}},
		{"5.4", fakeGroups{controllers: all, freezer: true, psi: true},
			cgFeatures{Freezer: true, Memory: true, MemoryHigh: true, Pids: true, CPU: true, PSI: true},
			nil, []string{"cgroup-kill", "clone-into-cgroup"}},
		{"5.10", fakeGroups{controllers: all, freezer: true, clone3: true, psi: true},
			cgFeatures{Freezer: true, CloneInto: true, Memory: true, MemoryHigh: true, Pids: true, CPU: true, PSI: true},
			nil, []string{"cgroup-kill"}},
		{"6.18", fakeGroups{controllers: all, freezer: true, kill: true, clone3: true, psi: true},
			cgFeatures{Freezer: true, Kill: true, CloneInto: true, Memory: true, MemoryHigh: true, Pids: true, CPU: true, PSI: true},
			nil, nil},
		// The memory controller on v1 beside the v2 tree (an Android
		// vendor kernel's memcg), the rest on v2.
		{"vendor 5.4", fakeGroups{controllers: []string{"pids", "cpu"}, freezer: true},
			cgFeatures{Freezer: true, Pids: true, CPU: true},
			[]string{"cgroup-memory"}, []string{"cgroup-kill", "clone-into-cgroup", "psi"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, base := fakeCgroupfs(t, c.k)
			g, err := setupCgroupsOn(h)
			if err != nil {
				t.Fatal(err)
			}
			if g.feat != c.want {
				t.Errorf("features %+v, want %+v", g.feat, c.want)
			}
			if got := cgRead(filepath.Join(base, "cgroup.subtree_control")); got != "+"+strings.Join(c.k.controllers, " +") {
				t.Errorf("subtree_control %q", got)
			}
			if g.held() != !c.k.clone3 {
				t.Errorf("held %v with CLONE_INTO_CGROUP %v", g.held(), c.k.clone3)
			}
			k := assessKernel(kernelFacts{release: c.name, cgroups: "v2", sandbox: mainline, groups: g, root: true, psi: true, memAvailable: true})
			if got := slices.Sorted(maps.Keys(k.Missing)); !slices.Equal(got, c.missing) {
				t.Errorf("missing %v, want %v", got, c.missing)
			}
			if got := slices.Sorted(maps.Keys(k.Fallback)); !slices.Equal(got, c.alt) {
				t.Errorf("fallbacks %v, want %v", got, c.alt)
			}
			if k.Refuse != "" || k.Groups == nil || *k.Groups != c.want {
				t.Errorf("report %+v", k)
			}
		})
	}
	// No group to take over: the reason, and nothing claimed.
	for _, c := range []struct {
		self, why string
		euid      int
	}{
		{"0::/system.slice/phone-selftest.service", "not root", 1000},
		{"12:memory:/system.slice/x\n0::/system.slice/x", "(hybrid)", 0},
		{"12:memory:/system.slice/x\n1:name=systemd:/system.slice/x", "(v1)", 0},
		{"0::/", "root group", 0},
	} {
		h, _ := fakeCgroupfs(t, fakeGroups{controllers: []string{"memory"}})
		h.self, h.euid = c.self, c.euid
		_, err := setupCgroupsOn(h)
		if err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%q as uid %d: %v, want %q", c.self, c.euid, err, c.why)
		}
	}
	h, _ := fakeCgroupfs(t, fakeGroups{controllers: []string{"memory"}})
	h.delegated = func(string) bool { return false }
	if _, err := setupCgroupsOn(h); err == nil || !strings.Contains(err.Error(), "not delegated") {
		t.Errorf("undelegated: %v", err)
	}
	k := assessKernel(kernelFacts{cgroups: "v1", sandbox: mainline, cgOff: "not on the unified (v2) hierarchy (v1)", root: true, psi: true, memAvailable: true})
	if !strings.Contains(k.Missing["cgroups"], "(v1): no per-app memory") || k.Groups != nil || len(k.Missing) != 1 {
		t.Errorf("v1: %+v", k.Missing)
	}
}

func TestCgModes(t *testing.T) {
	for self, want := range map[string]string{"0::/a": "v2", "": "none", "0::/a\n3:cpu:/a": "hybrid",
		"3:cpu:/a\n0::/a": "hybrid", "3:cpu:/a\n1:name=systemd:/a": "v1"} {
		if got := cgMode(self); got != want {
			t.Errorf("%q: %s, want %s", self, got, want)
		}
	}
}

// The sandbox's layers on kernels from KaiOS 2.5's 3.10 on: what is
// missing, what is done another way, and when no app may start.
func TestAssessKernel(t *testing.T) {
	g := &cgroups{feat: cgFeatures{Freezer: true, Kill: true, CloneInto: true, Memory: true, MemoryHigh: true, Pids: true, CPU: true, PSI: true}}
	for _, c := range []struct {
		name         string
		sb           sandbox.Kernel
		root         bool
		missing, alt []string
		refuse       string
	}{
		{"6.18", mainline, true, nil, nil, ""},
		{"6.1", sandbox.Kernel{Landlock: 3, SeccompFilter: true, SeccompTSync: true, KillProcess: true, NoNewPrivs: true, Pidfd: true, MemfdSeal: true}, true,
			[]string{"landlock-scope", "landlock-tcp"}, nil, ""},
		{"5.4", sandbox.Kernel{SeccompFilter: true, SeccompTSync: true, KillProcess: true, NoNewPrivs: true, Pidfd: true, MemfdSeal: true}, true,
			[]string{"landlock"}, nil, ""},
		{"4.9", sandbox.Kernel{SeccompFilter: true, SeccompTSync: true, NoNewPrivs: true, MemfdSeal: true}, true,
			[]string{"landlock"}, []string{"pidfd", "seccomp-kill-process"}, ""},
		{"3.10", sandbox.Kernel{SeccompFilter: true, NoNewPrivs: true}, true,
			[]string{"landlock", "memfd-seal"}, []string{"pidfd", "seccomp-kill-process", "seccomp-tsync"}, ""},
		{"no filters", sandbox.Kernel{NoNewPrivs: true, Pidfd: true, MemfdSeal: true, NoNamespaces: []string{"pid"}}, true,
			[]string{"landlock", "namespaces", "seccomp"}, nil, "the kernel has no seccomp filters, pid namespaces"},
		{"no filters, not root", sandbox.Kernel{NoNewPrivs: true, Pidfd: true, MemfdSeal: true}, false,
			[]string{"landlock", "seccomp", "uid"}, nil, ""},
		{"Landlock off", sandbox.Kernel{LandlockOff: true, SeccompFilter: true, SeccompTSync: true, KillProcess: true, NoNewPrivs: true, Pidfd: true, MemfdSeal: true}, true,
			[]string{"landlock"}, nil, ""},
	} {
		k := assessKernel(kernelFacts{release: c.name, cgroups: "v2", sandbox: c.sb, groups: g, root: c.root, psi: true, memAvailable: true})
		if got := slices.Sorted(maps.Keys(k.Missing)); !slices.Equal(got, c.missing) {
			t.Errorf("%s: missing %v, want %v", c.name, got, c.missing)
		}
		if got := slices.Sorted(maps.Keys(k.Fallback)); !slices.Equal(got, c.alt) {
			t.Errorf("%s: fallbacks %v, want %v", c.name, got, c.alt)
		}
		if k.Refuse != c.refuse {
			t.Errorf("%s: refuse %q, want %q", c.name, k.Refuse, c.refuse)
		}
	}
	k := assessKernel(kernelFacts{sandbox: sandbox.Kernel{LandlockOff: true, SeccompFilter: true, NoNewPrivs: true}, groups: &cgroups{feat: cgFeatures{Freezer: true}}, root: true})
	if !strings.Contains(k.Missing["landlock"], "lsm=") || k.Fallback["psi"] == "" || k.Fallback["mem-available"] == "" {
		t.Errorf("Landlock off, no PSI, no MemAvailable: %+v %+v", k.Missing, k.Fallback)
	}
}

// Without the cgroup freezer (no cgroups, or before Linux 5.2) a
// background app stops with SIGSTOP, and so does what it started.
func TestFreezeWithoutFreezer(t *testing.T) {
	for _, cg := range []*cgroups{nil, {base: t.TempDir(), feat: cgFeatures{Memory: true, Pids: true}}} {
		cmd := exec.Command("/bin/sh", "-c", "sleep 30 & echo $!; wait")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 32)
		n, _ := out.Read(buf)
		child, _ := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
		c := &Core{cg: cg, apps: []*App{{M: &Manifest{ID: "com.test.x"}, running: true, proc: cmd.Process}}}
		a := c.apps[0]
		if err := c.freezeApp(a, true); err != nil {
			t.Fatalf("cgroups %v: freeze: %v", cg != nil, err)
		}
		for _, pid := range []int{cmd.Process.Pid, child} {
			if !waitState(pid, "Tt") {
				t.Errorf("cgroups %v: %d is %q after freezing, want stopped", cg != nil, pid, procState(pid))
			}
		}
		if s := c.ProcState("com.test.x"); s != "frozen" {
			t.Errorf("cgroups %v: ProcState %s", cg != nil, s)
		}
		if err := c.freezeApp(a, false); err != nil {
			t.Fatal(err)
		}
		for _, pid := range []int{cmd.Process.Pid, child} {
			if !waitState(pid, "SR") {
				t.Errorf("cgroups %v: %d is %q after thawing", cg != nil, pid, procState(pid))
			}
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}
}

func procState(pid int) string {
	raw, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	s := string(raw)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2 : i+3]
	}
	return ""
}

func waitState(pid int, states string) bool {
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if s := procState(pid); s != "" && strings.Contains(states, s) {
			return true
		}
	}
	return false
}

// Before Linux 5.14 (no cgroup.kill) the Shell kills a group's processes
// itself.
func TestKillWithoutCgroupKill(t *testing.T) {
	g := &cgroups{base: t.TempDir()}
	dir := g.dir("com.test.x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	g.killAll(dir, 200*time.Millisecond)
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
			t.Fatalf("sleep ended with %v", err)
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the group's process lives on")
	}
	if _, err := os.Stat(filepath.Join(dir, "cgroup.kill")); err == nil {
		t.Error("cgroup.kill made where the kernel has none")
	}
}

// cgroupfs has the files the kernel offers; writing one it lacks fails
// rather than leaving a file that pretends the feature is there.
func TestCgWriteDoesNotCreate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cgroup.freeze")
	if err := cgWrite(p, "1"); err == nil {
		t.Fatal("wrote a file cgroupfs does not have")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("cgWrite created it")
	}
	_ = os.WriteFile(p, []byte("0"), 0o644)
	if err := cgWrite(p, "1"); err != nil || cgRead(p) != "1" {
		t.Fatalf("write: %v, %q", err, cgRead(p))
	}
}

// The launcher waits for the Shell to move it into its group.
func TestHoldLauncher(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", `eval "head -c 1 <&$PHONE_SANDBOX_HOLD" >/dev/null; echo released`)
	cmd.ExtraFiles = []*os.File{os.Stdin}
	release, err := holdLauncher(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cmd.Env, "PHONE_SANDBOX_HOLD=4") {
		t.Fatalf("env %v", cmd.Env)
	}
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		b := make([]byte, 16)
		n, _ := out.Read(b)
		got <- string(b[:n])
	}()
	select {
	case s := <-got:
		t.Fatalf("ran before it was released: %q", s)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	select {
	case s := <-got:
		if s != "released\n" {
			t.Fatalf("output %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("still held after release")
	}
	_ = cmd.Wait()
}

// Before Linux 5.7 an app cannot be born in its group (CLONE_INTO_CGROUP):
// it is held and moved there instead of failing to start.
func TestStartWithoutCloneInto(t *testing.T) {
	r := newHostileRig(t, map[string]string{})
	base := t.TempDir()
	r.c.cg = &cgroups{base: base, feat: cgFeatures{Freezer: true, Memory: true, Pids: true, CPU: true}}
	a := r.launch(probeID)
	if !a.ready {
		t.Fatal("the app did not start")
	}
	if st, err := os.Stat(filepath.Join(base, "app."+probeID)); err != nil || !st.IsDir() {
		t.Fatalf("no group made for it: %v", err)
	}
	log, _ := os.ReadFile(filepath.Join(r.runDir, "shell.log"))
	if want := filepath.Join(base, "app."+probeID, "cgroup.procs"); !strings.Contains(string(log), want) {
		t.Errorf("the Shell did not move it into %s:\n%s", want, log)
	}
	r.c.cg = nil
}

// Kernels before 3.14 have no MemAvailable: the estimate, not zero, so
// the low-memory killer does not take the system for short all the time.
func TestMeminfoWithoutMemAvailable(t *testing.T) {
	old := "MemTotal:         510000 kB\nMemFree:          150000 kB\nBuffers:           20000 kB\nCached:           120000 kB\nShmem:             10000 kB\n"
	total, avail, est := parseMeminfo(old)
	if total != 510000 || avail != 280000 || !est {
		t.Fatalf("3.10: %d %d %v", total, avail, est)
	}
	if (memState{totalKB: total, availKB: avail, psi: -1}).low() {
		t.Error("a 3.10 kernel with half its memory free reads as low")
	}
	total, avail, est = parseMeminfo("MemTotal: 2000000 kB\nMemFree: 100 kB\nMemAvailable: 900000 kB\nCached: 5 kB\n")
	if total != 2000000 || avail != 900000 || est {
		t.Fatalf("3.14 on: %d %d %v", total, avail, est)
	}
}

// The compatibility report says which protections are not in force, the
// attestation covers that, and reports from before still verify.
func TestReportSaysWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := keystore.LoadRootPub(keystore.RootPubPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	k := assessKernel(kernelFacts{release: "4.19.157", cgroups: "v2", sandbox: sandbox.Kernel{SeccompFilter: true, SeccompTSync: true, KillProcess: true, NoNewPrivs: true, MemfdSeal: true},
		groups: &cgroups{feat: cgFeatures{Memory: true, Pids: true, CPU: true}}, root: true, memAvailable: true})
	r := CompatReport{System: Brand, Result: "PASS", Coverage: "1/1", Cases: []CaseResult{{Name: "a", Status: "PASS"}},
		Capabilities: map[string]CapStat{}, Sandbox: k.compat()}
	r.Attestation, _ = ks.Attest(cts.Signer, cts.ReportNonce(r), "")
	raw, _ := json.MarshalIndent(r, "", "  ")
	got, err := cts.VerifyReport(raw, root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sandbox == nil || got.Sandbox.Kernel != "4.19.157" || !reflect.DeepEqual(slices.Sorted(maps.Keys(got.Sandbox.Missing)), []string{"cgroup-freezer", "landlock"}) {
		t.Fatalf("report's sandbox %+v", got.Sandbox)
	}
	forged := strings.Replace(string(raw), `"landlock": `, `"landlock-off": `, 1)
	if _, err := cts.VerifyReport([]byte(forged), root); err == nil {
		t.Fatal("a report with its missing protections edited verified")
	}
	// A report from before the field: no "sandbox" at all, the same digest.
	before := CompatReport{System: Brand, Result: "PASS", Coverage: "1/1", Cases: []CaseResult{{Name: "a", Status: "PASS"}}, Capabilities: map[string]CapStat{}}
	before.Attestation, _ = ks.Attest(cts.Signer, cts.ReportNonce(before), "")
	raw, _ = json.Marshal(before)
	if strings.Contains(string(raw), `"sandbox"`) {
		t.Fatalf("an empty sandbox is written: %s", raw)
	}
	if _, err := cts.VerifyReport(raw, root); err != nil {
		t.Fatalf("a report without the field: %v", err)
	}
}

func TestDumpHasTheKernel(t *testing.T) {
	apps := t.TempDir()
	_ = os.MkdirAll(filepath.Join(apps, "x"), 0o755)
	_ = os.WriteFile(filepath.Join(apps, "x", "manifest.json"), []byte(`{"id": "com.test.x", "name": "x", "exec": "true"}`), 0o644)
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown()
	raw, _ := json.Marshal(c.Dump())
	var d struct {
		Kernel kernelReport `json:"kernel"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || d.Kernel.Release == "" || d.Kernel.Cgroups == "" {
		t.Fatalf("dump: %v %s", err, raw)
	}
	// Not root: no per-app uid, no cgroups; whatever else this kernel lacks.
	for _, name := range []string{"uid", "cgroups"} {
		if d.Kernel.Missing[name] == "" {
			t.Errorf("the developer machine's dump does not say %s is missing: %+v", name, d.Kernel.Missing)
		}
	}
	if rep := c.CompatReport(nil, true); rep.Sandbox == nil || rep.Sandbox.Missing["cgroups"] == "" {
		t.Errorf("report: %+v", rep.Sandbox)
	}
}

// The probe is the way apps start: on a kernel before 5.7 (this machine's,
// maybe) it fails, and so it does for a directory that is no cgroup.
func TestCloneIntoProbe(t *testing.T) {
	d, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := cloneIntoProbe(d); err == nil {
		t.Fatal("started a process inside a directory that is no cgroup")
	} else {
		t.Logf("on this kernel: %v", err)
	}
}
