package shell

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/kario-os/kario/ui"
)

// Motion (docs/37-motion.md): the pixel renderer animates the change from
// one frame to the next, but only the Shell knows which way the user went,
// so each frame that follows a move says so: a page pushed (deeper, or an
// app opened) or popped (back, or home), a menu or dialog rising or sinking.
// Settings › Animations reduces (no transitions) or turns off all motion.

const (
	transPush = "push"
	transPop  = "pop"
	transRise = "rise"
	transSink = "sink"
)

// FrameTransition is a frame's transition; N tells one from the next.
type FrameTransition struct {
	Kind string `json:"kind"`
	N    uint64 `json:"n"`
}

// place is where a frame is, for telling the way from one to the next.
type place struct {
	screen  string
	app     string
	depth   int
	idle    bool
	boot    bool
	overlay string // the overlay's kind, "" without
}

func (c *Core) placeOf(f *Frame) place {
	p := place{screen: f.Screen, idle: f.Idle != nil, boot: f.Boot != nil}
	if a := c.current(); a != nil {
		p.app, p.depth = a.M.ID, len(a.stack)
	}
	if f.Overlay != nil {
		p.overlay = f.Overlay.Kind
	}
	return p
}

// risingOverlay: menus and dialogs rise; full-screen overlays (the
// viewfinder, an incoming call, the alarm) and pickers just appear.
func risingOverlay(kind string) bool {
	switch kind {
	case "", "scan", "capture", "picker", "incoming", "alarm":
		return false
	}
	return true
}

// transitionFor is how cur follows prev: "" for none.
func transitionFor(prev, cur place) string {
	switch {
	case prev.screen == "" && !prev.idle, prev.boot, cur.boot:
		return ""
	case prev.overlay == "" && risingOverlay(cur.overlay):
		return transRise
	case cur.overlay == "" && risingOverlay(prev.overlay):
		return transSink
	case prev.overlay != "" || cur.overlay != "":
		return ""
	case prev.screen == cur.screen && prev.idle == cur.idle:
		return ""
	case cur.idle:
		return transPop
	case prev.idle, prev.app != cur.app, cur.depth > prev.depth:
		return transPush
	case cur.depth < prev.depth:
		return transPop
	}
	return ""
}

// An overlay's cycle: the scan line's, a ringing call's pulse, an alarm's
// (or an emergency alert's) pulse.
const (
	scanCycle  = 2400 * time.Millisecond
	ringCycle  = 1500 * time.Millisecond
	alarmCycle = time.Second
)

// overlayPhase is an overlay's phase (0…1) a cycle every period since
// start, and the period in ms for the frame. A renderer that runs the
// phase itself (feature "phase") gets its start, 0, and the period, so the
// frame stays the same while the phase runs and none is sent for it;
// others get the phase now and no period.
func (c *Core) overlayPhase(start time.Time, period time.Duration) (float64, int) {
	ms := period.Milliseconds()
	if c.renderer != nil && c.renderer.Has("phase") {
		return 0, int(ms)
	}
	return float64(time.Since(start).Milliseconds()%ms) / float64(ms), 0
}

// Settings › Animations: ui.MotionLevels, the setting taking the index.
const (
	motionReduced = ui.MotionReduced
	motionOff     = ui.MotionOff
)

// lowBatteryMotion: below this battery level, not charging, standard motion
// is reduced.
const lowBatteryMotion = 15

// motion is what the renderer is told: the setting, reduced on a low battery.
func (c *Core) motion() string {
	if m := c.settings.Motion; m != "" {
		return m
	}
	if p := c.power(); !p.Simulated && !p.Charging && p.Level > 0 && p.Level < lowBatteryMotion {
		return motionReduced
	}
	return ""
}

func (c *Core) setMotion(v float64) error {
	i := int(v)
	if float64(i) != v || i < 0 || i >= len(ui.MotionLevels) {
		return errBadMotion
	}
	c.settings.Motion = ui.MotionLevels[i]
	return nil
}

var errBadMotion = errors.New("motion: 0 standard, 1 reduced, 2 off")

// MotionStepsEnv names the file a pixel renderer (phone-paint, headless)
// times the live display's steps into: each frame it gets drawn as on a
// panel, each step of the motion it starts a period of the panel's rate
// later (the last "rate", else 60 Hz), a line a frame ({"draw": [ms...],
// "transition": [ms...]}, docs/37-motion.md §5).
const MotionStepsEnv = "PHONE_PAINT_STEPS"

// lateStepMS: a step that takes longer comes a frame late at 60 Hz.
const lateStepMS = 25

// MotionReport sums up a selftest's MotionStepsEnv file: the 50% and 95%
// step times and the steps later than lateStepMS. It reports; it judges
// nothing, so a slow or busy machine fails no run.
func MotionReport(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var draws, trans []float64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var l struct{ Draw, Transition []float64 }
		// A renderer killed mid-line (the watchdog's case) leaves half of one.
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			draws, trans = append(draws, l.Draw...), append(trans, l.Transition...)
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if len(draws) == 0 {
		return "", errors.New("no steps timed")
	}
	late := 0
	for _, v := range append(append([]float64(nil), draws...), trans...) {
		if v > lateStepMS {
			late++
		}
	}
	q := func(v []float64, p float64) float64 {
		if len(v) == 0 {
			return 0
		}
		s := append([]float64(nil), v...)
		sort.Float64s(s)
		return s[int(math.Round(float64(len(s)-1)*p))]
	}
	return fmt.Sprintf("%d steps drawn as on a panel (p50 %.2f ms, p95 %.2f ms, max %.2f ms), %d transition steps (p50 %.2f ms, p95 %.2f ms), %d later than %d ms; this machine's times, not checked",
		len(draws), q(draws, 0.5), q(draws, 0.95), q(draws, 1), len(trans), q(trans, 0.5), q(trans, 0.95), late, lateStepMS), nil
}
