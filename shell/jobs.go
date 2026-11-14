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
// wake and runs once the phone is plugged in. A due job wakes or starts its
// app the same way as push data, within the same hourly wake budget, and
// gets a "job" event.

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
)

type job struct {
	App      string    `json:"app"`
	ID       string    `json:"id"`
	Every    int       `json:"every_min"`
	Charging bool      `json:"charging,omitempty"`
	Next     time.Time `json:"next"`
	Runs     int       `json:"runs,omitempty"`
	Batch    int       `json:"batch,omitempty"` // the wake it last ran in
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
	c.jobs = append(c.jobs, &job{App: a.M.ID, ID: args.ID, Every: args.EveryMin, Charging: args.Charging,
		Next: c.now().Add(time.Duration(args.EveryMin) * time.Minute)})
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
func (c *Core) runJobs() {
	now, pw := c.now(), c.power()
	var ready []*job
	wake := false
	for _, j := range c.jobs {
		if now.Before(j.Next) || j.Charging && !pw.Charging {
			continue
		}
		ready = append(ready, j)
		if !now.Before(j.deadline()) || j.Charging || now.Sub(c.lastPushAt) < piggybackAfterPush {
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
		a := c.appByID(j.App)
		if a == nil {
			continue
		}
		budget := pushWakeBudget
		if j.Charging {
			budget = chargingJobWake
		}
		c.deliverBackground(a, ui.Msg{T: ui.MsgEvent, Event: ui.EventJob, Value: j.ID}, budget, false, "job "+j.ID)
	}
	c.saveJobs()
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
