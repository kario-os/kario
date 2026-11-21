package displayd

import (
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

var t0 = time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)

func ms(n int) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }

func rate(t *testing.T, p *Policy, at time.Time, want int, why string) time.Time {
	t.Helper()
	hz, reason, next := p.Decide(at)
	if hz != want || why != "" && reason != why {
		t.Fatalf("at %v: %d Hz (%s), want %d Hz (%s)", at.Sub(t0), hz, reason, want, why)
	}
	return next
}

// A key goes to the high rate at once and holds it Hold after the last
// press; a run of presses (a scroll) keeps it up.
func TestKeysRaiseAndTheHoldLowers(t *testing.T) {
	p := NewPolicy([]int{120, 60})
	rate(t, p, ms(0), 60, "idle")
	for _, at := range []int{0, 150, 300, 450} {
		p.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceInput, Kind: svc.DisplayInteraction}, ms(at))
		rate(t, p, ms(at), 120, svc.DisplaySourceInput)
	}
	next := rate(t, p, ms(800), 120, svc.DisplaySourceInput)
	if !next.Equal(ms(450).Add(Hold)) {
		t.Fatalf("the next decision at %v, want the hold's end", next.Sub(t0))
	}
	rate(t, p, ms(850), 60, "idle")
}

// The renderer's animation: up until it ends, then the hold; one that
// never ends comes down after maxMotion.
func TestAnimationEndsWithTheHold(t *testing.T) {
	p := NewPolicy([]int{60, 120})
	p.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceRenderer, Kind: svc.DisplayAnimating}, ms(0))
	rate(t, p, ms(5000), 120, svc.DisplaySourceRenderer)
	p.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceRenderer, Kind: svc.DisplayEnd}, ms(5000))
	rate(t, p, ms(5300), 120, "")
	rate(t, p, ms(5400), 60, "idle")
	p.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceRenderer, Kind: svc.DisplayAnimating}, ms(6000))
	rate(t, p, ms(6000).Add(maxMotion), 120, "")
	rate(t, p, ms(6000).Add(maxMotion+Hold), 60, "idle")
	// An app's: for its time and the hold.
	p.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceApp + "com.x", Kind: svc.DisplayAnimating, MS: 1000}, ms(0))
	rate(t, p, ms(1399), 120, "app:com.x")
	rate(t, p, ms(1400), 60, "idle")
}

// Content's frame rate picks the lowest rate it divides; a cap holds it
// down; motion goes over it.
func TestContentRates(t *testing.T) {
	for _, c := range []struct {
		rates []int
		fps   []int
		want  int
	}{
		{[]int{60, 120}, []int{24}, 120},
		{[]int{60, 120}, []int{30}, 60},
		{[]int{60, 120}, []int{60}, 60},
		{[]int{60, 120}, []int{15}, 60},
		{[]int{48, 60, 90, 120}, []int{24}, 48},
		{[]int{30, 60, 120}, []int{30}, 30},
		{[]int{30, 60, 120}, []int{24, 30}, 120},
		{[]int{60, 90}, []int{24}, 60},
		{[]int{60, 90}, []int{25}, 60},
		{[]int{60}, []int{144}, 60},
		{[]int{50, 60, 100, 120}, []int{25}, 50},
	} {
		p := NewPolicy(c.rates)
		for i, f := range c.fps {
			p.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceVideo + string(rune('a'+i)), Kind: svc.DisplayContent, FPS: f}, t0)
		}
		if hz, _, _ := p.Decide(t0); hz != c.want {
			t.Errorf("panel %v, content %v fps: %d Hz, want %d", c.rates, c.fps, hz, c.want)
		}
	}
	p := NewPolicy([]int{60, 120})
	p.Hint(svc.DisplayHintArgs{Source: "video:1", Kind: svc.DisplayContent, FPS: 24}, t0)
	rate(t, p, t0, 120, "content:24")
	p.SetCap(svc.DisplayCapBattery, 60)
	rate(t, p, t0, 60, "cap:battery")
	p.SetCap(svc.DisplayCapBattery, 0)
	p.Hint(svc.DisplayHintArgs{Source: "video:1", Kind: svc.DisplayClear}, t0)
	p.Hint(svc.DisplayHintArgs{Source: "video:2", Kind: svc.DisplayContent, FPS: 30}, t0)
	rate(t, p, t0, 60, "content:30")
	p.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceInput, Kind: svc.DisplayInteraction}, t0)
	rate(t, p, t0, 120, svc.DisplaySourceInput)
	if p.Hint(svc.DisplayHintArgs{Source: "video:3", Kind: svc.DisplayContent}, t0) {
		t.Fatal("a content hint without a frame rate was taken")
	}
}

// The standard and high settings fix the rate; caps hold every setting
// down, the lowest cap first; idle goes as low as the floor where the
// panel can.
func TestSettingsAndCaps(t *testing.T) {
	p := NewPolicy([]int{30, 60, 90, 120})
	rate(t, p, t0, 30, "idle")
	p.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceInput, Kind: svc.DisplayInteraction}, t0)
	rate(t, p, t0, 120, "")
	if !p.SetSetting(ui.RefreshStandard) {
		t.Fatal("standard refused")
	}
	rate(t, p, t0, 60, "setting")
	p.SetSetting(ui.RefreshHigh)
	rate(t, p, ms(1000), 120, "setting")
	p.SetCap(svc.DisplayCapThermal, 90)
	rate(t, p, ms(1000), 90, "cap:thermal")
	p.SetCap(svc.DisplayCapBattery, 60)
	rate(t, p, ms(1000), 60, "cap:battery")
	p.SetCap(svc.DisplayCapBattery, 0)
	p.SetCap(svc.DisplayCapThermal, 0)
	rate(t, p, ms(1000), 120, "setting")
	if p.SetSetting("fast") {
		t.Fatal("an unknown setting was taken")
	}
	// A cap under every rate: the lowest.
	p.SetCap(svc.DisplayCapThermal, 20)
	rate(t, p, ms(1000), 30, "cap:thermal")
	// The standard rate of a panel without 60 Hz.
	q := NewPolicy([]int{90, 120})
	q.SetSetting(ui.RefreshStandard)
	rate(t, q, t0, 90, "setting")
	// A panel of one rate: that rate, whatever moves.
	one := NewPolicy([]int{60})
	one.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceInput, Kind: svc.DisplayInteraction}, t0)
	rate(t, one, t0, 60, "")
	// No panel yet: nothing.
	if hz, _, _ := NewPolicy(nil).Decide(t0); hz != 0 {
		t.Fatalf("no rates: %d Hz", hz)
	}
}

// An app's hints go with its prefix (it left the front); the reason is the
// motion that began first, not a key renewed since.
func TestClearPrefixAndReason(t *testing.T) {
	p := NewPolicy([]int{60, 120})
	p.Hint(svc.DisplayHintArgs{Source: "app:a", Kind: svc.DisplayAnimating, MS: 2000}, ms(0))
	p.Hint(svc.DisplayHintArgs{Source: "app:b", Kind: svc.DisplayContent, FPS: 24}, ms(0))
	p.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceInput, Kind: svc.DisplayInteraction}, ms(100))
	rate(t, p, ms(200), 120, "app:a")
	p.ClearPrefix(svc.DisplaySourceApp)
	rate(t, p, ms(200), 120, svc.DisplaySourceInput)
	rate(t, p, ms(600), 60, "idle")
}
