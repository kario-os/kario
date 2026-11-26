package shell

import (
	"io"
	"log"
	"testing"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

func dateView(value, keyboard string) (*View, *ui.Node) {
	v := newView(ui.Screen{ID: "edit", Body: []ui.Node{{Kind: ui.KindInput, ID: "f", Keyboard: keyboard, Value: value}, {Kind: ui.KindInput, ID: "next"}}})
	return v, &v.S.Body[0]
}

func press(c *Core, v *View, n *ui.Node, keys ...Key) {
	for _, k := range keys {
		c.dateKey(v, n, k)
	}
}

var (
	kLeft  = Named(KeyLeft)
	kRight = Named(KeyRight)
	kUp    = Named(KeyUp)
	kDown  = Named(KeyDown)
)

// The date field in Simplified Chinese (year first, the weekday after),
// the part being edited in brackets; ← → pick a part, ↑ ↓ step it (a day
// carries into the month and the year, a month keeps the day inside it),
// digits type it.
func TestDateField(t *testing.T) {
	defer i18n.SetLang(i18n.Lang())
	i18n.SetLang(i18n.SimplifiedChinese)
	c := &Core{log: log.New(io.Discard, "", 0)}
	c.settings.Region = "CN"
	v, n := dateView("2026-11-17", ui.KeyboardDate)
	check := func(value, shown string) {
		t.Helper()
		if v.inputs["f"] != value || c.inputText(v, n) != shown {
			t.Fatalf("value %q shown %q, want %q %q", v.inputs["f"], c.inputText(v, n), value, shown)
		}
	}
	v.focus = "f"
	check("2026-11-17", "2026-11-17 "+tr.T("Tue"))
	if c.dateKey(v, n, kUp) {
		t.Fatal("↑ with no part selected should move the focus")
	}
	press(c, v, n, kRight)
	check("2026-11-17", "[2026]-11-17 "+tr.T("Tue"))
	press(c, v, n, kRight, kUp)
	check("2026-12-17", "2026-[12]-17 "+tr.T("Thu"))
	press(c, v, n, kRight)
	for i := 0; i < 15; i++ {
		press(c, v, n, kUp)
	}
	check("2027-01-01", "2027-01-[01] "+tr.T("Fri"))
	press(c, v, n, kDown)
	check("2026-12-31", "2026-12-[31] "+tr.T("Thu"))
	press(c, v, n, kLeft, kUp, kUp)
	check("2027-02-28", "2027-[02]-28 "+tr.T("Sun"))
	press(c, v, n, kLeft, kDown)
	check("2026-02-28", "[2026]-02-28 "+tr.T("Sat"))
	if !c.dateKey(v, n, Named(KeyEsc)) || v.dateEdit != nil {
		t.Fatal("Back ends the editing first")
	}
	// Digits without a part selected start at the first; a part done selects the next.
	press(c, v, n, Runes("2028"))
	check("2028-02-28", "2028-[02]-28 "+tr.T("Mon"))
	press(c, v, n, Runes("0229"))
	check("2028-02-29", "2028-02-29 "+tr.T("Tue"))
	if v.dateEdit != nil {
		t.Fatal("the last part done ends the editing")
	}
	// A first digit no second can follow completes the part: 4 is April.
	press(c, v, n, kRight, kRight, Runes("4"))
	check("2028-04-29", "2028-04-[29] "+tr.T("Sat"))
	press(c, v, n, Runes("3"))
	check("2028-04-29", "2028-04-[3_] "+tr.T("Sat"))
	press(c, v, n, Named(KeyBackspace))
	check("2028-04-29", "2028-04-[29] "+tr.T("Sat"))
	press(c, v, n, Runes("31"))
	check("2028-04-30", "2028-04-30 "+tr.T("Sun"))
	if c.dateKey(v, n, Named(KeyEnter)) {
		t.Fatal("OK with nothing selected is the field's (next field, or submit)")
	}
	press(c, v, n, Named(KeyBackspace))
	if v.inputs["f"] != "" {
		t.Fatalf("backspace with nothing selected clears: %q", v.inputs["f"])
	}
	press(c, v, n, kRight)
	if _, ok := fieldValue(ui.KeyboardDate, v.inputs["f"]); !ok {
		t.Fatalf("an empty field starts from today: %q", v.inputs["f"])
	}
	// A full keyboard's number layer types digits without Fn.
	c.cfg.SKU.Fn = map[string]string{"e": "2", "r": "3", "w": "1", "s": "4"}
	press(c, v, n, Named(KeyEsc), Runes("ewew"), Runes("w"), Runes("w"), Runes("ew"))
	if v.inputs["f"] != "2121-11-21" {
		t.Fatalf("typed through the Fn layer: %q", v.inputs["f"])
	}
}

func TestDateFieldOrders(t *testing.T) {
	defer i18n.SetLang(i18n.Lang())
	i18n.SetLang(i18n.English)
	c := &Core{log: log.New(io.Discard, "", 0)}
	v, n := dateView("2026-11-17", ui.KeyboardDate)
	for region, want := range map[string]string{"US": "11/17/2026 Tue", "GB": "17/11/2026 Tue", "JP": "2026-11-17 Tue"} {
		c.settings.Region = region
		if got := c.inputText(v, n); got != want {
			t.Errorf("%s: %q, want %q", region, got, want)
		}
	}
	c.settings.Region = "US"
	v.focus = "f"
	press(c, v, n, kRight, kUp)
	if v.inputs["f"] != "2026-12-17" {
		t.Fatalf("month first in the US: %q", v.inputs["f"])
	}
	for _, x := range []struct {
		region string
		first  int
	}{{"CN", 1}, {"US", 0}, {"TW", 0}, {"DE", 1}, {"EG", 6}, {"", 1}} {
		if got := firstWeekday(x.region); got != x.first {
			t.Errorf("first weekday of %q: %d, want %d", x.region, got, x.first)
		}
	}
	if dateOrder("zh-Hant", "US") != ui.DateYMD || dateOrder("es", "MX") != ui.DateDMY || dateOrder("en", "PH") != ui.DateMDY {
		t.Error("date orders")
	}
}

// The time field: 24-hour, or the 12-hour clock with its mark; minutes go
// by five and carry into the hour.
func TestTimeField(t *testing.T) {
	defer i18n.SetLang(i18n.Lang())
	i18n.SetLang(i18n.SimplifiedChinese)
	c := &Core{log: log.New(io.Discard, "", 0)}
	v, n := dateView("09:00", ui.KeyboardTime)
	v.focus = "f"
	press(c, v, n, Runes("1730"))
	if v.inputs["f"] != "17:30" || c.inputText(v, n) != "17:30" || v.dateEdit != nil {
		t.Fatalf("typed: %q %q", v.inputs["f"], c.inputText(v, n))
	}
	press(c, v, n, kLeft, kUp)
	if v.inputs["f"] != "17:35" {
		t.Fatalf("↑ on the minutes: %q", v.inputs["f"])
	}
	v.inputs["f"] = "00:00"
	press(c, v, n, kDown)
	if v.inputs["f"] != "23:55" {
		t.Fatalf("↓ past midnight: %q", v.inputs["f"])
	}
	press(c, v, n, Runes("7"))
	if v.inputs["f"] != "23:07" {
		t.Fatalf("a minute's 7 completes it: %q", v.inputs["f"])
	}
	c.settings.Clock12 = true
	v.inputs["f"] = "17:35"
	if got := c.inputText(v, n); got != tr.T("PM")+" 05:35" {
		t.Fatalf("12-hour: %q", got)
	}
	press(c, v, n, kRight)
	if got := c.inputText(v, n); got != "["+tr.T("PM")+"] 05:35" {
		t.Fatalf("the mark comes first in Chinese: %q", got)
	}
	press(c, v, n, kUp)
	if v.inputs["f"] != "05:35" {
		t.Fatalf("↑ on the mark: %q", v.inputs["f"])
	}
	press(c, v, n, Runes("11"))
	if v.inputs["f"] != "11:35" {
		t.Fatalf("an hour typed on the 12-hour clock: %q", v.inputs["f"])
	}
	press(c, v, n, Named(KeyEsc), Runes("0430"))
	if v.inputs["f"] != "04:30" {
		t.Fatalf("digits pass the mark: %q", v.inputs["f"])
	}
}
