package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/ui"
)

// Automatic system updates, as Android installs them in the background:
// with Settings › System update › Automatic updates on (a product turns it
// on by default in its profile's settings), the shell checks the update
// server once a day and installs what it offers while the phone is on
// Wi-Fi that is not metered (never mobile data, metered.go), has the
// battery at least autoUpdateBattery percent full or is on the charger,
// and is not in a call. RAUC, and the
// incremental bundle's applier under it, run at a low CPU and I/O priority
// (rauc.service's drop-in, phone-priority.conf). A notification then offers
// to restart now or tonight: between nightFrom and nightTo o'clock, with
// the screen off, no call and no alarm due within nightAlarmMargin.
//
// When the conditions go away while it installs (a call, the charger out
// with the battery low, a metered network), the install stops: RAUC can
// neither pause nor cancel, so the install fails on purpose, leaving what
// it wrote. An incremental bundle's applier stops at a checkpoint once
// the stop file appears (delta-hook gives it --stop); a full bundle's
// download, which an automatic install takes through the shell
// (updateproxy.go), ends as when the network goes, and so does the
// download under an applier that does not stop within updateStopGrace.
// The update waits, paused, and goes on once the conditions hold again:
// the incremental bundle resumes at its checkpoint, and the full bundle's
// adaptive install takes the blocks it already wrote from the slot. The
// slot written is never bootable meanwhile: RAUC marks it bad before
// writing and primary only after an install that succeeded, and the hooks
// remove its descriptor before it is written.

const (
	autoUpdateEvery = 24 * time.Hour
	// autoUpdateRetry: after a check that failed or found the phone not
	// meeting the conditions.
	autoUpdateRetry   = time.Hour
	autoUpdateBattery = 50
	nightFrom         = 2
	nightTo           = 5
	nightAlarmMargin  = time.Hour
	updateNotif       = "system-update"
	// While an automatic install runs, the network is looked at this
	// often; a paused one checks its conditions this often.
	autoWatchNetwork = 30 * time.Second
	autoPauseCheck   = time.Minute
	// updateStopGrace: how long an applier asked to stop has before the
	// download ends under it.
	updateStopGrace = 30 * time.Second
)

// autoUpdate is kept in the state directory: the last automatic check that
// came to an end (nothing to install, installed, or failed), and whether
// the user chose to restart tonight. last is what the last automatic step
// came to, for the log and the selftest.
type autoUpdate struct {
	Checked time.Time `json:"checked"`
	Tonight bool      `json:"tonight,omitempty"`
	tried   time.Time
	busy    bool
	loaded  bool
	last    string
}

// autoSays records what an automatic step came to.
func (c *Core) autoSays(format string, a ...any) {
	c.update.auto.last = fmt.Sprintf(format, a...)
	c.log.Printf("automatic update: %s", c.update.auto.last)
}

func (c *Core) autoUpdatePath() string {
	return filepath.Join(c.cfg.StateDir, "shell", "update-auto.json")
}

func (c *Core) loadAutoUpdate() {
	a := &c.update.auto
	a.loaded = true
	if raw, err := os.ReadFile(c.autoUpdatePath()); err == nil {
		_ = json.Unmarshal(raw, a)
	}
	// Restarted since the user chose tonight: nothing waits for it.
	if a.Tonight && c.updateStatus().Pending == "" {
		a.Tonight = false
		c.saveAutoUpdate()
	}
}

func (c *Core) saveAutoUpdate() {
	raw, _ := json.Marshal(c.update.auto)
	_ = os.MkdirAll(filepath.Dir(c.autoUpdatePath()), 0o700)
	_ = os.WriteFile(c.autoUpdatePath(), raw, 0o600)
}

// updateTick runs from the shell's tick: a download retry that is due, the
// conditions of an automatic install running, a restart chosen for
// tonight, a paused install that may go on, and the daily automatic check.
func (c *Core) updateTick() {
	u := &c.update
	u.init()
	c.retryInstall()
	c.watchInstall()
	if !u.auto.loaded {
		c.loadAutoUpdate()
	}
	now := c.now()
	if u.auto.Tonight {
		c.nightRestart(now)
	}
	if u.paused != "" && (!c.settings.AutoSystemUpdate || u.state != "idle" || u.offer == nil) {
		u.paused = ""
	}
	if !c.settings.AutoSystemUpdate || c.cfg.UpdateURL == "" || u.auto.busy || u.state == "installing" || u.state == "installed" {
		return
	}
	if u.paused != "" {
		c.resumePaused(now)
		return
	}
	if ago(now, u.auto.Checked) < autoUpdateEvery || ago(now, u.auto.tried) < autoUpdateRetry {
		return
	}
	u.auto.tried = now
	// A system still on trial (not yet marked good) would have the update
	// written over the slot it falls back to.
	if st := c.updateStatus(); !st.Supported || st.Pending != "" || st.Good != st.Slot {
		return
	}
	u.auto.busy = true
	c.autoConditions(func(why string) {
		if why != "" {
			u.auto.busy = false
			c.autoSays("not now: %s", why)
			return
		}
		c.updateCheck(func(_ any, err error) {
			u.auto.busy = false
			switch {
			case err != nil:
				c.autoSays("the check failed: %v", err)
			case u.offer == nil:
				u.auto.Checked = c.now()
				c.saveAutoUpdate()
				c.autoSays("nothing to install (%s)", firstNonEmpty(u.held, "none offered"))
			case u.state == "installing" || u.state == "installed":
			default:
				c.autoSays("installing %s", u.offer.Version)
				c.startInstall(nil)
			}
		})
	})
}

// autoConditions says why the phone should not install an update in the
// background now ("" when it may).
func (c *Core) autoConditions(then func(why string)) {
	if why := c.autoBlocked(); why != "" {
		then(why)
		return
	}
	c.whenUnmetered(then)
}

// autoBlocked is what of the conditions the shell knows without looking at
// the network: a call, the battery.
func (c *Core) autoBlocked() string {
	pw := c.power()
	switch {
	case c.anyCall():
		return "in a call"
	case !pw.Charging && pw.Level < autoUpdateBattery:
		return fmt.Sprintf("the battery is at %d%% and not charging", pw.Level)
	}
	return ""
}

// watchInstall stops an automatic install whose conditions went away:
// a call or the battery at once, the network as often as it is looked at.
// An applier asked to stop that has not within updateStopGrace has the
// download end under it.
func (c *Core) watchInstall() {
	u := &c.update
	if u.state != "installing" || !u.automatic || !u.retryAt.IsZero() {
		return
	}
	now := c.now()
	if u.stopping != "" {
		if !u.streamCut && now.Sub(u.stopAt) >= updateStopGrace {
			u.streamCut = true
			c.cutStream()
		}
		return
	}
	if why := c.autoBlocked(); why != "" {
		c.stopInstall(why)
		return
	}
	if u.watching || ago(now, u.watchAt) < autoWatchNetwork {
		return
	}
	u.watchAt, u.watching = now, true
	c.whenUnmetered(func(why string) {
		u.watching = false
		if why != "" && u.state == "installing" && u.automatic && u.retryAt.IsZero() && u.stopping == "" {
			c.stopInstall(why)
		}
	})
}

// stopInstall makes the install running fail where it can go on from: an
// incremental bundle's applier stops at a checkpoint, a full bundle's
// download ends.
func (c *Core) stopInstall(why string) {
	u := &c.update
	u.stopping, u.stopAt = why, c.now()
	c.autoSays("stopping: %s", why)
	if len(u.plan) > 1 {
		err := os.WriteFile(u.stopFile, nil, 0o644)
		if err == nil {
			return
		}
		c.log.Printf("system update: %v", err)
	}
	u.streamCut = true
	c.cutStream()
}

// cutStream ends the download of the install running: what it downloads
// through the shell (installProxy).
func (c *Core) cutStream() {
	u := &c.update
	switch {
	case u.cutStream != nil:
		if err := u.cutStream(); err != nil {
			c.log.Printf("system update: ending the download: %v", err)
		}
	case u.proxy != nil:
		u.proxy.stop()
	default:
		c.log.Printf("system update: ending the download: it does not go through the shell")
	}
}

// resumePaused goes on with an automatic install that stopped, once the
// conditions hold again.
func (c *Core) resumePaused(now time.Time) {
	u := &c.update
	if ago(now, u.pausedAt) < autoPauseCheck {
		return
	}
	u.pausedAt, u.auto.busy = now, true
	c.autoConditions(func(why string) {
		u.auto.busy = false
		switch {
		case u.paused == "" || u.state != "idle" || u.offer == nil:
		case why != "":
			u.paused = why
		default:
			c.autoSays("going on with %s", u.offer.Version)
			c.startInstall(nil)
		}
	})
}

// pauseText is why a paused automatic install waits, for Settings.
func pauseText(why string) string {
	switch {
	case why == "":
		return ""
	case why == "in a call":
		return tr.T("The automatic update is paused during the call; it goes on afterwards")
	case strings.HasPrefix(why, "the battery"):
		return tr.T("The automatic update is paused: the battery is low; it goes on once the phone charges")
	}
	return tr.T("The automatic update is paused until the phone is on Wi-Fi that is not metered")
}

// whenUnmetered tells then why the network the phone is on is not one to
// download an update on ("" it is: unmetered Wi-Fi), looking off the loop.
func (c *Core) whenUnmetered(then func(why string)) {
	if f := c.update.unmetered; f != nil {
		f(func(ok bool) {
			then(map[bool]string{false: "not on Wi-Fi"}[ok])
		})
		return
	}
	c.lookNetwork(func(n netNow) { then(unmeteredWhy(n)) })
}

// autoInstalled: an automatic install is done; the user picks the restart.
func (c *Core) autoInstalled() {
	u := &c.update
	u.auto.Checked = c.now()
	c.saveAutoUpdate()
	c.autoSays("installed %s", u.version)
	if c.sys == nil {
		return
	}
	c.addNotif(c.sys, ui.Notification{ID: updateNotif, Title: tr.T("System update installed"),
		Body:    tr.T("%s is ready. Restart now, or tonight while the phone is not in use.", u.version),
		Actions: []ui.Action{{ID: "restart", Label: tr.T("Restart now"), Key: "r"}, {ID: "tonight", Label: tr.T("Tonight"), Key: "t"}}})
}

// autoFailed: an automatic install failed for good; tomorrow's check tries
// again.
func (c *Core) autoFailed() {
	c.update.auto.Checked = c.now()
	c.saveAutoUpdate()
	c.autoSays("failed: %s", c.update.err)
}

// updateNotifAction is the user's answer to the installed update's
// notification.
func (c *Core) updateNotifAction(action string) {
	switch action {
	case "restart":
		if c.updateStatus().Pending != "" {
			c.restartIntoUpdate("from its notification")
		}
	case "tonight":
		c.update.auto.Tonight = true
		c.saveAutoUpdate()
		c.log.Printf("system update: restarting tonight")
		c.showToast(tr.T("The phone restarts tonight while not in use"))
	}
}

// nightRestart restarts into the slot installed, once it is night and
// nobody is using the phone.
func (c *Core) nightRestart(now time.Time) {
	if h := now.In(c.zone()).Hour(); h < nightFrom || h >= nightTo || !c.screen.off || c.anyCall() || c.alarmWithin(now, nightAlarmMargin) {
		return
	}
	c.update.auto.Tonight = false
	c.saveAutoUpdate()
	if c.updateStatus().Pending == "" {
		return
	}
	c.autoSays("restarting into the update: it is night and the phone is not in use")
	c.restartIntoUpdate("tonight, the phone not in use")
}

// alarmWithin: an alarm rings within d of now.
func (c *Core) alarmWithin(now time.Time, d time.Duration) bool {
	if c.alarms == nil {
		return false
	}
	for _, e := range c.alarms.list {
		if !e.Next.IsZero() && !e.Next.Before(now) && e.Next.Sub(now) < d {
			return true
		}
	}
	return false
}
