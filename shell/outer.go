package shell

import "time"

// A flip's outer display (the SKU's outer, 24-sku-framework.md §9.10.9).
// One that holds its picture with the phone asleep shows the time, kept to
// the minute by a wake each minute that redraws only the time (sleep.go).
// The renderer has no target for the outer display yet (§9.10.11): clock
// is the time it is to show.
type outerState struct {
	clock string
}

// holdsPicture: the panel keeps its picture with the phone asleep (a
// memory LCD at microwatts, e-paper unpowered); an LCD or an OLED shows
// nothing unlit.
func (o SKUOuter) holdsPicture() bool { return o.Kind == "memory-lcd" || o.Kind == "e-paper" }

// outerTick keeps the outer display's time to the minute.
func (c *Core) outerTick() {
	if !c.cfg.SKU.Outer.holdsPicture() {
		return
	}
	c.outer.clock = c.clockText(c.now())
}

// outerMinute is the wake for the outer display's next minute, rtcEarly
// into it so that the RTC does not ring before it; zero where the outer
// display does not show the time asleep.
func (c *Core) outerMinute(now time.Time) time.Time {
	if !c.cfg.SKU.Outer.holdsPicture() {
		return time.Time{}
	}
	return now.Truncate(time.Minute).Add(time.Minute + rtcEarly)
}
