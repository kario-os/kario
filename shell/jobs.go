package shell

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/kario-os/kario/ui"
)

// Background jobs (Android's JobScheduler / WorkManager, iOS background
// refresh): an app asks to be woken every N minutes (15 at least) to sync.
// Each run has a window, from when it is due to a deadline half a period
// later (at most 30 minutes). Nothing wakes the phone before a deadline:
// when one is reached, every job whose window is open runs in the same
// wake, and so do they when the radio is up anyway (push data just
// arrived). A job may ask to run only on external power; it never forces a
// wake and runs once the phone is plugged in; one may ask for an unmetered
// network (a large download): it waits until the phone is on Wi-Fi that is
// not metered (metered.go); one may ask for a network: it waits until there
// is one its app may use, Wi-Fi while the app's mobile data is blocked in
// the background (background data off, its own limit used up;
// netpolicy.go), as Android's JobScheduler holds a restricted app's jobs.
// A due job wakes or starts its app the same way as push data, within the
// same hourly wake budget, and gets a "job" event; it runs until that wake
// ends.
//
// A job that asked for an unmetered network or a network and runs when
// the phone leaves one it may use (looked at every jobNetWatch while such
// a job runs), or one that asked for the charger and runs when it is
// unplugged, is stopped, as Android's JobScheduler stops it: the app gets
// a "job.stop" event with the reason, and jobStopGrace later the job's
// wake ends (the app freezes, unless it is on the screen or another of its
// jobs still runs). The job keeps its schedule and its conditions, and
// runs again as soon as they hold, waking the phone for it. The Shell's
// own jobs ask for neither; its own large downloads (automatic
// system updates, updateauto.go) stop themselves.

const (
	minJobEvery = 15 * time.Minute
	maxJobFlex  = 30 * time.Minute
	// piggybackAfterPush: network activity this recent means the radio is
	// still up, so due jobs ride along.
	piggybackAfterPush = time.Minute
	// chargingJobWake: a job that waits for the charger may do heavy work
	// (the store's updates), so it runs a minute before the app is frozen
	// again, as iOS gives processing tasks on external power minutes.
	chargingJobWake = time.Minute
	// jobNetWatch: how often the network is looked at while a job that
	// asked for an unmetered one runs; jobStopGrace: how long an app told
	// to stop a job has before the job's wake ends.
	jobNetWatch  = 2 * time.Second
	jobStopGrace = 5 * time.Second
)

type job struct {
	App       string    `json:"app"`
	ID        string    `json:"id"`
	Every     int       `json:"every_min"`
	Charging  bool      `json:"charging,omitempty"`
	Unmetered bool      `json:"unmetered,omitempty"`
	Network   bool      `json:"network,omitempty"`
	Next      time.Time `json:"next"`
	Runs      int       `json:"runs,omitempty"`
	Batch     int       `json:"batch,omitempty"` // the wake it last ran in
	// Retry: stopped for the network or the charger, it runs again once
	// its conditions hold; Stops counts the times.
	Retry bool `json:"retry,omitempty"`
	Stops int  `json:"stops,omitempty"`
	// until: the end of the wake it runs in (zero: it does not run);
	// stopAt: when its app was told to stop it; held: why it waits for
	// its network, though due.
	until, stopAt time.Time
	held          string
}

// jobWatch is the look at the network for the jobs running.
type jobWatch struct {
	at      time.Time
	looking bool
}

func (j *job) every() time.Duration { return time.Duration(j.Every) * time.Minute }

func (j *job) deadline() time.Time { return j.Next.Add(min(j.every()/2, maxJobFlex)) }

func (c *Core) jobsPath() string { return filepath.Join(c.cfg.StateDir, "shell", "jobs.json") }

type jobState struct {
	Jobs    []*job `json:"jobs"`
	Batches int    `json:"batches"`
}

func (c *Core) loadJobs() {
	raw, err := os.ReadFile(c.jobsPath())
	if err != nil {
		return
	}
	var st jobState
	if json.Unmarshal(raw, &st) == nil && st.Jobs != nil {
		c.jobs, c.jobBatches = st.Jobs, st.Batches
		return
	}
	_ = json.Unmarshal(raw, &c.jobs)
}

func (c *Core) saveJobs() {
	raw, _ := json.MarshalIndent(jobState{Jobs: c.jobs, Batches: c.jobBatches}, "", "  ")
	_ = os.MkdirAll(filepath.Dir(c.jobsPath()), 0o700)
	_ = os.WriteFile(c.jobsPath(), raw, 0o600)
}

func (c *Core) scheduleJob(a *App, args ui.JobArgs) error {
	if args.ID == "" || time.Duration(args.EveryMin)*time.Minute < minJobEvery {
		return errors.New("a job needs an id and at least 15 minutes between runs")
	}
	c.cancelJob(a, args.ID)
	c.jobs = append(c.jobs, &job{App: a.M.ID, ID: args.ID, Every: args.EveryMin, Charging: args.Charging, Unmetered: args.Unmetered,
		Network: args.Network, Next: c.now().Add(time.Duration(args.EveryMin) * time.Minute)})
	c.saveJobs()
	return nil
}

func (c *Core) cancelJob(a *App, id string) {
	for i, j := range c.jobs {
		if j.App == a.M.ID && j.ID == id {
			c.jobs = append(c.jobs[:i], c.jobs[i+1:]...)
			c.saveJobs()
			return
		}
	}
}

// runJobs runs a wake's worth of due jobs if anything calls for a wake
// (called from the shell's periodic tick, on plugging in, after push data).
// A job stopped for the network is due at once and calls for a wake.
func (c *Core) runJobs() {
	c.watchJobs()
	now, pw := c.now(), c.power()
	var ready []*job
	wake := false
	for _, j := range c.jobs {
		if now.Before(j.Next) && !j.Retry || !j.stopAt.IsZero() || j.Charging && !pw.Charging {
			continue
		}
		if why := c.jobNetHeld(j); why != "" {
			if why != j.held {
				c.log.Printf("job %s of %s waits: %s", j.ID, j.App, why)
			}
			j.held = why
			continue
		}
		j.held = ""
		ready = append(ready, j)
		if j.Retry || !now.Before(j.deadline()) || j.Charging || now.Sub(c.lastPushAt) < piggybackAfterPush {
			wake = true
		}
	}
	if !wake {
		return
	}
	c.jobBatches++
	// The radio comes up for these jobs: the push connection's keep-alive
	// rides along instead of waking it again later.
	c.radioActive()
	for _, j := range ready {
		j.Next = now.Add(j.every())
		j.Runs++
		j.Batch = c.jobBatches
		j.Retry = false
		if j.App == systemJob {
			c.runSystemJob(j.ID)
			continue
		}
		a := c.appByID(j.App)
		if a == nil {
			continue
		}
		budget := pushWakeBudget
		if j.Charging {
			budget = chargingJobWake
		}
		j.until, j.stopAt = time.Now().Add(budget), time.Time{}
		c.deliverBackground(a, ui.Msg{T: ui.MsgEvent, Event: ui.EventJob, Value: j.ID}, budget, false, "job "+j.ID)
	}
	c.saveJobs()
}

// jobNetHeld says why the job waits for its network ("" it need not), by
// the last look at the network; a look older than meterStale starts
// another for the next time.
func (c *Core) jobNetHeld(j *job) string {
	if !j.Unmetered && !j.Network {
		return ""
	}
	m := &c.radios.meter
	if c.now().Sub(m.at) > meterStale && !m.busy {
		m.busy = true
		c.lookNetwork(func(netNow) { m.busy = false })
	}
	if m.at.IsZero() {
		return "no look at the network yet"
	}
	return c.jobNetWhy(j, m.now)
}

// jobNetWhy says why the network n does not suit the job ("" it does).
func (c *Core) jobNetWhy(j *job, n netNow) string {
	switch {
	case j.Unmetered:
		return unmeteredWhy(n)
	case !j.Network || n.Kind == netWifi:
		return ""
	case n.Kind != netMobile:
		return "no network"
	}
	if a := c.appByID(j.App); a != nil {
		if why := c.mobileBlock(a); why != "" {
			return "on mobile data, blocked for the app (" + why + ")"
		}
	}
	return ""
}

// watchJobs ends the runs whose wake is over, the wakes of the jobs told to
// stop jobStopGrace ago, stops the runs of jobs that asked for the charger
// once it is unplugged, and while a job that asked for an unmetered
// network or a network runs looks at the network every jobNetWatch.
func (c *Core) watchJobs() {
	now, watch, charger := time.Now(), false, false
	for _, j := range c.jobs {
		switch {
		case j.until.IsZero():
		case !now.Before(j.until):
			j.until, j.stopAt = time.Time{}, time.Time{}
		case !j.stopAt.IsZero():
			if now.Sub(j.stopAt) >= jobStopGrace {
				c.endJob(j)
			}
		default:
			charger = charger || j.Charging
			watch = watch || j.Unmetered || j.Network
		}
	}
	if charger && !c.power().Charging {
		c.stopJobs(func(j *job) (string, string) {
			if !j.Charging {
				return "", ""
			}
			return ui.JobStopUnplugged, "the charger was unplugged"
		})
	}
	w := &c.radios.meter.jobs
	if !watch || w.looking || now.Sub(w.at) < jobNetWatch {
		return
	}
	w.at, w.looking = now, true
	c.lookNetwork(func(n netNow) {
		w.looking = false
		c.stopJobs(func(j *job) (string, string) {
			why := c.jobNetWhy(j, n)
			switch {
			case why == "":
				return "", ""
			case n.Kind == netNone:
				return ui.JobStopOffline, why
			case !j.Unmetered:
				return ui.JobStopBlocked, why
			}
			return ui.JobStopMetered, why
		})
	})
}

// stopJobs tells the apps whose running jobs need what the phone no longer
// has (stop gives such a job's reason and why, "" for the others) to stop
// them; the jobs run again once it is back.
func (c *Core) stopJobs(stop func(*job) (reason, why string)) {
	now, stopped := time.Now(), false
	for _, j := range c.jobs {
		if j.until.IsZero() || !now.Before(j.until) || !j.stopAt.IsZero() {
			continue
		}
		reason, why := stop(j)
		if reason == "" {
			continue
		}
		j.stopAt, j.Retry, stopped = now, true, true
		j.Stops++
		c.log.Printf("job %s of %s stopped: %s", j.ID, j.App, why)
		if a := c.appByID(j.App); a != nil && a.running && !a.frozen {
			raw, _ := json.Marshal(ui.JobStop{ID: j.ID, Reason: reason})
			c.deliver(a, ui.Msg{T: ui.MsgEvent, Event: ui.EventJobStop, Value: string(raw)})
		}
	}
	if stopped {
		c.saveJobs()
	}
}

// endJob ends the wake of a job its app was told to stop: the app freezes
// as at the end of a wake, unless it is on the screen or another of its
// jobs still runs.
func (c *Core) endJob(j *job) {
	j.until, j.stopAt = time.Time{}, time.Time{}
	a := c.appByID(j.App)
	if a == nil || !a.running || a == c.fg || c.freezeAfter() < 0 {
		return
	}
	for _, o := range c.jobs {
		if o.App == j.App && !o.until.IsZero() && o.stopAt.IsZero() {
			return
		}
	}
	if end := time.Now().Add(-c.freezeAfter()); a.bgSince.After(end) {
		a.bgSince = end
	}
	c.log.Printf("job %s of %s ended %s after it was told to stop", j.ID, j.App, jobStopGrace)
}

// systemJob owns the Shell's own jobs (the contacts sync, accounts.go):
// they share the apps' wakes and run in the Shell.
const systemJob = ""

// setSystemJob keeps the Shell's job id running every so often, or (every
// 0) not at all.
func (c *Core) setSystemJob(id string, every time.Duration) {
	changed := false
	for i, j := range c.jobs {
		if j.App != systemJob || j.ID != id {
			continue
		}
		if every > 0 && j.every() == every {
			return
		}
		c.jobs = append(c.jobs[:i], c.jobs[i+1:]...)
		changed = true
		break
	}
	if every > 0 {
		c.jobs = append(c.jobs, &job{App: systemJob, ID: id, Every: int(every / time.Minute), Next: c.now().Add(every)})
		changed = true
	}
	if changed {
		c.saveJobs()
	}
}

func (c *Core) runSystemJob(id string) {
	switch id {
	case accountJob:
		c.accountsDue()
	case familyJob:
		c.familyDue()
	}
}

// JobRuns is how often a job ran and in which wake it last did (tests).
func (c *Core) JobRuns(app, id string) (runs, batch int) {
	for _, j := range c.jobs {
		if j.App == app && j.ID == id {
			return j.Runs, j.Batch
		}
	}
	return 0, 0
}

// JobStops is how often a job was stopped for the network or the charger
// (tests).
func (c *Core) JobStops(app, id string) int {
	for _, j := range c.jobs {
		if j.App == app && j.ID == id {
			return j.Stops
		}
	}
	return 0
}

// JobHeld is why a due job waits for its network, "" for none (tests).
func (c *Core) JobHeld(app, id string) string {
	for _, j := range c.jobs {
		if j.App == app && j.ID == id {
			return j.held
		}
	}
	return ""
}
