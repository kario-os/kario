package shell

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/kario-os/kario/ui"
)

// Touch is a second way to the keys, on a phone with a touchscreen (the
// renderer's feature "touch", 23-renderer-protocol.md §3;
// 10-keyboard-interaction-model.md §2.1). The renderer follows one finger
// and says where it is, in logical pixels of the frame, and what it came
// down on as drawn there; every gesture does what keys do, as the keys
// would do it:
//
//   - a tap on a body node (a list's row, a grid's tile) puts the focus
//     there and presses OK (an input field takes the focus only);
//   - a tap on the soft keys' bar presses the soft key under it: Options,
//     OK, the page's own key;
//   - a tap on the title bar, or a swipe to the right from the left edge,
//     is Back (over a full-screen overlay too: the viewfinder);
//   - a finger dragged up or down on the body scrolls it, the content
//     following the finger, where it is taller than its area (dragScroll),
//     and moves nothing where all of it shows; on the standby screen and
//     over a menu or dialog it moves the focus a step each touchStep it
//     goes (up: the focus goes down);
//   - a swipe to the left or the right is → or ← (the next or previous
//     photo, a setting's value; ← goes back on a plain list);
//   - with the focus on a text field, a finger dragged sideways moves its
//     caret with it, a character each touchCaret it goes (to the left:
//     the caret goes left), as the keys' ← → do;
//   - a tap on a progress bar, or a swipe along it, is the digit for the
//     tenth of it where the finger is, where the page binds the digits (a
//     player's 0–9, docs/10 §2): it goes there;
//   - a long press on a body node puts the focus there and opens Options
//     (the node's actions first: the menu key, docs/10 §1);
//   - over a menu or a dialog a tap on an entry, a choice or a tile picks
//     it, with the arrows and OK as the keys would (overlayTouch), and a
//     tap around it is Back, which closes it.
//
// A touch never lights the screen, and the locked keypad takes none but
// over a dialog (an incoming call): in a pocket nothing happens; one from
// a touchscreen plugged in while locked is taken nowhere (touchFrom). In a
// call one counts only where it cannot be a cheek's (calltouch.go): on
// a call ringing in, with the sound off the earpiece, or once a volume
// key or a finger held on the screen woke touch. The keys a touch presses
// carry the time it came down (Key.At), so a dialog that rose under the
// finger takes no answer from it (overlayTakes). Each gesture goes into
// the log with what it did.

// touchState is the finger followed, from its coming down.
type touchState struct {
	// n numbers the touches: a long press's timer is for one of them.
	n    int
	down bool
	// dev: the touchscreen it is on, as the renderer numbers its input
	// devices (0: one that numbers none).
	dev int
	// at: when it came down, where, and on what; top: the overlay on top
	// then (nil: none).
	at     time.Time
	x0, y0 float64
	hit    TouchHit
	top    overlay
	// moved: past touchSlop, no tap; vertical: a drag up or down; caret:
	// a drag sideways in a text field; steps: the focus's or the caret's
	// steps it took (down and right positive).
	moved, vertical, caret bool
	steps                  int
	// scroll: the body under a drag, as laid out at the coming down (nil:
	// the renderer said no layout of it; the drag steps the focus).
	scroll *touchScroll
	// held: the long press went; what the finger does after it is nothing.
	held bool
	// ear: in a call it came down where a cheek may (touchAtEar): it
	// presses nothing, held still it may wake touch (callHeldUp).
	ear bool
}

// touchScroll is a body a drag scrolls: from the offset it showed at the
// finger's coming down, by as much as the finger goes, to at most max
// (0: all of it shows, the drag moves nothing).
type touchScroll struct {
	v         *View
	l         *Layout
	from, max float64
}

// TouchHit is what a touch came down on, as the renderer drew it: Area is
// soft (Soft: 0, 1, 2 from the left), status, title, ime (Row), body (the
// frame for Screen; Node and in a grid Tile, or between nodes), overlay
// (Kind; what it picks: Item, Choice or Tile, or elsewhere on it), outside
// (the screen around a menu or dialog of Kind), idle or boot.
type TouchHit struct {
	Area   string `json:"area"`
	Screen string `json:"screen,omitempty"`
	Node   *int   `json:"node,omitempty"`
	Tile   *int   `json:"tile,omitempty"`
	Soft   int    `json:"soft,omitempty"`
	Row    int    `json:"row,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Item   *int   `json:"item,omitempty"`
	Choice *int   `json:"choice,omitempty"`
	// Key: the on-screen keypad's key (area "keypad"); "" between keys.
	Key string `json:"key,omitempty"`
}

// The gestures' distances, in logical pixels (0.28 mm on the OnePlus 6T,
// 0.18 mm on the 2.8" target).
const (
	// touchSlop: a finger that moves no further is still a tap.
	touchSlop = 8.0
	// touchStep: how far a finger is dragged for each step of the focus,
	// about a list's row.
	touchStep = 24.0
	// touchEdge: a swipe to the right starting this close to the left edge
	// is Back. A finger coming in from the side is first felt with the
	// middle of its tip a few millimetres in (5.6 mm here on the 6T).
	touchEdge = 20.0
	// touchSwipe: how far a finger goes sideways for a swipe, further than
	// up or down; the drag was sideways from the start (past touchSlop).
	// A thumb's swipe is an arc: no straighter than that is asked.
	touchSwipe = 24.0
	// touchCaret: how far a finger is dragged sideways in a text field for
	// each character the caret moves, about a character of the body's text.
	touchCaret = 12.0
)

// softKeys are the soft keys' bar, left to right.
var softKeys = [3]string{KeyTab, KeyEnter, KeyF2}

// Touch takes what the renderer says the finger it follows did: "down",
// "move", "up" or "cancel", at (x, y); hit is what it came down on.
func (c *Core) Touch(phase string, x, y float64, hit *TouchHit, at time.Time) {
	c.touchFrom(0, phase, x, y, hit, at)
}

// touchFrom is Touch for a finger on touchscreen dev. A touchscreen
// plugged in while locked touches nothing until the unlock
// (inputlock.go); with two touchscreens a finger coming down on either is
// followed, and the other's meanwhile is left alone.
func (c *Core) touchFrom(dev int, phase string, x, y float64, hit *TouchHit, at time.Time) {
	if c.factory != nil {
		c.factoryTouch(phase, x, y)
		return
	}
	if c.touchLockedOut(dev, phase) {
		return
	}
	t := &c.touch
	if phase != "down" && t.down && dev != t.dev {
		return
	}
	switch phase {
	case "down":
		c.touchDown(x, y, hit, at)
		c.touch.dev = dev
	case "move":
		if t.down {
			c.touchMove(x, y)
		}
	case "up":
		if t.down {
			c.touchMove(x, y)
			c.touchUp(x, y, at)
		}
	default:
		*t = touchState{n: t.n}
	}
}

func (c *Core) topOverlay() overlay {
	if n := len(c.overlays); n > 0 {
		return c.overlays[n-1]
	}
	return nil
}

// touchLong is how long a finger is held for a long press: the long press
// set in Settings › Accessibility, as the keys'.
func (c *Core) touchLong() time.Duration {
	if ms := c.settings.A11y.LongPress; ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return time.Duration(ui.DefaultLongPress) * time.Millisecond
}

func (c *Core) touchDown(x, y float64, hit *TouchHit, at time.Time) {
	n := c.touch.n + 1
	c.touch = touchState{n: n}
	if hit == nil || c.screen.off {
		return
	}
	top := c.topOverlay()
	_, ringing := top.(*incomingOverlay)
	if _, cell := top.(*cellIncomingOverlay); cell {
		ringing = true
	}
	if !ringing && c.touchAtEar() {
		c.touch = touchState{n: n, down: true, at: at, x0: x, y0: y, hit: *hit, top: top, ear: true}
		return
	}
	c.Wake("touch")
	c.lastKey = c.now()
	c.displayInput()
	if top == nil && c.keylock && !c.anyCall() {
		return
	}
	c.callTouched()
	c.touch = touchState{n: n, down: true, at: at, x0: x, y0: y, hit: *hit, top: top}
	if top == nil && hit.Area == "body" {
		c.touch.scroll = c.dragScroll(hit.Screen)
	}
	time.AfterFunc(c.touchLong(), func() { c.Post(func() { c.touchHeld(n) }) })
}

// dragScroll is the body of screen, the page on top, as the renderer last
// laid it out; nil when it said nothing of that frame.
func (c *Core) dragScroll(screen string) *touchScroll {
	v, l := c.current().top(), c.layout
	if v == nil || l == nil || l.Screen != screen || v.frameScreen != screen || len(l.Nodes) != v.frameNodes || l.Content <= 0 {
		return nil
	}
	from := l.Offset
	if v.dragged {
		from = v.dragOff
	}
	return &touchScroll{v: v, l: l, from: from, max: math.Max(0, l.Content-l.Area)}
}

// drags: a finger dragged from there scrolls or moves the focus (on the
// body, the standby screen, a menu or dialog and around it), not from the
// bars.
func (t *touchState) drags() bool {
	switch t.hit.Area {
	case "body", "idle", "overlay", "outside":
		return true
	}
	return false
}

func (c *Core) touchMove(x, y float64) {
	t := &c.touch
	if t.held {
		return
	}
	dx, dy := x-t.x0, y-t.y0
	if !t.moved {
		if math.Hypot(dx, dy) < touchSlop {
			return
		}
		t.moved, t.vertical = true, math.Abs(dy) > math.Abs(dx)
		t.caret = !t.vertical && t.top == nil && t.hit.Area == "body" && t.x0 >= touchEdge && c.caretField()
	}
	if t.caret {
		c.touchSteps(t, int(dx/touchCaret), KeyRight, KeyLeft)
		return
	}
	if t.ear || !t.vertical || !t.drags() {
		return
	}
	if s := t.scroll; s != nil {
		c.dragTo(s, s.from-dy)
		return
	}
	c.touchSteps(t, int(-dy/touchStep), KeyDown, KeyUp)
}

// touchSteps presses fwd or back until t has taken want steps.
func (c *Core) touchSteps(t *touchState, want int, fwd, back string) {
	k := Key{At: t.at}
	for t.steps != want {
		k.Name = fwd
		if t.steps > want {
			k.Name = back
			t.steps--
		} else {
			t.steps++
		}
		c.Key(k)
	}
}

// caretField: the page on top has the focus on a text field, whose caret
// ← → move.
func (c *Core) caretField() bool {
	v := c.current().top()
	if v == nil {
		return false
	}
	n := v.focused()
	return n != nil && n.Kind == ui.KindInput
}

func (c *Core) touchUp(x, y float64, at time.Time) {
	t := c.touch
	c.touch = touchState{n: t.n}
	dx, dy := x-t.x0, y-t.y0
	gesture := fmt.Sprintf("%+.0f × %+.0f px in %d ms from the %s", dx, dy, at.Sub(t.at).Milliseconds(), t.hit.Area)
	switch {
	case t.ear:
		c.callHeldUp(t, at, gesture)
	case t.held:
	case !t.moved && at.Sub(t.at) >= c.touchLong():
		c.touchLongPress(t)
	case !t.moved:
		c.touchTap(t)
	case t.caret:
		c.log.Printf("touch: drag %s: the caret %+d characters", gesture, t.steps)
	case t.vertical:
		switch s := t.scroll; {
		case !t.drags():
			c.log.Printf("touch: drag %s: nothing (not from the body)", gesture)
		case s == nil:
			c.log.Printf("touch: drag %s: %d steps of the focus", gesture, t.steps)
		case s.max <= 0:
			c.log.Printf("touch: drag %s: nothing (all of the page shows)", gesture)
		default:
			c.log.Printf("touch: drag %s: scrolled from %.0f to %.0f of %.0f", gesture, s.from, s.v.dragOff, s.max)
		}
	default:
		what := ""
		switch {
		case t.x0 < touchEdge && dx >= touchSwipe && dx > math.Abs(dy):
			what = "Back"
			c.touchKey(KeyEsc, t)
		case c.touchSeek(t, x):
			what = "a seek along the bar"
		case math.Abs(dx) < touchSwipe || math.Abs(dx) <= math.Abs(dy):
			what = "nothing (too short or not sideways)"
		case !t.drags():
			what = "nothing (not from the body)"
		case dx > 0:
			what = "←"
			c.touchKey(KeyLeft, t)
		default:
			what = "→"
			c.touchKey(KeyRight, t)
		}
		c.log.Printf("touch: swipe %s: %s", gesture, what)
	}
}

// dragTo shows the body s scrolled to off (clamped to it), the focus
// kept on what shows.
func (c *Core) dragTo(s *touchScroll, off float64) {
	v := s.v
	if s.max <= 0 || c.current().top() != v {
		return
	}
	off = math.Max(0, math.Min(off, s.max))
	if v.dragged && v.dragOff == off {
		return
	}
	v.dragged, v.dragOff = true, off
	dragFocus(v, s.l, off)
}

// dragFocus keeps the focus on the body as it shows scrolled to off: once
// its node, or its tile's row in a grid, has gone out of the area, the
// nearest one wholly in it takes the focus, on the side it went out by
// (a tile of its column first). A node taller than the area keeps it while
// some of it shows, its own scroll following (scroll.frac), so the keys go
// on from what shows.
func dragFocus(v *View, l *Layout, off float64) {
	type spot struct {
		id        string
		y, h      float64
		grid, col int
	}
	var spots []spot
	cur := -1
	for i, ids := range v.frameIDs {
		if i >= len(l.Nodes) || i >= len(v.frameBody) || len(ids) == 0 {
			break
		}
		n := l.Nodes[i]
		k := v.frameBody[i]
		if k < 0 || k >= len(v.S.Body) {
			continue
		}
		cols, grid := 1, -1
		if v.S.Columns > 1 && isTile(&v.S.Body[k]) {
			cols, grid = v.S.Columns, i
		}
		rows := (len(ids) + cols - 1) / cols
		for j, id := range ids {
			if b := v.indexOf(id); b < 0 || !v.S.Body[b].Focusable() {
				continue
			}
			if id == v.focus {
				cur = len(spots)
			}
			th := n.H / float64(rows)
			spots = append(spots, spot{id: id, y: n.Y + float64(j/cols)*th, h: th, grid: grid, col: j % cols})
		}
	}
	if cur < 0 {
		return
	}
	s := spots[cur]
	if s.h > l.Area && s.y < off+l.Area && s.y+s.h > off {
		v.pixel, v.pfrac = true, math.Max(0, math.Min(1, (off-s.y)/(s.h-l.Area)))
		return
	}
	shows := func(p spot) bool { return p.y >= off-0.5 && p.y+p.h <= off+l.Area+0.5 }
	if shows(s) {
		return
	}
	step := 1
	if s.y+s.h > off+l.Area {
		step = -1
	}
	pick := -1
	for j := cur + step; j >= 0 && j < len(spots); j += step {
		p := spots[j]
		if !shows(p) {
			if pick >= 0 {
				break
			}
			continue
		}
		if pick < 0 || p.grid == s.grid && p.col == s.col {
			pick = j
		}
		if s.grid < 0 || p.grid != s.grid || p.col == s.col {
			break
		}
	}
	if pick < 0 {
		return
	}
	id := spots[pick].id
	v.focus, v.focusIdx, v.ensure, v.stick, v.pixel = id, v.indexOf(id), true, false, false
}

// undrag: a key goes to the page on top, which shows from then on as the
// keys have it, from where the drag left it (the focus is on what shows).
func (c *Core) undrag() {
	if v := c.current().top(); v != nil {
		v.dragged = false
	}
}

// touchSeek: t came down on a progress bar of the page on top whose page
// binds the digits; at x it is the digit of that tenth of the bar, pressed.
func (c *Core) touchSeek(t touchState, x float64) bool {
	h, v, l := t.hit, c.current().top(), c.layout
	if t.top != nil || c.topOverlay() != nil || h.Area != "body" || h.Node == nil || v == nil || h.Screen != v.frameScreen ||
		l == nil || l.Screen != h.Screen || l.Width <= 0 || *h.Node < 0 || *h.Node >= len(v.frameBody) {
		return false
	}
	if k := v.frameBody[*h.Node]; k < 0 || k >= len(v.S.Body) || v.S.Body[k].Kind != ui.KindProgress {
		return false
	}
	d := int(math.Floor((x - l.Left) / l.Width * 10))
	d = max(0, min(9, d))
	digit := string(rune('0' + d))
	if act, _ := shortcut(v, v.focused(), digit); act == nil {
		return false
	}
	c.Press(Key{Name: KeyRunes, Runes: []rune(digit), At: t.at})
	return true
}

// touchHeld: the long press's time has come for touch n.
func (c *Core) touchHeld(n int) {
	t := &c.touch
	if t.n != n || !t.down || t.moved || t.held {
		return
	}
	t.held = true
	c.touchLongPress(*t)
}

// touchKey presses a key for touch t, as from the keypad (its feedback
// too), at the time the finger came down.
func (c *Core) touchKey(name string, t touchState) {
	c.Press(Key{Name: name, At: t.at})
}

func (c *Core) touchLongPress(t touchState) {
	if t.top != nil || c.topOverlay() != nil {
		return
	}
	switch t.hit.Area {
	case "body":
		c.touchNode(t, KeyTab)
	case "keypad":
		c.keypadPress(t.hit.Key, true, t)
	}
}

func (c *Core) touchTap(t touchState) {
	if top := c.topOverlay(); t.top != nil || top != nil {
		// One that rose or went under the finger is not what it touched.
		if top == t.top {
			c.overlayTouch(top, t)
		}
		return
	}
	switch t.hit.Area {
	case "soft":
		if t.hit.Soft >= 0 && t.hit.Soft < len(softKeys) {
			c.touchKey(softKeys[t.hit.Soft], t)
		}
	case "title":
		c.touchKey(KeyEsc, t)
	case "body":
		if !c.touchSeek(t, t.x0) {
			c.touchNode(t, KeyEnter)
		}
	case "keypad":
		c.keypadPress(t.hit.Key, false, t)
	}
}

// touchNode puts the focus on the body node t came down on, as the frame
// it was drawn from has it, and presses key; OK on an input field only
// focuses it (OK there would move on or send it) and brings back the
// on-screen keypad that Back put away.
func (c *Core) touchNode(t touchState, key string) {
	h := t.hit
	if h.Node == nil {
		return
	}
	tile := 0
	if h.Tile != nil {
		tile = *h.Tile
	}
	n := c.tapFocus(h.Screen, *h.Node, tile)
	if n != nil && n.Kind == ui.KindInput {
		c.keypad.hidden = ""
	}
	if n == nil || key == KeyEnter && n.Kind == ui.KindInput {
		return
	}
	c.touchKey(key, t)
}

// overlayTouch is a tap on the overlay on top, which was on top when it
// came down: on the soft keys' bar the soft key; around a menu or dialog
// Back; on what it picks, that, by the overlay's own keys (any overlay
// whose frame lists items, choices or tiles: no overlay knows of touch).
func (c *Core) overlayTouch(o overlay, t touchState) {
	h := t.hit
	switch h.Area {
	case "soft":
		if h.Soft >= 0 && h.Soft < len(softKeys) {
			c.touchKey(softKeys[h.Soft], t)
		}
	case "outside":
		c.touchKey(KeyEsc, t)
	case "overlay":
		f := o.frame(c)
		if f == nil || f.Kind != h.Kind {
			return
		}
		switch {
		case h.Item != nil:
			sel := func(f *FrameOverlay) int {
				return slices.IndexFunc(f.Items, func(it FrameMenuItem) bool { return it.Sel })
			}
			// A menu's arrows go from entry to entry (its headers left out),
			// the symbols' ← → from one to the next.
			next, prev := KeyDown, KeyUp
			if f.Kind == "symbols" {
				next, prev = KeyRight, KeyLeft
			}
			if i := *h.Item; i >= 0 && i < len(f.Items) && !f.Items[i].Header {
				if m, ok := o.(*menuOverlay); ok {
					m.tapped = true
				}
				c.overlayPick(o, t, i, sel, next, prev)
			}
		case h.Tile != nil:
			if i := *h.Tile; i >= 0 && i < len(f.Tiles) {
				c.overlayPick(o, t, i, func(f *FrameOverlay) int { return f.Choice }, KeyRight, KeyLeft)
			}
		case h.Choice != nil:
			c.overlayChoice(o, t, *h.Choice)
		}
	}
}

// overlayPick moves the overlay's selection (sel of its frame) to i a key
// at a time, next or prev, and chooses it with OK. A key that does not
// move it, or that ends the overlay, stops it: what it lists is not chosen
// that way (the permission dialog's permissions).
func (c *Core) overlayPick(o overlay, t touchState, i int, sel func(*FrameOverlay) int, next, prev string) {
	at := sel(o.frame(c))
	for steps := 0; at >= 0 && at != i && steps < 256; steps++ {
		k := next
		if i < at {
			k = prev
		}
		c.Key(Key{Name: k, At: t.at})
		if c.topOverlay() != o {
			return
		}
		now := sel(o.frame(c))
		if now == at {
			return
		}
		at = now
	}
	if at == i {
		c.touchKey(KeyEnter, t)
	}
}

// overlayChoice is a tap on choice i of a dialog: the one selected is OK's
// and, in Kario's dialogs, the last is Back's (Cancel, Deny, Decline,
// Snooze); another is selected first with ← →, as the permission
// dialog's middle one (Approximate).
func (c *Core) overlayChoice(o overlay, t touchState, i int) {
	f := o.frame(c)
	n := len(f.Choices)
	switch {
	case i < 0 || i >= n:
	case i == f.Choice:
		c.touchKey(KeyEnter, t)
	case i == n-1:
		c.touchKey(KeyEsc, t)
	default:
		c.overlayPick(o, t, i, func(f *FrameOverlay) int { return f.Choice }, KeyRight, KeyLeft)
	}
}
