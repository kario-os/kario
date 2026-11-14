package shell

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/media"
	"github.com/kario-os/kario/sandbox"
	"github.com/kario-os/kario/ui"
)

// Web view nodes (14 §5): the page at a web node's Src is rendered by
// phone-webview (WPE WebKit). There is one such process per host app, in a
// sandbox of its own: the uid "<app>/web", the host's network permission, a
// data directory under <state>/web, a cgroup next to the app's; every web
// node of the app is a view in it. A view lives while its node is in one of
// the app's screens and is hidden from the engine while not on screen, so a
// page keeps its state under another screen or app. Its frames go into a
// media surface handed to the renderer like a video's: the page's pixels
// never pass through the shell. The shell forwards keys, edits the focused
// text field through the system input method, shows the page's dialogs and
// list boxes as system UI naming the page, and answers phone.call through
// the broker, as the host app under the page's origin.

// webCaps are the capabilities pages may call; the host app must hold the
// permissions, runtime ones are granted per origin.
var webCaps = map[string]bool{ui.CapShareOpen: true, ui.CapClipboardSet: true, ui.CapLocationGet: true, ui.CapAuthConfirm: true}

const (
	webIdle     = 30 * time.Second // how long a process without views stays
	webRestarts = 3                // starts a minute before an app's pages give up
	webPidsMax  = 512              // WebKit's processes are thread-heavy
)

type webProc struct {
	app   *App
	cmd   *exec.Cmd
	ctl   *net.UnixConn
	ended bool
	idle  time.Time // since when it has no views
}

type webView struct {
	proc       *webProc
	id         int
	key        string
	src        string
	file       *os.File // the page's surface (from phone-webview)
	view       *media.Surface
	w, h       int
	seq        uint32
	frames     int
	url        string
	title      string
	focus      string
	loading    bool
	canBack    bool
	canForward bool
	zoom       float64
	// edit is the kind of text field the page's focus is on ("" if none),
	// value its text; field counts focus moves, so the input method knows a
	// new field from more text in the same one.
	edit    string
	value   string
	field   int
	visible bool
}

type webItem struct {
	Label    string `json:"label"`
	Enabled  bool   `json:"enabled"`
	Selected bool   `json:"selected"`
	Group    bool   `json:"group"`
}

type webMsg struct {
	T       string          `json:"t"`
	View    int             `json:"view"`
	W       int             `json:"w"`
	H       int             `json:"h"`
	Seq     uint32          `json:"seq"`
	URL     string          `json:"url"`
	Title   string          `json:"title"`
	Focus   string          `json:"focus"`
	Loading bool            `json:"loading"`
	CanBack bool            `json:"can_back"`
	Edit    string          `json:"edit"`
	Value   string          `json:"value"`
	ID      int             `json:"id"`
	Cap     string          `json:"cap"`
	Args    json.RawMessage `json:"args"`
	Kind    string          `json:"kind"`
	Message string          `json:"message"`
	Items   []webItem       `json:"items"`
	URI     string          `json:"uri"`
	// The browser's additions (webbrowse.go).
	CanForward bool      `json:"can_forward"`
	Zoom       float64   `json:"zoom"`
	Count      int       `json:"count"`
	Name       string    `json:"name"`
	Mime       string    `json:"mime"`
	OK         bool      `json:"ok"`
	Types      []string  `json:"types"`
	Default    string    `json:"default"`
	Sites      []webSite `json:"sites"`
}

// webWant is a web node the last frame showed.
type webWant struct {
	app *App
	src string
}

// Text fields typed into without the input method, as the shell's own
// input nodes: passwords, numbers.
var webDirect = map[string]bool{"password": true, "number": true, "tel": true}

func webKey(a *App, v *View, n *ui.Node) string { return a.M.ID + "/" + v.S.ID + "/" + n.ID }

// webSize is a page's surface: the renderer's body area in physical pixels
// (phone-paint's layout: margins, status, title and soft key bars).
func (c *Core) webSize() (int, int, float64) {
	d := c.screenDev
	s := d.Scale
	if s <= 0 {
		s = 1
	}
	w, h := float64(d.Width)/s-12, float64(d.Height)/s-64
	if d.Width <= 0 || w < 100 || h < 100 {
		return 456, 512, 2
	}
	return int(w * s), int(h * s), s
}

// webviewBinary is phone-webview, or (developer machine only) the one
// PHONE_WEBVIEW names, run as it is.
func (c *Core) webviewBinary() (string, bool) {
	if p := os.Getenv("PHONE_WEBVIEW"); p != "" && !c.Sandboxed() {
		return p, false
	}
	p := filepath.Join(c.cfg.AppBinDir, "phone-webview")
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// HasWebView: pages can be shown (phone-webview and a pixel renderer).
func (c *Core) HasWebView() bool {
	p, _ := c.webviewBinary()
	return p != "" && c.renderer != nil
}

// webMemory is the memory an app's pages may use: the cgroup limit of its
// web process tree; WebKit is told a part of it, so that it frees caches
// and reloads an oversized page before the kernel steps in. WPE's tree (the
// view, the web and the network process) takes about 250 MB for an
// ordinary page, so less than 384 MB has it thrash at its own limit.
func (c *Core) webMemory() int64 {
	switch mb := c.cfg.SKU.MemoryMB; {
	case mb >= 3500:
		return 768 << 20
	case mb > 0 && mb < 1500:
		return 384 << 20
	}
	return 512 << 20
}

// webCache is WebKit's cache model by memory tier: none on the smallest
// phones (a page stays within its web process's limit; going back reloads
// it), a document browser's in the middle, a web browser's on the biggest.
func (c *Core) webCache() string {
	switch mb := c.cfg.SKU.MemoryMB; {
	case mb > 0 && mb < 1500:
		return "viewer"
	case mb > 0 && mb < 3500:
		return "browser"
	}
	return "web"
}

// webGPU: the SKU lets pages render on the GPU and the device has a render
// node (never the display itself) for the sandbox to open.
func (c *Core) webGPU() bool {
	if !c.cfg.SKU.WebGPU {
		return false
	}
	nodes, _ := filepath.Glob("/dev/dri/renderD*")
	return len(nodes) > 0
}

// webProcFor is the app's web process, started if needed (at most
// webRestarts times a minute).
func (c *Core) webProcFor(a *App) (*webProc, error) {
	if p := c.webprocs[a.M.ID]; p != nil && !p.ended {
		return p, nil
	}
	now := c.now()
	var recent []time.Time
	for _, t := range c.webStarts[a.M.ID] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= webRestarts {
		return nil, fmt.Errorf("its web process ended %d times within a minute", len(recent))
	}
	p, err := c.startWebProc(a)
	if err != nil {
		return nil, err
	}
	c.webStarts[a.M.ID] = append(recent, now)
	c.webprocs[a.M.ID] = p
	c.webStarted++
	return p, nil
}

func (c *Core) startWebProc(a *App) (*webProc, error) {
	exe, sandboxed := c.webviewBinary()
	if exe == "" {
		return nil, errors.New("no web view on this device")
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	pf := os.NewFile(uintptr(fds[0]), "webview-ctl")
	child := os.NewFile(uintptr(fds[1]), "webview-ctl-child")
	defer child.Close()
	conn, err := net.FileConn(pf)
	pf.Close()
	if err != nil {
		return nil, err
	}
	data := filepath.Join(c.cfg.StateDir, "web", a.M.ID)
	for _, d := range []string{"cache", "data", "config", "run", "tmp"} {
		_ = os.MkdirAll(filepath.Join(data, d), 0o700)
	}
	mem, gpu := c.webMemory(), c.webGPU()
	args := []string{"--memory-limit", fmt.Sprint(mem * 6 / 10 >> 20), "--cache-model", c.webCache()}
	if gpu {
		args = append(args, "--gpu")
	}
	if rules, ok := c.webBlockRules(a); ok {
		args = append(args, "--block", rules)
	}
	var cmd *exec.Cmd
	if sandboxed {
		if cmd, err = c.webCommand(a, exe, data, args, gpu); err != nil {
			conn.Close()
			return nil, err
		}
	} else {
		cmd = exec.Command(exe, args...)
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "PHONE_LANG=" + tr.Lang(), "TZ=" + c.zoneName(), "HOME=" + data,
		"XDG_CACHE_HOME=" + filepath.Join(data, "cache"), "XDG_DATA_HOME=" + filepath.Join(data, "data"),
		"XDG_CONFIG_HOME=" + filepath.Join(data, "config"), "XDG_RUNTIME_DIR=" + filepath.Join(data, "run"),
		"TMPDIR=" + filepath.Join(data, "tmp")}
	cmd.ExtraFiles = []*os.File{child}
	if lf, err := os.OpenFile(filepath.Join(c.cfg.RunDir, "webview.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = lf, lf
		defer lf.Close()
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	group := webGroup(a)
	var cgfd *os.File
	if c.cg != nil && !c.cg.scopes {
		if cgfd, err = c.cg.prepareLimits(group, limits{mem: mem, pids: webPidsMax}); err == nil {
			cmd.SysProcAttr.UseCgroupFD, cmd.SysProcAttr.CgroupFD = true, int(cgfd.Fd())
		} else {
			c.log.Printf("%s: cgroup: %v", group, err)
		}
	}
	err = StartProcess(cmd)
	if cgfd != nil {
		cgfd.Close()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	if c.cg != nil && c.cg.scopes {
		if err := c.cg.attachLimits(group, cmd.Process.Pid, limits{mem: mem, pids: webPidsMax}); err != nil {
			c.log.Printf("%s: scope: %v", group, err)
		}
	}
	p := &webProc{app: a, cmd: cmd, ctl: conn.(*net.UnixConn)}
	go p.read(c)
	go func() { _ = cmd.Wait() }()
	c.log.Printf("web process for %s: pid %d (memory %d MB%s)", a.M.ID, cmd.Process.Pid, mem>>20, map[bool]string{true: ", GPU"}[gpu])
	return p, nil
}

// webGroup is the cgroup of an app's web process, a sibling of the app's.
func webGroup(a *App) string { return a.M.ID + ".web" }

// webCommand runs phone-webview through the sandbox launcher: its own uid
// (one per host app), fresh namespaces, the host's network permission, and
// with gpu the GPU's render node (never the display device).
func (c *Core) webCommand(a *App, exe, data string, args []string, gpu bool) (*exec.Cmd, error) {
	launcher := filepath.Join(c.cfg.AppBinDir, "phone-sandbox")
	if _, err := os.Stat(launcher); err != nil {
		return nil, err
	}
	p := sandbox.Policy{DataDir: data, Code: []string{"/usr", "/lib", "/lib64", "/bin", "/sbin"},
		ReadOnly: []string{"/etc", "/proc"}, Net: c.netAllowed(a), GPU: gpu}
	attr := &syscall.SysProcAttr{}
	if c.Sandboxed() {
		uid, err := c.appUID(a.M.ID + "/web")
		if err != nil {
			return nil, err
		}
		root := filepath.Join(c.cfg.StateDir, "web")
		if err := os.MkdirAll(data, 0o700); err != nil {
			return nil, err
		}
		for _, d := range []string{c.cfg.StateDir, root} {
			if err := os.Chmod(d, 0o711); err != nil {
				return nil, err
			}
		}
		if err := chownTree(data, int(uid)); err != nil {
			return nil, err
		}
		p.UID, p.Namespaces = uid, true
		attr.Cloneflags = syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS
		if !p.Net {
			attr.Cloneflags |= syscall.CLONE_NEWNET
		}
		if err := c.applyNetPolicy(); err != nil {
			c.log.Printf("traffic: %v", err)
		}
	}
	raw, _ := json.Marshal(p)
	cmd := exec.Command(launcher, append([]string{string(raw), exe}, args...)...)
	cmd.SysProcAttr = attr
	return cmd, nil
}

func chownTree(dir string, uid int) error {
	return filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, uid)
	})
}

func (p *webProc) send(v any) {
	raw, _ := json.Marshal(v)
	_, _ = p.ctl.Write(raw)
}

func (wv *webView) send(m map[string]any) {
	m["view"] = wv.id
	wv.proc.send(m)
}

func (p *webProc) read(c *Core) {
	buf, oob := make([]byte, 1<<16), make([]byte, syscall.CmsgSpace(4))
	for {
		n, oobn, _, _, err := p.ctl.ReadMsgUnix(buf, oob)
		if err != nil || n == 0 {
			c.Post(func() { c.webProcEnded(p) })
			return
		}
		var m webMsg
		if json.Unmarshal(buf[:n], &m) != nil {
			continue
		}
		var file *os.File
		if msgs, err := syscall.ParseSocketControlMessage(oob[:oobn]); err == nil && len(msgs) == 1 {
			if fds, err := syscall.ParseUnixRights(&msgs[0]); err == nil && len(fds) == 1 {
				file = os.NewFile(uintptr(fds[0]), "webview-surface")
			}
		}
		c.Post(func() { c.webMessage(p, m, file) })
	}
}

func (c *Core) webViewOf(p *webProc, id int) *webView {
	for _, wv := range c.webviews {
		if wv.proc == p && wv.id == id {
			return wv
		}
	}
	return nil
}

func (c *Core) webMessage(p *webProc, m webMsg, file *os.File) {
	wv := c.webViewOf(p, m.View)
	if wv == nil {
		if file != nil {
			file.Close()
		}
		return
	}
	switch m.T {
	case "surface":
		if file == nil || m.W != wv.w || m.H != wv.h || wv.file != nil {
			if file != nil {
				file.Close()
			}
			return
		}
		wv.file = file
	case "frame":
		wv.seq = m.Seq
		wv.frames++
	case "state":
		if m.Focus != wv.focus || m.Edit != wv.edit {
			wv.field++
		}
		page := m.URL != wv.url || m.Title != wv.title || m.Loading != wv.loading || m.CanBack != wv.canBack ||
			m.CanForward != wv.canForward || m.Zoom != wv.zoom
		wv.url, wv.title, wv.focus, wv.loading, wv.canBack = m.URL, m.Title, m.Focus, m.Loading, m.CanBack
		wv.canForward, wv.zoom = m.CanForward, m.Zoom
		wv.edit, wv.value = m.Edit, m.Value
		if page {
			c.webEventFor(wv)
		}
	case "call":
		c.webCall(wv, m)
		return
	case "dialog":
		c.webDialog(wv, m)
	case "menu":
		c.webMenu(wv, m)
	case "blocked":
		c.log.Printf("web view %s: not opened: %s", wv.key, m.URI)
	case "found":
		c.webFound(m.Count)
	case "download":
		c.webDownload(wv, m)
	case "choose-file":
		c.webChooseFile(wv, m)
	case "cleared":
		if m.OK {
			c.showToast(tr.T("Website data cleared"))
		}
	case "sites":
		c.webSites(wv, m.Sites)
	case "forgot":
		if m.OK {
			c.log.Printf("web view %s: data of %s deleted", wv.key, m.Name)
			c.showToast(tr.T("Deleted the website data of “%s”", m.Name))
		} else {
			c.showToast(tr.T("Could not delete the website data of “%s”", m.Name))
		}
	}
	c.postFrame()
}

// webProcEnded: the process is gone (it crashed, the memory limit, the app
// ended); its views go, and the ones on screen come back in a new process.
func (c *Core) webProcEnded(p *webProc) {
	if p.ended {
		return
	}
	p.ended = true
	c.log.Printf("web process for %s ended", p.app.M.ID)
	c.dropWebProc(p)
	c.postFrame()
}

func (c *Core) dropWebProc(p *webProc) {
	for key, wv := range c.webviews {
		if wv.proc == p {
			wv.release()
			delete(c.webviews, key)
		}
	}
	if c.webprocs[p.app.M.ID] == p {
		delete(c.webprocs, p.app.M.ID)
	}
	if c.cg != nil {
		c.cg.release(webGroup(p.app))
	}
}

func (c *Core) stopWebProc(p *webProc) {
	p.ended = true
	_ = p.ctl.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	c.dropWebProc(p)
	c.log.Printf("web process for %s stopped", p.app.M.ID)
}

// endWebProc stops the app's web process with the app.
func (c *Core) endWebProc(a *App) {
	if p := c.webprocs[a.M.ID]; p != nil {
		c.stopWebProc(p)
	}
}

// freezeWeb freezes or thaws the app's web process with the app (cgroups
// only; without them its views are just hidden).
func (c *Core) freezeWeb(a *App, on bool) {
	if c.cg != nil && c.webprocs[a.M.ID] != nil {
		_ = c.cg.freeze(webGroup(a), on)
	}
}

func (wv *webView) release() {
	if wv.view != nil {
		wv.view.Close()
	} else if wv.file != nil {
		wv.file.Close()
	}
	wv.view, wv.file = nil, nil
}

func (c *Core) closeWebView(wv *webView) {
	wv.send(map[string]any{"t": "close"})
	wv.release()
	delete(c.webviews, wv.key)
}

// webCall answers the page's phone.call: through the broker as the host
// app, under the page's origin ("Web page <host>" in the device's language),
// so grants and system dialogs are the page's own.
func (c *Core) webCall(wv *webView, m webMsg) {
	reply := func(res ui.Msg, files []*os.File) {
		closeFiles(files)
		out := map[string]any{"t": "reply", "id": m.ID, "ok": res.OK}
		if res.OK {
			out["result"] = json.RawMessage(res.Data)
			if len(res.Data) == 0 {
				out["result"] = nil
			}
		} else {
			out["error"] = res.Err
		}
		wv.send(out)
	}
	origin := webOrigin(wv.url)
	if !webCaps[m.Cap] || origin == "" {
		reply(ui.Msg{Err: fmt.Sprintf("%s is not available to web pages", m.Cap)}, nil)
		return
	}
	c.broker.handleFor(wv.proc.app, ui.Msg{T: ui.MsgReq, Cap: m.Cap, Args: m.Args, Origin: origin}, reply)
}

// webOrigin names a page in grants and system dialogs.
func webOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return tr.T("Web page %s", u.Host)
}

func (wv *webView) who() string {
	return tr.T("“%s” › %s", wv.proc.app.M.Name, firstNonEmpty(webOrigin(wv.url), tr.T("Web page")))
}

// webDialog shows a page's alert or confirm as a system dialog that names
// the page, prompt as a system screen with an input (webPrompt); the page
// waits for the answer. Leaving a page is always allowed.
func (c *Core) webDialog(wv *webView, m webMsg) {
	answer := func(ok bool) { wv.send(map[string]any{"t": "answer", "id": m.ID, "ok": ok}) }
	text := tr.T("%s:\n%s", wv.who(), cv.Truncate(m.Message, 200))
	switch m.Kind {
	case "alert":
		c.pushOverlay(&confirmOverlay{text: text, yes: tr.T("Got it"), no: tr.T("Close"), onYes: func() { answer(true) }, onNo: func() { answer(true) }})
	case "confirm":
		c.pushOverlay(&confirmOverlay{text: text, yes: tr.T("OK"), no: tr.T("Cancel"), onYes: func() { answer(true) }, onNo: func() { answer(false) }})
	case "prompt":
		c.webPrompt(wv, m)
	case "beforeunload":
		answer(true)
	default:
		c.log.Printf("web view %s: %s dialogs are not supported", wv.key, m.Kind)
		answer(false)
	}
}

// webMenu shows a page's list box as a system menu.
func (c *Core) webMenu(wv *webView, m webMsg) {
	choose := func(i int) { wv.send(map[string]any{"t": "choose", "id": m.ID, "index": i}) }
	// nav.js reports a focused list box as "<label>\uff1a<choice>" (a
	// full-width colon).
	group, _, _ := strings.Cut(wv.focus, "\uff1a")
	var es []menuEntry
	for i, it := range m.Items {
		if it.Group {
			group = it.Label
			continue
		}
		if !it.Enabled {
			continue
		}
		i, label := i, it.Label
		if it.Selected {
			label = "● " + label
		}
		es = append(es, menuEntry{group: cv.Truncate(firstNonEmpty(group, tr.T("Choose")), 16), label: label, find: it.Label, run: func() { choose(i) }})
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Choose"), entries: es, onCancel: func() { choose(-1) }})
}

// webSurface is the surface a frame names "web:<key>", for the renderer.
func (c *Core) webSurface(id string) (*os.File, int, int, bool) {
	wv := c.webviews[strings.TrimPrefix(id, "web:")]
	if wv == nil || wv.file == nil {
		return nil, 0, 0, false
	}
	return wv.file, wv.w, wv.h, true
}

// webFrameNode is a web node for the pixel renderer: its page as a media
// surface filling the body.
func (c *Core) webFrameNode(n *ui.Node, v *View, fn *FrameNode) {
	a := c.current()
	key := webKey(a, v, n)
	c.wantWeb[key] = webWant{a, n.Src}
	fn.Kind, fn.Fill, fn.Waiting = ui.KindMedia, true, tr.T("Opening the web page…")
	wv := c.webviews[key]
	if wv == nil || wv.file == nil || wv.frames == 0 {
		if wv == nil && c.webprocs[a.M.ID] == nil && len(c.webStarts[a.M.ID]) >= webRestarts {
			fn.Waiting = tr.T("The web view has quit")
		}
		return
	}
	if c.renderer != nil && c.renderer.Has("shm") {
		c.frameSurfs["web:"+key] = true
		fn.Image = &FrameImage{W: wv.w, H: wv.h, Surface: "web:" + key, Seq: wv.seq}
		return
	}
	// A renderer without shared memory gets the pixels in the frame.
	if wv.view == nil {
		s, err := media.OpenSurface(wv.file)
		if err != nil {
			return
		}
		wv.view = s
	}
	if f := wv.view.Latest(); f != nil {
		fn.Image = &FrameImage{W: f.W, H: f.H, RGB: base64.StdEncoding.EncodeToString(f.Pix)}
	}
}

// webNodes are the web nodes in the screens of running apps.
func (c *Core) webNodes() map[string]bool {
	live := map[string]bool{}
	for _, a := range c.apps {
		if !a.running {
			continue
		}
		for _, v := range a.stack {
			for i := range v.S.Body {
				if v.S.Body[i].Kind == ui.KindWeb {
					live[webKey(a, v, &v.S.Body[i])] = true
				}
			}
		}
	}
	return live
}

// reconcileWeb opens the views the last frame showed, hides those it did
// not, closes those whose screen is gone and stops processes left idle.
func (c *Core) reconcileWeb() {
	for key, w := range c.wantWeb {
		wv := c.webviews[key]
		switch {
		case wv == nil:
			p, err := c.webProcFor(w.app)
			if err != nil {
				c.log.Printf("web view %s: %v", key, err)
				continue
			}
			c.webNextID++
			wv = &webView{proc: p, id: c.webNextID, key: key, src: w.src, loading: true, visible: true}
			var scale float64
			wv.w, wv.h, scale = c.webSize()
			wv.send(map[string]any{"t": "open", "url": w.src, "w": wv.w, "h": wv.h, "scale": scale})
			c.webviews[key] = wv
			c.log.Printf("web view %s: %s", key, w.src)
		case wv.src != w.src:
			wv.src = w.src
			wv.send(map[string]any{"t": "open", "url": w.src})
		}
		if !wv.visible {
			wv.visible = true
			wv.send(map[string]any{"t": "show", "visible": true})
		}
	}
	live := c.webNodes()
	for key, wv := range c.webviews {
		if !live[key] {
			c.closeWebView(wv)
			continue
		}
		if _, on := c.wantWeb[key]; !on && wv.visible {
			wv.visible = false
			wv.send(map[string]any{"t": "show", "visible": false})
		}
	}
	c.reapWebProcs()
}

// reapWebProcs stops the web processes of apps that ended and those
// without views for webIdle.
func (c *Core) reapWebProcs() {
	now := c.now()
	for _, p := range c.webprocs {
		views := false
		for _, wv := range c.webviews {
			views = views || wv.proc == p
		}
		switch {
		case !p.app.running:
			c.stopWebProc(p)
		case views:
			p.idle = time.Time{}
		case p.idle.IsZero():
			p.idle = now
		case now.Sub(p.idle) >= webIdle:
			c.stopWebProc(p)
		}
	}
}

// WebProcsStarted counts the web processes started since boot (selftest).
func (c *Core) WebProcsStarted() int { return c.webStarted }

// WebPainted reports whether every web view on screen shows a painted
// page, not a single colour (a view that fails to render sends blank
// frames). A moved focus is reported after the frame showing it, so after
// a script's wait for the focus line the latest frame has it. The reason
// says why not.
func (c *Core) WebPainted() (bool, string) {
	shown := 0
	for key, wv := range c.webviews {
		if !wv.visible {
			continue
		}
		shown++
		if wv.file == nil || wv.frames == 0 {
			return false, key + " has sent no frame"
		}
		if wv.view == nil {
			s, err := media.OpenSurface(wv.file)
			if err != nil {
				return false, err.Error()
			}
			wv.view = s
		}
		f := wv.view.Latest()
		if f == nil || len(f.Pix) < 3 {
			return false, key + " has no complete frame"
		}
		if !varied(f.Pix) {
			return false, fmt.Sprintf("%s frame %d is a single colour #%02x%02x%02x", key, wv.seq, f.Pix[0], f.Pix[1], f.Pix[2])
		}
	}
	if shown == 0 {
		return false, "no web view is on screen"
	}
	return true, ""
}

func varied(rgb []byte) bool {
	for i := 3; i+2 < len(rgb); i += 3 {
		if rgb[i] != rgb[0] || rgb[i+1] != rgb[1] || rgb[i+2] != rgb[2] {
			return true
		}
	}
	return false
}

// webNodeKey routes keys to the focused web node's page: arrows move between
// links, OK activates, back goes back in the page first; in a text field
// the keys type, through the input method.
func (c *Core) webNodeKey(a *App, v *View, n *ui.Node, k Key) bool {
	wv := c.webviews[webKey(a, v, n)]
	if wv == nil || wv.proc.ended {
		return false
	}
	if wv.edit != "" && c.webEditKey(v, n, wv, k) {
		return true
	}
	switch k.Name {
	case KeyUp, KeyDown, KeyLeft, KeyRight, KeyEnter:
		wv.send(map[string]any{"t": "key", "key": map[string]string{KeyUp: "up", KeyDown: "down", KeyLeft: "left", KeyRight: "right", KeyEnter: "enter"}[k.Name]})
		return true
	case KeyEsc, KeyBackspace:
		if wv.canBack {
			wv.send(map[string]any{"t": "back"})
			return true
		}
	}
	return false
}

// webEditKey types into the page's focused text field. The field's text
// is mirrored in the view's inputs, where the input method composes as in
// the shell's own input nodes; every change is set on the page.
func (c *Core) webEditKey(v *View, n *ui.Node, wv *webView, k Key) bool {
	v.inputs[n.ID] = wv.value
	defer c.webPushValue(v, n)
	if !webDirect[wv.edit] && c.imeKey(k) {
		return true
	}
	switch k.Name {
	case KeyRunes:
		v.inputs[n.ID] += string(k.Runes)
		return true
	case KeyBackspace:
		r := []rune(v.inputs[n.ID])
		if len(r) > 0 {
			v.inputs[n.ID] = string(r[:len(r)-1])
		}
		return true
	case KeyEnter:
		wv.send(map[string]any{"t": "submit"})
		return true
	}
	return false
}

// webPushValue sets the mirrored text on the page if it changed.
func (c *Core) webPushValue(v *View, n *ui.Node) {
	wv := c.webviews[webKey(c.current(), v, n)]
	if wv == nil || wv.edit == "" || v.inputs[n.ID] == wv.value {
		return
	}
	wv.value = v.inputs[n.ID]
	wv.send(map[string]any{"t": "value", "value": wv.value})
}

// webField is the page text field the input method may compose into: the
// focused web node's, unless it takes no input method.
func (c *Core) webField(v *View, n *ui.Node) (*webView, bool) {
	wv := c.webviews[webKey(c.current(), v, n)]
	if wv == nil || wv.edit == "" || webDirect[wv.edit] {
		return nil, false
	}
	return wv, true
}

// drawWeb is a web node in the text renderer: what the page is and where
// the focus is (the page itself needs the pixel renderer).
func (c *Core) drawWeb(a *App, v *View, n *ui.Node, W int, focused bool) *cv.Canvas {
	wv := c.webviews[webKey(a, v, n)]
	title, addr, focus, note, field := "", n.Src, "", "", ""
	switch {
	case wv != nil:
		title, focus = wv.title, wv.focus
		if wv.url != "" {
			addr = wv.url
		}
		if wv.loading || wv.frames == 0 {
			note = tr.T("Loading…")
		}
		if wv.edit != "" {
			val := wv.value
			if wv.edit == "password" {
				val = strings.Repeat("•", len([]rune(val)))
			}
			if c.imeComposing(v, n) {
				val += "[" + c.ime.Preedit() + "]"
			}
			field = tr.T("Input: %s", val+"▏")
		}
	case len(c.webStarts[a.M.ID]) >= webRestarts && c.webprocs[a.M.ID] == nil:
		note = tr.T("The web view has quit")
	case c.HasWebView():
		note = tr.T("Opening…")
	default:
		note = tr.T("This device has no web view (it needs the pixel UI); reading mode works")
	}
	lines := []string{tr.T("Web page · %s", firstNonEmpty(title, tr.T("(untitled)"))), addr}
	if focus != "" {
		lines = append(lines, tr.T("Focus: %s", focus))
	}
	if field != "" {
		lines = append(lines, field)
	}
	if note != "" {
		lines = append(lines, note)
	}
	if focused {
		if field != "" {
			lines = append(lines, tr.T("Type text · OK submit · ↑↓ leave the field"))
		} else {
			lines = append(lines, tr.T("↑↓←→ move between links · OK open · Back go back"))
		}
	}
	b := cv.New(W, len(lines), stBase)
	for i, l := range lines {
		s := st(colFG, colBG)
		if i > 0 {
			s = st(colDim, colBG)
		}
		if i == 0 && focused {
			s = bold(st(colAccent, colBG))
		}
		b.Text(1, i, cv.Truncate(l, W-2), s, W-2)
	}
	return b
}
