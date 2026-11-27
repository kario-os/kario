package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// netSpeed is the status bar's network speed (the net_speed setting): the
// bytes in and out of every interface but the loopback, per second, sampled
// once a second while the setting is on.
type netSpeed struct {
	at    time.Time
	bytes uint64
	bps   float64
	ok    bool
}

const netClassDir = "/sys/class/net"

// netBytes is what all the interfaces have carried so far.
var netBytes = func() (uint64, bool) {
	ents, err := os.ReadDir(netClassDir)
	if err != nil {
		return 0, false
	}
	var total uint64
	for _, e := range ents {
		if e.Name() == "lo" {
			continue
		}
		for _, f := range []string{"rx_bytes", "tx_bytes"} {
			raw, err := os.ReadFile(filepath.Join(netClassDir, e.Name(), "statistics", f))
			if err != nil {
				continue
			}
			if n, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64); err == nil {
				total += n
			}
		}
	}
	return total, true
}

func (c *Core) sampleSpeed() {
	if !c.settings.NetSpeed {
		return
	}
	now := time.Now()
	if !c.speed.at.IsZero() && now.Sub(c.speed.at) < time.Second {
		return
	}
	b, ok := netBytes()
	if !ok {
		return
	}
	// An interface that went away (or a counter that wrapped) takes its
	// bytes with it: that second is skipped, not shown as a burst.
	if !c.speed.at.IsZero() && b >= c.speed.bytes {
		c.speed.bps = float64(b-c.speed.bytes) / now.Sub(c.speed.at).Seconds()
		c.speed.ok = true
	}
	c.speed.at, c.speed.bytes = now, b
}

// speedText is the status bar's speed, "" with the setting off.
func (c *Core) speedText() string {
	if !c.settings.NetSpeed {
		return ""
	}
	if c.snapping != nil {
		return formatSpeed(0)
	}
	return formatSpeed(c.speed.bps)
}

// formatSpeed writes bytes a second as phones do: one decimal under 10
// (0.0K/s), whole numbers above, M/s from 1000 K/s.
func formatSpeed(bps float64) string {
	k := bps / 1024
	if k < 1000 {
		if k < 10 {
			return fmt.Sprintf("%.1fK/s", k)
		}
		return fmt.Sprintf("%.0fK/s", k)
	}
	m := k / 1024
	if m < 10 {
		return fmt.Sprintf("%.1fM/s", m)
	}
	return fmt.Sprintf("%.0fM/s", m)
}
