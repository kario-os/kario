package shell

import (
	"testing"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/ui"
)

var testFnLayer = map[string]string{"q": "#", "w": "1", "e": "2", "r": "3", "a": "*", "o": "+", "h": ":", ".": "0"}

// A number field takes each key's Fn character, shifted or not; keys
// without one type as they are.
func TestFnRunes(t *testing.T) {
	c := &Core{}
	if got := string(c.fnRunes([]rune("wer"))); got != "wer" {
		t.Errorf("without a layer: %q", got)
	}
	c.cfg.SKU.Fn = testFnLayer
	if got := string(c.fnRunes([]rune("wEr.h!x"))); got != "1230:!x" {
		t.Errorf("fnRunes = %q, want 1230:!x", got)
	}
}

// The standby screen dials only when every key typed has a dial
// character behind it.
func TestDialFor(t *testing.T) {
	c := &Core{}
	if d := c.dialFor("wer"); d != "" {
		t.Errorf("without a layer: %q", d)
	}
	c.cfg.SKU.Fn = testFnLayer
	for q, want := range map[string]string{"wer": "123", "wea": "12*", "qo1": "#+1", "weh": "", "wex": "", "": ""} {
		if d := c.dialFor(q); d != want {
			t.Errorf("dialFor(%q) = %q, want %q", q, d, want)
		}
	}
}

// The status bar says 123 in a number field of a keyboard with an Fn
// layer, and Fn or 123 in a text field while Fn is tapped or locked.
func TestInputMode(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		show := func(n ui.Node) { c.sys.stack = []*View{newView(ui.Screen{ID: "t", Body: []ui.Node{n}})} }
		show(ui.Node{Kind: ui.KindInput, ID: "n", Keyboard: "phone"})
		if m := c.inputMode(); m != "" {
			t.Errorf("number field without a layer: %q", m)
		}
		c.SetFnLayer(testFnLayer)
		if m := c.inputMode(); m != "123" {
			t.Errorf("number field: %q, want 123", m)
		}
		show(ui.Node{Kind: ui.KindInput, ID: "t"})
		text := c.inputMode()
		c.Press(Named(KeyFnOnce))
		if m := c.inputMode(); m != "Fn" {
			t.Errorf("Fn tapped: %q", m)
		}
		c.Press(Named(KeyFnLock))
		if m := c.inputMode(); m != "123" {
			t.Errorf("Fn locked: %q", m)
		}
		c.Press(Named(KeyFnOff))
		if m := c.inputMode(); m != text || m == "" {
			t.Errorf("Fn off: %q, want the input method's %q", m, text)
		}
		c.Press(Named(KeyShiftOnce))
		if m := c.inputMode(); m != "Abc" {
			t.Errorf("Shift tapped: %q", m)
		}
		c.Press(Named(KeyShiftLock))
		if m := c.inputMode(); m != "ABC" {
			t.Errorf("Shift locked: %q", m)
		}
		c.Press(Named(KeyShiftOff))
		if m := c.inputMode(); m != text {
			t.Errorf("Shift off: %q, want %q", m, text)
		}
		return nil
	})
}

// A letter held in a text field types its Fn character instead: the
// renderer's long press replaces the letter it typed at the press, a
// script's long press types the character alone. In a list the long press
// does nothing more; keys without an Fn character are not touched.
func TestLongFn(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		v := newView(ui.Screen{ID: "t", Body: []ui.Node{{Kind: ui.KindInput, ID: "f"}}})
		c.sys.stack = []*View{v}
		c.SetFnLayer(testFnLayer)
		c.ime.SetMode(ime.En)
		c.Press(Runes("w"))
		c.Press(Key{Name: KeyRunes, Runes: []rune("w"), Long: true, Retype: true})
		c.Press(LongRunes("e"))
		c.Press(Runes("x"))
		c.Press(LongRunes("x"))
		if got := v.inputs["f"] + c.ime.Preedit(); got != "12xx" {
			t.Errorf("text field: %q, want 12xx", got)
		}
		list := newView(ui.Screen{ID: "l", Body: []ui.Node{{Kind: ui.KindItem, ID: "a", Title: "A"}}})
		c.sys.stack = []*View{list}
		if !c.longFn(Key{Name: KeyRunes, Runes: []rune("w"), Long: true, Retype: true}) {
			t.Error("a list took the renderer's long press")
		}
		if c.longFn(LongRunes("w")) {
			t.Error("a list's long press from a script was swallowed")
		}
		return nil
	})
}
