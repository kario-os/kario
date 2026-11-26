package shell

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/cts"
)

// GUIStderr, when set, also receives the renderer's error output (device
// mode, where the console is the only log anyone sees).
var GUIStderr io.Writer

// GUI drives a pixel renderer: any program that speaks the renderer protocol
// (RendererProto, specified in 23-renderer-protocol.md). Images carry
// phone-paint or phone-gui in the renderer slot, so nothing here may depend
// on how a renderer is built.
// Shell → renderer on its stdin: "hello", "frame" (frame.go), "keys"
// (a11y.go), "snap", "quit", "power", "rate", "touch" (a headless one's,
// the selftest's).
// Renderer → shell on its stdout: "ready", "display", "key", "snapped",
// "layout", "tap", "power", "input", "rate", "motion", "touch",
// "sound_switch", "shown".
type GUI struct {
	c    *Core
	dev  Device
	cmd  *exec.Cmd
	in   io.WriteCloser
	enc  *json.Encoder
	last string
	doc  map[string]any // the last frame as the renderer holds it (patches)
	// place: where the last frame was; tn numbers transitions (motion.go).
	place place
	tn    uint64
	stats GUIStats
	info  RendererInfo
	ready chan guiMsg
	acks  chan guiMsg
	done  chan struct{}
	// side is a socket pair's end the renderer holds as fd 3: shared
	// memory surfaces (media frames) are handed over on it, once each.
	side *net.UnixConn
	// sent: the shared memory each surface name was handed over as (its
	// inode); a name whose memory changed (a new page in the same node) is
	// sent again, and the renderer replaces its mapping.
	sent map[string]string
	// recycled: asked to quit so that it starts again (a language change),
	// not a crash.
	recycled bool
	// snapOnly: a renderer of its own for one screenshot (devshot.go); what
	// it says besides ready and snapped is not about the screen.
	snapOnly bool
	// timing: it says what each frame took ("drawn"), for the timeline
	// (trace.go); timed are the frames sent that it has not reported yet.
	timing bool
	timed  []sentFrame
	// latency: it times inputs to the panel (feature "latency"): its keys
	// and touches carry ids, and the frame that shows what they did names
	// them (awaiting: those taken since the last frame sent); marked: the
	// start of each app whose first page a frame named.
	latency  bool
	awaiting []awaited
	marked   map[string]time.Time
}

// awaited is an input of the renderer's taken by the Shell, and when.
type awaited struct {
	id uint64
	at time.Time
}

const (
	// maxAwaiting bounds the inputs waiting for a frame to name them.
	maxAwaiting = 256
	// answerWindow: an input whose frame takes longer than this is named
	// by none (whatever frame comes then is not its answer).
	answerWindow = 2 * time.Second
	// startWindow: an app whose first page comes later than this after
	// its start was started in the background, not waited for.
	startWindow = 30 * time.Second
)

// sentFrame is a frame sent to a renderer that reports each: its number
// (frames and patches counted alike), when, how large, as a patch or not,
// and the screen on top.
type sentFrame struct {
	n      int
	at     time.Time
	bytes  int
	patch  bool
	screen string
}

// maxTimed bounds the frames kept waiting for the renderer's report.
const maxTimed = 64

// RendererProto is the renderer protocol: the major number changes only
// with incompatible changes; renderers of the same major interoperate (new
// fields are optional and unknown ones are ignored on both sides).
const RendererProto = "phone.frame/1"

// frameVersion is a major of the renderer protocol the Shell speaks
// (23-renderer-protocol.md §2): its own, RendererProto's, as is; during a
// transition the one before it as well, for at least one release, its
// lines turned each way (in from the renderer, out to it; ui.ProtoForm's
// FromMajor and ToMajor can be them). A renderer's ready names the major
// it chose of those hello offered.
type frameVersion struct {
	major int
	in    func(map[string]json.RawMessage) error
	out   func(map[string]json.RawMessage)
}

var frameVersions = []frameVersion{{major: ownFrameMajor()}}

func ownFrameMajor() int {
	n, _ := protoMajor(RendererProto)
	return n
}

func (c *Core) frameVersions() []frameVersion {
	if c.frames != nil {
		return c.frames
	}
	return frameVersions
}

func frameName(major int) string { return "phone.frame/" + strconv.Itoa(major) }

// frameHello is what hello says of the versions: proto the oldest spoken
// (a renderer that does not negotiate reads only it) and, when there are
// several, protos with all of them, newest first.
func frameHello(vers []frameVersion, hello map[string]any) {
	majors := make([]int, len(vers))
	for i, v := range vers {
		majors[i] = v.major
	}
	slices.Sort(majors)
	hello["proto"] = frameName(majors[0])
	if len(majors) > 1 {
		var names []string
		for _, m := range slices.Backward(majors) {
			names = append(names, frameName(m))
		}
		hello["protos"] = names
	}
}

func frameNames(vers []frameVersion) string {
	var names []string
	for _, v := range vers {
		names = append(names, frameName(v.major))
	}
	return strings.Join(names, " and ")
}

// frameWriter turns what goes to the renderer into the form of the major
// it speaks, line by line, once the handshake chose one that needs it.
type frameWriter struct {
	io.WriteCloser
	out func(map[string]json.RawMessage)
}

func (w *frameWriter) Write(p []byte) (int, error) {
	if w.out == nil {
		return w.WriteCloser.Write(p)
	}
	var turned []byte
	for _, line := range bytes.SplitAfter(p, []byte("\n")) {
		body, nl := bytes.CutSuffix(line, []byte("\n"))
		if len(body) > 0 {
			t, err := turnLine(body, func(m map[string]json.RawMessage) error { w.out(m); return nil })
			if err != nil {
				return 0, err
			}
			turned = append(turned, t...)
		}
		if nl {
			turned = append(turned, '\n')
		}
	}
	if _, err := w.WriteCloser.Write(turned); err != nil {
		return 0, err
	}
	return len(p), nil
}

// turnLine applies f to a JSON line's fields.
func turnLine(line []byte, f func(map[string]json.RawMessage) error) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, err
	}
	if err := f(fields); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

// DefaultRenderer is the renderer slot under the install prefix: a link to
// whichever renderer the image carries.
const DefaultRenderer = "lib/phone/renderer"

// nobodyID is the nobody user and group.
const nobodyID = 65534

// RendererInfo is what a renderer says about itself in "ready".
type RendererInfo = cts.RendererInfo

type guiMsg struct {
	T        string        `json:"t"`
	Name     string        `json:"name,omitempty"`
	Key      string        `json:"key,omitempty"`
	Text     string        `json:"text,omitempty"`
	Long     bool          `json:"long,omitempty"`
	Retype   bool          `json:"retype,omitempty"`
	Shift    bool          `json:"shift,omitempty"`
	Chr      int           `json:"chr,omitempty"`
	Err      string        `json:"err,omitempty"`
	Proto    string        `json:"proto,omitempty"`
	Renderer *RendererInfo `json:"renderer,omitempty"`
	Features []string      `json:"features,omitempty"`
	Display  *Display      `json:"display,omitempty"`
	// Frame: the frame the renderer holds, in "snapped" when asked to echo.
	Frame map[string]any `json:"frame,omitempty"`
	// Layout: the geometry of the frame laid out ("layout").
	Layout *Layout `json:"layout,omitempty"`
	// On: the panel's power, confirmed ("power"); an animation began or
	// ended ("motion").
	On *bool `json:"on,omitempty"`
	// MS: how long turning the panel took ("power"), how long the power
	// key was down ("key"). Warn: what the renderer noticed about a panel
	// it did turn ("power").
	MS   int    `json:"ms,omitempty"`
	Warn string `json:"warn,omitempty"`
	// Hz: the rate of the mode the renderer switched the panel to ("rate").
	Hz int `json:"hz,omitempty"`
	// Kind and Added: a keyboard or mouse plugged in or out ("input"); OK:
	// it can press OK (missing: taken as yes, so that a renderer that does
	// not say never makes the phone one of buttons only).
	Kind  string `json:"kind,omitempty"`
	Added bool   `json:"added,omitempty"`
	OK    *bool  `json:"ok,omitempty"`
	// Screen, Node and Tile: where the pointer clicked ("tap").
	Screen string `json:"screen,omitempty"`
	Node   int    `json:"node,omitempty"`
	Tile   int    `json:"tile,omitempty"`
	// Phase, X, Y and Hit: what the finger a touchscreen follows did,
	// where on the frame (logical pixels), and at "down" what it came
	// down on ("touch", touch.go).
	Phase string    `json:"phase,omitempty"`
	X     float64   `json:"x,omitempty"`
	Y     float64   `json:"y,omitempty"`
	Hit   *TouchHit `json:"hit,omitempty"`
	// Position: where the phone's sound switch is ("sound_switch": ring,
	// vibrate or silent), when the renderer opened it and at each move.
	Position string `json:"position,omitempty"`
	// What frame N took the renderer ("drawn"), in milliseconds: reading
	// it, laying it out, each step of drawing it as the panel would and of
	// the transition it starts.
	N            int       `json:"n,omitempty"`
	ParseMS      float64   `json:"parse_ms,omitempty"`
	LayoutMS     float64   `json:"layout_ms,omitempty"`
	DrawMS       []float64 `json:"draw_ms,omitempty"`
	TransitionMS []float64 `json:"transition_ms,omitempty"`
	// In: a key's or a touch's id (feature "latency"), which the frame
	// showing what it did names back. App: whose first page reached the
	// panel MS after its start ("shown").
	In  uint64 `json:"in,omitempty"`
	App string `json:"app,omitempty"`
}

// Display is the panel a renderer actually drives (on a device the display
// mode decides, not the device profile).
type Display struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Scale  float64 `json:"scale,omitempty"`
}

// protoMajor returns the major number of "phone.frame/<major>[.<minor>]".
func protoMajor(p string) (int, bool) {
	v, ok := strings.CutPrefix(p, "phone.frame/")
	if !ok {
		return 0, false
	}
	v, _, _ = strings.Cut(v, ".")
	n, err := strconv.Atoi(v)
	return n, err == nil
}

// checkRenderer accepts a renderer of a major the shell speaks (vers).
func checkRenderer(m guiMsg, vers []frameVersion) (RendererInfo, *frameVersion, error) {
	var info RendererInfo
	if m.Renderer != nil {
		info = *m.Renderer
	}
	info.Proto, info.Features = m.Proto, m.Features
	v, err := pickFrame(vers, m.Proto)
	return info, v, err
}

// pickFrame is the major a renderer's ready names, of those spoken.
func pickFrame(vers []frameVersion, proto string) (*frameVersion, error) {
	got, ok := protoMajor(proto)
	if !ok {
		return nil, fmt.Errorf("renderer did not say which protocol it speaks (want %s)", frameNames(vers))
	}
	for i := range vers {
		if vers[i].major == got {
			return &vers[i], nil
		}
	}
	return nil, fmt.Errorf("renderer speaks %s, this shell speaks %s", proto, frameNames(vers))
}

func StartGUI(c *Core, path string, dev Device, args ...string) (*GUI, error) {
	c.screenDev = dev
	g, m, err := launchRenderer(c, path, dev, c.procEnv(os.Environ()), false, args...)
	if err != nil {
		return nil, err
	}
	c.renderer = &g.info
	if m.Display != nil {
		applyDisplay(&c.screenDev, *m.Display)
	}
	c.log.Printf("renderer: %s (%s, features %s)", g.info, g.info.Proto, strings.Join(g.info.Features, " "))
	c.gui = g
	// A renderer that ended reported no device going; this one reports
	// each again.
	c.inputs.n, c.inputs.ok = 0, 0
	c.panelReset()
	c.displayRenderer()
	g.Keys(c.keySettings())
	return g, nil
}

// Keys tells a renderer that reads the keyboard (feature "keys") how to
// time and combine keys (a11y.go's keySettings).
func (g *GUI) Keys(msg map[string]any) {
	if !g.info.Has("keys") {
		return
	}
	if err := g.enc.Encode(msg); err != nil {
		g.c.log.Printf("renderer: %v", err)
	}
}

// launchRenderer starts a renderer in env and waits until it is ready; it
// changes nothing of the Shell's, so a snapshot renderer (snapOnly) starts
// off the loop.
func launchRenderer(c *Core, path string, dev Device, env []string, snapOnly bool, args ...string) (*GUI, guiMsg, error) {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if snapOnly && os.Geteuid() == 0 {
		// Headless, it drops nothing itself: it starts as what the panel's
		// renderer drops to (23-renderer-protocol.md §5).
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: nobodyID, Gid: nobodyID}
		cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWNET
	}
	side, child, err := sidePair()
	if err != nil {
		return nil, guiMsg{}, err
	}
	cmd.ExtraFiles = []*os.File{child}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, guiMsg{}, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, guiMsg{}, err
	}
	if logf, err := os.OpenFile(filepath.Join(c.cfg.RunDir, "gui.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stderr = logf
		if GUIStderr != nil {
			cmd.Stderr = io.MultiWriter(logf, GUIStderr)
		}
	}
	err = StartProcess(cmd)
	child.Close()
	if err != nil {
		side.Close()
		return nil, guiMsg{}, err
	}
	vers := c.frameVersions()
	fw := &frameWriter{WriteCloser: in}
	g := &GUI{c: c, dev: dev, cmd: cmd, in: fw, enc: json.NewEncoder(fw), side: side, sent: map[string]string{},
		ready: make(chan guiMsg, 1), acks: make(chan guiMsg, 4), done: make(chan struct{}), snapOnly: snapOnly}
	go g.read(out, vers)
	hello := map[string]any{"t": "hello", "device": dev, "side_fd": 3}
	frameHello(vers, hello)
	timing := !snapOnly && active.Load() != nil
	if timing {
		hello["timing"] = true
	}
	if !snapOnly && c.cfg.RunDir != "" {
		hello["stats"] = filepath.Join(c.cfg.RunDir, PaintStatsFile)
	}
	if err := g.enc.Encode(hello); err != nil {
		return nil, guiMsg{}, err
	}
	select {
	case m := <-g.ready:
		info, v, err := checkRenderer(m, vers)
		if err != nil {
			g.Close()
			return nil, m, err
		}
		if v.major != ownFrameMajor() {
			c.log.Printf("renderer: speaks the older %s", frameName(v.major))
		}
		fw.out = v.out
		g.info = info
		g.timing = timing && info.Has("timing")
		g.latency = info.Has("latency")
		return g, m, nil
	case <-g.done:
		return nil, guiMsg{}, errors.New("renderer exited during start (see gui.log)")
	case <-time.After(10 * time.Second):
		g.Close()
		return nil, guiMsg{}, errors.New("renderer did not become ready")
	}
}

// Power turns the panel off or on (screen.go), where the renderer can.
func (g *GUI) Power(on bool) {
	if !g.info.Has("power") {
		return
	}
	raw, _ := json.Marshal(map[string]any{"t": "power", "on": on})
	if _, err := g.in.Write(append(raw, '\n')); err != nil {
		g.c.log.Printf("renderer: %v", err)
	}
}

// PlayTouch gives a headless renderer that takes touches (feature "touch")
// a finger's phase at (x, y), logical pixels of the frame: it answers as
// it would for its own touchscreen (the selftest's touch steps).
func (g *GUI) PlayTouch(phase string, x, y float64) error {
	if !g.info.Has("touch") {
		return fmt.Errorf("the renderer (%s) takes no touch", g.info)
	}
	return g.enc.Encode(map[string]any{"t": "touch", "phase": phase, "x": x, "y": y})
}

// Rate tells the renderer the refresh rate phone-displayd chose: it
// switches the panel to that mode where it drives one, and paces its
// frames by it.
func (g *GUI) Rate(hz int) {
	raw, _ := json.Marshal(map[string]any{"t": "rate", "hz": hz})
	if _, err := g.in.Write(append(raw, '\n')); err != nil {
		g.c.log.Printf("renderer: %v", err)
	}
}

// Info is the renderer's identity and features from the handshake.
func (g *GUI) Info() RendererInfo { return g.info }

// adopt takes the size the renderer really drives, reported after start;
// layout decisions (grid columns and the like) follow it.
func (g *GUI) adopt(d Display) { g.c.Post(func() { applyDisplay(&g.c.screenDev, d) }) }

func applyDisplay(dev *Device, d Display) {
	if d.Width <= 0 || d.Height <= 0 {
		return
	}
	dev.Width, dev.Height = d.Width, d.Height
	if d.Scale > 0 {
		dev.Scale = d.Scale
	}
}

// read follows the renderer's output; the lines after its ready are of
// the major ready names (vers'), turned into the Shell's own.
func (g *GUI) read(r io.Reader, vers []frameVersion) {
	defer close(g.done)
	sc := bufio.NewScanner(r)
	// A snapshot's echo carries the whole frame.
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var ver *frameVersion
	for sc.Scan() {
		line := sc.Bytes()
		if ver != nil && ver.in != nil {
			t, err := turnLine(line, ver.in)
			if err != nil {
				g.c.log.Printf("renderer: %s: %v", frameName(ver.major), err)
				continue
			}
			line = t
		}
		var m guiMsg
		if json.Unmarshal(line, &m) != nil || g.snapOnly && m.T != "ready" && m.T != "snapped" {
			continue
		}
		switch m.T {
		case "ready":
			if ver == nil {
				ver, _ = pickFrame(vers, m.Proto)
			}
			select {
			case g.ready <- m:
			default:
			}
		case "display":
			if m.Display != nil {
				g.adopt(*m.Display)
			}
		case "snapped":
			g.acks <- m
		case "drawn":
			arrived := time.Now()
			g.c.Post(func() { g.drawn(m, arrived) })
		case "input":
			kind, name, added, ok := m.Kind, m.Name, m.Added, m.OK == nil || *m.OK
			g.c.Post(func() {
				if g.c.gui == g {
					g.c.inputChanged(kind, name, added, ok)
				}
			})
		case "power":
			if m.On != nil {
				on, err, ms, warn := *m.On, m.Err, m.MS, m.Warn
				g.c.Post(func() {
					// A renderer that has gone answers no more: the one after
					// it starts afresh (panelReset).
					if g.c.gui == g {
						g.c.panelAnswered(on, err, ms, warn)
					}
				})
			}
		case "sound_switch":
			pos := m.Position
			g.c.Post(func() {
				if g.c.gui == g {
					g.c.soundSwitched(pos)
				}
			})
		case "rate":
			hz, err := m.Hz, m.Err
			g.c.Post(func() { g.c.displayApplied(hz, err) })
		case "motion":
			on := m.On != nil && *m.On
			g.c.Post(func() { g.c.displayMotion(on) })
		case "layout":
			if m.Layout != nil {
				l := m.Layout
				g.c.Post(func() { g.c.layout = l })
			}
		case "tap":
			screen, node, tile := m.Screen, m.Node, m.Tile
			g.c.Post(func() { g.c.Tap(screen, node, tile) })
		case "touch":
			if !g.info.Has("touch") {
				continue
			}
			phase, x, y, hit, at, in := m.Phase, m.X, m.Y, m.Hit, time.Now(), m.In
			g.c.Post(func() {
				g.c.Touch(phase, x, y, hit, at)
				g.taken(in)
			})
		case "shown":
			app, ms := m.App, time.Duration(m.MS)*time.Millisecond
			g.c.Post(func() { g.c.log.Printf("%s first page shown %s after start", app, ms) })
		case "key":
			k := Runes(m.Text)
			k.Long, k.Retype, k.Chr = m.Long, m.Retype, m.Chr
			if m.Key != "" {
				name, ok := keyNames[m.Key]
				if !ok {
					continue
				}
				k = Named(name)
				k.Long, k.Shift = m.Long, m.Shift
				k.Held = time.Duration(m.MS) * time.Millisecond
			}
			k.At = time.Now()
			// Logged as it comes, before the loop takes it: a loop that no
			// longer turns still shows that the presses reached the Shell.
			power := k.Name == KeyPower
			if power {
				what := "short press"
				if k.Long {
					what = "long press"
				}
				if k.Held > 0 {
					what += fmt.Sprintf(" (held %d ms)", k.Held.Milliseconds())
				}
				g.c.screenLog("power key: from the renderer: %s", what)
			}
			g.c.loop.keyIn(power)
			in := m.In
			g.c.Post(func() {
				g.c.loop.keyTaken()
				g.c.Press(k)
				g.taken(in)
			})
		}
	}
	if err := sc.Err(); err != nil {
		// Its output can no longer be followed: end it, as a crash would.
		g.c.log.Printf("renderer: %v; stopping it", err)
		_ = g.cmd.Process.Kill()
	}
}

// GUIStats counts what went to the renderer: frames, how many as patches,
// and the bytes sent against the bytes whole frames would have taken.
type GUIStats struct {
	Frames, Patches   int
	Bytes, WholeBytes int
}

func (s GUIStats) String() string {
	return fmt.Sprintf("%d frames (%d as patches), %d KB sent, %d KB as whole frames",
		s.Frames, s.Patches, s.Bytes>>10, s.WholeBytes>>10)
}

// Stats is what this renderer process was sent.
func (g *GUI) Stats() GUIStats { return g.stats }

// Push sends the current frame if it changed: as a JSON merge patch
// (RFC 7386) against the previous one when the renderer takes patches and
// the patch is smaller, else whole. It must run on the core loop, after
// Render.
func (g *GUI) Push() error {
	// Off, the panel shows nothing: no frames (screen.go).
	if g.c.screen.off {
		g.c.takeBump()
		g.awaiting = g.awaiting[:0]
		return nil
	}
	f := g.c.Frame(g.dev)
	here := g.c.placeOf(f)
	if t := transitionFor(g.place, here); t != "" {
		g.tn++
		f.Transition = &FrameTransition{Kind: t, N: g.tn}
	}
	if !g.snapOnly {
		f.Bump = g.c.takeBump()
	}
	g.place = here
	raw, err := json.Marshal(f)
	if v := g.c.snapping; v != nil && err == nil {
		raw, err = v.frame(raw)
	}
	if err != nil || string(raw) == g.last {
		if err == nil {
			g.noFrame()
		}
		return err
	}
	for id := range g.c.frameSurfs {
		if f, _, _ := g.surface(id); f != nil && g.sent[id] != fileID(f) {
			ino, err := g.sendSurface(id)
			if err != nil {
				g.c.log.Printf("renderer: surface %s: %v", id, err)
			}
			g.sent[id] = ino
		}
	}
	g.last = string(raw)
	msg := raw
	if g.info.Has("patch") {
		var cur map[string]any
		_ = json.Unmarshal(raw, &cur)
		if g.doc != nil {
			if p, _ := json.Marshal(map[string]any{"t": "patch", "patch": mergePatch(g.doc, cur)}); len(p) < len(raw) {
				msg = p
				g.stats.Patches++
			}
		}
		g.doc = cur
	}
	g.stats.Frames++
	g.stats.Bytes += len(msg) + 1
	g.stats.WholeBytes += len(raw) + 1
	if g.timing {
		if len(g.timed) == maxTimed {
			g.timed = g.timed[1:]
		}
		g.timed = append(g.timed, sentFrame{n: g.stats.Frames, at: time.Now(), bytes: len(msg) + 1, patch: len(msg) < len(raw), screen: f.Screen})
	}
	if g.latency {
		msg = g.marks(msg, f)
	}
	if _, err = g.in.Write(append(msg, '\n')); err != nil {
		return err
	}
	// After the frame that stopped naming them: the renderer unmaps them.
	for id := range g.sent {
		if !g.c.frameSurfs[id] {
			drop, _ := json.Marshal(map[string]any{"t": "drop", "id": id})
			_, _, _ = g.side.WriteMsgUnix(drop, nil, nil)
			delete(g.sent, id)
		}
	}
	return nil
}

// PaintStatsFile, in the run directory, is where a renderer that measures
// (feature "latency") writes its numbers (hello's stats; 39-oneplus-6t.md
// §8.1).
const PaintStatsFile = "paint-stats.json"

// taken: the renderer's input id was handled; the next frame sent names
// it.
func (g *GUI) taken(id uint64) {
	if id == 0 || !g.latency {
		return
	}
	if len(g.awaiting) == maxAwaiting {
		g.awaiting = g.awaiting[1:]
	}
	g.awaiting = append(g.awaiting, awaited{id, time.Now()})
}

// noFrame: a push found nothing new to send. The inputs taken meanwhile
// showed nothing, unless the foreground app is still to answer (an action
// out, keys held for it, its first page to come): they wait for its frame,
// answerWindow at most.
func (g *GUI) noFrame() {
	if len(g.awaiting) == 0 {
		return
	}
	if a := g.c.current(); a == nil || a.system || !a.input.up && len(a.input.held) == 0 && a.top() != nil {
		g.awaiting = g.awaiting[:0]
		return
	}
	g.awaiting = slices.DeleteFunc(g.awaiting, func(w awaited) bool { return time.Since(w.at) > answerWindow })
}

// marks adds to a frame message msg (f's) what it shows for the renderer
// to time (23-renderer-protocol.md §3): the inputs taken since the last
// frame (in), an app's first page (start).
func (g *GUI) marks(msg []byte, f *Frame) []byte {
	extra := map[string]any{}
	var ids []uint64
	for _, w := range g.awaiting {
		if time.Since(w.at) <= answerWindow {
			ids = append(ids, w.id)
		}
	}
	g.awaiting = g.awaiting[:0]
	if len(ids) > 0 {
		extra["in"] = ids
	}
	if st := g.startMark(f); st != nil {
		extra["start"] = st
	}
	if len(extra) == 0 || len(msg) < 2 || msg[len(msg)-1] != '}' {
		return msg
	}
	raw, _ := json.Marshal(extra)
	out := make([]byte, 0, len(msg)+len(raw))
	out = append(out, msg[:len(msg)-1]...)
	out = append(out, ',')
	return append(out, raw[1:]...)
}

// startMark: the first page of an app this Shell started (not one taken
// over), with its process's start on CLOCK_MONOTONIC, the renderer's
// clock; the renderer says when it reached the panel ("shown").
func (g *GUI) startMark(f *Frame) map[string]any {
	a := g.c.current()
	if a == nil || a.system || a.adopted || a.started.IsZero() || f.Screen == "" || g.marked[a.M.ID].Equal(a.started) {
		return nil
	}
	if g.marked == nil {
		g.marked = map[string]time.Time{}
	}
	g.marked[a.M.ID] = a.started
	at := monoMicros(a.started)
	if time.Since(a.started) > startWindow || at <= 0 {
		return nil
	}
	return map[string]any{"app": a.M.ID, "at": at}
}

// monoMicros is t on CLOCK_MONOTONIC (the clock of evdev's and DRM's
// timestamps) in microseconds, 0 if it cannot be read.
func monoMicros(t time.Time) int64 {
	var ts unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts) != nil {
		return 0
	}
	return ts.Nano()/1e3 - time.Since(t).Microseconds()
}

// AskStats has a renderer that measures (feature "latency") write its
// numbers to hello's stats file now (phone-shell --dump).
func (g *GUI) AskStats() {
	if !g.latency {
		return
	}
	if err := g.enc.Encode(map[string]string{"t": "stats"}); err != nil {
		g.c.log.Printf("renderer: %v", err)
	}
}

// drawn takes the renderer's report of frame m.N, which arrived then, to
// the timeline; the frames sent before it, which it did not report (one it
// could not read), are dropped.
func (g *GUI) drawn(m guiMsg, arrived time.Time) {
	for i, f := range g.timed {
		if f.n != m.N {
			continue
		}
		g.timed = g.timed[i+1:]
		if t := active.Load(); t != nil {
			t.rendererFrame(f, arrived, m.ParseMS, m.LayoutMS, m.DrawMS, m.TransitionMS)
		}
		return
	}
}

// mergePatch is the JSON merge patch (RFC 7386) turning old into cur:
// changed members, null for removed ones, objects patched member by member,
// other values replaced whole. One extension: an array that keeps its
// length is patched element by element as {"$set": {"<index>": patch}}, so
// moving the focus in a grid of photo thumbnails does not resend them all
// (frames never contain a "$set" member).
func mergePatch(old, cur map[string]any) map[string]any {
	p := map[string]any{}
	for k := range old {
		if _, ok := cur[k]; !ok {
			p[k] = nil
		}
	}
	for k, v := range cur {
		o, had := old[k]
		if !had {
			p[k] = v
			continue
		}
		if d, changed := diffValue(o, v); changed {
			p[k] = d
		}
	}
	return p
}

func diffValue(o, v any) (any, bool) {
	switch vv := v.(type) {
	case map[string]any:
		if om, ok := o.(map[string]any); ok {
			sub := mergePatch(om, vv)
			return sub, len(sub) > 0
		}
	case []any:
		if oa, ok := o.([]any); ok && len(oa) == len(vv) && len(vv) > 0 {
			set := map[string]any{}
			for i := range vv {
				if d, changed := diffValue(oa[i], vv[i]); changed {
					set[strconv.Itoa(i)] = d
				}
			}
			if len(set) == 0 {
				return nil, false
			}
			if len(set) < len(vv) {
				return map[string]any{"$set": set}, true
			}
			return v, true
		}
	}
	return v, !reflect.DeepEqual(o, v)
}

// Snap asks the renderer to write the current screen as <name>.png.
func (g *GUI) Snap(name string) error {
	if !g.info.Has("snap") {
		return fmt.Errorf("pixel snapshot %s: the renderer (%s) has no snapshots", name, g.info)
	}
	if err := g.Push(); err != nil {
		return err
	}
	// A renderer that takes patches echoes the frame it rebuilt from them,
	// so every snapshot also checks the patches end to end.
	return g.snap(name, g.info.Has("patch"))
}

// snap asks for <name>.png of the frame the renderer has.
func (g *GUI) snap(name string, echo bool) error {
	if err := g.enc.Encode(map[string]any{"t": "snap", "name": name, "echo": echo}); err != nil {
		return err
	}
	select {
	case m := <-g.acks:
		if m.Err != "" {
			return fmt.Errorf("pixel snapshot %s: %s", name, m.Err)
		}
		if echo && !reflect.DeepEqual(m.Frame, g.doc) {
			return fmt.Errorf("pixel snapshot %s: the renderer's frame differs from the shell's at %s (patches went wrong)", name, firstDiff("", g.doc, m.Frame))
		}
		return nil
	case <-g.done:
		return errors.New("renderer exited")
	case <-time.After(20 * time.Second):
		return fmt.Errorf("pixel snapshot %s timed out", name)
	}
}

// Done is closed when the renderer process exits.
func (g *GUI) Done() <-chan struct{} { return g.done }

// Pid is the renderer process.
func (g *GUI) Pid() int { return g.cmd.Process.Pid }

// Kill ends the renderer as a crash would (the selftest's gui-crash).
func (g *GUI) Kill() error { return g.cmd.Process.Kill() }

// Recycle asks the renderer to quit so that the watchdog starts it again,
// in the language and time zone now in force; it runs on the core loop.
func (g *GUI) Recycle() {
	g.recycled = true
	if err := g.enc.Encode(map[string]string{"t": "quit"}); err != nil {
		g.c.log.Printf("renderer: %v", err)
	}
}

// Recycled: it quit because Recycle asked (the watchdog need not count a
// crash).
func (g *GUI) Recycled() bool { return g.recycled }

// Close asks the renderer to quit and kills it if it has not in 3 s (a
// renderer that stopped reading would block the request itself).
func (g *GUI) Close() {
	go func() {
		_ = g.enc.Encode(map[string]string{"t": "quit"})
		_ = g.in.Close()
	}()
	exited := make(chan struct{})
	go func() {
		_ = g.cmd.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		_ = g.cmd.Process.Kill()
		<-exited
	}
	_ = g.side.Close()
}

// firstDiff names where two decoded JSON values first differ.
func firstDiff(path string, want, got any) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s (want an object, got %v)", path, got)
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			if !reflect.DeepEqual(w[k], g[k]) {
				return firstDiff(path+"."+k, w[k], g[k])
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return fmt.Sprintf("%s (want %d elements, got %v)", path, len(w), got)
		}
		for i := range w {
			if !reflect.DeepEqual(w[i], g[i]) {
				return firstDiff(fmt.Sprintf("%s[%d]", path, i), w[i], g[i])
			}
		}
	}
	return fmt.Sprintf("%s (want %v, got %v)", path, want, got)
}

// sidePair is the side channel to a renderer: message boundaries kept, so a
// surface's description and its fd arrive together.
func sidePair() (*net.UnixConn, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fds[0]), "renderer-side")
	defer f.Close()
	c, err := net.FileConn(f)
	if err != nil {
		syscall.Close(fds[1])
		return nil, nil, err
	}
	return c.(*net.UnixConn), os.NewFile(uintptr(fds[1]), "renderer-side-child"), nil
}

// sendSurface hands the renderer a media stream's shared memory (read-only
// use; it maps it and reads the frame a FrameImage names).
func (g *GUI) sendSurface(id string) (string, error) {
	f, w, h := g.surface(id)
	if f == nil {
		return "", errors.New("no such surface")
	}
	msg, _ := json.Marshal(map[string]any{"t": "surface", "id": id, "w": w, "h": h})
	_, _, err := g.side.WriteMsgUnix(msg, syscall.UnixRights(int(f.Fd())), nil)
	return fileID(f), err
}

func (g *GUI) surface(id string) (*os.File, int, int) {
	if s := g.c.surfs[id]; s != nil {
		return s.File, s.W, s.H
	}
	if f, w, h, ok := g.c.webSurface(id); ok {
		return f, w, h
	}
	if f, w, h, ok := g.c.wallpaperFile(id); ok {
		return f, w, h
	}
	if f, w, h, ok := g.c.thumbFile(id); ok {
		return f, w, h
	}
	return nil, 0, 0
}

// fileID names an open file by device and inode (memfds have their own).
func fileID(f *os.File) string {
	var st syscall.Stat_t
	if syscall.Fstat(int(f.Fd()), &st) != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}
