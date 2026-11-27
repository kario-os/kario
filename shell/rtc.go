package shell

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/kario-os/kario/hal/rtc"
)

// The RTC follows the clock (hal/rtc), as the board's sku.json rtc says:
// when the Shell has set the time (the network's NITZ, the user) a
// settable RTC is written (timedated writes it itself), and a read-only
// one's offset is measured and kept in the persistent area, where
// phone-clock reads it at boot; while NTP keeps the clock, the offset is
// checked every rtcEvery and kept anew when the RTC has drifted rtcSlack
// from it (phone-clock.service also keeps it at shutdown). Only where the
// clock is the Shell's (canSetClock). The selftest's time steps (timeshift,
// the simulated sleep) move the Shell's clockSkew, not the system clock:
// they never reach the RTC or the offset.

const (
	rtcEvery = 5 * time.Minute
	rtcSlack = 500 * time.Millisecond
)

// rtcState is the offset kept and its measuring.
type rtcState struct {
	kept    rtc.Offset
	checked time.Time
	busy    bool
	// again: what set the time while a measurement was under way, which
	// may have read the clock before it was set.
	again string
}

var (
	measureRTC = rtc.Measure
	saveRTC    = rtc.Save
	writeRTC   = rtc.Write
)

func (c *Core) rtcPath() string { return filepath.Join(c.cfg.PersistDir, rtc.OffsetFile) }

func (c *Core) keepsRTCOffset() bool {
	return c.cfg.SKU.RTC.Offset && c.cfg.PersistDir != "" && canSetClock()
}

// startRTC reads the offset kept, on a board that keeps one.
func (c *Core) startRTC() {
	if c.cfg.SKU.ID != "" && !canSetClock() {
		c.log.Printf("clock: not this system's (not root, a chroot, or %s=host): the Shell leaves it and the RTC alone", rtc.OwnerEnv)
	}
	if !c.keepsRTCOffset() {
		return
	}
	o, err := rtc.Load(c.rtcPath())
	if err != nil {
		c.log.Printf("rtc: read-only, the time kept as an offset from it: none kept yet (%v)", err)
		return
	}
	c.locale.rtc.kept = o
	c.log.Printf("rtc: read-only, the time kept as an offset from it: %s, measured %s (%s)", offsetText(o), o.At.Format(time.RFC3339), o.By)
}

func offsetText(o rtc.Offset) string { return fmt.Sprintf("%+.3f s", float64(o.MS)/1000) }

// keepRTC: the Shell has set the clock (how: as setSystemClock; by:
// "network", "user").
func (c *Core) keepRTC(how, by string) {
	r := c.cfg.SKU.RTC
	switch {
	case r.Offset:
		c.measureOffset(by)
	case !r.ReadOnly && how != "timedated":
		// Read now: a test puts its stand-in back when it ends.
		write := writeRTC
		go func() {
			if err := write(time.Now()); err != nil {
				c.Post(func() { c.log.Printf("rtc: writing the time set (%s): %v", by, err) })
			}
		}()
	}
}

// rtcTick runs with the housekeeping: while NTP keeps the clock, the
// offset follows it.
func (c *Core) rtcTick() {
	s := &c.locale.rtc
	if !c.cfg.SKU.RTC.Offset || time.Since(s.checked) < rtcEvery {
		return
	}
	s.checked = time.Now()
	if c.settings.ManualTime || !ntpSynced() {
		return
	}
	c.measureOffset("ntp")
}

// measureOffset measures the offset and keeps it: always after the Shell
// set the time, after NTP only when it moved.
func (c *Core) measureOffset(by string) {
	s := &c.locale.rtc
	if !c.keepsRTCOffset() {
		return
	}
	if s.busy {
		if by != "ntp" {
			s.again = by
		}
		return
	}
	s.busy = true
	path, kept := c.rtcPath(), s.kept
	go func() {
		o, err := measureRTC(by)
		saved := false
		if err == nil && (by != "ntp" || o.Moved(kept, rtcSlack)) {
			err = saveRTC(path, o)
			saved = err == nil
		}
		c.Post(func() {
			s.busy = false
			switch {
			case err != nil:
				c.log.Printf("rtc: keeping the offset (%s): %v", by, err)
			case saved:
				s.kept = o
				c.log.Printf("rtc: offset %s kept (%s)", offsetText(o), by)
			}
			if again := s.again; again != "" {
				s.again = ""
				c.measureOffset(again)
			}
		})
	}()
}
