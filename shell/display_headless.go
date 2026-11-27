package shell

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// displayStep is the script's display command (headless.go): the refresh
// rate phone-displayd chose, on the simulated panel.
func (r *Runner) displayStep(arg string) error {
	c := r.C
	f := strings.Fields(arg)
	num := func(i, def int) (int, error) {
		if i >= len(f) {
			return def, nil
		}
		return strconv.Atoi(f[i])
	}
	if len(f) == 0 {
		return errors.New("display <hz> [ms] | went <hz> [ms] | stayed <hz> [ms] | reason <text> [ms] | thermal hot|cool | report")
	}
	switch f[0] {
	case "went":
		hz, err := num(1, 0)
		ms, err2 := num(2, 3000)
		if err != nil || err2 != nil || hz <= 0 {
			return errors.New("display went <hz> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { return c.DisplaySeen(hz) }, ms); !ok {
			now, why := c.DisplayRate()
			return fmt.Errorf("the panel did not go to %d Hz (it went %v; at %d Hz now, %s)", hz, c.display.seen[max(0, c.display.looked):], now, why)
		}
	case "stayed":
		hz, err := num(1, 0)
		ms, err2 := num(2, 1000)
		if err != nil || err2 != nil || hz <= 0 {
			return errors.New("display stayed <hz> [ms]")
		}
		r.pump(time.Duration(ms*max(1, r.Scale)) * time.Millisecond)
		if went := c.display.seen[max(0, c.display.looked):]; slices.ContainsFunc(went, func(v int) bool { return v != hz }) {
			return fmt.Errorf("the panel left %d Hz: it went %v", hz, went)
		}
	case "reason":
		if len(f) < 2 {
			return errors.New("display reason <text> [ms]")
		}
		ms, err := num(2, 3000)
		if err != nil {
			return err
		}
		if _, ok := r.waitFor(func(string) bool { _, why := c.DisplayRate(); return why == f[1] }, ms); !ok {
			hz, why := c.DisplayRate()
			return fmt.Errorf("the panel is at %d Hz for %q, want %q", hz, why, f[1])
		}
	case "thermal":
		if len(f) != 2 || f[1] != "hot" && f[1] != "cool" {
			return errors.New("display thermal hot|cool")
		}
		return c.DisplayThermal(f[1] == "hot")
	case "report":
		st := c.display.st
		fmt.Fprintf(r.Out, "PHONE-DISPLAY: panel %s, rates %v, at %d Hz (%s), %d switches\n", st.Panel, st.Rates, st.Hz, st.Reason, st.Switches)
	default:
		hz, err := num(0, 0)
		ms, err2 := num(1, 3000)
		if err != nil || err2 != nil || hz <= 0 {
			return errors.New("display <hz> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { now, _ := c.DisplayRate(); return now == hz && c.display.st.Want == hz }, ms); !ok {
			now, why := c.DisplayRate()
			return fmt.Errorf("the panel is at %d Hz (%s), want %d", now, why, hz)
		}
		c.display.looked = len(c.display.seen) - 1
	}
	return nil
}
