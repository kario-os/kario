package shell

import (
	"strings"
	"time"
	"unicode"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// Overlays are system UI. Apps can neither draw over them nor read the keys
// they receive; this is what makes permission prompts, payment confirmation
// and the scanner trustworthy.
type overlay interface {
	render(c *Core, can *cv.Canvas)
	frame(c *Core) *FrameOverlay
	key(c *Core, k Key)
}

type menuEntry struct {
	group, label, key string
	// find is the untruncated text the palette matches against; labels are
	// shortened for the screen.
	find string
	// name is the label without its count (system entries), as a screen's
	// own action that does the same is labelled.
	name string
	run  func()
}

type menuOverlay struct {
	title   string
	entries []menuEntry
	sel     int
	palette bool
	query   string
	// onCancel runs when the menu is closed without a choice.
	onCancel func()
}

func (c *Core) openMenu(a *App, v *View, palette bool) {
	var es []menuEntry
	node := v.focused()
	if node != nil {
		for _, act := range node.Actions {
			if act.Hidden {
				continue
			}
			act := act
			es = append(es, menuEntry{group: cv.Truncate(nodeLabel(node), 16), label: act.Label, key: keyLabel(act.Key), find: nodeLabel(node), run: func() { c.invoke(a, v, node, act) }})
		}
	}
	for _, act := range v.S.Actions {
		if act.Hidden {
			continue
		}
		act := act
		es = append(es, menuEntry{group: tr.T("This page"), label: act.Label, key: keyLabel(act.Key), run: func() { c.invoke(a, v, node, act) }})
	}
	// Paste comes first for a focused input, so its key beats a page action
	// on the same key.
	if e, ok := c.pasteEntry(v, node); ok && !a.system {
		es = append([]menuEntry{e}, es...)
	}
	if palette {
		for i := range v.S.Body {
			n := &v.S.Body[i]
			// App tiles on system screens are covered by the Apps entries.
			if a.system && strings.HasPrefix(n.ID, "app:") {
				continue
			}
			if n.Focusable() && n.ID != "" && n.ID != v.focus {
				// By ID: the app may update the page while the palette is open.
				id := n.ID
				es = append(es, menuEntry{group: tr.T("Go to"), label: cv.Truncate(nodeLabel(n), 20), find: nodeLabel(n), run: func() {
					if i := v.indexOf(id); i >= 0 {
						v.focus, v.focusIdx, v.ensure, v.stick = id, i, true, false
					}
				}})
			}
		}
	}
	if palette {
		for _, x := range c.apps {
			x := x
			es = append(es, menuEntry{group: tr.T("Apps"), label: x.M.Name, run: func() {
				c.sys.stack = c.sys.stack[:1]
				c.Launch(x, "")
			}})
		}
		// Commands the apps contribute (R12): open the app at that place.
		for _, x := range c.apps {
			for _, cmd := range x.M.Commands {
				x, cmd := x, cmd
				es = append(es, menuEntry{group: tr.T("Commands"), label: x.M.Name + " · " + cmd.Label, run: func() {
					c.sys.stack = c.sys.stack[:1]
					c.Launch(x, cmd.Arg)
				}})
			}
		}
	}
	// The system entries, less what this screen already offers (the idle
	// screen has its own Menu, Switch apps and Notification center).
	have := map[string]bool{}
	for _, e := range es {
		have[e.label] = true
	}
	for _, e := range c.globalEntries() {
		if !have[firstNonEmpty(e.name, e.label)] {
			es = append(es, e)
		}
	}
	title := tr.T("Options")
	if palette {
		title = tr.T("Commands")
	}
	c.pushOverlay(&menuOverlay{title: title, entries: es, palette: palette})
}

func nodeLabel(n *ui.Node) string {
	for _, s := range []string{n.Title, n.Text, n.Label, n.Placeholder, n.ID} {
		if s != "" {
			return s
		}
	}
	return n.Kind
}

func (c *Core) globalEntries() []menuEntry {
	var es []menuEntry
	if top := c.current().top(); !c.current().system || top == nil || top.S.ID != "sys.idle" {
		es = append(es, menuEntry{group: tr.T("System"), label: tr.T("Back to the standby screen"), key: "F4", run: c.GoHome})
	}
	es = append(es, []menuEntry{
		{group: tr.T("System"), label: tr.T("Menu"), run: func() { c.GoHome(); c.openSystem("sys.menu") }},
		{group: tr.T("System"), label: tr.T("Switch apps"), key: "F3", run: func() { c.openSystem("sys.tasks") }},
		{group: tr.T("System"), label: tr.T("Notification center (%d)", len(c.notifs)), name: tr.T("Notification center"), run: func() { c.openSystem("sys.notifs") }},
	}...)
	if s := c.nowPlaying(); s != nil {
		label := tr.T("Pause “%s”", s.Name)
		if s.State == ui.PlayPaused {
			label = tr.T("Resume “%s”", s.Name)
		}
		es = append(es, menuEntry{group: tr.T("System"), label: label, run: c.toggleMedia})
	}
	for _, ci := range c.calls {
		ci := ci
		if c.fg != ci.app || !ci.onCallScreen(c) {
			es = append([]menuEntry{{group: tr.T("System"), label: tr.T("Back to the call"), key: "R", run: func() {
				c.setForeground(ci.app)
				ci.app.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventCallResume, Value: ci.id})
			}}}, es...)
		}
	}
	return es
}

func (m *menuOverlay) visible() []menuEntry {
	if m.query == "" {
		return m.entries
	}
	var out []menuEntry
	q := strings.ToLower(m.query)
	for _, e := range m.entries {
		if strings.Contains(strings.ToLower(e.label), q) || strings.Contains(strings.ToLower(e.group), q) || strings.Contains(strings.ToLower(e.find), q) {
			out = append(out, e)
		}
	}
	return out
}

func (m *menuOverlay) key(c *Core, k Key) {
	vis := m.visible()
	switch k.Name {
	case KeyEsc:
		c.popOverlay(m)
		if m.onCancel != nil {
			m.onCancel()
		}
	case KeyUp:
		m.sel = max(0, m.sel-1)
	case KeyDown, KeyTab:
		m.sel = min(len(vis)-1, m.sel+1)
	case KeyEnter:
		if m.sel < len(vis) {
			c.popOverlay(m)
			vis[m.sel].run()
		}
	case KeyBackspace:
		if m.palette && m.query != "" {
			r := []rune(m.query)
			m.query, m.sel = string(r[:len(r)-1]), 0
		}
	case KeyRunes:
		if m.palette {
			m.query, m.sel = m.query+string(k.Runes), 0
			return
		}
		for _, e := range vis {
			if e.key != "" && e.key == string(k.Runes) {
				c.popOverlay(m)
				e.run()
				return
			}
		}
	}
}

func (m *menuOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	vis := m.visible()
	type menuRow struct {
		header    bool
		text, key string
		idx       int
	}
	var rows []menuRow
	last := ""
	for i, e := range vis {
		if e.group != last {
			rows = append(rows, menuRow{header: true, text: e.group, idx: -1})
			last = e.group
		}
		rows = append(rows, menuRow{text: e.label, key: e.key, idx: i})
	}
	extra := 2
	if m.palette {
		extra = 3
	}
	maxRows := H - 4 - extra
	start := 0
	for i, r := range rows {
		if r.idx == m.sel && i >= maxRows {
			start = i - maxRows + 1
		}
	}
	shown := rows[start:min(len(rows), start+maxRows)]
	h := len(shown) + extra
	if len(vis) == 0 {
		h++
	}
	y0 := H - 1 - h
	s := st(colFG, colMenu)
	can.Box(1, y0, W-2, h, st(colDim, colMenu), false)
	can.Text(3, y0, " "+m.title+" ", bold(s), W-6)
	y := y0 + 1
	if m.palette {
		can.Text(3, y, "/ "+m.query+"▏", st(colAccent, colMenu), W-6)
		y++
	}
	for _, r := range shown {
		if r.header {
			can.Text(3, y, r.text, st(colDim, colMenu), W-6)
		} else {
			rs := s
			if r.idx == m.sel {
				rs = bold(st(colFG, colMenuSel))
				can.Fill(2, y, W-4, 1, rs)
			}
			can.Text(4, y, cv.Truncate(r.text, W-12), rs, W-12)
			if r.key != "" {
				can.Text(W-4-cv.StringWidth(r.key), y, r.key, st(colDim, rs.BG), 4)
			}
		}
		y++
	}
	if len(vis) == 0 {
		can.Text(4, y, tr.T("(No match)"), st(colDim, colMenu), W-8)
	}
}

type confirmOverlay struct {
	text  string
	onYes func()
	onNo  func()
	// yes and no label the two choices (default Confirm, Cancel).
	yes, no string
}

func (o *confirmOverlay) labels() (string, string) {
	yes, no := o.yes, o.no
	if yes == "" {
		yes = tr.T("Confirm")
	}
	if no == "" {
		no = tr.T("Cancel")
	}
	return yes, no
}

func (o *confirmOverlay) key(c *Core, k Key) {
	switch k.Name {
	case KeyEnter:
		c.popOverlay(o)
		o.onYes()
	case KeyEsc:
		c.popOverlay(o)
		if o.onNo != nil {
			o.onNo()
		}
	}
}

func (o *confirmOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	var lines []string
	for _, part := range strings.Split(o.text, "\n") {
		lines = append(lines, cv.Wrap(part, W-8)...)
	}
	h := len(lines) + 4
	y0 := (H - h) / 2
	can.Box(2, y0, W-4, h, st(colWarn, colMenu), false)
	for i, l := range lines {
		can.Text(4, y0+1+i, l, bold(st(colFG, colMenu)), W-8)
	}
	yes, no := o.labels()
	can.Text(4, y0+h-2, "Enter "+yes+" · Esc "+no, st(colDim, colMenu), W-8)
}

// permOverlay asks for runtime consent; the app name comes from the signed
// manifest, never from the requesting app.
type permOverlay struct {
	app      *App
	origin   string
	purposes map[string]string
	perms    []string
	yes      bool
	decide   func(bool)
}

// label gives the purpose of whoever asks: the app's own from its manifest,
// or, for content it hosts, the one the host passed on for it (never the
// host's own: that is not the mini-program's reason).
func (o *permOverlay) label(p string) string {
	why := o.app.M.Purposes[p]
	if o.origin != "" {
		why = o.purposes[p]
	}
	if why == "" || len([]rune(why)) > ui.MaxPurpose {
		return permLabel(p)
	}
	return tr.T("%s: %s", permLabel(p), why)
}

func (o *permOverlay) key(c *Core, k Key) {
	switch k.Name {
	case KeyLeft, KeyRight, KeyUp, KeyDown, KeyTab:
		o.yes = !o.yes
	case KeyEnter:
		c.popOverlay(o)
		o.decide(o.yes)
	case KeyEsc:
		c.popOverlay(o)
		o.decide(false)
	}
}

func (o *permOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	who := tr.T("“%s” asks to use:", o.app.M.Name)
	extra := 0
	if o.origin != "" {
		who = tr.T("In “%s”: “%s”", o.app.M.Name, o.origin)
		extra = 1
	}
	h := len(o.perms) + 6 + extra
	y0 := (H - h) / 2
	bs := st(colFG, colTrustBG)
	can.Box(2, y0, W-4, h, st(colTrusted, colTrustBG), true)
	can.Text(4, y0+1, tr.T("■ System permission"), bold(st(colTrusted, colTrustBG)), W-8)
	can.Text(4, y0+2, cv.Truncate(who, W-8), bs, W-8)
	if extra == 1 {
		can.Text(4, y0+3, tr.T("Asks to use (granted to “%s”):", cv.Truncate(o.app.M.Name, 8)), bs, W-8)
	}
	for i, p := range o.perms {
		can.Text(6, y0+3+extra+i, "· "+o.label(p), bold(bs), W-10)
	}
	yes, no := "  "+tr.T("Allow")+"  ", "  "+tr.T("Deny")+"  "
	ys, ns := st(colFG, colMenu), st(colFG, colMenu)
	if o.yes {
		ys = bold(st(colBG, colOK))
	} else {
		ns = bold(st(colBG, colDanger))
	}
	y := y0 + h - 2
	can.Text(6, y, yes, ys, 8)
	can.Text(W-14, y, no, ns, 8)
}

// authOverlay is the trusted confirmation UI. The PIN never reaches the app:
// it goes from this overlay straight to the keystore, which signs exactly the
// prompt shown here.
type authOverlay struct {
	app     *App
	origin  string
	prompt  string
	pin     string
	err     string
	busy    bool
	submit  func(pin string)
	cancel  func()
	started time.Time
	// bio: a fingerprint confirms too (biometric.go); bioNote: why it no
	// longer does. done: answered, by the PIN or the finger.
	bio     bool
	bioNote string
	done    bool
}

const pinLen = 6

func (o *authOverlay) key(c *Core, k Key) {
	if o.busy || o.done {
		return
	}
	switch k.Name {
	case KeyEsc:
		c.popOverlay(o)
		o.cancel()
	case KeyBackspace:
		if o.pin != "" {
			o.pin = o.pin[:len(o.pin)-1]
		}
	case KeyRunes:
		for _, r := range k.Runes {
			if unicode.IsDigit(r) && len(o.pin) < pinLen {
				o.pin += string(r)
			}
		}
		if len(o.pin) == pinLen {
			o.busy, o.err = true, ""
			pin := o.pin
			o.pin = ""
			o.submit(pin)
		}
	}
}

func (o *authOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	bs := st(colFG, colTrustBG)
	can.Box(0, 1, W, H-1, st(colTrusted, colTrustBG), true)
	can.Text(2, 2, tr.T("■ System security check"), bold(st(colTrusted, colTrustBG)), W-4)
	can.Text(2, 3, cv.Truncate(tr.T("App: %s (%s)", o.app.M.Name, o.app.M.ID), W-4), st(colDim, colTrustBG), W-4)
	y := 5
	if o.origin != "" {
		can.Text(2, 4, cv.Truncate(tr.T("Source: %s (hosted by this app)", o.origin), W-4), st(colWarn, colTrustBG), W-4)
		y = 6
	}
	for _, l := range cv.Wrap(o.prompt, W-6) {
		can.Text(3, y, l, bold(bs), W-6)
		y++
	}
	if bl := o.bioLines(); len(bl) > 0 {
		y++
		for _, line := range bl {
			for _, l := range cv.Wrap(line, W-6) {
				can.Text(3, y, l, st(colAccent, colTrustBG), W-6)
				y++
			}
		}
	}
	y++
	dots := strings.Repeat("● ", len(o.pin)) + strings.Repeat("○ ", pinLen-len(o.pin))
	can.Text(3, y, tr.T("Payment PIN  %s", dots), bs, W-6)
	y += 2
	if o.busy {
		can.Text(3, y, tr.T("Verifying…"), st(colAccent, colTrustBG), W-6)
	} else if o.err != "" {
		can.Text(3, y, o.err, bold(st(colDanger, colTrustBG)), W-6)
	}
	can.Text(3, H-4, tr.T("Digit keys to enter · Esc cancel"), st(colDim, colTrustBG), W-6)
	for i, l := range cv.Wrap(tr.T("The system draws this screen; apps cannot cover it or read what is typed. What is signed is what is shown above."), W-6) {
		if H-3+i < H-1 {
			can.Text(3, H-3+i, l, st(colDim, colTrustBG), W-6)
		}
	}
}

// scanOverlay is the system scanner: the app receives the decoded text only,
// never camera frames.
type scanOverlay struct {
	app    *App
	origin string
	stream string
	cancel func()
	t0     time.Time
}

func (o *scanOverlay) key(c *Core, k Key) {
	if k.Name == KeyEsc {
		c.popOverlay(o)
		o.cancel()
	}
}

func (o *scanOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	can.Fill(0, 1, W, H-1, stBase)
	can.Fill(0, 1, W, 1, st(colFG, colTitle))
	can.Text(1, 1, tr.T("Scan · System camera"), bold(st(colTrusted, colTitle)), W-2)
	rows := H - 7
	c.blitStream(can, 1, 2, W-2, rows, o.stream, false, tr.T("Opening the camera…"))
	line := 2 + int(time.Since(o.t0).Milliseconds()/120)%rows
	can.Restyle(4, line, W-8, func(s cv.Style) cv.Style { s.FG = colOK; return s })
	can.Text(1, H-5, cv.Truncate(tr.T("Put the QR code in the frame · asked by %s", o.who()), W-2), st(colFG, colBG), W-2)
	can.Text(1, H-4, tr.T("The app gets only what is read, never the picture"), st(colDim, colBG), W-2)
	can.Fill(0, H-1, W, 1, st(colFG, colSoft))
	can.Text(1, H-1, tr.T("Esc cancel"), st(colFG, colSoft), W-2)
}

// who names the app the scan is for, and the page in it that asked.
func (o *scanOverlay) who() string {
	if o.origin != "" {
		return tr.T("“%s” (%s)", o.origin, o.app.M.Name)
	}
	return tr.T("“%s”", o.app.M.Name)
}

// incomingOverlay is the system incoming-call screen (CallKit-like): it works
// over any app, and the app is told only "answer" or "decline".
type incomingOverlay struct {
	app *App
	n   ui.Notification
	at  time.Time
}

func (o *incomingOverlay) key(c *Core, k Key) {
	if k.Name == KeyEnter || k.Name == KeyEsc {
		c.quiet("voip-ring:" + o.app.M.ID)
	}
	switch k.Name {
	case KeyEnter:
		c.popOverlay(o)
		c.setForeground(o.app)
		o.app.send(ui.Msg{T: ui.MsgNotifAction, Value: o.n.ID, Action: "answer"})
	case KeyEsc:
		c.popOverlay(o)
		o.app.send(ui.Msg{T: ui.MsgNotifAction, Value: o.n.ID, Action: "decline"})
	}
}

func (o *incomingOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	bg := cv.RGB{16, 40, 30}
	can.Fill(0, 1, W, H-1, st(colFG, bg))
	center := func(y int, s string, sty cv.Style) {
		can.Text((W-cv.StringWidth(s))/2, y, s, sty, W)
	}
	kind := tr.T("Video call invitation")
	if o.n.Body != "" {
		kind = o.n.Body
	}
	center(4, kind, st(colDim, bg))
	center(7, o.n.Title, bold(st(colFG, bg)))
	center(9, tr.T("From “%s”", o.app.M.Name), st(colDim, bg))
	dots := []string{"●  ○  ○", "○  ●  ○", "○  ○  ●"}
	center(12, dots[int(time.Since(o.at).Milliseconds()/500)%3], st(colOK, bg))
	center(H-4, tr.T("Enter answer        Esc decline"), bold(st(colFG, bg)))
	can.Text(2, H-3, "●", st(colOK, bg), 1)
	can.Text(W-3, H-3, "●", st(colDanger, bg), 1)
}
