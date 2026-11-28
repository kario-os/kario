package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/kario-os/kario/netfw"
)

// The selftest's steps for data usage (datausage.go), listed in
// headless.go: bytes charged to a line as if it had used them, the days
// kept aside and put back so that a case leaves them as it found them,
// and the apps' mobile data blocks in force (netpolicy.go).

func (r *Runner) dataStep(cmd, arg string) error {
	c := r.C
	f := strings.Fields(arg)
	switch {
	case cmd == "net-blocked":
		rest, ms := lastNum(arg, 5000)
		f = strings.Fields(rest)
		if len(f) != 2 {
			return errors.New("usage: net-blocked <app-id> <why>|none [ms]")
		}
		want := f[1]
		if want == "none" {
			want = ""
		}
		var got string
		if _, ok := r.waitFor(func(string) bool { got = c.radios.netBlocks[f[0]]; return got == want }, ms); !ok {
			return fmt.Errorf("%s's mobile data blocked: %q, want %q (the policy says %q)", f[0], got, want, c.netBlocks()[f[0]])
		}
		fmt.Fprintf(r.Out, "PHONE-NET-BLOCKED: %s %s (%s)\n", f[0], firstNonEmpty(got, "none"), c.dataSource())
	case cmd == "data-charge":
		if len(f) != 4 || (f[1] != "wifi" && f[1] != "mobile") {
			return errors.New("usage: data-charge <app-id>|hotspot|system wifi|mobile <down> <up>")
		}
		down, err1 := strconv.ParseUint(f[2], 10, 64)
		up, err2 := strconv.ParseUint(f[3], 10, 64)
		if err1 != nil || err2 != nil {
			return errors.New("data-charge: the bytes are whole numbers")
		}
		u := netfw.Usage{MobileRx: down, MobileTx: up}
		if f[1] == "wifi" {
			u = netfw.Usage{WifiRx: down, WifiTx: up}
		}
		c.addData(f[0], u)
		c.checkDataPlan()
		c.saveData(true)
	case len(f) == 1 && f[0] == "save":
		l := c.dataLedger()
		raw, err := json.Marshal(l)
		if err != nil {
			return err
		}
		h := c.hotspot.usage
		h.Days = maps.Clone(h.Days)
		l.kept = &dataKept{ledger: raw, hotspot: h}
	case len(f) == 1 && f[0] == "restore":
		l := c.dataLedger()
		if l.kept == nil {
			return errors.New("data-usage restore without a save")
		}
		// What the counters still hold went since the save: counted now, it
		// goes with the ledger, and the baselines are the current ones.
		c.countData()
		back := dataUsage{held: l.held, loaded: true, at: l.at, links: l.links, looked: l.looked}
		if err := json.Unmarshal(l.kept.ledger, &back); err != nil {
			return err
		}
		u := &c.hotspot.usage
		u.Days, u.Session, u.Warned, u.Cut = l.kept.hotspot.Days, l.kept.hotspot.Session, l.kept.hotspot.Warned, l.kept.hotspot.Cut
		c.radios.data = back
		c.checkDataPlan()
		c.saveData(true)
		c.saveHotspotUsage(true)
	case len(f) == 1 && f[0] == "report":
		v := c.dataUsageView()
		fmt.Fprintf(r.Out, "PHONE-DATA-USAGE: %s, cycle %s to %s, mobile %d B (today %d B, last cycle %d B), Wi-Fi %d B, roaming %d B, held %v, past the limit %v; lines %+v\n",
			v.Source, v.Start, v.End, v.Mobile, v.MobileToday, v.PrevMobile, v.Wifi, v.Roaming, v.Held, v.Override, v.Lines)
	default:
		return errors.New("usage: data-usage save|restore|report")
	}
	return nil
}
