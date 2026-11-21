package displayd

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The policy (08-display-adaptive-refresh.md §5): with the setting on
// automatic the panel runs at its highest rate while anything moves (a key
// pressed, the renderer animating, the app in front animating), holds it
// for Hold after the motion ends, and otherwise runs at the rate content
// asks for (a video: the lowest rate its frame rate divides) or else at
// the idle rate, the lowest at or above idleFloor. The standard and high
// settings fix the rate. Caps (a low battery, a hot phone) hold any of
// them down. Raising is at once; lowering waits for the hold.
const (
	Hold      = 400 * time.Millisecond
	idleFloor = 30
	// maxMotion bounds a motion hint that names no end (the renderer's:
	// one that crashed mid-animation does not keep the rate up), and a
	// content hint's time.
	maxMotion  = 10 * time.Second
	maxContent = time.Hour
)

type hint struct {
	kind  string
	until time.Time // the end of its motion, the hold included
	fps   int
	since time.Time
}

// Policy decides; it keeps no clock of its own, so tests give it the time.
type Policy struct {
	rates   []int
	setting string
	caps    map[string]int
	hints   map[string]hint
}

func NewPolicy(rates []int) *Policy {
	p := &Policy{setting: ui.RefreshAuto, caps: map[string]int{}, hints: map[string]hint{}}
	p.SetRates(rates)
	return p
}

// SetRates: the panel's rates (its modes at the size it runs at).
func (p *Policy) SetRates(rates []int) {
	var out []int
	for _, r := range rates {
		if r > 0 && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	sort.Ints(out)
	p.rates = out
}

func (p *Policy) Rates() []int { return slices.Clone(p.rates) }

func (p *Policy) Setting() string { return p.setting }

// SetSetting takes ui.RefreshAuto, ui.RefreshStandard or ui.RefreshHigh.
func (p *Policy) SetSetting(s string) bool {
	if !slices.Contains(ui.RefreshRates, s) {
		return false
	}
	p.setting = s
	return true
}

// SetCap: the highest rate source allows, 0 none.
func (p *Policy) SetCap(source string, hz int) {
	if hz <= 0 {
		delete(p.caps, source)
		return
	}
	p.caps[source] = hz
}

func (p *Policy) Caps() map[string]int {
	if len(p.caps) == 0 {
		return nil
	}
	out := map[string]int{}
	for k, v := range p.caps {
		out[k] = v
	}
	return out
}

// Hint takes a hint at now; false for one it does not know.
func (p *Policy) Hint(h svc.DisplayHintArgs, now time.Time) bool {
	d := time.Duration(h.MS) * time.Millisecond
	since := now
	if old, ok := p.hints[h.Source]; ok && old.kind == h.Kind && old.until.After(now) {
		since = old.since
	}
	switch h.Kind {
	case svc.DisplayInteraction:
		p.hints[h.Source] = hint{kind: h.Kind, until: now.Add(Hold), since: since}
	case svc.DisplayAnimating:
		if d <= 0 || d > maxMotion {
			d = maxMotion
		}
		p.hints[h.Source] = hint{kind: h.Kind, until: now.Add(d + Hold), since: since}
	case svc.DisplayEnd:
		if old, ok := p.hints[h.Source]; ok && old.fps == 0 && old.until.After(now.Add(Hold)) {
			old.until = now.Add(Hold)
			p.hints[h.Source] = old
		}
	case svc.DisplayContent:
		if h.FPS <= 0 || h.FPS > ui.DisplayHintMaxFPS {
			return false
		}
		if d <= 0 || d > maxContent {
			d = maxContent
		}
		p.hints[h.Source] = hint{kind: h.Kind, until: now.Add(d), fps: h.FPS, since: since}
	case svc.DisplayClear:
		delete(p.hints, h.Source)
	default:
		return false
	}
	return true
}

// ClearPrefix drops the hints of every source that starts so.
func (p *Policy) ClearPrefix(prefix string) {
	for s := range p.hints {
		if strings.HasPrefix(s, prefix) {
			delete(p.hints, s)
		}
	}
}

// standard is the rate the standard setting fixes: the highest at or
// under ui.StandardHz, or the panel's lowest.
func (p *Policy) standard() int {
	best := 0
	for _, r := range p.rates {
		if r <= ui.StandardHz {
			best = r
		}
	}
	if best == 0 && len(p.rates) > 0 {
		best = p.rates[0]
	}
	return best
}

func (p *Policy) high() int {
	if len(p.rates) == 0 {
		return 0
	}
	return p.rates[len(p.rates)-1]
}

func (p *Policy) idle() int {
	for _, r := range p.rates {
		if r >= idleFloor {
			return r
		}
	}
	return p.high()
}

// forContent: the lowest rate every frame rate divides; without one, the
// lowest at or above the fastest; without that, the highest.
func (p *Policy) forContent(fps []int) int {
	for _, r := range p.rates {
		ok := true
		for _, f := range fps {
			ok = ok && r%f == 0
		}
		if ok {
			return r
		}
	}
	fastest := slices.Max(fps)
	for _, r := range p.rates {
		if r >= fastest {
			return r
		}
	}
	return p.high()
}

// capped: the highest rate at or under limit, or the lowest.
func (p *Policy) capped(limit int) int {
	best := 0
	for _, r := range p.rates {
		if r <= limit {
			best = r
		}
	}
	if best == 0 && len(p.rates) > 0 {
		best = p.rates[0]
	}
	return best
}

// Decide is the rate at now and why, and when the decision may change
// next (zero: not until something else happens).
func (p *Policy) Decide(now time.Time) (hz int, reason string, next time.Time) {
	for s, h := range p.hints {
		if !h.until.After(now) {
			delete(p.hints, s)
			continue
		}
		if next.IsZero() || h.until.Before(next) {
			next = h.until
		}
	}
	if len(p.rates) == 0 {
		return 0, "", next
	}
	switch p.setting {
	case ui.RefreshStandard:
		hz, reason, next = p.standard(), "setting", time.Time{}
	case ui.RefreshHigh:
		hz, reason, next = p.high(), "setting", time.Time{}
	default:
		hz, reason = p.auto()
	}
	limit, by := 0, ""
	for s, c := range p.caps {
		if limit == 0 || c < limit || c == limit && s < by {
			limit, by = c, s
		}
	}
	if limit > 0 {
		if c := p.capped(limit); c < hz {
			hz, reason = c, "cap:"+by
		}
	}
	return hz, reason, next
}

func (p *Policy) auto() (int, string) {
	var moving []string
	var fps []int
	for s, h := range p.hints {
		if h.fps > 0 {
			fps = append(fps, h.fps)
		} else {
			moving = append(moving, s)
		}
	}
	if len(moving) > 0 {
		// The motion that started first names the reason: a key's hint
		// renewed on every press does not take it from the scroll it
		// started.
		sort.Slice(moving, func(i, j int) bool {
			a, b := p.hints[moving[i]], p.hints[moving[j]]
			if !a.since.Equal(b.since) {
				return a.since.Before(b.since)
			}
			return moving[i] < moving[j]
		})
		return p.high(), moving[0]
	}
	if len(fps) > 0 {
		sort.Ints(fps)
		return p.forContent(fps), "content:" + strconv.Itoa(fps[len(fps)-1])
	}
	return p.idle(), "idle"
}
