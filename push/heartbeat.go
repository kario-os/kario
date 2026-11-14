package push

import "time"

// Heartbeat learns how long the device's connection may stay silent: mobile
// networks drop idle NAT mappings after an operator-specific time, and every
// heartbeat wakes the radio. It starts short, doubles the interval after a
// connection outlives a few heartbeats, and falls back to the last good
// interval when a connection dies while idle, never probing past an interval
// that failed again.
type Heartbeat struct {
	Cur  time.Duration `json:"cur"`
	Good time.Duration `json:"good"` // the longest interval that held
	Ceil time.Duration `json:"ceil"` // intervals at or above this failed
}

const (
	HeartbeatMin   = 15 * time.Second
	HeartbeatStart = 30 * time.Second
	HeartbeatMax   = 28 * time.Minute
	// HeartbeatProbe is how many heartbeats a connection must outlive before
	// the interval grows.
	HeartbeatProbe = 3
)

func NewHeartbeat() Heartbeat {
	return Heartbeat{Cur: HeartbeatStart, Ceil: HeartbeatMax + time.Second}
}

// Survived: the connection outlived HeartbeatProbe heartbeats at Cur.
func (h *Heartbeat) Survived() {
	if h.Cur > h.Good {
		h.Good = h.Cur
	}
	if next := h.Cur * 2; next < h.Ceil && next <= HeartbeatMax {
		h.Cur = next
	} else if h.Cur < h.Ceil-time.Second && h.Ceil <= HeartbeatMax {
		// Close in on the failed interval.
		h.Cur = (h.Cur + h.Ceil) / 2
	}
}

// Dropped: the connection died while idle at Cur.
func (h *Heartbeat) Dropped() {
	if h.Cur < h.Ceil {
		h.Ceil = h.Cur
	}
	switch {
	case h.Good > 0 && h.Good < h.Cur:
		h.Cur = h.Good
	default:
		h.Good = 0
		h.Cur = max(h.Cur/2, HeartbeatMin)
	}
}
