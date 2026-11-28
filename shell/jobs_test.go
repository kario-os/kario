package shell

import (
	"testing"
	"time"
)

// A job that asked for the charger and runs when it is unplugged is told
// to stop (reason unplugged), as one on an unmetered network is when the
// network goes; its wake ends after the grace, it does not run again on
// the battery and runs again once the phone is plugged in, keeping its
// schedule and its conditions. A job that did not ask, or whose wake is
// over, is not stopped.
func TestChargingJobStops(t *testing.T) {
	c := updateCore(t, "true")
	store := &App{M: &Manifest{ID: "com.phone.store"}, running: true, ready: true, out: make(chan outbound, 64)}
	weather := &App{M: &Manifest{ID: "com.phone.weather"}, running: true, ready: true, out: make(chan outbound, 64)}
	c.apps = []*App{store, weather}
	past := c.now().Add(-2 * time.Hour)
	c.jobs = []*job{{App: "com.phone.store", ID: "updates", Every: 60, Charging: true, Next: past},
		{App: "com.phone.weather", ID: "refresh", Every: 60, Next: past}}
	c.runJobs()
	if runs, _ := c.JobRuns("com.phone.store", "updates"); runs != 0 {
		t.Fatalf("the charging job ran on the battery: %d", runs)
	}
	jobEvents(weather)

	c.SetPower(Power{Level: 82, Charging: true})
	if runs, _ := c.JobRuns("com.phone.store", "updates"); runs != 1 {
		t.Fatalf("plugged in, the job ran %d times", runs)
	}
	if ev := jobEvents(store); len(ev) != 1 || ev[0] != "job updates" {
		t.Fatalf("the store got %q", ev)
	}
	c.jobs[1].Next = past
	c.runJobs()
	jobEvents(weather)

	c.SetPower(Power{Level: 82})
	if c.JobStops("com.phone.store", "updates") != 1 || c.JobStops("com.phone.weather", "refresh") != 0 {
		t.Fatalf("stops: store %d, weather %d", c.JobStops("com.phone.store", "updates"), c.JobStops("com.phone.weather", "refresh"))
	}
	if ev := jobEvents(store); len(ev) != 1 || ev[0] != `job.stop {"id":"updates","reason":"unplugged"}` {
		t.Fatalf("the store got %q", ev)
	}
	if len(jobEvents(weather)) != 0 {
		t.Fatal("the weather's job was stopped")
	}
	c.runJobs()
	if store.bgSince.Before(time.Now().Add(-c.freezeAfter())) {
		t.Fatal("the job's wake ended before the grace")
	}
	j := c.jobs[0]
	j.stopAt = time.Now().Add(-jobStopGrace)
	c.runJobs()
	if !j.until.IsZero() || store.bgSince.After(time.Now().Add(-c.freezeAfter())) {
		t.Fatalf("the job's wake did not end: until %v, the app freezes after %v", j.until, store.bgSince)
	}
	if runs, _ := c.JobRuns("com.phone.store", "updates"); runs != 1 || !j.Retry {
		t.Fatalf("ran again on the battery: %d runs, retry %v", runs, j.Retry)
	}

	c.SetPower(Power{Level: 82, Charging: true})
	if runs, _ := c.JobRuns("com.phone.store", "updates"); runs != 2 || j.Retry || !j.Charging || j.Next.Sub(c.now()) < 59*time.Minute {
		t.Fatalf("plugged in again: %d runs, retry %v, %+v", runs, j.Retry, j)
	}
	if ev := jobEvents(store); len(ev) != 1 || ev[0] != "job updates" {
		t.Fatalf("the store got %q", ev)
	}

	// A run whose wake is over is not stopped.
	j.until = time.Now().Add(-time.Second)
	c.SetPower(Power{Level: 82})
	if j.Stops != 1 || len(jobEvents(store)) != 0 {
		t.Fatalf("a run that is over was stopped: %+v", j)
	}
}
