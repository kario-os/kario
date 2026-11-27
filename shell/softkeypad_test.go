package shell

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"unicode"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/tools/prebuilt"
	"github.com/kario-os/kario/ui"
)

var keypadForm = ui.Screen{ID: "form", Title: "Form", Body: []ui.Node{
	{Kind: ui.KindInput, ID: "name", Placeholder: "Name"},
	{Kind: ui.KindInput, ID: "time", Placeholder: "Time", Keyboard: ui.KeyboardNumber},
	{Kind: ui.KindInput, ID: "tel", Placeholder: "Number", Keyboard: ui.KeyboardPhone},
	{Kind: ui.KindInput, ID: "day", Placeholder: "Day", Keyboard: ui.KeyboardDate},
	{Kind: ui.KindInput, ID: "pin", Placeholder: "PIN", Secret: true},
	{Kind: ui.KindItem, ID: "save", Title: "Save", Actions: []ui.Action{{ID: "save", Label: "Save", Primary: true}}},
}}

// keypadRig is a touchRig on a phone like the OnePlus 6T with no keyboard
// connected: a touchscreen and keys that do not type, the 9-key input
// method; the form is on screen over the list.
func keypadRig(t *testing.T) *touchRig {
	t.Helper()
	r := newTouchRig(t, "touch")
	r.device("touchscreen", "fts_ts", true, false)
	r.device("keyboard", "gpio-keys", true, false)
	_ = r.c.onLoop(func() error { r.c.SetKeypad("t9"); return nil })
	r.push(keypadForm)
	return r
}

// device: the renderer says an input device came or went.
func (r *touchRig) device(kind, name string, added, ok bool) {
	r.t.Helper()
	b, _ := json.Marshal(map[string]any{"t": "input", "kind": kind, "name": name, "added": added, "ok": ok})
	if _, err := r.out.Write(append(b, '\n')); err != nil {
		r.t.Fatal(err)
	}
	r.idle()
}

// keypad is the on-screen keypad of the frame drawn now, nil without one.
func (r *touchRig) keypad() *FrameKeypad {
	var k *FrameKeypad
	dev, _ := DeviceByID("bar-t9")
	_ = r.c.onLoop(func() error { k = r.c.Frame(dev).Keypad; return nil })
	return k
}

func (r *touchRig) layout() string {
	var l string
	_ = r.c.onLoop(func() error { l = r.c.keypadLayout(); return nil })
	return l
}

// key is a tap (or a long press) on the keypad's key id.
func (r *touchRig) key(id string, long bool) {
	r.t.Helper()
	r.drawn()
	h := TouchHit{Area: "keypad", Key: id}
	r.say("down", 120, 400, &h)
	if long {
		r.idle()
		_ = r.c.onLoop(func() error { r.c.touchHeld(r.c.touch.n); return nil })
	}
	r.say("up", 120, 400, nil)
	r.idle()
}

func (r *touchRig) keys(ids ...string) {
	for _, id := range ids {
		r.key(id, false)
	}
}

func (r *touchRig) value(id string) string {
	var s string
	_ = r.c.onLoop(func() error { s = r.c.current().top().inputs[id]; return nil })
	return s
}

// focusOn taps the form's node i.
func (r *touchRig) focusOn(i int) {
	r.t.Helper()
	r.drawn()
	r.tap(120, 60, r.row(i))
	r.idle()
}

// The keypad shows under a field with the focus on a phone whose renderer
// takes touches and that has no keys that type; a keyboard with an OK key
// connected puts it away; it is not there without a field, under a menu
// or where nothing said which keys the phone has (a terminal).
func TestSoftKeypadShowsOnATypelessPhone(t *testing.T) {
	if !alone(t) {
		return
	}
	plain := newTouchRig(t, "touch")
	plain.push(keypadForm)
	if k := plain.keypad(); k != nil {
		t.Fatal("a keypad where no device was reported")
	}
	nontouch := newTouchRig(t)
	nontouch.device("keyboard", "gpio-keys", true, false)
	nontouch.push(keypadForm)
	if k := nontouch.keypad(); k != nil {
		t.Fatal("a keypad on a renderer that takes no touches")
	}

	r := keypadRig(t)
	if got := r.focus(); got != "name" {
		t.Fatalf("the focus on %q", got)
	}
	k := r.keypad()
	if k == nil || len(k.Rows) != 4 {
		t.Fatalf("the keypad under a text field: %+v", k)
	}
	if got, want := k.Rows[0][1], (FrameKey{ID: "2", Label: "2", Sub: "abc"}); got != want {
		t.Errorf("2 is %+v, want %+v", got, want)
	}
	if got := k.Rows[3][3]; got.ID != KeyEnter || got.Style != "ok" || got.Label == "" {
		t.Errorf("OK is %+v", got)
	}
	if got := k.Rows[3][2]; got.ID != "#" || got.Sub != ime.New(ime.T9Pad).Indicator() {
		t.Errorf("# is %+v, want the input method's mode under it", got)
	}
	ids := ""
	for _, row := range k.Rows {
		for _, key := range row {
			ids += key.ID + " "
		}
	}
	if want := "1 2 3 backspace 4 5 6 left 7 8 9 right * 0 # enter "; ids != want {
		t.Errorf("the keys are %q, want %q", ids, want)
	}

	r.device("keyboard", "bt-kbd", true, true)
	if k := r.keypad(); k != nil {
		t.Fatal("a keypad with a keyboard connected")
	}
	r.device("keyboard", "bt-kbd", false, true)
	if r.layout() != keypadText {
		t.Fatal("no keypad once the keyboard went")
	}

	for i, want := range []string{keypadText, keypadDigits, keypadPhone, keypadDate, keypadDigits, ""} {
		r.focusOn(i)
		if got := r.layout(); got != want {
			t.Errorf("on %s the keypad is %q, want %q", keypadForm.Body[i].ID, got, want)
		}
	}
	r.focusOn(2)
	if row := r.keypad().Rows[3]; row[0].ID != "*" || row[1].Sub != "+" || row[2].ID != "#" {
		t.Errorf("a phone number's bottom row: %+v", row)
	}
	r.focusOn(1)
	if row := r.keypad().Rows[3]; row[0].ID != "" || row[2].ID != "" {
		t.Errorf("a number's bottom row: %+v", row)
	}
	r.focusOn(3)
	if row := r.keypad().Rows[3]; row[0].ID != KeyDown || row[2].ID != KeyUp {
		t.Errorf("a date's bottom row: %+v", row)
	}

	r.focusOn(0)
	r.key("*", false)
	until(t, r.c, "the symbols", func() bool { return len(r.c.overlays) > 0 })
	if k := r.keypad(); k != nil {
		t.Fatal("a keypad over the symbols")
	}
	r.tap(120, 10, TouchHit{Area: "outside"})
	until(t, r.c, "the symbols closed", func() bool { return len(r.c.overlays) == 0 })
	if got := r.layout(); got != keypadText {
		t.Fatalf("the symbols closed, the keypad is %q", got)
	}

	_ = r.c.onLoop(func() error { r.c.SetKeypad("qwerty"); return nil })
	if got := r.layout(); got != "" {
		t.Errorf("a 9-key pad for a keyboard's input method: %q", got)
	}
	r.focusOn(1)
	if got := r.layout(); got != keypadDigits {
		t.Errorf("a number field with a keyboard's input method: %q", got)
	}
}

// Its keys are pressed as the keypad's: digits into a number field, + by
// 0 held into a phone number's, Backspace and ← → there; multi-tap and
// pinyin through the input method into a text field, # its next mode, #
// held its digits.
func TestSoftKeypadTypes(t *testing.T) {
	if !alone(t) {
		return
	}
	r := keypadRig(t)

	r.focusOn(1)
	r.keys("0", "7", "3", "0")
	if got := r.value("time"); got != "0730" {
		t.Fatalf("the time is %q", got)
	}
	r.keys("left", "backspace")
	if got := r.value("time"); got != "070" {
		t.Fatalf("← and Backspace: %q", got)
	}
	r.keys("*", "#")
	if got := r.value("time"); got != "070" {
		t.Fatalf("keys a number's keypad does not have typed: %q", got)
	}

	r.focusOn(2)
	r.key("0", true)
	r.keys("4", "4", "*", "#")
	if got := r.value("tel"); got != "+44*#" {
		t.Fatalf("the number is %q", got)
	}

	r.focusOn(0)
	r.keys("#", "#", "#")
	if got := r.keypad().Rows[3][2].Sub; got != "Abc" {
		t.Fatalf("after # three times the mode is %q", got)
	}
	if got := r.keypad().Rows[1][0].Sub; got != "GHI" {
		t.Errorf("4's letters in Abc: %q", got)
	}
	r.keys("4", "4", "right", "4", "4", "4", "right")
	if got := r.value("name"); got != "Hi" {
		t.Fatalf("multi-tap typed %q", got)
	}
	r.keys("#", "#", "#")
	r.keys("9", "4", "6", "6", "4")
	var cands bool
	dev, _ := DeviceByID("bar-t9")
	_ = r.c.onLoop(func() error { f := r.c.Frame(dev); cands = len(f.IME) > 0 && f.Keypad != nil; return nil })
	if !cands {
		t.Fatal("no candidates above the keypad")
	}
	r.key(KeyEnter, false)
	before := r.value("name")
	got, ok := strings.CutPrefix(before, "Hi")
	for _, c := range got {
		ok = ok && unicode.Is(unicode.Han, c)
	}
	if !ok || got == "" {
		t.Fatalf("pinyin 94664 and OK typed %q", before)
	}
	r.key("#", true)
	r.keys("5")
	if got := r.value("name"); got != before+"5" {
		t.Fatalf("# held, then 5: %q", got)
	}
}

// Back puts the keypad away, the field keeping the focus; the next Back
// goes back. A tap on the field brings it back, and so does the focus
// coming back to it.
func TestSoftKeypadBack(t *testing.T) {
	if !alone(t) {
		return
	}
	r := keypadRig(t)
	r.focusOn(1)
	back := func() {
		r.drawn()
		r.tap(120, 26, TouchHit{Area: "title"})
		r.idle()
	}
	back()
	if got := r.layout(); got != "" {
		t.Fatalf("after Back the keypad is %q", got)
	}
	if got := title(r.a); got != "Form" || r.focus() != "time" {
		t.Fatalf("Back with the keypad up left the field: on %q, the focus on %q", got, r.focus())
	}
	r.key("5", false)
	if got := r.value("time"); got != "" {
		t.Fatalf("a key of the keypad put away typed %q", got)
	}
	r.focusOn(1)
	if got := r.layout(); got != keypadDigits {
		t.Fatalf("tapped, the field's keypad is %q", got)
	}
	back()
	press := func(k string) {
		_ = r.c.onLoop(func() error { r.c.Press(Named(k)); return nil })
		r.drawn()
	}
	press(KeyDown)
	if got := r.focus(); got != "tel" || r.layout() != keypadPhone {
		t.Fatalf("down: the focus on %q, the keypad %q", got, r.layout())
	}
	press(KeyUp)
	if got := r.layout(); got != keypadDigits {
		t.Fatalf("the focus back on the field, its keypad is %q", got)
	}
	back()
	back()
	until(t, r.c, "back on the list", func() bool { return title(r.a) == "List" })
}

// keypadSteps is the selftest's runner on phone-paint as built here
// (touchSteps) with the form on screen (appSteps).
func keypadSteps(t *testing.T) (*Runner, func(cmd, arg string)) {
	t.Helper()
	paint := prebuilt.Paint(t)
	form := keypadForm
	form.Body = append([]ui.Node{form.Body[1]}, form.Body[0])
	r, step, _ := appSteps(t, form, paint, "--headless", "--snap-dir", t.TempDir())
	return r, step
}

// appSteps is the selftest's runner on a renderer (args its own) with an
// app in front that the test plays: it answers the Shell's pings and has
// s on screen; show updates s (by its ID) to another.
func appSteps(t *testing.T, s ui.Screen, renderer string, args ...string) (r *Runner, step func(cmd, arg string), show func(ui.Screen)) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "apps", "test.form")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"test.form","name":"Form","exec":"none"}`), 0o644)
	run, err := os.MkdirTemp("", "keypad-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	c, err := New(Config{W: 40, H: 24, AppsDir: filepath.Join(root, "apps"), RunDir: run, StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Shutdown)
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	app, shell := os.NewFile(uintptr(fds[0]), "app"), os.NewFile(uintptr(fds[1]), "shell")
	t.Cleanup(func() { app.Close() })
	c.Adopt([]Inherited{{ID: "test.form", Pid: sleep.Process.Pid, File: shell, Major: ui.ProtoMajor}})
	c.setForeground(c.appByID("test.form"))
	send := func(m ui.Msg) {
		b, _ := json.Marshal(m)
		_, _ = app.Write(append(b, '\n'))
	}
	go func() {
		rd := bufio.NewReader(app)
		for {
			line, err := rd.ReadBytes('\n')
			if err != nil {
				return
			}
			var m ui.Msg
			if json.Unmarshal(line, &m) == nil && m.T == ui.MsgPing {
				send(ui.Msg{T: ui.MsgPong, Value: m.Value})
			}
		}
	}()
	dev, _ := DeviceByID("bar-t9")
	g, err := StartGUI(c, renderer, dev, args...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	r = &Runner{C: c, GUI: g, Out: io.Discard, Scale: 1}
	step = func(cmd, arg string) {
		t.Helper()
		if err := r.step(cmd, arg); err != nil {
			t.Fatalf("%s %s: %v", cmd, arg, err)
		}
	}
	send(ui.Msg{T: ui.MsgPush, Screen: &s})
	step("at", "test.form/"+s.ID)
	return r, step, func(s ui.Screen) { send(ui.Msg{T: ui.MsgUpdate, Screen: &s}) }
}

// The keypad as phone-paint draws it: its keys where the renderer laid
// them out, above the soft keys; a touch on one is that key pressed, on
// the gap beside 0 nothing; Back puts it away.
func TestSoftKeypadOnPhonePaint(t *testing.T) {
	if !alone(t) {
		return
	}
	r, step := keypadSteps(t)
	c := r.C
	value := func(id string) string { return c.current().top().inputs[id] }
	step("buttons-only", "on")
	step("keypad", "t9")
	step("soft-keypad", "digits")
	for _, k := range []string{"0", "7", "3", "0"} {
		step("touch", "tap key "+k)
	}
	if got := value("time"); got != "0730" {
		t.Fatalf("the time is %q", got)
	}
	_, h := r.touchSize()
	keys := c.layout.Keys
	if len(keys) != 14 {
		t.Fatalf("the digits' keypad has %d keys laid out: %+v", len(keys), keys)
	}
	last := keys[len(keys)-1]
	if last.ID != KeyEnter || last.Y+last.H > h-20 || last.Y+last.H < h-30 {
		t.Fatalf("OK at %+v, not just above the soft keys (the frame %v high)", last, h)
	}
	gap := keys[len(keys)-2]
	step("touch", fmt.Sprintf("tap %.0f %.0f", gap.X-gap.W/2, gap.Y+gap.H/2))
	if got := value("time"); got != "0730" {
		t.Fatalf("the gap beside 0 typed: %q", got)
	}
	step("touch", "tap row Name")
	step("soft-keypad", "text")
	for _, k := range []string{"#", "#", "#", "2", "3", "right", "#", "#", "#", "9", "4", "6", "6", "4"} {
		step("touch", "tap key "+k)
	}
	step("touch", "tap key enter")
	if got := value("name"); !regexp.MustCompile(`^Ad\p{Han}+$`).MatchString(got) {
		t.Fatalf("the name is %q", got)
	}
	step("touch", "tap 50% 26")
	step("soft-keypad", "text")
	step("touch", "tap 50% 26")
	step("soft-keypad", "none")
	step("at", "test.form/form")
	if _, err := r.gestureOf("tap key 5"); err == nil {
		t.Fatal("a key of the keypad put away was aimed at")
	}
}
