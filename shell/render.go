package shell

import (
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"math"
	"path/filepath"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/media"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

var (
	colBG      = cv.RGB{18, 18, 24}
	colFG      = cv.RGB{222, 222, 228}
	colDim     = cv.RGB{130, 132, 145}
	colAccent  = cv.RGB{95, 170, 255}
	colOK      = cv.RGB{100, 205, 125}
	colWarn    = cv.RGB{240, 175, 70}
	colDanger  = cv.RGB{240, 95, 95}
	colFocus   = cv.RGB{44, 62, 96}
	colStatus  = cv.RGB{8, 8, 12}
	colTitle   = cv.RGB{38, 42, 58}
	colSoft    = cv.RGB{28, 29, 38}
	colBubIn   = cv.RGB{52, 54, 64}
	colBubOut  = cv.RGB{38, 92, 58}
	colMenu    = cv.RGB{34, 37, 50}
	colMenuSel = cv.RGB{66, 98, 160}
	colTrusted = cv.RGB{232, 192, 80}
	colTrustBG = cv.RGB{26, 22, 12}

	stBase = cv.Style{FG: colFG, BG: colBG}
)

// textThemes are the text renderer's colours for Settings › Theme (themes).
var textThemes = map[string]struct{ bg, fg, dim, accent, focus, status, title, soft, menu, menuSel cv.RGB }{
	"":      {cv.RGB{18, 18, 24}, cv.RGB{222, 222, 228}, cv.RGB{130, 132, 145}, cv.RGB{95, 170, 255}, cv.RGB{44, 62, 96}, cv.RGB{8, 8, 12}, cv.RGB{38, 42, 58}, cv.RGB{28, 29, 38}, cv.RGB{34, 37, 50}, cv.RGB{66, 98, 160}},
	"light": {cv.RGB{244, 245, 247}, cv.RGB{29, 33, 41}, cv.RGB{107, 114, 128}, cv.RGB{31, 111, 235}, cv.RGB{214, 230, 255}, cv.RGB{228, 230, 235}, cv.RGB{255, 255, 255}, cv.RGB{233, 235, 239}, cv.RGB{255, 255, 255}, cv.RGB{190, 214, 255}},
	"retro": {cv.RGB{180, 198, 156}, cv.RGB{28, 40, 20}, cv.RGB{76, 92, 60}, cv.RGB{36, 56, 24}, cv.RGB{146, 168, 118}, cv.RGB{162, 182, 136}, cv.RGB{168, 187, 142}, cv.RGB{162, 182, 136}, cv.RGB{169, 188, 144}, cv.RGB{138, 158, 112}},
	"amber": {cv.RGB{21, 18, 12}, cv.RGB{243, 230, 200}, cv.RGB{168, 154, 124}, cv.RGB{255, 176, 0}, cv.RGB{62, 48, 18}, cv.RGB{12, 10, 6}, cv.RGB{36, 30, 19}, cv.RGB{28, 24, 16}, cv.RGB{34, 29, 20}, cv.RGB{110, 80, 20}},
}

// applyTheme switches the text renderer's colours (the pixel renderers get
// the theme's name in each frame).
func applyTheme(name string) {
	t, ok := textThemes[name]
	if !ok {
		t = textThemes[""]
	}
	colBG, colFG, colDim, colAccent, colFocus = t.bg, t.fg, t.dim, t.accent, t.focus
	colStatus, colTitle, colSoft, colMenu, colMenuSel = t.status, t.title, t.soft, t.menu, t.menuSel
	stBase = cv.Style{FG: colFG, BG: colBG}
}

func st(fg, bg cv.RGB) cv.Style { return cv.Style{FG: fg, BG: bg} }

func bold(s cv.Style) cv.Style { s.Bold = true; return s }

// Render draws the device screen. It also reconciles media subscriptions:
// only surfaces actually on screen receive frames.
func (c *Core) Render() *cv.Canvas {
	W, H := c.cfg.W, c.cfg.H
	can := cv.New(W, H, stBase)
	c.want = map[string]bool{}
	if p := c.BootProgress(); p >= 0 {
		c.drawBoot(can, p)
		c.reconcileSubs()
		return can
	}
	a := c.current()
	v := a.top()
	title := a.M.Name
	if v != nil {
		title = v.S.Title
	}
	c.drawStatus(can)
	can.Fill(0, 1, W, 1, st(colFG, colTitle))
	can.Text(1, 1, cv.Truncate(title, W-2), bold(st(colFG, colTitle)), W-2)
	// The candidate bar takes rows from the body so the input stays visible.
	imeRows := 0
	if _, n := c.imeTarget(); n != nil && c.ime.Active() {
		imeRows = len(c.ime.Rows())
	}
	if v == nil {
		msg := tr.T("Starting “%s”…", a.M.Name)
		can.Text((W-cv.StringWidth(msg))/2, H/2, msg, st(colDim, colBG), W)
	} else {
		c.drawView(can, v, 2, H-3-imeRows)
	}
	c.drawSoftkeys(can, a, v)
	c.drawIME(can)
	if c.toast != nil {
		w := min(W-2, cv.StringWidth(c.toast.body)+2)
		x, y := (W-w)/2, H-3-imeRows
		can.Fill(x, y, w, 1, st(colBG, colFG))
		can.Text(x+1, y, cv.Truncate(c.toast.body, w-2), st(colBG, colFG), w-2)
	}
	if c.banner != nil {
		can.Fill(0, 1, W, 2, st(colFG, cv.RGB{30, 60, 100}))
		can.Text(1, 1, cv.Truncate(c.banner.title, W-2), bold(st(colFG, cv.RGB{30, 60, 100})), W-2)
		can.Text(1, 2, cv.Truncate(c.banner.body, W-2), st(colFG, cv.RGB{30, 60, 100}), W-2)
	}
	for _, o := range c.overlays {
		o.render(c, can)
	}
	c.reconcileSubs()
	c.reconcileVideos()
	return can
}

func (c *Core) drawStatus(can *cv.Canvas) {
	W := c.cfg.W
	s := st(cv.RGB{170, 172, 182}, colStatus)
	can.Fill(0, 0, W, 1, s)
	can.Text(1, 0, c.clockText(c.now()), s, 5)
	right := c.telRight() + " " + c.batteryText()
	if c.keylock {
		right = "🔒 " + right
	}
	if _, n := c.imeTarget(); n != nil {
		right = c.ime.Indicator() + " " + right
	}
	can.Text(W-1-cv.StringWidth(right), 0, right, s, W)
	if mid, kind := c.statusMid(); mid != "" {
		col := colOK
		if kind == "secure" {
			col = colTrusted
		}
		can.Text(8, 0, mid, bold(st(col, colStatus)), W-20)
	}
}

func clock(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

func (c *Core) drawSoftkeys(can *cv.Canvas, a *App, v *View) {
	W, y := c.cfg.W, c.cfg.H-1
	s := st(cv.RGB{200, 202, 212}, colSoft)
	can.Fill(0, y, W, 1, s)
	if c.keylock && !c.anyCall() {
		can.Text(1, y, tr.T("Tab Unlock"), s, 8)
		return
	}
	can.Text(1, y, tr.T("Tab Options"), s, 8)
	if v == nil {
		return
	}
	center := ""
	if n := v.focused(); n != nil {
		switch {
		case n.Kind == ui.KindInput && c.imeComposing(v, n):
			center = "⏎" + tr.T("Pick")
		case n.Kind == ui.KindInput && n.Submit != "":
			center = "⏎" + submitLabel(v, n)
		case n.PrimaryAction() != nil:
			center = "⏎" + n.PrimaryAction().Label
		case len(n.Actions) > 0:
			center = "⏎" + tr.T("Menu")
		}
	} else if act := pageAction(v, v.S.Center); act != nil {
		center = "⏎" + act.Label
	}
	center = cv.Truncate(center, 12)
	can.Text((W-cv.StringWidth(center))/2, y, center, bold(s), 14)
	if act := pageAction(v, v.S.Right); act != nil {
		r := cv.Truncate("F2"+act.Label, 11)
		can.Text(W-1-cv.StringWidth(r), y, r, s, 12)
	}
}

type block struct {
	idx int
	h   int
	can *cv.Canvas
}

func (c *Core) drawView(can *cv.Canvas, v *View, top, bodyH int) {
	W := c.cfg.W
	var blocks []block
	total := 0
	focusTop, focusBot := -1, -1
	v.geom, v.bodyH = map[int][2]int{}, bodyH
	for i := 0; i < len(v.S.Body); i++ {
		n := &v.S.Body[i]
		if cols := v.S.Columns; cols > 1 && isTile(n) {
			_, end := tileRun(v.S.Body, i)
			for r := i; r < end; r += cols {
				var row []*ui.Node
				for k := r; k < min(end, r+cols); k++ {
					row = append(row, &v.S.Body[k])
				}
				b := c.drawTileRow(row, v.focus, W, cols)
				for k := r; k < min(end, r+cols); k++ {
					v.geom[k] = [2]int{total, b.H}
					if v.S.Body[k].ID == v.focus {
						focusTop, focusBot = total, total+b.H
					}
				}
				blocks = append(blocks, block{idx: r, h: b.H, can: b})
				total += b.H
			}
			i = end - 1
			continue
		}
		focused := n.ID != "" && n.ID == v.focus && n.Focusable()
		b := c.drawNode(n, v, W, focused)
		if b == nil {
			continue
		}
		if focused {
			focusTop, focusBot = total, total+b.H
		}
		v.geom[i] = [2]int{total, b.H}
		blocks = append(blocks, block{idx: i, h: b.H, can: b})
		total += b.H
	}
	maxScroll := max(0, total-bodyH)
	if v.stick {
		v.scroll = maxScroll
	}
	if v.ensure && focusTop >= 0 {
		if focusTop < v.scroll {
			v.scroll = focusTop
		}
		if focusBot > v.scroll+bodyH {
			v.scroll = min(focusTop, focusBot-bodyH)
		}
		v.ensure = false
	}
	v.scroll = max(0, min(v.scroll, maxScroll))
	y := -v.scroll + layoutLead(v.S.Layout, bodyH-total)
	for _, b := range blocks {
		if y+b.h > 0 && y < bodyH {
			sub := b.can
			clipTop := max(0, -y)
			rows := min(b.h-clipTop, bodyH-max(y, 0))
			for r := 0; r < rows; r++ {
				row := cv.New(W, 1, stBase)
				row.Blit(0, -(clipTop + r), sub)
				can.Blit(0, top+max(y, 0)+r, row)
			}
		}
		y += b.h
	}
	if total > bodyH {
		pos := int(float64(v.scroll) / float64(maxScroll) * float64(bodyH-1))
		for r := 0; r < bodyH; r++ {
			ch, col := "│", colTitle
			if r == pos {
				ch, col = "┃", colDim
			}
			can.Text(W-1, top+r, ch, st(col, colBG), 1)
		}
	}
}

func styleFor(name string) cv.Style {
	switch name {
	case ui.StyleDim:
		return st(colDim, colBG)
	case ui.StyleAccent:
		return st(colAccent, colBG)
	case ui.StyleTitle, ui.StyleBig, ui.StyleDisplay:
		return bold(stBase)
	case ui.StyleOK:
		return st(colOK, colBG)
	case ui.StyleWarn:
		return st(colWarn, colBG)
	}
	return stBase
}

func (c *Core) drawNode(n *ui.Node, v *View, W int, focused bool) *cv.Canvas {
	inner := W - 2
	switch n.Kind {
	case ui.KindSection:
		b := cv.New(W, 1, stBase)
		t := "─ " + n.Text + " "
		b.Text(1, 0, t+strings.Repeat("─", max(0, inner-cv.StringWidth(t))), st(colDim, colBG), inner)
		return b
	case ui.KindText:
		lines := cv.Wrap(n.Text, inner)
		b := cv.New(W, len(lines), stBase)
		s := styleFor(n.Style)
		for i, l := range lines {
			x := 1
			if n.Style == ui.StyleBig || n.Style == ui.StyleDisplay {
				x = (W - cv.StringWidth(l)) / 2
			}
			b.Text(x, i, l, s, inner)
		}
		return b
	case ui.KindItem:
		if n.Style == ui.StyleBubbleIn || n.Style == ui.StyleBubbleOut {
			return c.drawBubble(n, W, focused)
		}
		return c.drawItem(n, W, focused)
	case ui.KindInput:
		b := cv.New(W, 2, stBase)
		b.Text(0, 0, strings.Repeat("─", W), st(colTitle, colBG), W)
		bg := colBG
		if focused {
			bg = colFocus
		}
		b.Fill(0, 1, W, 1, st(colFG, bg))
		val := v.inputs[n.ID]
		if n.Secret {
			val = strings.Repeat("•", len([]rune(val)))
		}
		b.Text(1, 1, "›", st(colAccent, bg), 1)
		pre := ""
		if focused && !n.Secret && c.imeComposing(v, n) {
			pre = c.ime.Preedit()
		}
		if val == "" && !focused {
			b.Text(3, 1, cv.Truncate(n.Placeholder, inner-2), st(colDim, bg), inner-2)
		} else {
			shown := val
			for shown != "" && cv.StringWidth(shown)+cv.StringWidth(pre) > inner-3 {
				shown = string([]rune(shown)[1:])
			}
			used := b.Text(3, 1, shown, st(colFG, bg), inner-2)
			if pre != "" {
				ps := st(colAccent, bg)
				ps.Under = true
				used += b.Text(3+used, 1, pre, ps, inner-2-used)
			}
			if focused {
				b.Text(3+used, 1, "▏", st(colAccent, bg), 1)
				if val == "" && pre == "" {
					b.Text(4, 1, cv.Truncate(n.Placeholder, inner-3), st(colDim, bg), inner-3)
				}
			}
		}
		return b
	case ui.KindButton:
		b := cv.New(W, 1, stBase)
		label := "[ " + n.Text + " ]"
		s := st(colAccent, colBG)
		if focused {
			s = bold(st(colBG, colAccent))
		}
		b.Text((W-cv.StringWidth(label))/2, 0, label, s, W)
		return b
	case ui.KindImage:
		return c.drawImage(n, W, focused)
	case ui.KindCanvas:
		if n.Canvas == nil {
			return nil
		}
		return c.drawCanvasNode(n, W, focused)
	case ui.KindMedia:
		return c.drawMedia(n, W, focused)
	case ui.KindWeb:
		return c.drawWeb(c.current(), v, n, W, focused)
	case ui.KindDoc:
		return c.drawDoc(n, W, focused)
	case ui.KindProgress:
		b := cv.New(W, 1, stBase)
		x := 1
		if n.Text != "" {
			x += b.Text(x, 0, n.Text+" ", st(colDim, colBG), inner/2)
		}
		note := ""
		if n.Trailing != "" {
			note = " " + n.Trailing
		}
		bar := max(1, W-1-x-cv.StringWidth(note))
		done := int(math.Round(clampFrac(n.Fraction) * float64(bar)))
		b.Text(x, 0, strings.Repeat("━", done), st(colAccent, colBG), done)
		b.Text(x+done, 0, strings.Repeat("─", bar-done), st(colDim, colBG), bar-done)
		b.Text(x+bar, 0, note, st(colDim, colBG), W-1-x-bar)
		return b
	}
	return nil
}

func (c *Core) drawItem(n *ui.Node, W int, focused bool) *cv.Canvas {
	inner := W - 3
	h := 1
	if n.Subtitle != "" || n.Badge != "" {
		h = 2
	}
	imgRows := 0
	if n.Src != "" {
		imgRows = max(2, n.Rows)
	}
	b := cv.New(W, h+imgRows, stBase)
	bg := colBG
	if focused {
		bg = colFocus
		b.Fill(0, 0, W, h+imgRows, st(colFG, bg))
		for r := 0; r < h+imgRows; r++ {
			b.Text(0, r, "▌", st(colAccent, bg), 1)
		}
	}
	x := 2
	if n.Icon != "" {
		x += b.Text(x, 0, n.Icon, bold(st(colAccent, bg)), 2) + 1
	}
	trail := n.Trailing
	tw := cv.StringWidth(trail)
	b.Text(x, 0, cv.Truncate(n.Title, inner-x-tw), bold(st(colFG, bg)), inner-x-tw)
	if trail != "" {
		b.Text(W-1-tw, 0, trail, st(colDim, bg), tw)
	}
	if h == 2 {
		bw := 0
		if n.Badge != "" {
			badge := " " + n.Badge + " "
			bw = cv.StringWidth(badge)
			b.Text(W-1-bw, 1, badge, bold(st(colFG, colDanger)), bw)
		}
		b.Text(x, 1, cv.Truncate(n.Subtitle, inner-x-bw), st(colDim, bg), inner-x-bw)
	}
	if imgRows > 0 {
		img := c.imageCanvas(n.Src, min(W-x-2, imgRows*4), imgRows, n.Secure)
		b.Blit(x, h, img)
	}
	return b
}

func (c *Core) drawBubble(n *ui.Node, W int, focused bool) *cv.Canvas {
	out := n.Style == ui.StyleBubbleOut
	maxW := W * 3 / 4
	lines := cv.Wrap(n.Title, maxW-2)
	bw := 0
	for _, l := range lines {
		bw = max(bw, cv.StringWidth(l))
	}
	imgRows := 0
	if n.Src != "" {
		imgRows = max(2, n.Rows)
		bw = max(bw, min(maxW-2, imgRows*4))
	}
	bw += 2
	h := 1 + len(lines) + imgRows
	b := cv.New(W, h+1, stBase)
	bg := colBubIn
	if out {
		bg = colBubOut
	}
	if focused {
		bg = cv.RGB{bg.R + 30, bg.G + 30, bg.B + 40}
	}
	x := 2
	if out {
		x = W - 2 - bw
	}
	meta := n.Subtitle
	mx := x
	if out {
		mx = W - 2 - cv.StringWidth(meta)
	}
	b.Text(mx, 0, meta, st(colDim, colBG), W-2)
	b.Fill(x, 1, bw, len(lines)+imgRows, st(colFG, bg))
	for i, l := range lines {
		b.Text(x+1, 1+i, l, st(colFG, bg), bw-2)
	}
	if imgRows > 0 {
		b.Blit(x+1, 1+len(lines), c.imageCanvas(n.Src, bw-2, imgRows, n.Secure))
	}
	if focused {
		mark := 0
		if out {
			mark = W - 1
		}
		for r := 1; r < h; r++ {
			b.Text(mark, r, "▌", st(colAccent, colBG), 1)
		}
	}
	return b
}

func (c *Core) drawImage(n *ui.Node, W int, focused bool) *cv.Canvas {
	rows := n.Rows
	if strings.HasPrefix(n.Src, "qr:") {
		rows = 0
	}
	img := c.imageCanvas(n.Src, W-4, rows, n.Secure)
	extra := 0
	if n.Label != "" {
		extra = 1
	}
	b := cv.New(W, img.H+extra, stBase)
	b.Blit((W-img.W)/2, 0, img)
	if n.Label != "" {
		b.Text((W-cv.StringWidth(n.Label))/2, img.H, n.Label, st(colDim, colBG), W)
	}
	if focused {
		for r := 0; r < img.H; r++ {
			b.Text(0, r, "▌", st(colAccent, colBG), 1)
		}
	}
	return b
}

// imageCanvas renders system-decoded images: "qr:<text>" and "gen:<seed>"
// (procedural pictures standing in for photos).
func (c *Core) imageCanvas(src string, maxW, rows int, secure bool) *cv.Canvas {
	switch {
	case strings.HasPrefix(src, "qr:"):
		q, err := qrcode.New(strings.TrimPrefix(src, "qr:"), qrcode.Low)
		if err != nil {
			return cv.New(1, 1, stBase)
		}
		q.DisableBorder = true
		bm := q.Bitmap()
		n := len(bm) + 4
		h := (n + 1) / 2
		b := cv.New(n, h, stBase)
		b.Pixels(0, 0, n, h, func(i, j int) cv.RGB {
			x, y := i-2, j-2
			if x >= 0 && y >= 0 && x < len(bm) && y < len(bm) && bm[y][x] {
				return cv.RGB{0, 0, 0}
			}
			return cv.RGB{255, 255, 255}
		}, secure)
		return b
	case strings.HasPrefix(src, ui.MediaPrefix):
		rows = max(2, rows)
		w := max(4, min(maxW, rows*3))
		f := c.photoImage(src, w, 2*rows, secure)
		if f == nil {
			break
		}
		pix, _ := base64.StdEncoding.DecodeString(f.RGB)
		b := cv.New(f.W, (f.H+1)/2, stBase)
		b.Pixels(0, 0, f.W, (f.H+1)/2, func(i, j int) cv.RGB {
			k := (min(j, f.H-1)*f.W + i) * 3
			return cv.RGB{R: pix[k], G: pix[k+1], B: pix[k+2]}
		}, secure)
		return b
	case strings.HasPrefix(src, "gen:"):
		h := fnv.New32a()
		h.Write([]byte(src))
		seed := float64(h.Sum32()%1000) / 1000
		w := max(4, maxW)
		rows = max(2, rows)
		b := cv.New(w, rows, stBase)
		b.Pixels(0, 0, w, rows, func(i, j int) cv.RGB {
			u, vv := float64(i)/float64(w), float64(j)/float64(2*rows)
			sky := vv < 0.55+0.1*math.Sin(u*6+seed*9)
			if sky {
				return cv.RGB{uint8(80 + 100*seed), uint8(140 + 60*vv), 230}
			}
			sun := math.Hypot(u-0.7-0.2*seed, vv-0.3) < 0.12
			if sun {
				return cv.RGB{250, 220, 90}
			}
			return cv.RGB{uint8(40 + 80*seed), uint8(120 + 60*u), uint8(60 + 40*vv)}
		}, secure)
		return b
	}
	b := cv.New(max(1, maxW), max(1, rows), st(colDim, colBG))
	b.Text(0, 0, tr.T("[Image]"), st(colDim, colBG), 4)
	return b
}

func (c *Core) drawMedia(n *ui.Node, W int, focused bool) *cv.Canvas {
	rows := max(4, n.Rows)
	w := W - 2
	b := cv.New(W, rows, stBase)
	a := c.current()
	stream, pip := n.Stream, n.Pip
	if !c.streamShown(a, stream) {
		stream = ""
	}
	if !c.streamShown(a, pip) {
		pip = ""
	}
	c.blitStream(b, 1, 0, w, rows, stream, n.Secure, tr.T("Connecting…"))
	if pip != "" {
		pw, ph := max(8, w/3), max(3, rows/3)
		c.blitStream(b, 1+w-pw, rows-ph, pw, ph, pip, n.Secure, "…")
	}
	if n.Label != "" {
		lbl := " " + n.Label + " "
		b.Text(2, 0, lbl, bold(st(colFG, cv.RGB{0, 0, 0})), w-2)
	}
	if focused {
		for r := 0; r < rows; r++ {
			b.Text(0, r, "▌", st(colAccent, colBG), 1)
		}
	}
	return b
}

func (c *Core) blitStream(b *cv.Canvas, x, y, w, rows int, stream string, secure bool, waiting string) {
	key := fmt.Sprintf("%s@%dx%d", stream, w, rows*2)
	c.want[key] = true
	sub := c.subs[key]
	var f *media.Frame
	if sub != nil {
		f, _ = sub.Latest()
	}
	if f == nil {
		b.Fill(x, y, w, rows, st(colDim, cv.RGB{10, 10, 14}))
		b.Text(x+max(0, (w-cv.StringWidth(waiting))/2), y+rows/2, waiting, st(colDim, cv.RGB{10, 10, 14}), w)
		return
	}
	b.Pixels(x, y, w, rows, func(i, j int) cv.RGB {
		r, g, bb := f.At(i*f.W/w, j*f.H/(rows*2))
		return cv.RGB{r, g, bb}
	}, secure)
}

func (c *Core) reconcileSubs() {
	sock := filepath.Join(c.cfg.RunDir, svc.MediadFramesSock)
	want := map[string]bool{}
	for _, m := range []map[string]bool{c.want, c.wantGUI} {
		for k := range m {
			want[k] = true
		}
	}
	for key := range want {
		if sk, ok := strings.CutPrefix(key, "surf:"); ok {
			if s, ok := c.surfs[sk]; ok && !s.Ended() {
				continue
			}
			at := strings.LastIndex(sk, "@")
			var w, h int
			fmt.Sscanf(sk[at+1:], "%dx%d", &w, &h)
			s, err := media.SubscribeSurface(sock, sk[:at], w, h, func() { c.postFrame() })
			if err != nil {
				c.log.Printf("surface %s: %v", sk, err)
				continue
			}
			c.surfs[sk] = s
			continue
		}
		if s, ok := c.subs[key]; ok && !s.Ended() {
			continue
		}
		var stream string
		var w, h int
		at := strings.LastIndex(key, "@")
		stream = key[:at]
		fmt.Sscanf(key[at+1:], "%dx%d", &w, &h)
		s, err := media.Subscribe(sock, stream, w, h, func() {
			c.postFrame()
		})
		if err != nil {
			c.log.Printf("subscribe %s: %v", key, err)
			continue
		}
		c.subs[key] = s
	}
	for key, s := range c.subs {
		if !want[key] {
			_, n := s.Latest()
			c.Frames += n
			s.Close()
			delete(c.subs, key)
		}
	}
	for key, s := range c.surfs {
		if !want["surf:"+key] {
			_, n := s.Seq()
			c.Frames += n
			s.Close()
			delete(c.surfs, key)
		}
	}
}

// FramesSeen counts frames delivered to surfaces, including live ones.
func (c *Core) FramesSeen() int {
	n := c.Frames
	for _, s := range c.subs {
		_, k := s.Latest()
		n += k
	}
	for _, s := range c.surfs {
		_, k := s.Seq()
		n += k
	}
	return n
}

// layoutLead is how far down content with that much room to spare starts.
func layoutLead(layout string, spare int) int {
	if spare <= 0 {
		return 0
	}
	switch layout {
	case ui.LayoutCenter:
		return spare * 55 / 100
	case ui.LayoutBottom:
		return spare
	}
	return 0
}
