package shell

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// The fake apps of the hostile-app tests are this test binary started again
// by the Shell (TestFakeApp); what each does follows from its app id.
const (
	fakeAppEnv   = "PHONE_FAKE_APP"
	fakeChildEnv = "PHONE_FAKE_CHILD"
	fakeAppExec  = "fake-app"
	// The well-behaved app whose round trips measure how responsive the
	// Shell stays, and the file it writes them to (microseconds, one a line).
	probeID  = "test.probe"
	probeRTT = "rtt"
	// What a fake app writes into its data directory for the test to read.
	fakeChildPID = "child.pid"
	fakeBusy     = "busy"
	fakeStopped  = "stopped"
)

// Bounds the hostile-app tests hold the Shell to, generous enough for a
// loaded CI machine (the measured values are logged).
const (
	maxLoopLatency  = 250 * time.Millisecond
	maxProbeRTT     = 500 * time.Millisecond
	maxHeapGrowth   = 96 << 20
	hostileDuration = 3 * time.Second
)

// TestFakeApp is not a test: run as a child process it plays an app.
func TestFakeApp(t *testing.T) {
	if os.Getenv(fakeAppEnv) == "" {
		t.Skip("helper process")
	}
	if os.Getenv(fakeChildEnv) != "" {
		fakeChild()
	}
	if os.Getenv(teeRealEnv) != "" {
		os.Exit(teeApp())
	}
	os.Exit(fakeApp(strings.TrimPrefix(os.Getenv("PHONE_APP_ID"), "test.")))
}

type fakeConn struct {
	uc *net.UnixConn
	rd *bufio.Reader
}

func (f *fakeConn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = f.uc.Write(append(b, '\n'))
	return err
}

func (f *fakeConn) raw(s string) error {
	_, err := f.uc.Write([]byte(s))
	return err
}

func (f *fakeConn) recv() (ui.Msg, error) {
	var m ui.Msg
	line, err := f.rd.ReadBytes('\n')
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(line, &m)
}

// hello says hello and waits for start.
func (f *fakeConn) hello() {
	_ = f.send(ui.Msg{T: ui.MsgHello, App: os.Getenv("PHONE_APP_ID"), Proto: ui.Proto})
	for {
		m, err := f.recv()
		if err != nil || m.T == ui.MsgStart {
			return
		}
	}
}

func page(id string, n int) *ui.Screen {
	s := &ui.Screen{ID: id, Title: id}
	for i := range n {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "n" + strconv.Itoa(i), Title: "item " + strconv.Itoa(i),
			Actions: []ui.Action{{ID: "open", Label: "Open", Primary: true}}})
	}
	return s
}

func dataFile(name string) string { return filepath.Join(os.Getenv("PHONE_DATA_DIR"), name) }

func fakeApp(kind string) int {
	if kind == "sdkgo" {
		return sdkGoApp()
	}
	fd := os.NewFile(3, "ui")
	if kind == "fork" {
		// A child that inherited the socket writes on after the app exits.
		cmd := exec.Command(os.Args[0], os.Args[1:]...)
		cmd.Env = append(os.Environ(), fakeChildEnv+"=1")
		cmd.ExtraFiles = []*os.File{fd}
		if err := cmd.Start(); err != nil {
			return 2
		}
		_ = os.WriteFile(dataFile(fakeChildPID), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		time.Sleep(300 * time.Millisecond)
		return 0
	}
	if kind == "linger" {
		fd.Close()
		time.Sleep(time.Minute)
		return 0
	}
	nc, err := net.FileConn(fd)
	fd.Close()
	if err != nil {
		return 2
	}
	f := &fakeConn{uc: nc.(*net.UnixConn), rd: bufio.NewReaderSize(nc, 1<<20)}
	if name, ok := strings.CutPrefix(kind, replayPrefix); ok {
		return replayApp(f, filepath.Join(os.Getenv(replayEnv), name+".jsonl"))
	}
	if strings.HasPrefix(kind, "v1") {
		return fakeV1App(f, kind == "v1both")
	}
	if kind == "version" {
		// A major version this system does not speak.
		_ = f.send(ui.Msg{T: ui.MsgHello, App: os.Getenv("PHONE_APP_ID"), Proto: "phone.ui/1"})
		time.Sleep(time.Minute)
		return 0
	}
	f.hello()
	switch kind {
	case "slowchoice":
		// Answers a choice late, as a busy app on a slow phone does, with
		// "replace the top page" (input_test.go).
		_ = f.send(ui.Msg{T: ui.MsgReset, Screen: page("main", 3)})
		for {
			m, err := f.recv()
			if err != nil {
				return 0
			}
			switch {
			case m.T == ui.MsgPing:
				_ = f.send(ui.Msg{T: ui.MsgPong, Value: m.Value})
			case m.T == ui.MsgAction && m.ScreenID == "main":
				_ = f.send(ui.Msg{T: ui.MsgPush, Screen: page("detail", 2)})
			case m.T == ui.MsgAction && m.ScreenID == "detail":
				time.Sleep(300 * time.Millisecond)
				s := page("detail", 2)
				s.Title = "chosen"
				_ = f.send(ui.Msg{T: ui.MsgReplace, Screen: s})
			}
		}
	case "probe":
		_ = f.send(ui.Msg{T: ui.MsgReset, Screen: page("main", 3)})
		out, _ := os.OpenFile(dataFile(probeRTT), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		for req := 1; ; req++ {
			t0 := time.Now()
			if f.send(ui.Msg{T: ui.MsgReq, Req: req, Cap: ui.CapAppSettings}) != nil {
				return 0
			}
			for {
				m, err := f.recv()
				if err != nil {
					return 0
				}
				if m.T == ui.MsgPing {
					_ = f.send(ui.Msg{T: ui.MsgPong, Value: m.Value})
				}
				if m.T == ui.MsgRes && m.Req == req {
					break
				}
			}
			fmt.Fprintln(out, time.Since(t0).Microseconds())
			time.Sleep(20 * time.Millisecond)
		}
	case "flood":
		_ = f.send(ui.Msg{T: ui.MsgReset, Screen: page("main", 50)})
		upd := ui.Msg{T: ui.MsgUpdate, Screen: page("main", 50)}
		for i := 0; ; i++ {
			upd.Screen.Title = strconv.Itoa(i)
			if f.send(upd) != nil || f.send(ui.Msg{T: ui.MsgToast, Text: "flood"}) != nil {
				return 0
			}
		}
	case "big":
		// Messages just under the framing limit, as fast as they go.
		text := strings.Repeat("x", wire.MaxLine-200)
		for f.send(ui.Msg{T: ui.MsgToast, Text: text}) == nil {
		}
	case "toolong":
		_ = f.raw(`{"t":"toast","text":"` + strings.Repeat("y", wire.MaxLine+1024))
		time.Sleep(time.Minute)
	case "garbage":
		for i := 0; i < 100; i++ {
			if f.raw("{not json at all\n") != nil {
				return 0
			}
		}
		time.Sleep(time.Minute)
	case "typo":
		// Wrong types and nesting past the decoder's depth are malformed
		// too; nesting within it is a message like any other.
		deep := strings.Repeat("[", 5000) + strings.Repeat("]", 5000)
		_ = f.raw(`{"t":"toast","text":"ok","args":` + deep + "}\n")
		for i := 0; i < 100; i++ {
			_ = f.raw(`{"t":"reset","screen":{"id":"x","body":"not a list"}}` + "\n")
			_ = f.raw(`{"t":"toast","args":` + strings.Repeat("[", 20000) + "\n")
		}
		time.Sleep(time.Minute)
	case "unknown":
		// A newer app's messages: ignored, the app is not ended.
		for i := 0; i < 600; i++ {
			_ = f.send(map[string]any{"t": "future_" + strconv.Itoa(i%3), "x": i})
		}
		_ = f.send(ui.Msg{T: ui.MsgReset, Screen: page("after-unknown", 1)})
		time.Sleep(time.Minute)
	case "noread":
		_ = f.send(ui.Msg{T: ui.MsgReset, Screen: page("main", 1)})
		time.Sleep(time.Minute)
	case "fds":
		// Files an app may not pass: many per message (more than one read
		// takes), and messages claiming files they do not carry. The file
		// passed is a bare descriptor: an *os.File whose Fd alone is used
		// is closed once collected (a collection runs halfway), and the
		// app's own sends would fail (EBADF), not the Shell.
		null, err := syscall.Open(os.DevNull, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			return 2
		}
		fds := make([]int, 16)
		for i := range fds {
			fds[i] = null
		}
		stopped := func(i int, err error) int {
			_ = os.WriteFile(dataFile(fakeStopped), []byte(fmt.Sprintf("message %d: %v", i, err)), 0o600)
			return 0
		}
		for i := 0; i < 2000; i++ {
			if i == 1000 {
				runtime.GC()
			}
			b, _ := json.Marshal(ui.Msg{T: ui.MsgToast, Text: "fds", FDs: 100})
			if _, _, err := f.uc.WriteMsgUnix(append(b, '\n'), syscall.UnixRights(fds[:1+i%len(fds)]...), nil); err != nil {
				return stopped(i, err)
			}
		}
		if err := f.send(ui.Msg{T: ui.MsgReset, Screen: page("after-fds", 1)}); err != nil {
			return stopped(2000, err)
		}
		time.Sleep(time.Minute)
	case "reqloop":
		// Requests in a tight loop: ones opening a system dialog (one at a
		// time), ones needing the user's consent (one dialog for all), more
		// than may be in flight.
		// The replies are read as fast as they come and counted; the count
		// goes to the file apart, as a reader that stopped for a write per
		// reply would be ended for not reading (256 messages queued).
		var busy atomic.Int64
		go func() {
			for {
				m, err := f.recv()
				if err != nil {
					return
				}
				if m.T == ui.MsgRes && m.Err == ui.ErrBusy {
					busy.Add(1)
				}
			}
		}()
		go func() {
			written := int64(0)
			for range time.Tick(20 * time.Millisecond) {
				if n := busy.Load(); n != written {
					_ = os.WriteFile(dataFile(fakeBusy), []byte(strconv.FormatInt(n, 10)), 0o600)
					written = n
				}
			}
		}()
		for req := 1; req <= 510; req++ {
			cp := ui.CapContacts
			if req <= 10 {
				cp = ui.CapCameraCapture
			}
			_ = f.send(ui.Msg{T: ui.MsgReq, Req: req, Cap: cp})
		}
		time.Sleep(time.Minute)
	case "screens":
		// Absurd screens: far more nodes than allowed, megabytes of text in
		// one node, thousands of actions, a canvas past its limits.
		huge := page("huge", 20000)
		_ = f.send(ui.Msg{T: ui.MsgReset, Screen: huge})
		s := &ui.Screen{ID: "wide", Title: strings.Repeat("T", 100000)}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Text: strings.Repeat("long text ", 100000)})
		s.Body = append(s.Body, ui.Node{Kind: ui.KindDoc, Text: strings.Repeat("# head\n\n- item **bold**\n", 25000)})
		acts := ui.Node{Kind: ui.KindItem, ID: "acts", Title: "acts"}
		for i := range 10000 {
			acts.Actions = append(acts.Actions, ui.Action{ID: "a" + strconv.Itoa(i), Label: "a"})
		}
		s.Body = append(s.Body, acts)
		cv := &ui.Canvas{W: 1 << 20, H: 1 << 20}
		for i := range 10000 {
			cv.Ops = append(cv.Ops, ui.DrawOp{Op: "circle", X: i, Y: i, R: 1 << 20, Color: "#ffffff", Fill: true})
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindCanvas, ID: "cv", Canvas: cv})
		_ = f.send(ui.Msg{T: ui.MsgPush, Screen: s})
		for i := 0; i < 100; i++ {
			_ = f.send(ui.Msg{T: ui.MsgPush, Screen: page("deep"+strconv.Itoa(i), 1)})
		}
		_ = f.send(ui.Msg{T: ui.MsgPush, Screen: s})
		time.Sleep(time.Minute)
	}
	return 0
}

// fakeChild holds the app's socket after the app exited and writes on.
func fakeChild() {
	f := os.NewFile(3, "ui")
	for {
		if _, err := f.Write([]byte(`{"t":"toast","text":"orphan"}` + "\n")); err != nil {
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// hostileRig is a Shell in this process with fake apps; the test goroutine
// runs its event loop.
type hostileRig struct {
	t      *testing.T
	c      *Core
	state  string
	runDir string
	loop   []time.Duration
}

func newHostileRig(t *testing.T, apps map[string]string) *hostileRig {
	t.Helper()
	if testing.Short() {
		t.Skip("hostile apps skipped in -short mode")
	}
	if os.Geteuid() == 0 {
		t.Skip("the fake apps run without phone-sandbox, which root refuses")
	}
	root := t.TempDir()
	appsDir, binDir := filepath.Join(root, "apps"), filepath.Join(root, "bin")
	r := &hostileRig{t: t, state: filepath.Join(root, "state"), runDir: filepath.Join(root, "run")}
	fakesLeft(t, filepath.Join(r.state, "apps"))
	for _, d := range []string{appsDir, binDir, r.state, r.runDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) + " -test.run='^TestFakeApp$'\n"
	if err := os.WriteFile(filepath.Join(binDir, fakeAppExec), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	apps[probeID] = ""
	for id, extra := range apps {
		m := `{"id":"` + id + `","name":"` + id + `","exec":"` + fakeAppExec + `"` + extra + `}`
		dir := filepath.Join(appsDir, id)
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	logf, err := os.Create(filepath.Join(r.runDir, "shell.log"))
	if err != nil {
		t.Fatal(err)
	}
	// wire's notes about files go there too.
	log.SetOutput(logf)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		logf.Close()
	})
	c, err := New(Config{W: 40, H: 24, AppsDir: appsDir, AppBinDir: binDir, RunDir: r.runDir, StateDir: r.state,
		Log: logf, FreezeAfter: -1, AppEnv: []string{fakeAppEnv + "=1"}})
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	// Keys reach the screen instead of waking it.
	c.SetScreenAuto(false)
	t.Cleanup(func() {
		c.Shutdown()
		r.pump(200 * time.Millisecond)
	})
	return r
}

// pump runs the event loop for d, measuring how long a closure posted from
// another goroutine waits for it (a key, a service's answer).
func (r *hostileRig) pump(d time.Duration) {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			t0 := time.Now()
			select {
			case r.c.ev <- func() { r.loop = append(r.loop, time.Since(t0)) }:
			case <-stop:
				return
			}
		}
	}()
	end := time.After(d)
	for {
		select {
		case f := <-r.c.ev:
			f()
		case <-end:
			return
		}
	}
}

// until runs the loop until cond holds (checked between events) or d passes.
func (r *hostileRig) until(d time.Duration, cond func() bool) bool {
	for end := time.Now().Add(d); time.Now().Before(end); {
		if cond() {
			return true
		}
		r.pump(20 * time.Millisecond)
	}
	return cond()
}

func (r *hostileRig) app(id string) *App {
	a := r.c.appByID(id)
	if a == nil {
		r.t.Fatalf("no app %s", id)
	}
	return a
}

// fakeAppHello bounds a fake app's start: a new process of this test binary
// (some 50 MB), which a busy machine may first have to read back from the
// disk, seconds at times; one that never says hello still fails.
const fakeAppHello = 30 * time.Second

func (r *hostileRig) launch(id string) *App {
	a := r.app(id)
	r.c.Launch(a, "")
	if !r.until(fakeAppHello, func() bool { return a.ready || !a.running }) {
		r.t.Fatalf("%s did not say hello", id)
	}
	return a
}

func (r *hostileRig) probeRTTs() []time.Duration {
	raw, _ := os.ReadFile(filepath.Join(r.state, "apps", probeID, probeRTT))
	var out []time.Duration
	for _, l := range strings.Fields(string(raw)) {
		if us, err := strconv.Atoi(l); err == nil {
			out = append(out, time.Duration(us)*time.Microsecond)
		}
	}
	return out
}

func quantile(d []time.Duration, q float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := slices.Clone(d)
	slices.Sort(s)
	return s[min(len(s)-1, int(q*float64(len(s))))]
}

func heapInUse() uint64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapInuse
}

func openFDs() int {
	ents, _ := os.ReadDir("/proc/self/fd")
	return len(ents)
}

func (r *hostileRig) crash(id string) string {
	for _, rec := range r.c.Crashes() {
		if rec.App == id {
			return rec.Reason
		}
	}
	return ""
}

// attack runs one hostile app next to the probe for hostileDuration and
// checks the Shell kept its bounds; it returns the hostile app.
func (r *hostileRig) attack(id string) *App {
	t := r.t
	probe := r.app(probeID)
	before := len(r.probeRTTs())
	heap0, fds0, gor0 := heapInUse(), openFDs(), runtime.NumGoroutine()
	r.loop = nil
	a := r.launch(id)
	r.pump(hostileDuration)
	rtts := r.probeRTTs()[before:]
	heap1 := heapInUse()
	t.Logf("%s: loop latency p50 %v p99 %v max %v (%d samples); probe round trip p50 %v p99 %v (%d); heap %+d KB, fds %+d, goroutines %+d",
		id, quantile(r.loop, .5), quantile(r.loop, .99), quantile(r.loop, 1), len(r.loop),
		quantile(rtts, .5), quantile(rtts, .99), len(rtts), (int64(heap1)-int64(heap0))>>10, openFDs()-fds0, runtime.NumGoroutine()-gor0)
	if p := quantile(r.loop, .99); p > maxLoopLatency {
		t.Errorf("%s: the event loop's p99 latency %v is over %v", id, p, maxLoopLatency)
	}
	if !probe.running || len(rtts) < 10 {
		t.Errorf("%s: the probe app got %d answers (running %v)", id, len(rtts), probe.running)
	} else if p := quantile(rtts, .99); p > maxProbeRTT {
		t.Errorf("%s: the probe app's p99 round trip %v is over %v", id, p, maxProbeRTT)
	}
	if heap1 > heap0+maxHeapGrowth {
		t.Errorf("%s: the Shell's heap grew by %d MB", id, (heap1-heap0)>>20)
	}
	return a
}

// fakesLeft fails the test if a fake app (or a child of one) with its data
// under dir outlives it, and kills it.
func fakesLeft(t *testing.T, dir string) {
	t.Cleanup(func() {
		var left []int
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			left = left[:0]
			ents, _ := os.ReadDir("/proc")
			for _, e := range ents {
				pid, err := strconv.Atoi(e.Name())
				if err != nil {
					continue
				}
				env, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
				if strings.Contains(string(env), "PHONE_DATA_DIR="+dir+"/") {
					left = append(left, pid)
				}
			}
			if len(left) == 0 {
				return
			}
		}
		for _, pid := range left {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("%d fake app processes outlived the test: %v", len(left), left)
	})
}

func (r *hostileRig) ended(a *App, d time.Duration) bool {
	return r.until(d, func() bool { return !a.running })
}

func TestHostileApps(t *testing.T) {
	// Its bounds are on the process's heap, descriptors and event loop
	// too: alone, the tests running in parallel do not count.
	if !alone(t) {
		return
	}
	r := newHostileRig(t, map[string]string{
		"test.flood": "", "test.big": "", "test.toolong": "", "test.garbage": "", "test.typo": "",
		"test.unknown": "", "test.noread": "", "test.fds": "", "test.screens": "", "test.fork": "",
		"test.linger": "", "test.version": "",
		"test.reqloop": `,"permissions":["camera","contacts"]`,
	})
	r.launch(probeID)
	r.pump(time.Second)
	base := r.probeRTTs()
	t.Logf("baseline: probe round trip p50 %v p99 %v (%d)", quantile(base, .5), quantile(base, .99), len(base))

	t.Run("flood", func(t *testing.T) {
		r.t = t
		a := r.attack("test.flood")
		if !a.running || a.guard.throttled == 0 {
			t.Fatalf("flooding app: running %v, %d messages throttled", a.running, a.guard.throttled)
		}
		t.Logf("flood: %d messages throttled", a.guard.throttled)
		r.c.kill(a)
		r.ended(a, 5*time.Second)
	})
	t.Run("big", func(t *testing.T) {
		r.t = t
		a := r.attack("test.big")
		r.c.kill(a)
		r.ended(a, 5*time.Second)
	})
	for _, tc := range []struct{ id, reason string }{
		{"test.toolong", "a message over 4 MB"},
		{"test.garbage", "16 malformed messages"},
		{"test.typo", "16 malformed messages"},
		{"test.version", `speaks "phone.ui/1"`},
	} {
		t.Run(strings.TrimPrefix(tc.id, "test."), func(t *testing.T) {
			r.t = t
			a := r.attack(tc.id)
			if !r.ended(a, 5*time.Second) {
				t.Fatal("still running")
			}
			if got := r.crash(tc.id); !strings.Contains(got, tc.reason) {
				t.Fatalf("crash record %q, want %q", got, tc.reason)
			}
		})
	}
	t.Run("unknown", func(t *testing.T) {
		r.t = t
		a := r.attack("test.unknown")
		if !r.until(5*time.Second, func() bool { return a.top() != nil && a.top().S.ID == "after-unknown" }) || !a.running {
			t.Fatalf("an app sending unknown messages: running %v, top %v", a.running, a.top())
		}
		if n := len(a.guard.unknown); n != 3 {
			t.Fatalf("%d unknown types logged, want 3", n)
		}
		r.c.kill(a)
		r.ended(a, 5*time.Second)
	})
	t.Run("fds", func(t *testing.T) {
		r.t = t
		fds0 := openFDs()
		a := r.attack("test.fds")
		// Its 2000 messages go through the per-app budget: 500 at once,
		// then 250 a second, so 6 s when every one is read.
		if !r.until(15*time.Second, func() bool { return a.top() != nil && a.top().S.ID == "after-fds" }) {
			stopped, _ := os.ReadFile(filepath.Join(r.state, "apps", "test.fds", fakeStopped))
			t.Fatalf("the app passing files stopped being served: running %v, crash %q, its send failing %q",
				a.running, r.crash("test.fds"), stopped)
		}
		if n := openFDs(); n > fds0+8 {
			t.Fatalf("the Shell holds %d more descriptors", n-fds0)
		}
		r.c.kill(a)
		r.ended(a, 5*time.Second)
	})
	t.Run("noread", func(t *testing.T) {
		r.t = t
		a := r.launch("test.noread")
		event := ui.Msg{T: ui.MsgEvent, Event: "test", Value: strings.Repeat("e", 4096)}
		t0 := time.Now()
		for i := 0; i < 2000 && a.running && a.guard.violation == ""; i++ {
			a.send(event)
			r.pump(time.Millisecond)
		}
		t.Logf("noread: %d events queued without blocking in %v", appOutQueue, time.Since(t0).Round(time.Millisecond))
		if !r.ended(a, 5*time.Second) || !strings.Contains(r.crash("test.noread"), "not reading") {
			t.Fatalf("an app that never reads: running %v, crash %q", a.running, r.crash("test.noread"))
		}
	})
	t.Run("reqloop", func(t *testing.T) {
		r.t = t
		a := r.attack("test.reqloop")
		perms := func() (n int) {
			for _, o := range r.c.overlays {
				if _, ok := o.(*permOverlay); ok {
					n++
				}
			}
			return n
		}
		busy := func() string {
			b, _ := os.ReadFile(filepath.Join(r.state, "apps", "test.reqloop", fakeBusy))
			return string(b)
		}
		// A busy machine may still be reading the loop's requests (the
		// Shell slows an app down to its rate): what counts is where they
		// settle.
		if !r.until(15*time.Second, func() bool { return perms() == 2 && a.waiting == appMaxPending && busy() != "" }) {
			t.Fatalf("requests in a tight loop: %d consent dialogs, %d waiting, %q refused busy; running %v, crash %q",
				perms(), a.waiting, busy(), a.running, r.crash("test.reqloop"))
		}
		busy0 := busy()
		t.Logf("reqloop: two consent dialogs (camera, contacts), %d requests waiting, %s refused busy", a.waiting, busy0)
		r.c.Key(Named(KeyEsc))
		r.c.Key(Named(KeyEsc))
		if !r.until(5*time.Second, func() bool { return a.waiting == 0 }) {
			t.Fatalf("%d requests still waiting after both dialogs were answered", a.waiting)
		}
		r.c.kill(a)
		r.ended(a, 5*time.Second)
	})
	t.Run("screens", func(t *testing.T) {
		r.t = t
		a := r.attack("test.screens")
		r.until(15*time.Second, func() bool { return len(a.stack) == maxStackDepth })
		if len(a.stack) != maxStackDepth || len(a.stack[0].S.Body) != maxScreenNodes {
			t.Fatalf("stack %d, first page %d nodes", len(a.stack), len(a.stack[0].S.Body))
		}
		r.c.setForeground(a)
		t0 := time.Now()
		r.c.Render()
		first := time.Since(t0)
		t0 = time.Now()
		r.c.Render()
		again := time.Since(t0)
		t.Logf("screens: absurd page drawn in %v, again in %v", first, again)
		if first > time.Second || again > 2*maxLoopLatency {
			t.Errorf("drawing the absurd page takes %v, then %v", first, again)
		}
		r.c.kill(a)
		r.ended(a, 5*time.Second)
	})
	t.Run("fork", func(t *testing.T) {
		r.t = t
		a := r.launch("test.fork")
		if !r.ended(a, 5*time.Second) {
			t.Fatal("an app whose child holds its socket still counts as running")
		}
		raw, _ := os.ReadFile(filepath.Join(r.state, "apps", "test.fork", fakeChildPID))
		pid, _ := strconv.Atoi(string(raw))
		if pid == 0 || !r.until(3*time.Second, func() bool { return !processAlive(pid) }) {
			t.Fatalf("the app's child %d outlived it", pid)
		}
	})
	t.Run("linger", func(t *testing.T) {
		r.t = t
		a := r.app("test.linger")
		r.c.Launch(a, "")
		t0 := time.Now()
		if !r.ended(a, connGoneGrace+3*time.Second) {
			t.Fatal("an app that closed its socket keeps running")
		}
		t.Logf("linger: ended %v after its start", time.Since(t0).Round(time.Millisecond))
	})
	r.t = t
	if !r.app(probeID).running {
		t.Fatal("the probe app did not survive the hostile ones")
	}
}
