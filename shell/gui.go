package shell

import (
	"bufio"
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

	"github.com/kario-os/kario/cts"
)

// GUIStderr, when set, also receives the renderer's error output (device
// mode, where the console is the only log anyone sees).
var GUIStderr io.Writer

// GUI drives a pixel renderer: any program that speaks the renderer protocol
// (RendererProto, specified in 23-renderer-protocol.md). Images carry
// phone-paint or phone-gui in the renderer slot, so nothing here may depend
// on how a renderer is built.
// Shell → renderer on its stdin: "hello", "frame" (frame.go), "snap", "quit".
// Renderer → shell on its stdout: "ready", "display", "key", "snapped",
// "layout", "tap".
type GUI struct {
	c     *Core
	dev   Device
	cmd   *exec.Cmd
	in    io.WriteCloser
	enc   *json.Encoder
	last  string
	doc   map[string]any // the last frame as the renderer holds it (patches)
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
}

// RendererProto is the renderer protocol: the major number changes only
// with incompatible changes; renderers of the same major interoperate (new
// fields are optional and unknown ones are ignored on both sides).
const RendererProto = "phone.frame/1"

// DefaultRenderer is the renderer slot under the install prefix: a link to
// whichever renderer the image carries.
const DefaultRenderer = "lib/phone/renderer"

// RendererInfo is what a renderer says about itself in "ready".
type RendererInfo = cts.RendererInfo

type guiMsg struct {
	T        string        `json:"t"`
	Name     string        `json:"name,omitempty"`
	Key      string        `json:"key,omitempty"`
	Text     string        `json:"text,omitempty"`
	Long     bool          `json:"long,omitempty"`
	Err      string        `json:"err,omitempty"`
	Proto    string        `json:"proto,omitempty"`
	Renderer *RendererInfo `json:"renderer,omitempty"`
	Features []string      `json:"features,omitempty"`
	Display  *Display      `json:"display,omitempty"`
	// Frame: the frame the renderer holds, in "snapped" when asked to echo.
	Frame map[string]any `json:"frame,omitempty"`
	// Layout: the geometry of the frame laid out ("layout").
	Layout *Layout `json:"layout,omitempty"`
	// On: the panel's power, confirmed ("power").
	On *bool `json:"on,omitempty"`
	// Kind and Added: a keyboard or mouse plugged in or out ("input").
	Kind  string `json:"kind,omitempty"`
	Added bool   `json:"added,omitempty"`
	// Screen, Node and Tile: where the pointer clicked ("tap").
	Screen string `json:"screen,omitempty"`
	Node   int    `json:"node,omitempty"`
	Tile   int    `json:"tile,omitempty"`
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

// checkRenderer accepts a renderer of the shell's major version.
func checkRenderer(m guiMsg) (RendererInfo, error) {
	var info RendererInfo
	if m.Renderer != nil {
		info = *m.Renderer
	}
	info.Proto, info.Features = m.Proto, m.Features
	want, _ := protoMajor(RendererProto)
	got, ok := protoMajor(m.Proto)
	switch {
	case !ok:
		return info, fmt.Errorf("renderer did not say which protocol it speaks (want %s)", RendererProto)
	case got != want:
		return info, fmt.Errorf("renderer speaks %s, this shell speaks %s", m.Proto, RendererProto)
	}
	return info, nil
}

func StartGUI(c *Core, path string, dev Device, args ...string) (*GUI, error) {
	c.screenDev = dev
	cmd := exec.Command(path, args...)
	cmd.Env = c.procEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	side, child, err := sidePair()
	if err != nil {
		return nil, err
	}
	cmd.ExtraFiles = []*os.File{child}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
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
		return nil, err
	}
	g := &GUI{c: c, dev: dev, cmd: cmd, in: in, enc: json.NewEncoder(in), side: side, sent: map[string]string{},
		ready: make(chan guiMsg, 1), acks: make(chan guiMsg, 4), done: make(chan struct{})}
	go g.read(out)
	if err := g.enc.Encode(map[string]any{"t": "hello", "proto": RendererProto, "device": dev, "side_fd": 3}); err != nil {
		return nil, err
	}
	select {
	case m := <-g.ready:
		info, err := checkRenderer(m)
		if err != nil {
			g.Close()
			return nil, err
		}
		g.info = info
		c.renderer = &g.info
		if m.Display != nil {
			applyDisplay(&c.screenDev, *m.Display)
		}
		c.log.Printf("renderer: %s (%s, features %s)", info, info.Proto, strings.Join(info.Features, " "))
		c.gui = g
		if c.screen.off {
			g.Power(false)
		}
		return g, nil
	case <-g.done:
		return nil, errors.New("renderer exited during start (see gui.log)")
	case <-time.After(10 * time.Second):
		g.Close()
		return nil, errors.New("renderer did not become ready")
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

func (g *GUI) read(r io.Reader) {
	defer close(g.done)
	sc := bufio.NewScanner(r)
	// A snapshot's echo carries the whole frame.
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var m guiMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.T {
		case "ready":
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
		case "input":
			kind, name, added := m.Kind, m.Name, m.Added
			g.c.Post(func() { g.c.inputChanged(kind, name, added) })
		case "power":
			if m.On != nil {
				on, err := *m.On, m.Err
				g.c.Post(func() {
					g.c.screen.panel = on
					if err != "" {
						g.c.log.Printf("renderer: panel power: %s", err)
					}
				})
			}
		case "layout":
			if m.Layout != nil {
				l := m.Layout
				g.c.Post(func() { g.c.layout = l })
			}
		case "tap":
			screen, node, tile := m.Screen, m.Node, m.Tile
			g.c.Post(func() { g.c.Tap(screen, node, tile) })
		case "key":
			k := Runes(m.Text)
			k.Long = m.Long
			if m.Key != "" {
				name, ok := keyNames[m.Key]
				if !ok {
					continue
				}
				k = Named(name)
				k.Long = m.Long
			}
			g.c.Post(func() { g.c.Press(k) })
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
		return nil
	}
	raw, err := json.Marshal(g.c.Frame(g.dev))
	if err != nil || string(raw) == g.last {
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
	echo := g.info.Has("patch")
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
		keys := make([]string, 0, len(w)+len(g))
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
