package shell

import (
	"fmt"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// onForms runs f in a Shell drawn as each form factor: a grid's columns
// follow the screen (menuColumns). Keyboards send first and last for Fn +
// ← → (Home, End); on the 9-key pad only scripts do.
func onForms(t *testing.T, f func(t *testing.T, c *Core)) {
	for _, d := range Devices {
		t.Run(d.ID, func(t *testing.T) {
			c := loopCore(t, t.TempDir())
			_ = c.onLoop(func() error {
				c.screenDev = d
				keypad := d.Keypad
				if keypad == "full" {
					keypad = "qwerty"
				}
				c.SetKeypad(keypad)
				f(t, c)
				return nil
			})
		})
	}
}

type endStep struct {
	key  string
	want int
}

// gridSteps go round a grid of n cells and cols columns: first and last
// from the ends and from inside it, and again where they already are.
func gridSteps(n, cols int) []endStep {
	return []endStep{
		{KeyLast, n - 1}, {KeyLast, n - 1}, {KeyFirst, 0}, {KeyFirst, 0},
		{KeyDown, cols}, {KeyRight, cols + 1}, {KeyLast, n - 1}, {KeyUp, n - 1 - cols}, {KeyFirst, 0},
	}
}

// Fn + ← → in the photo picker: the first photo (the newest) and the last,
// from anywhere in the grid and at once, as the arrows move the choice in
// the dialog's first moment; OK then hands over that photo.
func TestPickerFirstLast(t *testing.T) {
	onForms(t, func(t *testing.T, c *Core) {
		var items []*mediaItem
		for i := range 11 {
			items = append(items, &mediaItem{ID: fmt.Sprintf("p%d", i), Name: fmt.Sprintf("Photo %d", i)})
		}
		got := ""
		o := &pickerOverlay{app: &App{M: &Manifest{ID: "com.example.chat", Name: "Chat"}}, items: items, done: func(it *mediaItem) { got = it.Name }}
		c.pushOverlay(o)
		for _, s := range gridSteps(len(items), c.menuColumns()) {
			c.Key(Named(s.key))
			if o.sel != s.want {
				t.Errorf("%s: photo %d chosen, want %d", s.key, o.sel, s.want)
			}
		}
		c.Key(Named(KeyLast))
		c.overlayShown[o] = time.Now().Add(-overlayArmDelay)
		c.Key(Named(KeyEnter))
		if want := items[len(items)-1].Name; got != want {
			t.Errorf("OK after last handed over %q, want %q", got, want)
		}
	})
}

// The same in the share sheet (and the intent picker, which is drawn by
// it): the first and last app.
func TestShareSheetFirstLast(t *testing.T) {
	onForms(t, func(t *testing.T, c *Core) {
		var targets []*App
		for i := range 7 {
			targets = append(targets, &App{M: &Manifest{ID: fmt.Sprintf("com.example.app%d", i), Name: fmt.Sprintf("App %d", i)}})
		}
		got := ""
		o := &shareOverlay{from: &App{M: &Manifest{ID: "com.example.chat", Name: "Chat"}}, what: "a piece of text", targets: targets, done: func(a *App) { got = a.M.Name }}
		c.pushOverlay(o)
		for _, s := range gridSteps(len(targets), c.menuColumns()) {
			c.Key(Named(s.key))
			if o.sel != s.want {
				t.Errorf("%s: app %d chosen, want %d", s.key, o.sel, s.want)
			}
		}
		c.Key(Named(KeyLast))
		c.overlayShown[o] = time.Now().Add(-overlayArmDelay)
		c.Key(Named(KeyEnter))
		if want := targets[len(targets)-1].M.Name; got != want {
			t.Errorf("OK after last shared with %q, want %q", got, want)
		}
	})
}

// In the system's menus (Options, the file picker, the contact picker, the
// command palette): the first and the last entry shown, not one hidden
// until something is typed; with what is typed, the first and last match;
// with no match, nothing to choose.
func TestMenuFirstLast(t *testing.T) {
	onForms(t, func(t *testing.T, c *Core) {
		ran := ""
		entry := func(group, label string) menuEntry {
			return menuEntry{group: group, label: label, run: func() { ran = label }}
		}
		files := &menuOverlay{title: "Choose a file", entries: []menuEntry{
			entry("Files", "b.txt"), entry("Files", "a.pdf"), entry("Files", "notes.md"), entry("Files", "manual.txt"),
		}}
		c.pushOverlay(files)
		for _, s := range []endStep{{KeyLast, 3}, {KeyLast, 3}, {KeyUp, 2}, {KeyFirst, 0}, {KeyFirst, 0}, {KeyDown, 1}, {KeyLast, 3}} {
			c.Key(Named(s.key))
			if files.sel != s.want {
				t.Errorf("file picker: %s: entry %d chosen, want %d", s.key, files.sel, s.want)
			}
		}
		c.Key(Named(KeyEnter))
		if ran != "manual.txt" || len(c.overlays) != 0 {
			t.Errorf("file picker: OK after last ran %q (%d overlays), want manual.txt", ran, len(c.overlays))
		}

		palette := &menuOverlay{title: "Commands", palette: true, entries: []menuEntry{
			entry("Apps", "Settings"), entry("Apps", "Messages"), entry("Apps", "Music"),
			{group: "Commands", label: "Settings · Bluetooth", search: true, run: func() { ran = "Bluetooth" }},
		}}
		c.pushOverlay(palette)
		c.Key(Named(KeyLast))
		c.Key(Named(KeyEnter))
		if ran != "Music" {
			t.Errorf("palette: OK after last ran %q, want Music (the command is shown only on search)", ran)
		}

		contacts := &menuOverlay{title: "Choose a contact", palette: true, entries: []menuEntry{
			entry("Contacts", "Li Si · Mobile 13800000002"), entry("Contacts", "Zhang San · Mobile 13800000001"),
			entry("Contacts", "Zhang San · Work 010 6552 0000"), entry("Contacts", "Wang Wu · Mobile 13800000003"),
		}}
		c.pushOverlay(contacts)
		c.Key(Runes("zhang"))
		c.Key(Named(KeyLast))
		if contacts.sel != 1 {
			t.Errorf("contact picker: last of 2 matches is %d", contacts.sel)
		}
		c.Key(Named(KeyFirst))
		c.Key(Named(KeyLast))
		c.Key(Named(KeyEnter))
		if ran != "Zhang San · Work 010 6552 0000" {
			t.Errorf("contact picker: OK after last ran %q", ran)
		}

		none := &menuOverlay{title: "Commands", palette: true, entries: []menuEntry{entry("Apps", "Settings")}}
		c.pushOverlay(none)
		c.Key(Runes("no such app"))
		ran = ""
		for _, k := range []string{KeyLast, KeyEnter, KeyFirst, KeyEnter} {
			c.Key(Named(k))
		}
		if none.sel != 0 || ran != "" || len(c.overlays) != 1 {
			t.Errorf("no match: entry %d chosen, %q ran, %d overlays", none.sel, ran, len(c.overlays))
		}
		c.popOverlay(none)
	})
}

// With nothing matching what is typed in a menu, ↓, Tab and OK choose
// nothing.
func TestMenuNoMatchDownOK(t *testing.T) {
	c, _ := lockCore(t)
	ran := false
	m := &menuOverlay{title: "Commands", palette: true, entries: []menuEntry{{group: "Apps", label: "Settings", run: func() { ran = true }}}}
	c.overlays = []overlay{m}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("OK with no match: %v", r)
		}
	}()
	for _, k := range []Key{Runes("zzz"), Named(KeyDown), Named(KeyTab), Named(KeyEnter)} {
		m.key(c, k)
	}
	if m.sel != 0 || ran || len(c.overlays) != 1 {
		t.Errorf("entry %d chosen, ran %v, %d overlays", m.sel, ran, len(c.overlays))
	}
}

// In the symbol table: the first and last symbol of the page shown; OK
// types it.
func TestSymbolTableFirstLast(t *testing.T) {
	onForms(t, func(t *testing.T, c *Core) {
		v := newView(ui.Screen{ID: "t", Body: []ui.Node{{Kind: ui.KindInput, ID: "f"}}})
		c.sys.stack = []*View{v}
		o := &symOverlay{v: v, id: "f"}
		c.pushOverlay(o)
		page := symbolPages[0]
		for _, s := range gridSteps(len(page), symCols) {
			c.Key(Named(s.key))
			if o.sel != s.want {
				t.Errorf("%s: symbol %d chosen, want %d", s.key, o.sel, s.want)
			}
		}
		c.Key(Named(KeyTab))
		c.Key(Named(KeyLast))
		if items := o.items(); o.sel != len(items)-1 || items[o.sel] != symbolPages[1][len(symbolPages[1])-1] {
			t.Errorf("next page: last is symbol %d", o.sel)
		}
		c.Key(Named(KeyEnter))
		if got := fieldText(v, "f"); got != symbolPages[1][len(symbolPages[1])-1]+"|" {
			t.Errorf("OK after last typed %q", got)
		}
	})
}
