package shell

import (
	"fmt"
	"strings"

	"github.com/kario-os/kario/audio/tts"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The screen reader (Settings › Accessibility): it says what the focus is
// on, read from the node tree the renderers draw (the role, the text, the
// state, the place in the list: "Bluetooth, on, item 3 of 12"), and what
// changes: a new page or dialog, the focused item's state, a toast, a
// notification banner. The keys move the focus as always; the reader only
// speaks. Reading on demand, it says nothing by itself, and the
// accessibility shortcut held reads the focused item. The lines go to
// phone-audiod's speech service (audio/tts), whose engine is simulated.

type readerState struct {
	// What was read last: the page or dialog, the focused node and what
	// was said of it, the toast and the banner.
	place, focus  string
	item          spokenItem
	toast, banner string
	// spoken numbers the lines this Shell asked for; said holds the
	// latest (tests and the selftest's "spoken").
	spoken int
	said   []spokenLine
}

type spokenLine struct {
	n    int
	text string
}

// spokenItem is a focused thing as the reader says it.
type spokenItem struct {
	label, role, state, place string
}

func (s spokenItem) String() string { return joinSpoken(s.label, s.role, s.state, s.place) }

// joinSpoken puts the non-empty parts of a line together with the
// language's pause (a comma, not a list's enumeration comma).
func joinSpoken(parts ...string) string {
	out := ""
	for _, p := range parts {
		switch p = strings.TrimSpace(p); {
		case p == "":
		case out == "":
			out = p
		default:
			out = tr.T("%s, %s", out, p)
		}
	}
	return out
}

// setReader turns the screen reader on or off, saying so.
func (c *Core) setReader(on bool) {
	if c.settings.A11y.Reader == on {
		return
	}
	c.settings.A11y.Reader = on
	c.a11y.reader = readerState{spoken: c.a11y.reader.spoken, said: c.a11y.reader.said}
	if on {
		c.speak(tr.T("Screen reader on"), true)
		return
	}
	c.speak(tr.T("Screen reader off"), true)
}

// speak hands a line to the speech service: interrupt cuts what is being
// said, else it waits its turn.
func (c *Core) speak(text string, interrupt bool) {
	if text = strings.TrimSpace(text); text == "" {
		return
	}
	r := &c.a11y.reader
	r.spoken++
	if r.said = append(r.said, spokenLine{r.spoken, text}); len(r.said) > 64 {
		r.said = r.said[len(r.said)-64:]
	}
	// One at a time, in order: a line that waits must not overtake the
	// one it waits behind.
	if c.a11y.speech == nil {
		c.a11y.speech = make(chan map[string]any, 64)
		go func(lines <-chan map[string]any) {
			for l := range lines {
				_ = c.audioRPC(svc.SpeechSay, l, nil)
			}
		}(c.a11y.speech)
	}
	select {
	case c.a11y.speech <- map[string]any{"text": text, "lang": c.Language(), "interrupt": interrupt}:
	default:
		c.log.Printf("screen reader: the speech service is behind; %q dropped", text)
	}
}

// readScreen reads what changed since it last looked (each Render): the
// page or dialog, the focus, the focused item's state, a toast, a banner.
func (c *Core) readScreen() {
	a := c.settings.A11y
	if !a.Reader || c.BootProgress() >= 0 {
		return
	}
	r := &c.a11y.reader
	place, title, focus, item := c.readerView()
	toast, banner := "", ""
	if c.toast != nil {
		toast = c.toast.body
	}
	if c.banner != nil {
		banner = joinSpoken(c.banner.title, c.banner.body)
	}
	if !a.OnDemand {
		switch {
		case place != r.place:
			// Just turned on, it lets "Screen reader on" finish.
			c.speak(joinSpoken(title, item.String()), r.place != "")
		case focus != r.focus || item.label != r.item.label:
			c.speak(item.String(), true)
		case item != r.item:
			c.speak(joinSpoken(item.state, item.place), true)
		}
		if toast != "" && toast != r.toast {
			c.speak(toast, false)
		}
		if banner != "" && banner != r.banner {
			c.speak(tr.T("Notification: %s", banner), false)
		}
	}
	r.place, r.focus, r.item, r.toast, r.banner = place, focus, item, toast, banner
}

// readNow reads the page or dialog and the focused item (on demand).
func (c *Core) readNow() {
	_, title, _, item := c.readerView()
	c.speak(joinSpoken(title, item.String()), true)
}

// readerView is where the reader is: the page or dialog (a key that
// changes with it), its title, the focused node's key and how it is said.
func (c *Core) readerView() (place, title, focus string, item spokenItem) {
	if n := len(c.overlays); n > 0 {
		return c.readOverlay(c.overlays[n-1])
	}
	a := c.current()
	v := a.top()
	if v == nil {
		return "app:" + a.M.ID, a.M.Name, "", spokenItem{}
	}
	place = a.M.ID + "/" + v.S.ID
	title = v.S.Title
	if title == "" {
		title = a.M.Name
	}
	if a.system && v.S.ID == "sys.idle" {
		in := c.idleInfo()
		return place, joinSpoken(tr.T("Standby"), in.Time, in.Date, in.Operator), "", spokenItem{}
	}
	n := v.focused()
	if n == nil {
		return place, title, "", spokenItem{}
	}
	pos, total := 0, 0
	for i := range v.S.Body {
		if v.S.Body[i].Focusable() {
			total++
			if v.S.Body[i].ID == n.ID {
				pos = total
			}
		}
	}
	item = c.speakNode(v, n)
	if total > 1 {
		item.place = tr.T("item %d of %d", pos, total)
	}
	return place, title, n.ID, item
}

// speakNode is how the reader says a node: its words (its own label for
// the reader first), what it is, its state.
func (c *Core) speakNode(v *View, n *ui.Node) spokenItem {
	var s spokenItem
	s.label = n.A11yLabel
	switch n.Kind {
	case ui.KindInput:
		if s.label == "" {
			s.label = n.Placeholder
		}
		s.role = tr.T("edit box")
		switch val := c.inputText(v, n); {
		case n.Secret && val != "":
			s.state = tr.T("%d characters entered", len([]rune(val)))
		case val != "":
			s.state = val
		default:
			s.state = tr.T("empty")
		}
	case ui.KindButton:
		if s.label == "" {
			s.label = n.Text
		}
		s.role = tr.T("button")
	case ui.KindProgress:
		if s.label == "" {
			s.label = n.Text
		}
		s.role = tr.T("progress bar")
		s.state = fmt.Sprintf("%d%%", int(clampFrac(n.Fraction)*100+0.5))
	case ui.KindMedia, ui.KindImage, ui.KindCanvas, ui.KindWeb:
		if s.label == "" {
			s.label = joinSpoken(n.Label, n.Title)
		}
		s.role = map[string]string{ui.KindMedia: tr.T("video"), ui.KindImage: tr.T("picture"), ui.KindCanvas: tr.T("picture"), ui.KindWeb: tr.T("web page")}[n.Kind]
	default:
		if s.label == "" {
			s.label = joinSpoken(n.Title, n.Text, n.Subtitle)
		}
		if n.Role == "" {
			s.state = joinSpoken(n.Trailing, n.Badge)
		}
	}
	switch n.Role {
	case ui.RoleSwitch:
		s.role, s.state = tr.T("switch"), map[bool]string{true: tr.T("on"), false: tr.T("off")}[n.Checked]
	case ui.RoleCheckbox:
		s.role, s.state = tr.T("checkbox"), map[bool]string{true: tr.T("checked"), false: tr.T("not checked")}[n.Checked]
	case ui.RoleRadio:
		s.role, s.state = tr.T("radio button"), map[bool]string{true: tr.T("selected"), false: tr.T("not selected")}[n.Checked]
	case ui.RoleButton:
		s.role = tr.T("button")
	case ui.RoleLink:
		s.role = tr.T("link")
	case ui.RoleHeading:
		s.role = tr.T("heading")
	}
	return s
}

// readOverlay is how the reader says a system dialog or menu: its title
// and text, and the menu entry or button chosen.
func (c *Core) readOverlay(o overlay) (place, title, focus string, item spokenItem) {
	place = fmt.Sprintf("overlay:%p", o)
	var f *FrameOverlay
	switch o := o.(type) {
	case *scanOverlay:
		f = &FrameOverlay{Title: tr.T("Scan · System camera")}
	case *captureOverlay:
		f = &FrameOverlay{Title: tr.T("Take a photo · System camera")}
	case *pickerOverlay:
		// Its frame draws thumbnails.
		f = &FrameOverlay{Title: tr.T("Choose a photo · System album")}
		if o.sel >= 0 && o.sel < len(o.items) {
			item.label = o.items[o.sel].Name
			item.place = tr.T("item %d of %d", o.sel+1, len(o.items))
			focus = fmt.Sprint(o.sel)
		}
	case *confirmOverlay:
		f = o.frame(c)
		yes, no := o.labels()
		f.Footer = []string{tr.T("OK: %s; Back: %s", yes, no)}
	default:
		f = o.frame(c)
	}
	lines := append([]string{f.Title}, f.Lines...)
	if f.Digits != "" {
		lines = append(lines, strings.Join(strings.Split(f.Digits, ""), " "))
	}
	if f.Warn != "" {
		lines = append(lines, f.Warn)
	}
	title = joinSpoken(append(lines, f.Footer...)...)
	var entries []FrameMenuItem
	for _, it := range f.Items {
		if !it.Header {
			entries = append(entries, it)
		}
	}
	for i, it := range entries {
		if it.Sel {
			item = spokenItem{label: it.Text, place: tr.T("item %d of %d", i+1, len(entries))}
			focus = fmt.Sprint("item:", i)
		}
	}
	if _, confirm := o.(*confirmOverlay); !confirm && focus == "" && len(f.Choices) > 0 && f.Choice >= 0 && f.Choice < len(f.Choices) {
		item = spokenItem{label: f.Choices[f.Choice], role: tr.T("button")}
		focus = fmt.Sprint("choice:", f.Choice)
	}
	if f.Note != "" {
		item.state = joinSpoken(item.state, f.Note)
	}
	if f.PIN[1] > 0 {
		item.state = joinSpoken(item.state, tr.T("%d of %d digits entered", f.PIN[0], f.PIN[1]))
	}
	return place, title, focus, item
}

// spokenSince is the lines asked for after line n (numbers from 1), and
// the number of the last.
func (c *Core) spokenSince(n int) ([]string, int) {
	r := &c.a11y.reader
	var out []string
	for _, l := range r.said {
		if l.n > n {
			out = append(out, l.text)
		}
	}
	return out, r.spoken
}

// speechHas: phone-audiod's speech service said the line (selftest).
func (c *Core) speechHas(text string) bool {
	var st tts.State
	if c.audioRPC(svc.SpeechState, nil, &st) != nil {
		return false
	}
	for _, u := range st.Said {
		if u.Text == text {
			return true
		}
	}
	return false
}
