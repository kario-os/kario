package shell

import (
	"strings"

	cv "github.com/kario-os/kario/canvas"
)

// A device that pairs by its user typing a code (a Bluetooth keyboard,
// IO capability KeyboardOnly: BlueZ's DisplayPasskey; one older than
// Bluetooth 2.1 gets a PIN the same way, DisplayPinCode) has the phone
// show the code: a system screen over the pairing page names the device
// and has the code to type on that keyboard, then Enter, in large digits
// where the renderer draws them so ("digits"), a line of the dialog
// otherwise. A keyboard that says how far its user got (BlueZ calls
// again with entered) has the digits typed marked off; one that never
// says has none marked. The screen goes when the pairing ends (BlueZ's
// Pair answers: paired or failed), when BlueZ cancels or lets the agent
// go, and at Back or Esc, which cancels the pairing (Device1's
// CancelPairing). Screenshots mask it as they do the code to compare.

type pairCodeOverlay struct {
	name, code string
	// entered: the digits typed on the device, as it last said; counts:
	// it has said so at least once.
	entered int
	counts  bool
	gone    <-chan struct{}
	back    func()
}

// showPairCode puts up, from any goroutine, the code to type on the
// device named name, entered digits of it typed there; called again for
// the same pairing (gone) it follows them. It goes when gone closes; back
// runs, off the loop, when the user closes it.
func (c *Core) showPairCode(name, code string, entered int, gone <-chan struct{}, back func()) {
	c.Post(func() {
		select {
		case <-gone:
			return
		default:
		}
		o := c.pairCodeUp()
		if o != nil && o.gone == gone && o.code == peerText(code, btCodeMax) {
			o.mark(entered)
			return
		}
		if o != nil {
			c.popOverlay(o)
		}
		o = &pairCodeOverlay{name: peerText(name, peerNameMax), code: peerText(code, btCodeMax), gone: gone, back: back}
		o.mark(entered)
		c.pushOverlay(o)
		go func() {
			<-gone
			c.Post(func() { c.popOverlay(o) })
		}()
	})
}

// hidePairCode takes the code to type away, from any goroutine.
func (c *Core) hidePairCode() {
	c.Post(func() {
		if o := c.pairCodeUp(); o != nil {
			c.popOverlay(o)
		}
	})
}

// pairCodeUp is the code to type, wherever it is among the overlays.
func (c *Core) pairCodeUp() *pairCodeOverlay {
	for _, o := range c.overlays {
		if p, ok := o.(*pairCodeOverlay); ok {
			return p
		}
	}
	return nil
}

func (o *pairCodeOverlay) mark(entered int) {
	o.counts = o.counts || entered > 0
	o.entered = min(max(entered, 0), len(o.code))
}

func (o *pairCodeOverlay) key(c *Core, k Key) {
	if k.Name == KeyEsc {
		c.popOverlay(o)
		go o.back()
	}
}

func (o *pairCodeOverlay) lines() []string {
	return []string{tr.T("“%s” pairs with a code typed on it.", o.name), tr.T("Type this code on its keys, then press Enter:")}
}

// footer is the key line, Back for the pixel renderer and Esc for the
// text renderer's, and what ends the screen by itself.
func (o *pairCodeOverlay) footer(text bool) []string {
	keys := tr.T("Back cancels the pairing")
	if text {
		keys = tr.T("Esc cancels the pairing")
	}
	return []string{keys, tr.T("This screen closes by itself once the pairing ends.")}
}

// rows are the code's characters spread out and a mark under each, typed
// or not.
func (o *pairCodeOverlay) rows() (code, marks string) {
	var cs, ms []string
	for i, r := range []rune(o.code) {
		m := "○"
		if i < o.entered {
			m = "●"
		}
		cs, ms = append(cs, string(r)), append(ms, m)
	}
	return strings.Join(cs, "  "), strings.Join(ms, "  ")
}

func (o *pairCodeOverlay) frame(c *Core) *FrameOverlay {
	f := &FrameOverlay{Kind: "auth", Title: tr.T("Bluetooth pairing"), Lines: o.lines(), Footer: o.footer(false)}
	if o.counts {
		f.PIN = [2]int{o.entered, len([]rune(o.code))}
	}
	if c.renderer != nil && c.renderer.Has("digits") {
		f.Digits = o.code
	} else {
		f.Lines = append(f.Lines, strings.Join(strings.Split(o.code, ""), " "))
	}
	return f
}

func (o *pairCodeOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	bs := st(colFG, colTrustBG)
	can.Box(0, 1, W, H-1, st(colTrusted, colTrustBG), true)
	can.Text(2, 2, "■ "+tr.T("Bluetooth pairing"), bold(st(colTrusted, colTrustBG)), W-4)
	var foot []string
	for _, l := range o.footer(true) {
		foot = append(foot, cv.Wrap(l, W-6)...)
	}
	end := H - 1 - len(foot)
	for i, l := range foot {
		can.Text(3, end+i, l, st(colDim, colTrustBG), W-6)
	}
	y := 4
	for _, l := range o.lines() {
		for _, w := range cv.Wrap(l, W-6) {
			if y < end {
				can.Text(3, y, w, bs, W-6)
			}
			y++
		}
	}
	y++
	code, marks := o.rows()
	x := max((W-cv.StringWidth(code))/2, 3)
	if y < end {
		can.Text(x, y, code, bold(st(colAccent, colTrustBG)), W-x-2)
	}
	if y++; o.counts && y < end {
		can.Text(x, y, marks, st(colDim, colTrustBG), W-x-2)
	}
}
