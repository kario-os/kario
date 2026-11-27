package pushd

import (
	"time"

	"golang.org/x/sys/unix"
)

// sinceBoot counts the time suspended too (CLOCK_BOOTTIME), which Go's
// monotonic clock leaves out; unlike the wall clock, it is never set.
func sinceBoot() time.Duration {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts)
	return time.Duration(ts.Nano())
}
