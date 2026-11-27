package shell

import (
	"testing"
	"time"
)

func TestFormatSpeed(t *testing.T) {
	for bps, want := range map[float64]string{
		0:                  "0.0K/s",
		512:                "0.5K/s",
		9.94 * 1024:        "9.9K/s",
		10 * 1024:          "10K/s",
		999 * 1024:         "999K/s",
		1000 * 1024:        "1.0M/s",
		12.6 * 1024 * 1024: "13M/s",
	} {
		if got := formatSpeed(bps); got != want {
			t.Errorf("formatSpeed(%v) = %q, want %q", bps, got, want)
		}
	}
}

// The speed is shown only with the setting on, from one sample a second; a
// counter that goes back (an interface gone) skips that second.
func TestNetSpeed(t *testing.T) {
	old := netBytes
	defer func() { netBytes = old }()
	var n uint64
	netBytes = func() (uint64, bool) { return n, true }
	c := &Core{}
	c.sampleSpeed()
	if got := c.speedText(); got != "" {
		t.Fatalf("with the setting off: %q", got)
	}
	c.settings.NetSpeed = true
	n = 1000
	c.sampleSpeed()
	if got := c.speedText(); got != "0.0K/s" {
		t.Fatalf("after the first sample: %q", got)
	}
	c.speed.at = c.speed.at.Add(-time.Second)
	n += 20 * 1024
	c.sampleSpeed()
	if got := c.speedText(); got != "20K/s" {
		t.Fatalf("20 KB in a second: %q", got)
	}
	n += 5 * 1024
	c.sampleSpeed()
	if got := c.speedText(); got != "20K/s" {
		t.Fatalf("a sample within the second was taken: %q", got)
	}
	c.speed.at = c.speed.at.Add(-time.Second)
	n = 10
	c.sampleSpeed()
	if got := c.speedText(); got != "20K/s" {
		t.Fatalf("a counter that went back: %q", got)
	}
}

// The status bar follows the two settings: the battery's percentage unless
// turned off, the speed only when turned on.
func TestStatusSettings(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		if s := c.frameStatus(); !s.Percent || s.Speed != "" {
			t.Errorf("by default: percent %v, speed %q", s.Percent, s.Speed)
		}
		c.settings.HidePercent, c.settings.NetSpeed = true, true
		if s := c.frameStatus(); s.Percent || s.Speed == "" {
			t.Errorf("percent off, speed on: percent %v, speed %q", s.Percent, s.Speed)
		}
		return nil
	})
}
