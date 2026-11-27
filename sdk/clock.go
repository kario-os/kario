package sdk

import (
	"strconv"
	"sync/atomic"
	"time"
)

// clockOffset is how far ahead of the machine's clock the system's is, in
// milliseconds (ui.EnvClockOffset, ui.EventClock): 0 outside a selftest.
var clockOffset atomic.Int64

// Now is the time as the system has it: the machine's clock, moved as a
// selftest moves the Shell's (timeshift, clock-at). Read the time of day
// from it (today's date, an alarm's minute, a message's time); measure
// durations with time.Now and time.Since.
func Now() time.Time {
	return time.Now().Add(time.Duration(clockOffset.Load()) * time.Millisecond)
}

func setClock(ms string) {
	if n, err := strconv.ParseInt(ms, 10, 64); err == nil {
		clockOffset.Store(n)
	}
}
