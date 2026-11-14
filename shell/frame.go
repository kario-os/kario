package shell

import (
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// The pixel renderer (phone-gui, Rust + Slint) draws the same semantic tree as
// the text renderer. After each change the shell sends it a Frame: what is on
// screen, not how it looks. Layout, fonts and pixels belong to the renderer.
// Scrolling travels as intent (focus, position within the content, position
// within a tall focused block) because every renderer lays out in its own
// units.

type Frame struct {
	T       string        `json:"t"`
	Screen  string        `json:"screen"`
	Status  FrameStatus   `json:"status"`
	Title   string        `json:"title"`
	Loading string        `json:"loading,omitempty"`
	Body    []FrameNode   `json:"body"`
	Focus   int           `json:"focus"`
	Scroll  FrameScroll   `json:"scroll"`
	Soft    [3]string     `json:"soft"`
	IME     []FrameIMERow `json:"ime,omitempty"`
	Toast   string        `json:"toast,omitempty"`
	Banner  []string      `json:"banner,omitempty"`
	Overlay *FrameOverlay `json:"overlay,omitempty"`
	Boot    *FrameBoot    `json:"boot,omitempty"`
	Idle    *FrameIdle    `json:"idle,omitempty"`
	// Layout places content that does not fill the body (ui.Layout*).
	Layout string `json:"layout,omitempty"`
	// Theme is Settings › Theme: "" (dark), "light", "retro", "amber".
	Theme string `json:"theme,omitempty"`
	// Zoom is Settings › Large text: the renderer draws everything this much
	// bigger (fewer logical pixels on the same panel), 0 for the normal size.
	Zoom float64 `json:"zoom,omitempty"`
}

// largeTextZoom is Large text's step: text, rows and bars together, as a
// phone's display size setting does, so the layout keeps its proportions.
const largeTextZoom = 1.2

// FrameIdle is the standby screen; the renderer draws it instead of Body.
type FrameIdle struct {
	Time     string   `json:"time"`
	Date     string   `json:"date"`
	Operator string   `json:"operator"`
	Lines    []string `json:"lines"`
	Hint     string   `json:"hint"`
	// Wallpaper is the standby picture at the screen's size, in a surface
	// of the shell's own (feature "shm").
	Wallpaper *FrameImage `json:"wallpaper,omitempty"`
}

type FrameBoot struct {
	Progress float64 `json:"progress"`
	Brand    string  `json:"brand"`
}

type FrameStatus struct {
	Time     string `json:"time"`
	Mid      string `json:"mid,omitempty"`
	MidKind  string `json:"mid_kind,omitempty"` // "call" | "secure" | "mic"
	IME      string `json:"ime,omitempty"`
	Net      string `json:"net"`
	Signal   int    `json:"signal"` // bars, 0-4
	Battery  int    `json:"battery"`
	Charging bool   `json:"charging,omitempty"`
	Locked   bool   `json:"locked,omitempty"` // keypad lock (a padlock in the status bar)
}

type FrameScroll struct {
	Pos  float64 `json:"pos"`  // 0 top … 1 bottom of the content
	Frac float64 `json:"frac"` // within a focused block taller than the screen, else -1
}

type FrameNode struct {
	Kind        string       `json:"kind"`
	Style       string       `json:"style,omitempty"`
	Text        string       `json:"text,omitempty"`
	Title       string       `json:"title,omitempty"`
	Subtitle    string       `json:"subtitle,omitempty"`
	Trailing    string       `json:"trailing,omitempty"`
	Badge       string       `json:"badge,omitempty"`
	Icon        string       `json:"icon,omitempty"`
	Label       string       `json:"label,omitempty"`
	Placeholder string       `json:"placeholder,omitempty"`
	Value       string       `json:"value,omitempty"`
	Preedit     string       `json:"preedit,omitempty"`
	Secret      bool         `json:"secret,omitempty"`
	Out         bool         `json:"out,omitempty"`
	Focused     bool         `json:"focused,omitempty"`
	Image       *FrameImage  `json:"image,omitempty"`
	Pip         *FrameImage  `json:"pip,omitempty"`
	Waiting     string       `json:"waiting,omitempty"`
	Doc         [][]FrameRun `json:"doc,omitempty"`
	Columns     int          `json:"columns,omitempty"`
	Tiles       []FrameTile  `json:"tiles,omitempty"`
	Sel         int          `json:"sel"` // focused tile, -1 for none
	Fraction    float64      `json:"fraction,omitempty"`
	// Fill: a media node taking the whole body (a web page) instead of a
	// picture's share of it; renderers that do not know it scale it down.
	Fill bool `json:"fill,omitempty"`
	// IconImage is a PNG path for an item's icon (system lists of apps).
	IconImage string `json:"icon_image,omitempty"`
}

type FrameTile struct {
	Icon  string `json:"icon,omitempty"`
	Image string `json:"image,omitempty"` // PNG path, drawn instead of Icon
	// Pixels is an inline image (photo thumbnails), drawn instead of both:
	// the renderer never reads the photo library itself.
	Pixels  *FrameImage `json:"pixels,omitempty"`
	Title   string      `json:"title"`
	Badge   string      `json:"badge,omitempty"`
	Focused bool        `json:"focused,omitempty"`
	// Photo: a photo grid's cell, the picture filling it without a label.
	Photo bool `json:"photo,omitempty"`
}

// FrameImage carries pixels the system decoded or produced itself (QR codes,
// stand-in photos, media frames); apps never send pixels.
type FrameImage struct {
	W   int    `json:"w"`
	H   int    `json:"h"`
	RGB string `json:"rgb,omitempty"` // base64, W*H*3 bytes
	// Surface names shared memory the renderer was handed (feature "shm")
	// holding the pixels; Seq is the frame in it to show.
	Surface   string `json:"surface,omitempty"`
	Seq       uint32 `json:"seq,omitempty"`
	Secure    bool   `json:"secure,omitempty"`
	Pixelated bool   `json:"pixelated,omitempty"`
}

// FrameRun is a stretch of one document line starting at column Col, Width
// cells wide. Box runs hold box-drawing characters and arrows, which the
// renderer draws as lines on the cell grid instead of using font glyphs.
type FrameRun struct {
	Text   string `json:"text"`
	Col    int    `json:"col"`
	Width  int    `json:"width"`
	Box    bool   `json:"box,omitempty"`
	FG     string `json:"fg"`
	BG     string `json:"bg,omitempty"`
	Bold   bool   `json:"bold,omitempty"`
	Italic bool   `json:"italic,omitempty"`
	Under  bool   `json:"under,omitempty"`
}

type FrameIMERow struct {
	Items    []string `json:"items"`
	Sel      int      `json:"sel"`
	Active   bool     `json:"active"`
	Numbered bool     `json:"numbered"`
	More     bool     `json:"more"`
}

type FrameOverlay struct {
	Kind    string          `json:"kind"` // menu | confirm | perm | auth | scan | capture | picker | incoming | alarm
	Title   string          `json:"title,omitempty"`
	Lines   []string        `json:"lines,omitempty"`
	Warn    string          `json:"warn,omitempty"`
	Items   []FrameMenuItem `json:"items,omitempty"`
	Query   *string         `json:"query,omitempty"`
	Choices []string        `json:"choices,omitempty"`
	Choice  int             `json:"choice"`
	PIN     [2]int          `json:"pin"` // entered, length
	Note    string          `json:"note,omitempty"`
	NoteBad bool            `json:"note_bad,omitempty"`
	Footer  []string        `json:"footer,omitempty"`
	Image   *FrameImage     `json:"image,omitempty"`
	Phase   float64         `json:"phase"` // animation position 0…1 (scan line, ringing); -1 for none
	Tiles   []FrameTile     `json:"tiles,omitempty"`
	Columns int             `json:"columns,omitempty"` // tile grid width (picker)
}

type FrameMenuItem struct {
	Header bool   `json:"header,omitempty"`
	Text   string `json:"text"`
	Key    string `json:"key,omitempty"`
	Sel    bool   `json:"sel,omitempty"`
}

// Pixel sizes requested from the media service for the pixel renderer.
const (
	mediaW, mediaH = 192, 144
	pipW, pipH     = 64, 48
)

// Frame describes the current screen for device d. Call it after Render, so
// the text layout it derives scroll intent from is current.
func (c *Core) Frame(d Device) *Frame {
	c.wantGUI, c.frameSurfs, c.wantWeb = map[string]bool{}, map[string]bool{}, map[string]webWant{}
	defer c.reconcileSubs()
	defer c.reconcileWeb()
	defer c.dropThumbs()
	a := c.current()
	v := a.top()
	f := &Frame{T: "frame", Focus: -1, Scroll: FrameScroll{Frac: -1}, Status: c.frameStatus(), Body: []FrameNode{}, Theme: c.settings.Theme}
	if c.settings.LargeText {
		f.Zoom = largeTextZoom
	}
	f.Title = a.M.Name
	f.Soft[0] = tr.T("Options")
	if v == nil {
		f.Loading = tr.T("Starting “%s”…", a.M.Name)
	} else {
		f.Screen = fmt.Sprintf("%s/%s", a.M.ID, v.S.ID)
		f.Title = v.S.Title
		f.Layout = v.S.Layout
		v.frameIDs = v.frameIDs[:0]
		for i := 0; i < len(v.S.Body); i++ {
			n := &v.S.Body[i]
			if v.S.Columns > 1 && isTile(n) {
				_, end := tileRun(v.S.Body, i)
				g := FrameNode{Kind: "grid", Columns: v.S.Columns, Sel: -1}
				for k := i; k < end; k++ {
					if v.S.Body[k].ID == v.focus {
						g.Sel, g.Focused = k-i, true
					}
				}
				for k := i; k < end; k++ {
					t := &v.S.Body[k]
					focused := t.ID == v.focus
					ft := FrameTile{Icon: t.Icon, Image: c.iconImage(a, t), Title: t.Title, Badge: t.Badge, Focused: focused}
					if it := c.media.get(t.Src); t.Src != "" && it != nil && c.mediaAllowed(a, it) {
						// A photo grid: the system draws the photo, the app names it.
						ft.Photo, ft.Title = true, ""
						if thumbShown(k-i, g.Sel, v.S.Columns) {
							ft.Pixels = c.thumbFrame(it, v.S.Columns)
						}
					}
					g.Tiles = append(g.Tiles, ft)
				}
				if g.Focused {
					f.Focus = len(f.Body)
				}
				ids := make([]string, 0, end-i)
				for k := i; k < end; k++ {
					ids = append(ids, v.S.Body[k].ID)
				}
				v.frameIDs = append(v.frameIDs, ids)
				f.Body = append(f.Body, g)
				i = end - 1
				continue
			}
			fn, ok := c.frameNode(n, v, d)
			if !ok {
				continue
			}
			fn.IconImage = c.iconImage(a, n)
			if fn.Focused {
				f.Focus = len(f.Body)
			}
			v.frameIDs = append(v.frameIDs, []string{n.ID})
			f.Body = append(f.Body, fn)
		}
		v.frameScreen, v.frameFocus, v.frameNodes = f.Screen, f.Focus, len(f.Body)
		f.Scroll = scrollIntent(v)
		f.Soft[1], f.Soft[2] = c.softLabels(v)
	}
	if c.keylock && !c.anyCall() {
		f.Soft = [3]string{tr.T("Unlock"), "", ""}
	}
	if _, n := c.imeTarget(); n != nil && c.ime.Active() {
		for _, r := range c.ime.Rows() {
			f.IME = append(f.IME, FrameIMERow{Items: r.Items, Sel: r.Sel, Active: r.Active, Numbered: r.Numbered, More: r.More})
		}
	}
	if c.toast != nil {
		f.Toast = c.toast.body
	}
	if c.banner != nil {
		f.Banner = []string{c.banner.title, c.banner.body}
	}
	if len(c.overlays) > 0 {
		f.Overlay = c.overlays[len(c.overlays)-1].frame(c)
	}
	if p := c.BootProgress(); p >= 0 {
		f.Boot = &FrameBoot{Progress: p, Brand: Brand}
	}
	if a.system && v != nil && v.S.ID == "sys.idle" {
		in := c.idleInfo()
		f.Idle = &FrameIdle{Time: in.Time, Date: in.Date, Operator: in.Operator, Lines: in.Lines, Hint: c.idleHint(),
			Wallpaper: c.wallpaperFrame()}
		if f.Idle.Lines == nil {
			f.Idle.Lines = []string{}
		}
	}
	return f
}

// submitLabel names what OK does in an input: its submit action (Call)
// when the page declares it, else Send.
func submitLabel(v *View, n *ui.Node) string {
	if act := pageAction(v, n.Submit); act != nil {
		return act.Label
	}
	return tr.T("Send")
}

// iconImage resolves the app icon for system screens that list apps
// ("app:<id>") or their notifications ("n:<i>").
func (c *Core) iconImage(a *App, n *ui.Node) string {
	if !a.system {
		return ""
	}
	if id, ok := strings.CutPrefix(n.ID, "app:"); ok {
		if x := c.appByID(id); x != nil {
			return x.M.IconPath()
		}
	}
	if f, ok := strings.CutPrefix(n.ID, "folder:"); ok {
		p := filepath.Join(filepath.Dir(c.cfg.AppsDir), "icons", "folder-"+f+".png")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
	var i int
	if _, err := fmt.Sscanf(n.ID, "n:%d", &i); err == nil && i >= 0 && i < len(c.notifs) {
		return c.notifs[i].app.M.IconPath()
	}
	return ""
}

func (c *Core) frameStatus() FrameStatus {
	pw := c.power()
	s := FrameStatus{Time: c.clockText(c.now()), Net: tr.T("No service"), Battery: pw.Level, Charging: pw.Charging, Locked: c.keylock}
	if c.settings.Airplane {
		s.Net = airplaneMark
	} else if n := c.simNet(); n != "" {
		s.Net = n
	} else if c.tel != nil && c.tel.Registered {
		s.Net, s.Signal = c.dataNet(), c.tel.Signal
	}
	if _, n := c.imeTarget(); n != nil {
		s.IME = c.ime.Indicator()
	}
	s.Mid, s.MidKind = c.statusMid()
	return s
}

// statusMid is the middle of the status bar and its kind: a call in progress
// (VoIP or cellular) when its screen is not showing, the microphone or the
// location in use, or the trusted-UI marker.
func (c *Core) statusMid() (mid, kind string) {
	for _, ci := range c.calls {
		if c.fg != ci.app || !ci.onCallScreen(c) {
			mid, kind = tr.T("● Call %s", clock(time.Since(ci.started))), "call"
		}
	}
	if call := c.activeCell(); call != nil && !c.cellOnScreen() {
		mid, kind = tr.T("● Call %s", clock(time.Since(time.Unix(call.Connected, 0)))), "call"
	}
	if mid == "" && c.recording() {
		mid, kind = tr.T("● Microphone"), "mic"
	}
	if mid == "" && c.locating() {
		mid, kind = tr.T("● Location"), "location"
	}
	for _, o := range c.overlays {
		if _, ok := o.(*authOverlay); ok {
			mid, kind = tr.T("■ Secure mode"), "secure"
		}
	}
	return mid, kind
}

func (c *Core) softLabels(v *View) (center, right string) {
	if n := v.focused(); n != nil {
		switch {
		case n.Kind == ui.KindInput && c.imeComposing(v, n):
			center = tr.T("Pick")
		case n.Kind == ui.KindInput && n.Submit != "":
			center = submitLabel(v, n)
		case n.PrimaryAction() != nil:
			center = n.PrimaryAction().Label
		case len(n.Actions) > 0:
			center = tr.T("Menu")
		}
	} else if act := pageAction(v, v.S.Center); act != nil {
		center = act.Label
	}
	if act := pageAction(v, v.S.Right); act != nil {
		right = act.Label
	}
	return center, right
}

// scrollIntent translates the text layout's scroll state into proportions.
func scrollIntent(v *View) FrameScroll {
	s := FrameScroll{Frac: -1}
	total := 0
	for _, g := range v.geom {
		total = max(total, g[0]+g[1])
	}
	if m := total - v.bodyH; m > 0 && v.bodyH > 0 {
		s.Pos = math.Min(1, float64(v.scroll)/float64(m))
	}
	if g, ok := v.geom[v.indexOf(v.focus)]; ok && v.bodyH > 0 && g[1] > v.bodyH {
		s.Frac = math.Max(0, math.Min(1, float64(v.scroll-g[0])/float64(g[1]-v.bodyH)))
	}
	if v.pixel {
		s.Frac = v.pfrac
	}
	return s
}

// Layout is the geometry a renderer reports for the frame it laid out
// ("layout", 23-renderer-protocol.md §3), in logical pixels.
type Layout struct {
	Screen string       `json:"screen"`
	Area   float64      `json:"area"`
	Offset float64      `json:"offset"`
	Nodes  []LayoutNode `json:"nodes"`
}

type LayoutNode struct {
	Y float64 `json:"y"`
	H float64 `json:"h"`
}

// pixelGeometry is the body area and the focused node's height as the
// renderer laid them out, when the renderer is the screen (kms) and its
// geometry is of the frame the view last sent.
func (c *Core) pixelGeometry(v *View) (area, h float64, ok bool) {
	l := c.layout
	if l == nil || c.renderer == nil || !c.renderer.Has("kms") || !c.renderer.Has("layout") {
		return 0, 0, false
	}
	if l.Screen != v.frameScreen || len(l.Nodes) != v.frameNodes || v.frameFocus < 0 || v.frameFocus >= len(l.Nodes) {
		return 0, 0, false
	}
	return l.Area, l.Nodes[v.frameFocus].H, true
}

// pixelScroll steps through a focused node taller than the area: 80% of
// the area per key, so a line stays on screen from one step to the next.
// false: the node is not tall, or its end is reached (focus moves on).
func pixelScroll(frac, area, h float64, dir int) (float64, bool) {
	if h <= area || area <= 0 {
		return frac, false
	}
	step := 0.8 * area / (h - area)
	switch {
	case dir > 0 && frac < 1:
		return math.Min(1, frac+step), true
	case dir < 0 && frac > 0:
		return math.Max(0, frac-step), true
	}
	return frac, false
}

func (c *Core) frameNode(n *ui.Node, v *View, d Device) (FrameNode, bool) {
	fn := FrameNode{Kind: n.Kind, Style: n.Style, Sel: -1, Focused: n.ID != "" && n.ID == v.focus && n.Focusable()}
	switch n.Kind {
	case ui.KindSection, ui.KindText, ui.KindButton:
		fn.Text = n.Text
	case ui.KindItem:
		fn.Title, fn.Subtitle, fn.Trailing, fn.Badge, fn.Icon = n.Title, n.Subtitle, n.Trailing, n.Badge, n.Icon
		if n.Style == ui.StyleBubbleIn || n.Style == ui.StyleBubbleOut {
			fn.Kind, fn.Out = "bubble", n.Style == ui.StyleBubbleOut
		}
		if n.Src != "" {
			fn.Image = c.frameImage(n.Src, max(2, n.Rows), n.Secure)
		}
	case ui.KindInput:
		fn.Placeholder, fn.Secret = n.Placeholder, n.Secret
		fn.Value = v.inputs[n.ID]
		if n.Secret {
			fn.Value = strings.Repeat("•", len([]rune(fn.Value)))
		} else if fn.Focused && c.imeComposing(v, n) {
			fn.Preedit = c.ime.Preedit()
		}
	case ui.KindImage:
		fn.Label = n.Label
		fn.Image = c.frameImage(n.Src, n.Rows, n.Secure)
	case ui.KindCanvas:
		// Renderers draw it as the picture it is.
		fn.Kind, fn.Label = ui.KindImage, n.Label
		if n.Canvas != nil {
			fn.Image = frameCanvas(n.Canvas)
		}
	case ui.KindMedia:
		fn.Label, fn.Waiting = n.Label, tr.T("Connecting…")
		a := c.current()
		w, h := mediaW, mediaH
		if p := c.players[n.Stream]; p != nil {
			// A video at the size it was decoded for this screen.
			w, h, fn.Waiting = p.w, p.h, tr.T("Opening…")
		}
		if c.streamShown(a, n.Stream) {
			fn.Image = c.frameStream(n.Stream, w, h, n.Secure)
		}
		if n.Pip != "" && c.streamShown(a, n.Pip) {
			fn.Pip = c.frameStream(n.Pip, pipW, pipH, n.Secure)
		}
	case ui.KindDoc:
		fn.Doc = c.frameDoc(n.Text, d.DocCols)
	case ui.KindWeb:
		c.webFrameNode(n, v, &fn)
	case ui.KindProgress:
		fn.Text, fn.Trailing, fn.Fraction = n.Text, n.Trailing, clampFrac(n.Fraction)
	default:
		return fn, false
	}
	return fn, true
}

func clampFrac(f float64) float64 { return math.Max(0, math.Min(1, f)) }

func encodeRGB(w, h int, pix []byte, secure, pixelated bool) *FrameImage {
	return &FrameImage{W: w, H: h, RGB: base64.StdEncoding.EncodeToString(pix), Secure: secure, Pixelated: pixelated}
}

// frameImage mirrors imageCanvas at pixel resolution.
func (c *Core) frameImage(src string, rows int, secure bool) *FrameImage {
	switch {
	case strings.HasPrefix(src, "qr:"):
		q, err := qrcode.New(strings.TrimPrefix(src, "qr:"), qrcode.Low)
		if err != nil {
			return nil
		}
		q.DisableBorder = true
		bm := q.Bitmap()
		n := len(bm) + 4
		pix := make([]byte, n*n*3)
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				v := byte(255)
				if bx, by := x-2, y-2; bx >= 0 && by >= 0 && bx < len(bm) && by < len(bm) && bm[by][bx] {
					v = 0
				}
				i := (y*n + x) * 3
				pix[i], pix[i+1], pix[i+2] = v, v, v
			}
		}
		return encodeRGB(n, n, pix, secure, true)
	case strings.HasPrefix(src, ui.MediaPrefix):
		ht := 180
		if rows > 0 {
			ht = min(240, rows*20)
		}
		return c.photoImage(src, 240, ht, secure)
	case strings.HasPrefix(src, "gen:"):
		h := fnv.New32a()
		h.Write([]byte(src))
		seed := float64(h.Sum32()%1000) / 1000
		w, ht := 160, 80
		if rows > 0 {
			ht = min(160, rows*20)
		}
		pix := make([]byte, w*ht*3)
		for j := 0; j < ht; j++ {
			for i := 0; i < w; i++ {
				u, vv := float64(i)/float64(w), float64(j)/float64(ht)
				var col cv.RGB
				switch {
				case vv < 0.55+0.1*math.Sin(u*6+seed*9):
					col = cv.RGB{R: uint8(80 + 100*seed), G: uint8(140 + 60*vv), B: 230}
				case math.Hypot(u-0.7-0.2*seed, vv-0.3) < 0.12:
					col = cv.RGB{R: 250, G: 220, B: 90}
				default:
					col = cv.RGB{R: uint8(40 + 80*seed), G: uint8(120 + 60*u), B: uint8(60 + 40*vv)}
				}
				k := (j*w + i) * 3
				pix[k], pix[k+1], pix[k+2] = col.R, col.G, col.B
			}
		}
		return encodeRGB(w, ht, pix, secure, false)
	}
	return nil
}

func (c *Core) frameStream(stream string, w, h int, secure bool) *FrameImage {
	key := fmt.Sprintf("%s@%dx%d", stream, w, h)
	if c.renderer != nil && c.renderer.Has("shm") {
		c.wantGUI["surf:"+key] = true
		sub := c.surfs[key]
		if sub == nil {
			return nil
		}
		seq, _ := sub.Seq()
		if seq == 0 {
			return nil
		}
		c.frameSurfs[key] = true
		return &FrameImage{W: w, H: h, Surface: key, Seq: seq, Secure: secure}
	}
	c.wantGUI[key] = true
	sub := c.subs[key]
	if sub == nil {
		return nil
	}
	f, _ := sub.Latest()
	if f == nil {
		return nil
	}
	return encodeRGB(f.W, f.H, f.Pix, secure, false)
}

func hexColor(c cv.RGB) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// frameDoc reuses the document renderer at the pixel renderer's monospace
// width, so line breaking, kinsoku and Mermaid diagrams match exactly.
func (c *Core) frameDoc(text string, cols int) [][]FrameRun {
	key := fmt.Sprintf("px%d\x00%s", cols, text)
	res := c.docCache[key]
	if res == nil {
		res = renderDoc(text, cols)
		c.docCache[key] = res
	}
	out := make([][]FrameRun, 0, len(res.lines))
	for _, line := range res.lines {
		runs := []FrameRun{}
		col := 0
		for _, sr := range line {
			bg := ""
			if sr.st.BG != colBG {
				bg = hexColor(sr.st.BG)
			}
			w := cv.StringWidth(string(sr.r))
			r := FrameRun{Text: string(sr.r), Col: col, Width: w, Box: isBoxRune(sr.r), FG: hexColor(sr.st.FG), BG: bg,
				Bold: sr.st.Bold, Italic: sr.st.Italic, Under: sr.st.Under}
			col += w
			if k := len(runs) - 1; k >= 0 && runs[k].Box == r.Box && runs[k].FG == r.FG && runs[k].BG == r.BG &&
				runs[k].Bold == r.Bold && runs[k].Italic == r.Italic && runs[k].Under == r.Under {
				runs[k].Text += r.Text
				runs[k].Width += w
				continue
			}
			runs = append(runs, r)
		}
		out = append(out, runs)
	}
	return out
}

func isBoxRune(r rune) bool {
	return r >= 0x2500 && r <= 0x257f || strings.ContainsRune("▲△▼▽▶▷►◀◁◄", r)
}
