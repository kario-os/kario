package shell

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/location"
	"github.com/kario-os/kario/ui"
)

// locationStep is the script's location command (headless.go): the world
// the receiver sees. The simulated receiver is moved directly; a named
// pipe's (the emulator's) gets its NMEA from a simulated receiver at the
// far end, which the first "location at" starts.
func (r *Runner) locationStep(arg string) error {
	c := r.C
	c.locInit()
	f := strings.Fields(arg)
	onOff := func(i int) (bool, error) {
		if len(f) <= i || (f[i] != "on" && f[i] != "off") {
			return false, errors.New("on or off")
		}
		return f[i] == "on", nil
	}
	wait := func(i, def int) int {
		if len(f) > i {
			if n, err := strconv.Atoi(f[i]); err == nil {
				return n
			}
		}
		return def
	}
	switch {
	case len(f) >= 3 && f[0] == "at":
		var v [4]float64
		for i := 1; i < len(f) && i <= 4; i++ {
			n, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				return fmt.Errorf("location at <lat> <lon> [<m/s> <course>]: %q", f[i])
			}
			v[i-1] = n
		}
		sim, err := c.locFeeder()
		if err != nil {
			return err
		}
		sim.Set(v[0], v[1], v[2], v[3])
		c.Trace("location at %.5f, %.5f (%s)", v[0], v[1], c.loc.spec)
		return nil
	case len(f) == 2 && f[0] == "sky":
		on, err := onOff(1)
		if err != nil {
			return err
		}
		sim, err := c.locFeeder()
		if err != nil {
			return err
		}
		sim.Sky(on)
		return nil
	case len(f) >= 2 && f[0] == "gnss":
		want, err := onOff(1)
		if err != nil {
			return err
		}
		if _, ok := r.waitFor(func(string) bool {
			return c.loc.running == want && (c.loc.sim == nil || c.loc.sim.On() == want)
		}, wait(2, 8000)); !ok {
			return fmt.Errorf("the receiver is on: %v (simulated end: %v), want %v", c.loc.running, c.loc.sim != nil && c.loc.sim.On(), want)
		}
		return nil
	case len(f) >= 2 && f[0] == "indicator":
		want, err := onOff(1)
		if err != nil {
			return err
		}
		if _, ok := r.waitFor(func(string) bool { return c.locating() == want }, wait(2, 8000)); !ok {
			return fmt.Errorf("location in use: %v, want %v", c.locating(), want)
		}
		return nil
	case len(f) >= 3 && f[0] == "background": // const-check: ok (a step's word, not the lifecycle state)
		want, err := onOff(2)
		if err != nil {
			return err
		}
		var got []string
		if _, ok := r.waitFor(func(string) bool {
			got = nil
			for _, a := range c.locBgUsers() {
				got = append(got, a.M.ID)
			}
			return slices.Contains(got, f[1]) == want
		}, wait(3, 8000)); !ok {
			return fmt.Errorf("using the location in the background: %v, want %s %s", got, f[1], f[2])
		}
		return nil
	case len(f) == 2 && f[0] == "source":
		want := f[1]
		if want == "none" {
			want = ""
		}
		if got := c.gnssKind(); got != want {
			return fmt.Errorf("the receiver is %q (%s), want %s", got, c.loc.spec, f[1])
		}
		return nil
	case len(f) == 2 && f[0] == "use" && (f[1] == location.KindFIFO || f[1] == location.KindSim):
		if c.cfg.SKU.ID != "" {
			return errors.New("a phone's receiver is its SKU's")
		}
		spec := location.Spec{Kind: f[1]}
		if f[1] == location.KindFIFO {
			spec.Path = filepath.Join(c.cfg.RunDir, "gnss.nmea")
		}
		c.locUse(spec)
		c.locSync()
		return nil
	case len(f) == 1 && f[0] == "report":
		l := c.loc.last
		n := c.locNetView()
		fmt.Fprintf(r.Out, "PHONE-LOCATION: receiver %s, %d fixes this run, last %.5f, %.5f ±%.1f m, %d satellites (network %v), error %q; network location %v (%v requests, %v answered on the phone, error %q)\n",
			c.loc.spec, c.loc.fixes, l.Lat, l.Lon, l.AccuracyM, l.Satellites, l.Network, c.loc.err, n["provider"], n["requests"], n["cached"], n["error"])
		return nil
	case len(f) >= 2 && f[0] == "recent":
		mode, ms := "", 2
		if len(f) > 2 && (f[2] == ui.StateBackground || f[2] == ui.StateForeground) {
			mode, ms = f[2], 3
		}
		var got []string
		if _, ok := r.waitFor(func(string) bool {
			got = nil
			found := false
			for _, a := range c.locationView()["recent"].([]locAccess) {
				got = append(got, fmt.Sprintf("%s(bg %v)", a.App, a.BgAt > 0))
				found = found || (a.App == f[1] && (mode == "" || (mode == ui.StateBackground) == (a.BgAt > 0)))
			}
			return found
		}, wait(ms, 3000)); !ok {
			return fmt.Errorf("recent location access: %v, want %s", got, strings.Join(f[1:min(3, len(f))], " "))
		}
		return nil
	case len(f) >= 2 && f[0] == "fix" && (f[1] == "gnss" || f[1] == "network"):
		want := f[1] == "network"
		if _, ok := r.waitFor(func(string) bool { return !c.loc.lastAt.IsZero() && c.loc.last.Network == want }, wait(2, 15000)); !ok {
			return fmt.Errorf("the last fix came from the network: %v, want %v (network location %v)", c.loc.last.Network, want, c.locNetView())
		}
		return nil
	case len(f) == 2 && f[0] == "network":
		on, err := onOff(1)
		if err != nil {
			return err
		}
		_, err = c.locationSetting(ui.SettingArgs{Key: ui.SettingLocationNetwork, Value: map[bool]float64{true: 1}[on]})
		return err
	}
	return errors.New("location at <lat> <lon> [<m/s> <course>] | sky on|off | gnss on|off [ms] | indicator on|off [ms] | background <app-id> on|off [ms] | source <kind>|none | use fifo|sim | recent <app-id> [background|foreground] [ms] | fix gnss|network [ms] | network on|off | report")
}

func (c *Core) gnssKind() string {
	c.locInit()
	return c.loc.spec.Kind
}

func (c *Core) netlocKind() string {
	c.locInit()
	return c.loc.net.kind
}

// locFeeder is the simulated receiver the script moves: the source itself,
// or for a named pipe one writing NMEA into it.
func (c *Core) locFeeder() (*location.Sim, error) {
	switch c.loc.spec.Kind {
	case location.KindSim:
		return c.loc.sim, nil
	case location.KindFIFO:
		if c.loc.feed == nil {
			sim := location.NewSim(time.Now)
			ctx, cancel := context.WithCancel(context.Background())
			c.loc.feed, c.loc.sim = cancel, sim
			c.loc.simNow.Store(sim)
			path := c.loc.spec.Path
			go func() {
				if err := sim.Feed(ctx, path); err != nil {
					c.Post(func() { c.log.Printf("location: feeding %s: %v", path, err) })
				}
			}()
			c.log.Printf("location: the selftest writes NMEA into %s", path)
		}
		return c.loc.sim, nil
	}
	return nil, fmt.Errorf("the receiver is %q: the script cannot move it", c.loc.spec)
}
