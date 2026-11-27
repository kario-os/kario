package shell

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/backlight"
)

// fakePanel is a renderer with the power feature and nothing else, as the
// Shell sees phone-paint on a phone: it reads the Shell's requests as they
// come, turns the panel on or off one request at a time, each taking took
// (a DSI panel's power sequence), and answers each with the panel's state
// and why it could not (GUI.read takes the answers, as from a renderer).
type fakePanel struct {
	mu   sync.Mutex
	on   bool
	took time.Duration
	// fail: the error the nth request (from 1) gets, "" for none; warn:
	// what the answer to one that succeeded warns of.
	fail, warn func(n int, on bool) string
	reqs       []panelReq
	// queued: requests read and not answered yet; most: the most at once.
	queued, most int
}

type panelReq struct {
	on, was   bool
	at, done  time.Time
	err, warn string
}

// panelRenderer gives c a fakePanel for its renderer, the panel on.
func panelRenderer(t *testing.T, c *Core, took time.Duration) *fakePanel {
	t.Helper()
	p := &fakePanel{on: true, took: took}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	g := &GUI{c: c, in: inW, enc: json.NewEncoder(inW), info: RendererInfo{Features: []string{"power"}},
		ready: make(chan guiMsg, 1), acks: make(chan guiMsg, 4), done: make(chan struct{})}
	work := make(chan panelReq, 64)
	go func() {
		sc := bufio.NewScanner(inR)
		for sc.Scan() {
			var m struct {
				T  string `json:"t"`
				On *bool  `json:"on"`
			}
			if json.Unmarshal(sc.Bytes(), &m) != nil || m.T != "power" || m.On == nil {
				continue
			}
			p.mu.Lock()
			p.queued++
			p.most = max(p.most, p.queued)
			p.mu.Unlock()
			work <- panelReq{on: *m.On, at: time.Now()}
		}
		close(work)
	}()
	go func() {
		defer outW.Close()
		for r := range work {
			time.Sleep(p.took)
			p.mu.Lock()
			r.was = p.on
			if p.fail != nil {
				r.err = p.fail(len(p.reqs)+1, r.on)
			}
			if r.err == "" {
				p.on = r.on
				if p.warn != nil {
					r.warn = p.warn(len(p.reqs)+1, r.on)
				}
			}
			r.done = time.Now()
			p.reqs = append(p.reqs, r)
			p.queued--
			on := p.on
			p.mu.Unlock()
			raw, _ := json.Marshal(map[string]any{"t": "power", "on": on, "err": r.err, "warn": r.warn})
			if _, err := fmt.Fprintf(outW, "%s\n", raw); err != nil {
				return
			}
		}
	}()
	go g.read(outR, c.frameVersions())
	t.Cleanup(func() {
		_ = inW.Close()
		<-g.done
	})
	_ = c.onLoop(func() error {
		c.gui = g
		return nil
	})
	t.Cleanup(func() { _ = c.onLoop(func() error { c.gui = nil; return nil }) })
	return p
}

func (p *fakePanel) requests() []panelReq {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]panelReq(nil), p.reqs...)
}

func (p *fakePanel) state() (on bool, queued, most int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.on, p.queued, p.most
}

// powerPress is the power key's short press from the renderer, on the loop.
func powerPress(c *Core) { _ = c.onLoop(func() error { c.Press(Key{Name: KeyPower}); return nil }) }

// settled waits until the Shell's screen, the renderer's panel and the
// backlight agree and nothing is on its way to the renderer: the screen
// off with the panel and the light off, or on with both on.
func settled(t *testing.T, c *Core, p *fakePanel, sim *backlight.Sim, within time.Duration) (off bool) {
	t.Helper()
	var why string
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		var shellOff, shellPanel bool
		_ = c.onLoop(func() error { shellOff, shellPanel = c.ScreenOff(), c.screen.panel; return nil })
		on, queued, _ := p.state()
		_, lit := sim.Level()
		if queued == 0 && on == !shellOff && shellPanel == on && lit == on {
			return shellOff
		}
		why = fmt.Sprintf("screen off %v, the Shell's panel %v, the panel %v (%d requests waiting), backlight lit %v", shellOff, shellPanel, on, queued, lit)
	}
	t.Fatalf("not settled in %s: %s; requests %+v", within, why, p.requests())
	return false
}

func panelCore(t *testing.T, took time.Duration) (*Core, *fakePanel, *backlight.Sim) {
	t.Helper()
	c := loopCore(t, t.TempDir())
	sim := backlight.NewSim()
	_ = c.onLoop(func() error {
		c.screen.bl = backlight.NewFader(sim)
		return nil
	})
	return c, panelRenderer(t, c, took), sim
}

// Presses a little slower than the backlight's fade out (each one gets to
// turn the panel off) while the panel takes its time: the renderer gets
// one change at a time, never one while another waits, and in the end the
// panel is as the last press left the screen. Before, every press went
// straight to the renderer: it was asked to turn the panel on while it
// was still turning it off.
func TestPanelOneChangeAtATime(t *testing.T) {
	if !alone(t) {
		return
	}
	c, p, sim := panelCore(t, 250*time.Millisecond)
	for i := 0; i < 7; i++ {
		powerPress(c)
		time.Sleep(350 * time.Millisecond)
	}
	if off := settled(t, c, p, sim, 5*time.Second); !off {
		t.Error("seven presses left the screen on")
	}
	if _, _, most := p.state(); most > 1 {
		t.Errorf("the renderer had %d panel changes waiting at once: %+v", most, p.requests())
	}
}

// Presses faster than the panel turns (100 ms apart, the panel takes half
// a second): what the renderer is asked for is only ever a change of the
// panel, the newest wanted state replacing one not sent yet, and the
// panel ends as the last press left the screen. Before, each press that
// lit the screen asked for the panel again, on or not.
func TestPanelRapidPressesCoalesce(t *testing.T) {
	if !alone(t) {
		return
	}
	c, p, sim := panelCore(t, 500*time.Millisecond)
	for i := 0; i < 9; i++ {
		powerPress(c)
		time.Sleep(100 * time.Millisecond)
	}
	if off := settled(t, c, p, sim, 5*time.Second); !off {
		t.Error("nine presses left the screen on")
	}
	reqs := p.requests()
	for _, r := range reqs {
		if r.on == r.was {
			t.Errorf("the renderer was asked to turn the panel %v while it was: %+v", onOff(r.on), reqs)
			break
		}
	}
	if len(reqs) > 2 {
		t.Errorf("%d panel changes for nine presses 100 ms apart: %+v", len(reqs), reqs)
	}
	for i := 0; i < 5; i++ {
		powerPress(c)
		time.Sleep(100 * time.Millisecond)
	}
	if off := settled(t, c, p, sim, 5*time.Second); off {
		t.Error("five more presses left the screen off")
	}
}

// After the panel went off, the next change waits until the panel has
// settled for panelSettle; it is never toggled faster than that.
func TestPanelSettlesBetweenChanges(t *testing.T) {
	if !alone(t) {
		return
	}
	c, p, sim := panelCore(t, 20*time.Millisecond)
	powerPress(c)
	for end := time.Now().Add(3 * time.Second); len(p.requests()) == 0 && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	powerPress(c)
	settled(t, c, p, sim, 5*time.Second)
	reqs := p.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests %+v, want off then on", reqs)
	}
	if gap := reqs[1].at.Sub(reqs[0].done); gap < panelSettle-20*time.Millisecond {
		t.Errorf("the panel went on %s after it went off, want at least %s", gap.Round(time.Millisecond), panelSettle)
	}
}

// The panel fails to come on (the driver says no): the Shell tries once
// more and the screen comes on. Before, the screen stayed black with the
// Shell sure it was on, until two more presses.
func TestPanelOnRetriedOnce(t *testing.T) {
	if !alone(t) {
		return
	}
	c, p, sim := panelCore(t, 20*time.Millisecond)
	p.fail = func(n int, on bool) string {
		if n == 2 {
			return "set_crtc: Invalid argument"
		}
		return ""
	}
	powerPress(c)
	settled(t, c, p, sim, 3*time.Second)
	powerPress(c)
	if off := settled(t, c, p, sim, 3*time.Second); off {
		t.Fatalf("the screen went off: %+v", p.requests())
	}
	if reqs := p.requests(); len(reqs) != 3 || !reqs[2].on {
		t.Errorf("requests %+v, want off, on (failed), on", reqs)
	}
}

// The panel comes on but shows no frame (phone-paint: no page flip
// completed after it came on), as the 6T's did after a suspend on
// 2026-11-22 until a second mode set by hand: the Shell turns it off and
// on once more, the backlight waiting for that, and leaves it on if it
// warns again. Before, it only logged the warning and lit the backlight
// over a black panel.
func TestPanelOnWithAWarningOffAndOnAgain(t *testing.T) {
	if !alone(t) {
		return
	}
	for _, again := range []bool{false, true} {
		c, p, sim := panelCore(t, 20*time.Millisecond)
		p.warn = func(n int, on bool) string {
			if on && (n == 2 || again) {
				return "no page flip completed in 500 ms after the panel came on"
			}
			return ""
		}
		powerPress(c)
		settled(t, c, p, sim, 3*time.Second)
		powerPress(c)
		var litAt time.Time
		for end := time.Now().Add(3 * time.Second); litAt.IsZero() && time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
			if _, lit := sim.Level(); lit {
				litAt = time.Now()
			}
		}
		if off := settled(t, c, p, sim, 3*time.Second); off {
			t.Fatalf("the screen went off: %+v", p.requests())
		}
		var ons []bool
		for _, r := range p.requests() {
			ons = append(ons, r.on)
		}
		if want := []bool{false, true, false, true}; !slices.Equal(ons, want) {
			t.Errorf("warning again %v: panel requests %v, want off, on (no frame), off, on", again, ons)
		}
		if reqs := p.requests(); len(reqs) == 4 && litAt.Before(reqs[3].done) {
			t.Errorf("the backlight lit %s before the panel came on the second time", reqs[3].done.Sub(litAt))
		}
	}
}

// The panel will not come on at all: after the second failure the Shell's
// screen is off as the panel is (its backlight off too), and the next
// press tries again from the start.
func TestPanelOnFailsTwice(t *testing.T) {
	if !alone(t) {
		return
	}
	c, p, sim := panelCore(t, 20*time.Millisecond)
	p.fail = func(n int, on bool) string {
		if on && n <= 3 {
			return "set_crtc: Connection timed out"
		}
		return ""
	}
	powerPress(c)
	settled(t, c, p, sim, 3*time.Second)
	powerPress(c)
	if off := settled(t, c, p, sim, 3*time.Second); !off {
		t.Fatal("the Shell kept the screen on with the panel off")
	}
	powerPress(c)
	if off := settled(t, c, p, sim, 3*time.Second); off {
		t.Fatalf("the next press did not light it: %+v", p.requests())
	}
}

// Lighting up, the backlight waits for the panel to be on: it does not
// shine on a panel that is still off or powering up (on the 6T the panel
// driver owns the backlight: writing it then races the panel's power
// sequence), and comes on with the renderer's answer.
func TestBacklightWaitsForThePanel(t *testing.T) {
	if !alone(t) {
		return
	}
	c, p, sim := panelCore(t, 300*time.Millisecond)
	powerPress(c)
	settled(t, c, p, sim, 3*time.Second)
	powerPress(c)
	if _, lit := sim.Level(); lit {
		t.Fatal("the backlight lit before the panel came on")
	}
	time.Sleep(150 * time.Millisecond)
	if _, lit := sim.Level(); lit {
		t.Fatal("the backlight lit while the panel was powering up")
	}
	if off := settled(t, c, p, sim, 3*time.Second); off {
		t.Fatal("the screen did not come on")
	}
}

// A renderer that does not answer (stuck in the kernel) leaves the Shell
// answering keys at once; when it answers, the panel goes the way the
// last press left the screen.
func TestPanelStuckRendererKeepsTheShellResponsive(t *testing.T) {
	if !alone(t) {
		return
	}
	c, p, sim := panelCore(t, 2*time.Second)
	for i := 0; i < 6; i++ {
		t0 := time.Now()
		powerPress(c)
		if d := time.Since(t0); d > 200*time.Millisecond {
			t.Fatalf("press %d took the Shell %s", i+1, d)
		}
		time.Sleep(400 * time.Millisecond)
	}
	if off := settled(t, c, p, sim, 8*time.Second); off {
		t.Error("six presses left the screen off")
	}
}
