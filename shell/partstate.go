package shell

import (
	"maps"
	"reflect"
	"slices"
	"time"
)

// partState is what a part of the script leaves in the Shell that the
// next part in the same Shell (the whole script, the emulator's --parts)
// must not find: the clock above all, which timeshift, the simulated sleep
// and clock-at move, but also the battery, the light sensor, the toggles
// the runner starts with and the jobs scheduled in it (the apps', the
// Shell's own). Each part starts from the boot's and gets its own back
// where the script returns to it, as when it runs alone on a stack of its
// own (SelectParts).
type partState struct {
	Skew        time.Duration `json:"skew"`
	Power       *Power        `json:"power,omitempty"`
	Lux         float64       `json:"lux"`
	FreezeAfter time.Duration `json:"freeze_after"`
	ScreenAuto  bool          `json:"screen_auto,omitempty"`
	Sleep       bool          `json:"sleep,omitempty"`
	Picker      bool          `json:"picker,omitempty"`
	Jobs        []partJob     `json:"jobs,omitempty"`
}

// partJob is a job a part left, with the wait it had left there: while
// another part runs it neither comes due nor wakes the phone, though it
// was due when its part was left.
type partJob struct {
	Job  job           `json:"job"`
	Wait time.Duration `json:"wait"`
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
// its next lines. A part starting for the first time gets the jobs the
// boot left.
func (r *Runner) enterPart(name string) {
	if name == r.part {
		return
	}
	if r.parts == nil {
		r.parts = map[string]partState{}
	}
	c := r.C
	cur := r.partState()
	cur.Jobs = c.putJobsAway()
	r.parts[r.part] = cur
	st, ok := r.parts[name]
	if !ok {
		st = r.bootState
		st.Jobs = r.parts[""].Jobs
	}
	r.part, r.front, r.partAt = name, c.fg, time.Now()
	jobs := st.Jobs
	st.Jobs, cur.Jobs = nil, nil
	if reflect.DeepEqual(st, cur) {
		c.jobsBack(jobs)
		return
	}
	c.Trace("selftest: part %s: clock %v ahead, power %v, lux %v, freeze after %v, screen-auto %v, sleep %v, picker %v, %d jobs",
		name, st.Skew, st.Power, st.Lux, st.FreezeAfter, st.ScreenAuto, st.Sleep, st.Picker, len(jobs))
	c.setClockSkew(st.Skew)
	c.jobsBack(jobs)
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

// putJobsAway takes the jobs out of the scheduler, each with its wait on
// the clock now.
func (c *Core) putJobsAway() []partJob {
	if len(c.jobs) == 0 {
		return nil
	}
	now := c.now()
	out := make([]partJob, len(c.jobs))
	for i, j := range c.jobs {
		out[i] = partJob{Job: *j, Wait: j.Next.Sub(now)}
	}
	c.jobs = nil
	c.saveJobs()
	return out
}

// jobsBack gives the scheduler jobs a part left, each due its wait on from
// now.
func (c *Core) jobsBack(jobs []partJob) {
	if len(jobs) == 0 {
		return
	}
	now := c.now()
	for _, p := range jobs {
		j := p.Job
		j.Next = now.Add(p.Wait)
		c.jobs = append(c.jobs, &j)
	}
	c.saveJobs()
}

// takeJobs: an app that comes to the front in a part, running since
// before the runner went on in it (another part started it), schedules no
// jobs in this part, as starting it here would have: the part takes those
// another part left it, each due a period on, as job.schedule makes them.
func (r *Runner) takeJobs() {
	c := r.C
	a := c.fg
	if a == r.front {
		return
	}
	r.front = a
	if a == nil || !a.running || !a.started.Before(r.partAt) || r.part == "" ||
		slices.ContainsFunc(c.jobs, func(j *job) bool { return j.App == a.M.ID }) {
		return
	}
	var took []string
	for _, p := range slices.Sorted(maps.Keys(r.parts)) {
		if p == r.part || p == "" {
			continue
		}
		for _, o := range r.parts[p].Jobs {
			if j := o.Job; j.App == a.M.ID && !slices.Contains(took, j.ID) {
				took = append(took, j.ID)
				c.jobs = append(c.jobs, &job{App: j.App, ID: j.ID, Every: j.Every, Charging: j.Charging, Unmetered: j.Unmetered,
					Network: j.Network, Next: c.now().Add(j.every())})
			}
		}
	}
	if len(took) > 0 {
		c.Trace("selftest: part %s: %s, started in another part, takes its jobs %v", r.part, a.M.ID, took)
		c.saveJobs()
	}
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
