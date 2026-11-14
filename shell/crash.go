package shell

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/ui"
)

// Crash records: an app that ends on its own with an error (a panic, a
// signal, a non-zero exit) leaves a record with the tail of its log, which
// the system's Settings › About phone lists (in production, opt-in reports to
// the developer through the store). Exits the system causes are not crashes:
// the low-memory killer, the user closing it after an ANR or from the task
// switcher, uninstall, the memory limit (reported on its own).
//
// An app that keeps crashing is not started in the background again (push
// and jobs) for a while: a crash loop would otherwise burn the battery
// unseen. What it was sent waits until the user opens it.

const (
	maxCrashRecords = 50
	crashLogLines   = 20
	crashLoopCount  = 3
	crashLoopWindow = 10 * time.Minute
)

type CrashRecord struct {
	Time    time.Time `json:"time"`
	App     string    `json:"app"`
	Name    string    `json:"name"`
	Version string    `json:"version,omitempty"`
	Reason  string    `json:"reason"`
	Log     []string  `json:"log,omitempty"`
}

func (c *Core) crashFile() string { return filepath.Join(c.cfg.StateDir, "crashes.json") }

func (c *Core) loadCrashes() {
	if c.crashes != nil {
		return
	}
	c.crashes = []CrashRecord{}
	if raw, err := os.ReadFile(c.crashFile()); err == nil {
		_ = json.Unmarshal(raw, &c.crashes)
	}
}

// exitReason describes how a process ended, or "" for a clean exit.
func exitReason(err error) string {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return ""
	}
	return ee.ProcessState.String()
}

func (c *Core) recordCrash(a *App, reason string) {
	c.loadCrashes()
	r := CrashRecord{Time: c.now(), App: a.M.ID, Name: a.M.Name, Version: a.M.Version, Reason: reason}
	if raw, err := os.ReadFile(filepath.Join(c.cfg.RunDir, a.M.ID+".log")); err == nil {
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		for _, l := range lines[max(0, len(lines)-crashLogLines):] {
			if rs := []rune(l); len(rs) > 200 {
				l = string(rs[:200]) + "…"
			}
			r.Log = append(r.Log, l)
		}
	}
	c.crashes = append(c.crashes, r)
	if n := len(c.crashes); n > maxCrashRecords {
		c.crashes = c.crashes[n-maxCrashRecords:]
	}
	raw, _ := json.MarshalIndent(c.crashes, "", "  ")
	if err := os.WriteFile(c.crashFile(), raw, 0o600); err != nil {
		c.log.Printf("crash record: %v", err)
	}
	c.log.Printf("app %s crashed: %s", a.M.ID, reason)
}

// crashLooping: the app crashed crashLoopCount times within crashLoopWindow.
func (c *Core) crashLooping(a *App) bool {
	c.loadCrashes()
	n, cut := 0, c.now().Add(-crashLoopWindow)
	for _, r := range c.crashes {
		if r.App == a.M.ID && r.Time.After(cut) {
			n++
		}
	}
	return n >= crashLoopCount
}

// Crashes are the kept records, oldest first.
func (c *Core) Crashes() []CrashRecord {
	c.loadCrashes()
	return c.crashes
}

// appEnvValue reads one of the variables apps get (the simulated clouds).
func (c *Core) appEnvValue(key string) string {
	for _, kv := range c.cfg.AppEnv {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return os.Getenv(key)
}

// confirmCrashSend asks before one crash record of a store app goes to its
// developer through the store backend: its reason, version, time and the
// last lines of the app's log, nothing else from the device.
func (c *Core) confirmCrashSend(i int) {
	recent := c.Crashes()[max(0, len(c.Crashes())-10):]
	if i < 0 || i >= len(recent) {
		c.showToast(tr.T("This record is gone"))
		return
	}
	r := recent[i]
	a := c.appByID(r.App)
	if a == nil {
		c.showToast(tr.T("“%s” has been uninstalled", r.Name))
		return
	}
	inst := readInstallRecord(a.M.dir)
	url := c.appEnvValue(ui.EnvStoreServer)
	switch {
	case inst.Store != "store":
		c.showToast(tr.T("Only apps installed from the app store can send crash records to their developer"))
		return
	case url == "":
		c.showToast(tr.T("Not connected to the app store"))
		return
	}
	text := tr.T("Send this crash of “%s” to its developer “%s”?\nWhat is sent: the reason, version, time and the last %d lines of its log.", a.M.Name, inst.Developer, len(r.Log))
	c.pushOverlay(&confirmOverlay{text: text, onYes: func() {
		body, _ := json.Marshal(map[string]any{"app": r.App, "version": r.Version, "reason": r.Reason,
			"time": r.Time.UTC().Format(time.RFC3339), "log": r.Log})
		go func() {
			res, err := (&http.Client{Timeout: 10 * time.Second}).Post(strings.TrimSuffix(url, "/")+"/v1/crashes", "application/json", bytes.NewReader(body))
			if err == nil {
				res.Body.Close()
				if res.StatusCode != 200 {
					err = errors.New(tr.T("The store answered %d", res.StatusCode))
				}
			}
			c.Post(func() {
				if err != nil {
					c.showToast(tr.T("Sending failed: %v", err))
					return
				}
				c.log.Printf("crash of %s sent to its developer", r.App)
				c.showToast(tr.T("Sent to the developer of “%s”", a.M.Name))
			})
		}()
	}})
}
