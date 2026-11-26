package shell

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/hal/input"
	"github.com/kario-os/kario/ui"
)

// TouchEnv, when set, says whoever runs the selftest touches the board's
// touchscreen when it asks (the touchscreen step): QEMU's harness, through
// QMP into its virtio multitouch device (yocto/qemu-buttons.py). The
// cases that need it skip where it is not set.
const TouchEnv = "PHONE_SELFTEST_TOUCH"

// A gesture of the touch steps: a finger down at the first point, held
// for a long press or moved to the second over ms, and lifted.
type gesture struct {
	kind string
	pts  [][2]float64
	ms   int
}

// touchSize is the frame in logical pixels, as the touch steps aim.
func (r *Runner) touchSize() (w, h float64) {
	d := r.C.screenDev
	s := d.Scale
	if s <= 0 {
		s = 1
	}
	return float64(d.Width) / s, float64(d.Height) / s
}

// gestureOf reads tap <at> | long <at> | swipe <x0> <y0> <x1> <y1> [ms],
// <at> being <x> <y> (logical pixels; a negative one from the right or the
// bottom edge, n% that much of the width or height) or row <text> (the
// middle of the body row whose title holds text, as the renderer laid it
// out) or bar <n>% (that far along the page's progress bar).
func (r *Runner) gestureOf(arg string) (gesture, error) {
	usage := fmt.Errorf("tap <x> <y> | tap row <text> | tap bar <n>%% | long <x> <y> | long row <text> | swipe <x0> <y0> <x1> <y1> [ms]")
	kind, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	rest = strings.TrimSpace(rest)
	w, h := r.touchSize()
	num := func(s string, edge float64) (float64, bool) {
		pct, ok := strings.CutSuffix(s, "%")
		v, err := strconv.ParseFloat(pct, 64)
		switch {
		case err != nil:
			return 0, false
		case ok:
			v = v / 100 * edge
		case v < 0:
			v += edge
		}
		return v, true
	}
	switch kind {
	case "tap", "long":
		g := gesture{kind: kind}
		if text, ok := strings.CutPrefix(rest, "row "); ok {
			at, err := r.rowAt(strings.TrimSpace(text))
			if err != nil {
				return g, err
			}
			g.pts = [][2]float64{at}
			return g, nil
		}
		if pct, ok := strings.CutPrefix(rest, "bar "); ok {
			n, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(pct), "%"), 64)
			if err != nil || n < 0 || n > 100 {
				return g, usage
			}
			at, err := r.barAt(n / 100)
			if err != nil {
				return g, err
			}
			g.pts = [][2]float64{at}
			return g, nil
		}
		f := strings.Fields(rest)
		if len(f) != 2 {
			return g, usage
		}
		x, ok1 := num(f[0], w)
		y, ok2 := num(f[1], h)
		if !ok1 || !ok2 {
			return g, usage
		}
		g.pts = [][2]float64{{x, y}}
		return g, nil
	case "swipe":
		f := strings.Fields(rest)
		if len(f) != 4 && len(f) != 5 {
			return gesture{}, usage
		}
		g := gesture{kind: kind, ms: 300}
		for i := 0; i < 4; i += 2 {
			x, ok1 := num(f[i], w)
			y, ok2 := num(f[i+1], h)
			if !ok1 || !ok2 {
				return g, usage
			}
			g.pts = append(g.pts, [2]float64{x, y})
		}
		if len(f) == 5 {
			n, err := strconv.Atoi(f[4])
			if err != nil || n < 0 {
				return g, usage
			}
			g.ms = n
		}
		return g, nil
	}
	return gesture{}, usage
}

// rowAt is the middle of the body row whose title holds text, in the
// renderer's last layout of the frame on screen (its feature layout).
func (r *Runner) rowAt(text string) ([2]float64, error) {
	w, _ := r.touchSize()
	return r.bodyAt("row "+text, func(v *View, i int, ids []string, l *Layout) (float64, bool) {
		k := v.indexOf(ids[0])
		return w / 2, len(ids) == 1 && k >= 0 && strings.Contains(v.S.Body[k].Title, text)
	})
}

// barAt is the point frac of the way along the page's progress bar, in its
// middle.
func (r *Runner) barAt(frac float64) ([2]float64, error) {
	return r.bodyAt("bar", func(v *View, i int, _ []string, l *Layout) (float64, bool) {
		k := -1
		if i < len(v.frameBody) {
			k = v.frameBody[i]
		}
		return l.Left + frac*l.Width, k >= 0 && k < len(v.S.Body) && v.S.Body[k].Kind == ui.KindProgress && l.Width > 0
	})
}

// bodyAt is the x find gives at the middle of the first body node of the
// renderer's last layout that it takes.
func (r *Runner) bodyAt(what string, find func(v *View, i int, ids []string, l *Layout) (float64, bool)) ([2]float64, error) {
	_, h := r.touchSize()
	r.drawNow()
	var at [2]float64
	var why error
	if _, ok := r.waitFor(func(string) bool {
		v := r.C.current().top()
		l := r.C.layout
		switch {
		case v == nil:
			why = fmt.Errorf("%s: no screen", what)
		case l == nil || l.Screen != v.frameScreen || len(l.Nodes) != len(v.frameIDs) || l.Top <= 0:
			why = fmt.Errorf("%s: the renderer has not laid the screen out (a pixel renderer's layout: require-touch)", what)
		default:
			why = fmt.Errorf("%s: no such row on the screen", what)
			for i, ids := range v.frameIDs {
				if len(ids) == 0 {
					continue
				}
				x, ok := find(v, i, ids, l)
				if !ok {
					continue
				}
				at = [2]float64{x, l.Top + l.Nodes[i].Y - l.Offset + l.Nodes[i].H/2}
				if at[1] <= l.Top || at[1] >= h-touchStep {
					why = fmt.Errorf("%s: not on the screen (at %.0f)", what, at[1])
					return false
				}
				return true
			}
		}
		return false
	}, 3000); !ok {
		return at, why
	}
	return at, nil
}

// touchReady waits as a person does before touching: for the boot screen
// to go, and a dialog that just rose to take answers (r.press's wait).
func (r *Runner) touchReady() {
	for end := time.Now().Add(2 * bootDuration); r.C.BootProgress() >= 0 && time.Now().Before(end); {
		r.pump(50 * time.Millisecond)
	}
	if d := r.C.overlayArmLeft(); d > 0 {
		r.pump(d)
	}
	r.drawNow()
}

// play gives the renderer one of the finger's phases and lets the Shell
// take its answer.
func (r *Runner) play(phase string, x, y float64) error {
	if r.GUI == nil {
		return fmt.Errorf("touch: no pixel renderer (require-touch)")
	}
	if err := r.GUI.PlayTouch(phase, x, y); err != nil {
		return err
	}
	r.pump(20 * time.Millisecond)
	return nil
}

// touch <gesture>: a finger on the screen as a person moves it, played to
// the pixel renderer, which takes it as from its touchscreen and says what
// it came down on, as phone-paint does on a phone (touch.go).
func (r *Runner) touch(arg string) error {
	g, err := r.gestureOf(arg)
	if err != nil {
		return err
	}
	r.touchReady()
	p := g.pts[0]
	if err := r.play("down", p[0], p[1]); err != nil {
		return err
	}
	switch g.kind {
	case "tap":
		r.pump(50 * time.Millisecond)
	case "long":
		r.pump(r.C.touchLong() + 200*time.Millisecond)
	case "swipe":
		q := g.pts[1]
		const steps = 8
		for i := 1; i <= steps; i++ {
			f := float64(i) / steps
			r.pump(time.Duration(g.ms/steps) * time.Millisecond)
			p = [2]float64{g.pts[0][0] + (q[0]-g.pts[0][0])*f, g.pts[0][1] + (q[1]-g.pts[0][1])*f}
			if err := r.play("move", p[0], p[1]); err != nil {
				return err
			}
		}
	}
	if err := r.play("up", p[0], p[1]); err != nil {
		return err
	}
	r.pump(40 * time.Millisecond)
	return nil
}

// touchscreen <gesture> [ms]: the same made on the board's own touchscreen
// when asked on the output (PHONE-SELFTEST-TOUCH: <n> tap|long|swipe and
// the points in ten-thousandths of the screen's width and height, a long
// press's or a swipe's milliseconds; the harness watches for it), read
// from the kernel's touchscreen device, whose axes span the frame (the
// emulator's), and played to the renderer as touch plays a gesture: the
// kernel's device, its driver and the decoding of its events tested on
// the way.
func (r *Runner) touchscreen(arg string) error {
	g, err := r.gestureOf(arg)
	if err != nil {
		return err
	}
	nodes := input.Touchscreens()
	if len(nodes) == 0 {
		return fmt.Errorf("touchscreen: no input device is a touchscreen")
	}
	r.touchReady()
	w, h := r.touchSize()
	along := func(v int32, rg input.Range, of float64) float64 {
		return (float64(v-rg.Min) + 0.5) / (float64(rg.Max-rg.Min) + 1) * of
	}
	fingers := make(chan fingerAt, 64)
	for _, node := range nodes {
		tw, err := input.WatchTouch(node)
		if err != nil {
			return fmt.Errorf("touchscreen %s: %w", node, err)
		}
		defer tw.Close()
		go func() {
			for ct := range tw.C {
				fingers <- fingerAt{ct.Phase, along(ct.X, tw.X, w), along(ct.Y, tw.Y, h)}
			}
		}()
	}
	part := func(v, of float64) int { return int(math.Round(math.Max(0, math.Min(v/of, 1)) * 10000)) }
	var pts []string
	for _, p := range g.pts {
		pts = append(pts, strconv.Itoa(part(p[0], w)), strconv.Itoa(part(p[1], h)))
	}
	ms := g.ms
	if g.kind == "long" {
		ms = int((r.C.touchLong() + 300*time.Millisecond).Milliseconds())
	}
	r.touches++
	line := fmt.Sprintf("PHONE-SELFTEST-TOUCH: %d %s %s", r.touches, g.kind, strings.Join(pts, " "))
	if g.kind != "tap" {
		line += fmt.Sprintf(" %d", ms)
	}
	fmt.Fprintln(r.Out, line)
	if err := r.follow(g, fingers, 20000); errors.Is(err, errUntouched) {
		return fmt.Errorf("touchscreen: not touched (%s)", strings.Join(nodes, ", "))
	} else if err != nil {
		return err
	}
	r.pump(40 * time.Millisecond)
	return nil
}

// fingerAt is the touchscreen's finger at a report of the device, on the
// frame (logical pixels).
type fingerAt struct {
	phase input.Phase
	x, y  float64
}

var errUntouched = errors.New("touchscreen: not touched")

// follow plays the finger of gesture g to the renderer as the device
// reports it, until it lifts; what comes after is not the step's. A finger
// that comes down further than touchSlop from where g starts is another
// touch (a request the harness answered again): it is left alone until it
// lifts. One still down when the step gives up is let go, or the Shell
// would hold it into a long press.
func (r *Runner) follow(g gesture, fingers <-chan fingerAt, ms int) error {
	start := g.pts[0]
	var last, other fingerAt
	down, lifted, elsewhere := false, false, false
	var failed error
	_, ok := r.waitFor(func(string) bool {
		for !lifted && failed == nil {
			var f fingerAt
			select {
			case f = <-fingers:
			default:
				return false
			}
			switch {
			case f.phase == input.Down && math.Hypot(f.x-start[0], f.y-start[1]) >= touchSlop:
				r.C.Trace("touchscreen: down at %.1f, %.1f, not at %.1f, %.1f: another touch, left alone", f.x, f.y, start[0], start[1])
				other, elsewhere = f, true
				continue
			case f.phase != input.Down && !down:
				continue
			}
			r.C.Trace("touchscreen: %s at %.1f, %.1f", f.phase, f.x, f.y)
			failed = r.play(f.phase.String(), f.x, f.y)
			last, down, lifted = f, true, f.phase == input.Up || f.phase == input.Cancel
		}
		return true
	}, ms)
	switch {
	case failed != nil:
		return failed
	case last.phase == input.Cancel:
		return fmt.Errorf("touchscreen: the device lost events with the finger down")
	case ok:
		return nil
	case down:
		if err := r.play("cancel", last.x, last.y); err != nil {
			return err
		}
		return fmt.Errorf("touchscreen: touched and not let go")
	case elsewhere:
		return fmt.Errorf("touchscreen: touched at %.0f, %.0f, not at %.0f, %.0f", other.x, other.y, start[0], start[1])
	}
	return errUntouched
}
