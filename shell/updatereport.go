package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kario-os/kario/ota"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// How an installed update went, for the update server's staged rollout
// (it raises the share of phones offered a version and pauses
// it when too many roll back). The shell remembers what it installed into
// which slot; once that slot is marked good, or the phone is back on the
// old one, it reports "good" or "rollback" with the rollout bucket,
// retrying on later starts until the server has it. The keystore signs the
// report as this device (ota.Report): the server can check it came from a
// genuine phone and count each phone once; the report itself names none.
// A day after a good install it reports again with the app crashes since
// (ota.KindDay), so a version that installs well but runs badly stops too.

type updateReport struct {
	Version string `json:"version"`
	Slot    string `json:"slot"`   // the slot it was installed into
	Server  string `json:"server"` // the update server it came from
	Outcome string `json:"outcome,omitempty"`
	Bucket  int    `json:"bucket"`
	Kind    string `json:"kind,omitempty"`
	// GoodAt: when the install was found good (the day report counts from).
	GoodAt  time.Time `json:"good_at,omitzero"`
	Crashes int       `json:"crashes,omitempty"`
}

func (r updateReport) wire(att *ui.Attestation) ota.Report {
	return ota.Report{Version: r.Version, Outcome: r.Outcome, Bucket: r.Bucket, Kind: r.Kind, Crashes: r.Crashes, Attestation: att}
}

const (
	// reportWait: how long a start waits for the verdict on its slot.
	reportWait = 15 * time.Minute
	// dayAfter: when the second report goes.
	dayAfter = 24 * time.Hour
)

func (c *Core) reportPath() string {
	return filepath.Join(c.cfg.StateDir, "shell", "update-report.json")
}

func (c *Core) dayReportPath() string {
	return filepath.Join(c.cfg.StateDir, "shell", "update-day.json")
}

func (c *Core) rememberInstall(r updateReport) {
	if r.Server == "" || r.Slot == "" {
		return
	}
	raw, _ := json.Marshal(r)
	if err := os.WriteFile(c.reportPath(), raw, 0o600); err != nil {
		c.log.Printf("system update: %v", err)
	}
}

var bootKV = regexp.MustCompile(`(?m)^(good|booted)=([ab])$`)

// updateOutcome reads the boot state: good once the installed slot was
// marked good, rollback once the phone runs the other slot, "" while the
// new slot is still on trial.
func updateOutcome(boot, slot string) string {
	kv := map[string]string{}
	for _, m := range bootKV.FindAllStringSubmatch(boot, -1) {
		kv[m[1]] = m[2]
	}
	switch {
	case kv["booted"] == slot && kv["good"] == slot:
		return ota.OutcomeGood
	case kv["booted"] != "" && kv["booted"] != slot:
		return ota.OutcomeRollback
	}
	return ""
}

// reportUpdate waits for the verdict on the last install and sends it,
// and the day report when one is due.
func (c *Core) reportUpdate() {
	c.reportDay()
	raw, err := os.ReadFile(c.reportPath())
	if err != nil {
		return
	}
	var r updateReport
	if json.Unmarshal(raw, &r) != nil {
		_ = os.Remove(c.reportPath())
		return
	}
	u := &c.update
	u.init()
	bootFile := filepath.Join(u.slots, "boot")
	r.Bucket = c.updateBucket()
	go func() {
		deadline := time.Now().Add(reportWait)
		for time.Now().Before(deadline) {
			state, _ := os.ReadFile(bootFile)
			if r.Outcome = updateOutcome(string(state), r.Slot); r.Outcome != "" {
				break
			}
			time.Sleep(10 * time.Second)
		}
		if r.Outcome == "" {
			return
		}
		if err := postReport(r, c.attestReport(r)); err != nil {
			c.log.Printf("system update: report %s %s: %v (again at the next start)", r.Version, r.Outcome, err)
			return
		}
		c.log.Printf("system update: reported %s %s", r.Version, r.Outcome)
		if r.Outcome == ota.OutcomeGood {
			day := updateReport{Version: r.Version, Server: r.Server, Outcome: ota.OutcomeGood, Kind: ota.KindDay, GoodAt: time.Now()}
			raw, _ := json.Marshal(day)
			if err := os.WriteFile(c.dayReportPath(), raw, 0o600); err != nil {
				c.log.Printf("system update: %v", err)
			}
		}
		_ = os.Remove(c.reportPath())
		c.reportDay()
	}()
}

// reportDay sends the day report once it is due, with the app crashes
// recorded since the install was found good; a phone switched off meanwhile
// sends it at the next start.
func (c *Core) reportDay() {
	raw, err := os.ReadFile(c.dayReportPath())
	if err != nil {
		return
	}
	var r updateReport
	if json.Unmarshal(raw, &r) != nil || r.Kind != ota.KindDay {
		_ = os.Remove(c.dayReportPath())
		return
	}
	r.Bucket = c.updateBucket()
	crashFile := c.crashFile()
	go func() {
		time.Sleep(time.Until(r.GoodAt.Add(dayAfter)))
		r.Crashes = crashesSince(crashFile, r.GoodAt)
		if err := postReport(r, c.attestReport(r)); err != nil {
			c.log.Printf("system update: day report %s: %v (again at the next start)", r.Version, err)
			return
		}
		c.log.Printf("system update: reported %s a day on: %d app crashes", r.Version, r.Crashes)
		_ = os.Remove(c.dayReportPath())
	}()
}

// crashesSince counts the crash records after t (read from the file: the
// records belong to the loop).
func crashesSince(path string, t time.Time) int {
	var recs []CrashRecord
	raw, _ := os.ReadFile(path)
	_ = json.Unmarshal(raw, &recs)
	n := 0
	for _, r := range recs {
		if r.Time.After(t) {
			n++
		}
	}
	return n
}

// attestReport has the keystore sign the report as this device. Without
// it the report goes unsigned: a server that wants signatures refuses it,
// and the next start tries again.
func (c *Core) attestReport(r updateReport) *ui.Attestation {
	if c.broker == nil {
		return nil
	}
	var att ui.Attestation
	nonce := ota.ReportNonce(r.wire(nil))
	if err := c.broker.rpc(svc.Keystored, svc.KeyAttest, map[string]string{"app": ota.ReportApp, "nonce": nonce}, &att); err != nil {
		c.log.Printf("system update: report not signed: %v", err)
		return nil
	}
	return &att
}

func postReport(r updateReport, att *ui.Attestation) error {
	body, _ := json.Marshal(r.wire(att))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(r.Server, "/")+"/report", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("update server: %s", resp.Status)
	}
	return nil
}
