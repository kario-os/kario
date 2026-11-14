package shell

import (
	"fmt"
	"sort"
	"time"

	"github.com/kario-os/kario/cts"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The compatibility report (phone-shell --cts, package cts): the shell
// fills it in from the run and has keystored sign it.

type CompatReport = cts.CompatReport

// SignReport asks keystored to attest the report.
func (c *Core) SignReport(r *CompatReport) error {
	var att ui.Attestation
	if err := c.broker.rpc(svc.Keystored, svc.KeyAttest, map[string]string{"app": cts.Signer, "nonce": cts.ReportNonce(*r)}, &att); err != nil {
		return err
	}
	r.Attestation = &att
	return nil
}

func (c *Core) CompatReport(cases []CaseResult, passed bool) CompatReport {
	r := CompatReport{System: Brand, Version: shownVersion(), Proto: c.about()["proto"].(string), About: c.about(),
		Date: time.Now().Format(time.RFC3339), Cases: cases, Capabilities: map[string]CapStat{}, Uncovered: []string{}}
	covered := 0
	for capName := range capPerms {
		st := c.broker.stats[capName]
		if st == nil || st.Calls == 0 {
			r.Uncovered = append(r.Uncovered, capName)
			continue
		}
		covered++
		r.Capabilities[capName] = *st
	}
	sort.Strings(r.Uncovered)
	r.Coverage = fmt.Sprintf("%d/%d", covered, len(capPerms))
	r.Renderer = c.renderer
	r.Result = "PASS"
	if !passed || len(r.Uncovered) > 0 {
		r.Result = "FAIL"
	}
	return r
}
