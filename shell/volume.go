package shell

import (
	"fmt"
	"strings"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/ui"
)

// Volume keys, the silent mode and the vibration motor (R23).
//
// The volume keys work on every screen, the locked keypad included: while
// the phone rings either key silences the ringer (as on every phone); while
// media plays they set the media volume; otherwise the ringer's. In silent
// mode the ringtone and notification sounds give way to vibration; alarms
// still ring.

const volumeStep = 0.1

func (c *Core) volumeKey(up bool) {
	if c.ringing() {
		c.silenceRing()
		c.showToast(tr.T("Silenced"))
		return
	}
	key, shown, cur := ui.SettingRingVolume, tr.T("Ring volume %d%%"), c.settings.RingVolume
	if c.mediaPlaying() {
		key, shown, cur = ui.SettingMediaVolume, tr.T("Media volume %d%%"), c.settings.MediaVolume
	}
	step := volumeStep
	if !up {
		step = -step
	}
	v := max(0, min(1, float64(int((cur+step)*10+0.5))/10))
	if err := c.settingsSet(key, v); err != nil {
		c.log.Printf("volume key: %v", err)
		return
	}
	c.showToast(fmt.Sprintf(shown, int(v*100+0.5)))
}

func isRing(key string) bool {
	return strings.HasPrefix(key, "ring:") || strings.HasPrefix(key, "voip-ring:")
}

// ringing: a ringtone is sounding (or would be, in silent mode).
func (c *Core) ringing() bool {
	for k := range c.sysWant {
		if isRing(k) && !c.silenced[k] {
			return true
		}
	}
	return false
}

// silenceRing stops the ringtone and the vibration of the calls ringing now;
// the calls themselves keep ringing on screen.
func (c *Core) silenceRing() {
	if c.silenced == nil {
		c.silenced = map[string]bool{}
	}
	for k := range c.sysWant {
		if isRing(k) {
			c.silenced[k] = true
			if id := c.sysStream[k]; id != "" {
				delete(c.sysStream, k)
				go func() { _ = c.audioRPC("control", map[string]string{"id": id, "op": "stop"}, nil) }()
			}
		}
	}
	c.log.Printf("ringer silenced by a volume key")
}

func (c *Core) mediaPlaying() bool {
	for _, s := range c.streams {
		if s.Category == audio.Media && s.State == ui.PlayPlaying {
			return true
		}
	}
	return false
}

// nowPlaying is the media stream the idle screen and the system menu show:
// playing, or paused by the user.
func (c *Core) nowPlaying() *audio.Stream {
	var best *audio.Stream
	for _, s := range c.streams {
		if s.Category != audio.Media || s.App == "system" || (s.State != ui.PlayPlaying && s.State != ui.PlayPaused) {
			continue
		}
		if best == nil || s.State == ui.PlayPlaying {
			s := s
			best = &s
		}
	}
	return best
}

// toggleMedia pauses or resumes the media playing (the system menu's entry).
func (c *Core) toggleMedia() {
	s := c.nowPlaying()
	if s == nil {
		return
	}
	op := "pause"
	if s.State == ui.PlayPaused {
		op = "resume"
	}
	id := s.ID
	go func() { _ = c.audioRPC("control", map[string]string{"id": id, "op": op}, nil) }()
}

// mediaKey: play / pause works on the stream itself; next and previous are
// the playing app's to do, so it gets a "media" event (the media session).
// During a call the headset button belongs to the call.
func (c *Core) mediaKey(op string) {
	s := c.nowPlaying()
	if s == nil || len(c.calls) > 0 {
		return
	}
	if op == "playpause" {
		c.toggleMedia()
		return
	}
	if a := c.appByID(s.App); a != nil && a.running {
		a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventMedia, Value: op})
	}
}

// silentSound reports whether a sound of category gives way to vibration.
func (c *Core) silentSound(category string) bool {
	return c.settings.Silent && (category == audio.Ring || category == audio.Notify)
}

// vibrateMS drives the vibration motor (hal/vibrator). Without one (the
// emulator, a developer's machine) the request is only counted.
func (c *Core) vibrateMS(ms int) {
	c.log.Printf("vibrate %d ms", ms)
	c.buzz(ms)
}

// buzz is vibrateMS without the log line (a key press's feedback).
func (c *Core) buzz(ms int) {
	c.vibrations++
	s := &c.screen
	if s.motor == nil {
		return
	}
	if err := s.motor.Vibrate(time.Duration(ms) * time.Millisecond); err != nil && !s.motorFailed {
		s.motorFailed = true
		c.log.Printf("vibration motor %s: %v", s.motor.Name(), err)
	}
}

// alarmRinging: an alarm is sounding.
func (c *Core) alarmRinging() bool {
	for k := range c.sysWant {
		if strings.HasPrefix(k, "alarm:") {
			return true
		}
	}
	return false
}

// vibrateRinging pulses while a call rings in silent mode, and while an
// alarm rings.
func (c *Core) vibrateRinging() {
	if !(c.settings.Silent && c.ringing()) && !c.alarmRinging() || time.Since(c.lastPulse) < 1500*time.Millisecond {
		return
	}
	c.lastPulse = time.Now()
	c.vibrateMS(800)
}

// Vibrations counts vibration requests (selftest).
func (c *Core) Vibrations() int { return c.vibrations }
