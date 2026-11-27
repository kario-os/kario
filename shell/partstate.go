package shell

import (
	"reflect"
	"time"
)

// partState is what a part of the script leaves in the Shell that the
// next part in the same Shell (the whole script, the emulator's --parts)
// must not find: the clock above all, which timeshift, the simulated sleep
// and clock-at move, but also the battery, the light sensor and the
// toggles the runner starts with. Each part starts from the boot's and
// gets its own back where the script returns to it, as when it runs alone
// on a stack of its own (SelectParts). Jobs are not a part's: they keep
// their waits (shiftJobs).
type partState struct {
	Skew        time.Duration `json:"skew"`
	Power       *Power        `json:"power,omitempty"`
	Lux         float64       `json:"lux"`
	FreezeAfter time.Duration `json:"freeze_after"`
	ScreenAuto  bool          `json:"screen_auto,omitempty"`
	Sleep       bool          `json:"sleep,omitempty"`
	Picker      bool          `json:"picker,omitempty"`
}

func (r *Runner) partState() partState {
	c := r.C
	st := partState{Skew: c.clockSkew, FreezeAfter: c.cfg.FreezeAfter, ScreenAuto: c.screen.auto, Sleep: c.sleep.allowed, Picker: !c.nfc.noPicker}
	if c.powerOverride != nil {
		p := *c.powerOverride
		st.Power = &p
	}
	if c.screen.alsSim {
		st.Lux, _ = c.screen.als.Lux()
	}
	return st
}

// enterPart goes on in part name: the part left keeps what it left for
// its next lines.
func (r *Runner) enterPart(name string) {
	if name == r.part {
		return
	}
	if r.parts == nil {
		r.parts = map[string]partState{}
	}
	cur := r.partState()
	r.parts[r.part] = cur
	st, ok := r.parts[name]
	if !ok {
		st = r.bootState
	}
	r.part = name
	if reflect.DeepEqual(st, cur) {
		return
	}
	c := r.C
	c.Trace("selftest: part %s: clock %v ahead, power %v, lux %v, freeze after %v, screen-auto %v, sleep %v, picker %v",
		name, st.Skew, st.Power, st.Lux, st.FreezeAfter, st.ScreenAuto, st.Sleep, st.Picker)
	c.shiftJobs(st.Skew - cur.Skew)
	c.setClockSkew(st.Skew)
	c.restorePower(st.Power)
	if st.Lux != cur.Lux {
		c.SetLux(st.Lux)
	}
	c.SetFreezeAfter(st.FreezeAfter)
	if st.ScreenAuto != cur.ScreenAuto {
		c.SetScreenAuto(st.ScreenAuto)
	}
	if st.Sleep != cur.Sleep {
		c.SetSleep(st.Sleep)
	}
	if st.Picker != cur.Picker {
		c.SetEndKeyPicker(st.Picker)
	}
	r.pump(300 * time.Millisecond)
}

// shiftJobs moves the jobs' next runs with the clock, which a part
// boundary moves by d with no time passing: a job scheduled on another
// part's clock (an app started there) waits as long as it would have
// there, rather than coming due at once or days late.
func (c *Core) shiftJobs(d time.Duration) {
	if d == 0 || len(c.jobs) == 0 {
		return
	}
	for _, j := range c.jobs {
		j.Next = j.Next.Add(d)
	}
	c.saveJobs()
}

// restorePower puts back a battery saved with SetPower, or (nil) the
// machine's.
func (c *Core) restorePower(p *Power) {
	charging := c.power().Charging
	c.powerOverride = nil
	if p != nil {
		q := *p
		c.powerOverride = &q
	}
	if c.power().Charging != charging {
		c.runJobs()
	}
}
