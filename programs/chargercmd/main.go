// phone-charger is charger mode's screen (28-battery.md §9): the
// bootloader started the phone because a charger was plugged in while it
// was off (androidboot.mode=charger), and phone-charger-generator booted
// phone-charger.target instead of the system. It starts the renderer and
// shows the battery's level, whether it charges and the charge current on
// the standby screen; the screen goes off after a few seconds and a press
// of the power key lights it again; holding the key starts the system (a
// reboot, which the bootloader takes as a normal start), or on a battery
// too low for that, once it has charged enough; a charger unplugged for a
// few seconds powers the phone off. With the screen off the phone
// suspends where the Shell would.
//
//	phone-charger [--sku sku.json] [--renderer path] [--state-dir dir] [--status file] [--dry-run]
package chargercmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kario-os/kario/hal/backlight"
	"github.com/kario-os/kario/hal/pm"
	"github.com/kario-os/kario/hal/power"
	"github.com/kario-os/kario/shell"
)

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	exe, _ := os.Executable()
	os.Exit(run(os.Args[1:], os.Stderr, filepath.Dir(filepath.Dir(exe))))
}

// slowPoll: with the screen off the supplies are read this often besides
// their uevents (a uevent the socket dropped is not missed for long).
// readyWait: how long the renderer has to say it is ready.
const (
	slowPoll  = 30 * time.Second
	readyWait = 10 * time.Second
)

// lockedWriter is the log, written by the charger and the renderer's
// error output alike.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func run(args []string, w io.Writer, prefix string) int {
	logw := &lockedWriter{w: w}
	say := func(f string, a ...any) { fmt.Fprintf(logw, "phone-charger: "+f+"\n", a...) }
	fs := flag.NewFlagSet("phone-charger", flag.ContinueOnError)
	fs.SetOutput(logw)
	skuFile := fs.String("sku", filepath.Join(prefix, "share", "phone", "sku.json"), "the board's sku.json")
	rendererPath := fs.String("renderer", filepath.Join(prefix, shell.DefaultRenderer), "the renderer")
	stateDir := fs.String("state-dir", "/var/lib/phone", "the Shell's state (settings.json: language, clock, theme)")
	statusFile := fs.String("status", "/run/phone/charger", "where the state is written at each change")
	dry := fs.Bool("dry-run", false, "log the reboot or power-off instead of doing it")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	sku, err := shell.LoadSKUFile(*skuFile)
	if err != nil {
		say("%v", err)
		return 1
	}
	dev, ok := sku.SKUDevice()
	if !ok {
		say("%s describes no screen", *skuFile)
		return 1
	}
	looks := shell.LoadStandby(*stateDir, sku)
	env := append(os.Environ(), "PHONE_LANG="+looks.Lang())
	r, err := startRenderer(*rendererPath, dev, env, logw)
	if err != nil {
		say("renderer: %v", err)
		return 1
	}
	defer r.stop()
	if !r.has("power") {
		say("the renderer (%s) cannot turn the panel off: the screen stays on", r.name)
	}
	r.send(map[string]any{"t": "keys", "power_long": holdToStart.Milliseconds()})

	var bl backlight.Backlight
	if os.Geteuid() == 0 {
		bl = backlight.Panel(sku.Backlight)
	}
	out := &output{r: r, bl: bl, level: looks.Brightness(), say: say}
	supplies := make(chan struct{}, 1)
	poll := slowPoll
	if err := power.Watch(func() {
		select {
		case supplies <- struct{}{}:
		default:
		}
	}); err != nil {
		poll = time.Second
		say("no power supply uevents (%v): reading them every %s", err, poll)
	}
	c := &charger{looks: looks, screen: out, log: say, act: func(what string) error {
		if *dry {
			say("dry run: would %s", what)
			return nil
		}
		return exec.Command("systemctl", what).Run()
	}}
	sl := &dozer{say: say}
	if sys, err := shell.OpenSleep(sku); err != nil {
		sl.off = err.Error()
		say("sleep: off (%v)", err)
	} else {
		sl.sys = sys
		wake := "the power key, the RTC every " + refresh.String()
		if chargers := pm.ChargersWake(); len(chargers) > 0 {
			wake += " and the chargers " + strings.Join(chargers, " ")
		}
		say("sleep: %s %s after the screen goes off; woken by %s", sys.Mode(), sleepAfter, wake)
	}
	out.ready()
	sup, battery := power.Read()
	g, _ := power.ReadGauge()
	c.begin(time.Now(), sup, battery, g)
	status := func() { writeStatus(*statusFile, c, out, sl) }
	status()
	handle := func(m msg) {
		switch {
		case m.T == "key" && m.Key == "power":
			say("power key: %s", map[bool]string{true: "held", false: "pressed"}[m.Long])
			c.key(time.Now(), m.Long)
		case m.T == "power" && m.On != nil:
			out.answered(*m.On, m.Err)
		}
	}
	reread := func() {
		sup, battery := power.Read()
		g, _ := power.ReadGauge()
		c.supply(time.Now(), sup, battery, g)
	}
	drain := func() bool {
		for {
			select {
			case m, ok := <-r.msgs:
				if !ok {
					return false
				}
				handle(m)
			case <-supplies:
				reread()
			default:
				return true
			}
		}
	}
	for {
		every := poll
		if c.busy() || out.err != nil || !sl.awake() {
			every = time.Second
		}
		select {
		case m, ok := <-r.msgs:
			if !ok {
				say("the renderer exited")
				return 1
			}
			handle(m)
		case <-supplies:
			reread()
		case <-time.After(every):
			reread()
			c.tick(time.Now())
			out.retry()
		}
		status()
		if sl.due(c, out, time.Now()) {
			if !sl.sleep(c, out, drain) {
				say("the renderer exited")
				return 1
			}
			reread()
			status()
		}
	}
}

// output is the screen on the renderer: frames as they change, the panel
// turned one change at a time (the next once the renderer answered), the
// backlight off before the panel goes off and lit once it says it is on,
// as the Shell does it (shell/screen.go).
type output struct {
	r     *renderer
	bl    backlight.Backlight
	level int
	say   func(string, ...any)

	last  []byte
	want  bool
	panel bool
	// asking: a change was asked for and not answered; err: why the last
	// one failed (asked again at the next tick).
	asking bool
	err    error
}

// ready: the renderer started with the panel on.
func (o *output) ready() {
	o.want, o.panel = true, true
	o.light()
}

func (o *output) Frame(f shell.Frame) {
	raw, err := json.Marshal(f)
	if err != nil || string(raw) == string(o.last) {
		return
	}
	o.last = raw
	o.r.sendRaw(raw)
}

func (o *output) Screen(on bool) {
	o.want = on
	o.sync()
}

func (o *output) sync() {
	if o.asking || o.want == o.panel || !o.r.has("power") {
		return
	}
	if !o.want && o.bl != nil {
		_ = o.bl.Power(false)
	}
	o.asking = true
	o.r.send(map[string]any{"t": "power", "on": o.want})
}

func (o *output) answered(on bool, why string) {
	asked := o.want
	o.asking, o.panel = false, on
	o.err = nil
	if on != asked && why != "" {
		o.err = errors.New(why)
		o.say("panel: could not turn it %s: %s", onOff(asked), why)
	}
	if on && o.want {
		o.light()
	}
	o.sync()
}

// settled: the panel is as the charger wants it, nothing asked and not
// answered (the backlight is off before a suspend).
func (o *output) settled() bool { return !o.asking && o.err == nil && o.panel == o.want }

// retry asks once more for a change that failed.
func (o *output) retry() {
	if o.err != nil && !o.asking {
		o.err = nil
		o.sync()
	}
}

func (o *output) light() {
	if o.bl == nil {
		return
	}
	if err := o.bl.Set(o.level); err != nil {
		o.say("backlight: %v", err)
	}
	_ = o.bl.Power(true)
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// writeStatus is the state, for a look from a shell over USB or serial
// and for the emulator's test (yocto/qemu-charger.sh).
func writeStatus(path string, c *charger, o *output, d *dozer) {
	st := map[string]any{"screen": onOff(c.on), "panel": onOff(o.panel), "battery": c.battery, "level": c.sup.Level,
		"charging": c.sup.Charging, "line": c.line(), "leaving": c.leaving, "status": c.gauge.Status,
		"too_low": c.tooLow(), "pending": c.pending, "sleep": d.state(), "sleeps": d.sleeps, "awake": d.why}
	if c.gauge.HasCurrent {
		st["current_ua"] = c.gauge.Current
	}
	raw, _ := json.Marshal(st)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, append(raw, '\n'), 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// msg is what the renderer says, of what the charger reads.
type msg struct {
	T        string   `json:"t"`
	Key      string   `json:"key,omitempty"`
	Long     bool     `json:"long,omitempty"`
	On       *bool    `json:"on,omitempty"`
	Err      string   `json:"err,omitempty"`
	Proto    string   `json:"proto,omitempty"`
	Features []string `json:"features,omitempty"`
	Renderer *struct {
		Name string `json:"name"`
	} `json:"renderer,omitempty"`
}

// renderer is the renderer process (23-renderer-protocol.md), spoken to
// as the Shell does, without the side socket (no shared memory surfaces).
type renderer struct {
	cmd      *exec.Cmd
	in       io.WriteCloser
	msgs     chan msg
	name     string
	features []string
}

func startRenderer(path string, dev shell.Device, env []string, logw io.Writer) (*renderer, error) {
	cmd := exec.Command(path)
	cmd.Env = env
	cmd.Stderr = logw
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r := &renderer{cmd: cmd, in: in, msgs: make(chan msg, 16)}
	go func() {
		defer close(r.msgs)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			var m msg
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				r.msgs <- m
			}
		}
	}()
	r.send(map[string]any{"t": "hello", "device": dev, "proto": shell.RendererProto})
	timeout := time.After(readyWait)
	for {
		select {
		case m, ok := <-r.msgs:
			if !ok {
				_ = cmd.Wait()
				return nil, errors.New("it exited during start")
			}
			if m.T != "ready" {
				continue
			}
			if !sameMajor(m.Proto, shell.RendererProto) {
				r.stop()
				return nil, fmt.Errorf("it speaks %q, phone-charger %s", m.Proto, shell.RendererProto)
			}
			r.features, r.name = m.Features, path
			if m.Renderer != nil && m.Renderer.Name != "" {
				r.name = m.Renderer.Name
			}
			return r, nil
		case <-timeout:
			r.stop()
			return nil, errors.New("it did not become ready")
		}
	}
}

// sameMajor: two "phone.frame/<major>[.<minor>]" of one major.
func sameMajor(a, b string) bool {
	major := func(p string) string {
		v, ok := strings.CutPrefix(p, "phone.frame/")
		if !ok {
			return ""
		}
		v, _, _ = strings.Cut(v, ".")
		return v
	}
	return major(a) != "" && major(a) == major(b)
}

func (r *renderer) has(feature string) bool {
	for _, f := range r.features {
		if f == feature {
			return true
		}
	}
	return false
}

func (r *renderer) send(m map[string]any) {
	raw, _ := json.Marshal(m)
	r.sendRaw(raw)
}

func (r *renderer) sendRaw(raw []byte) {
	_, _ = r.in.Write(append(raw, '\n'))
}

func (r *renderer) stop() {
	_ = r.in.Close()
	_ = r.cmd.Process.Kill()
	_ = r.cmd.Wait()
}
