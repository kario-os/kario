package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/audio"
	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/hal/vibrator"
	"github.com/kario-os/kario/ui"
)

// switchAt is the renderer's report of the sound switch (kms.rs, the
// sound switch's loop).
func switchAt(pos string) string { return `{"t":"sound_switch","position":"` + pos + `"}` }

// The sound switch sets the mode once the renderer reports it: the first
// report (where the phone started) quietly, each move with a toast; the
// silent mode setting gives way to it, the status bar and settings.get say
// the mode. Without a switch the setting is ring or vibrate as before.
func TestSoundSwitchSetsTheMode(t *testing.T) {
	c := menuCore(t)
	if m := c.soundMode(); m != ui.SoundRing || c.frameStatus().Sound != "" {
		t.Fatalf("without a switch: %s, status %q", m, c.frameStatus().Sound)
	}
	if err := c.settingsSet(ui.SettingSilent, 1); err != nil || c.soundMode() != ui.SoundVibrate || c.frameStatus().Sound != ui.SoundVibrate {
		t.Fatalf("the silent mode setting: %v, %s, status %q", err, c.soundMode(), c.frameStatus().Sound)
	}
	if v := c.soundView(); v["switch"] != false {
		t.Errorf("settings.get's sound without a switch: %v", v)
	}

	reported(t, c, switchAt(ui.SoundSilent))
	if m := c.soundMode(); m != ui.SoundSilent || c.toast != nil {
		t.Fatalf("the switch at silent when the phone started: %s, toast %+v", m, c.toast)
	}
	if err := c.settingsSet(ui.SettingSilent, 0); err != errSoundSwitch || !c.settings.Silent {
		t.Errorf("the setting under the switch: %v, silent %v", err, c.settings.Silent)
	}
	if v := c.soundView(); v["mode"] != ui.SoundSilent || v["switch"] != true {
		t.Errorf("settings.get's sound: %v", v)
	}
	if s := c.frameStatus().Sound; s != ui.SoundSilent {
		t.Errorf("status %q", s)
	}
	for _, tc := range []struct{ pos, toast, status string }{
		{ui.SoundVibrate, tr.T("Vibrate"), ui.SoundVibrate},
		{ui.SoundRing, tr.T("Ring"), ""},
		{ui.SoundSilent, tr.T("Silent"), ui.SoundSilent},
	} {
		c.toast = nil
		reported(t, c, switchAt(tc.pos))
		if c.soundMode() != tc.pos || c.toast == nil || c.toast.body != tc.toast || c.frameStatus().Sound != tc.status {
			t.Errorf("moved to %s: mode %s, toast %+v, status %q", tc.pos, c.soundMode(), c.toast, c.frameStatus().Sound)
		}
	}
	c.toast = nil
	reported(t, c, switchAt(ui.SoundSilent), switchAt("middle"))
	if c.soundMode() != ui.SoundSilent || c.toast != nil {
		t.Errorf("the same position again, an unknown one: %s, toast %+v", c.soundMode(), c.toast)
	}
}

// The text status bar marks vibrate and silent beside the network.
func TestSoundSwitchStatusMark(t *testing.T) {
	c := snapCore(t)
	for _, pos := range soundModes {
		c.soundSwitch.pos = pos
		can := cv.New(c.cfg.W, 1, stBase)
		c.drawStatus(can)
		got := can.Plain()
		for mode, mark := range soundMarks {
			if strings.Contains(got, mark) != (mode == pos) {
				t.Errorf("at %s the status bar has %s: %v (%q)", pos, mark, mode != pos, got)
			}
		}
	}
}

// Vibrate: a ringing call pulses the motor, a notification vibrates once;
// silent: neither; an alarm pulses in every mode. A call's ring note says
// which.
func TestSoundSwitchVibration(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		sim := &vibrator.Sim{}
		c.screen.motor = sim
		pulse := func(want ...string) int {
			c.lastPulse = time.Time{}
			c.sysWant = map[string]bool{}
			for _, k := range want {
				c.sysWant[k] = true
			}
			before := c.Vibrations()
			c.vibrateRinging()
			return c.Vibrations() - before
		}
		notify := func() int {
			before := c.Vibrations()
			c.sysWant = map[string]bool{}
			c.sound("notify:1", "notify", audio.Notify)
			c.notifyVibrate(300)
			return c.Vibrations() - before
		}
		call := &cellIncomingOverlay{call: ui.TelCall{ID: "1"}}
		for _, tc := range []struct {
			pos          string
			ring, notify int
			note         string
		}{
			{ui.SoundVibrate, 1, 2, tr.T("Silent mode · vibrate")},
			{ui.SoundSilent, 0, 0, tr.T("Silent mode · no vibration")},
		} {
			c.soundSwitch.pos = tc.pos
			if n := pulse("ring:1"); n != tc.ring {
				t.Errorf("%s: a ringing call pulsed %d times", tc.pos, n)
			}
			if n := pulse("alarm:a1"); n != 1 {
				t.Errorf("%s: a ringing alarm pulsed %d times", tc.pos, n)
			}
			if n := notify(); n != tc.notify {
				t.Errorf("%s: a notification vibrated %d times", tc.pos, n)
			}
			if got := call.ringNote(c); got != tc.note {
				t.Errorf("%s: the call says %q", tc.pos, got)
			}
			if !c.silentSound(audio.Ring) || !c.silentSound(audio.Notify) || c.silentSound(audio.Alarm) {
				t.Errorf("%s: held back ring %v, notify %v, alarm %v", tc.pos, c.silentSound(audio.Ring), c.silentSound(audio.Notify), c.silentSound(audio.Alarm))
			}
		}
		return nil
	})
}

// A call ringing when the switch moves: off ring its ringtone stops (and
// it vibrates or not), back at ring it sounds again; one silenced with a
// volume key stays silenced.
func TestSoundSwitchWhileRinging(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		c.soundSwitch.pos = ui.SoundRing
		c.sysWant = map[string]bool{"ring:1": true, "ring:2": true, "alarm:a1": true}
		c.sysStream = map[string]string{"ring:1": "s1", "alarm:a1": "s3"}
		c.silenced = map[string]bool{"ring:2": true}
		c.SetSoundSwitch(ui.SoundVibrate)
		if c.sysStream["ring:1"] != "" || !c.sysWant["ring:1"] || c.sysStream["alarm:a1"] != "s3" {
			t.Fatalf("at vibrate: want %v, streams %v", c.sysWant, c.sysStream)
		}
		c.SetSoundSwitch(ui.SoundSilent)
		c.SetSoundSwitch(ui.SoundRing)
		if !c.sysWant["ring:1"] || !c.sysWant["ring:2"] || c.sysStream["ring:2"] != "" {
			t.Errorf("back at ring: want %v, streams %v", c.sysWant, c.sysStream)
		}
		return nil
	})
}

// The selftest's steps: require-sound-switch skips the case unless the SKU
// has a switch; sound-switch moves it, restore puts it back where the
// renderer said (nowhere here).
func TestSoundSwitchSteps(t *testing.T) {
	script := "== slider\nrequire-sound-switch\nsound-switch vibrate\nsound-switch restore\n"
	var out strings.Builder
	r := &Runner{C: snapCore(t), Out: &out}
	r.Run(script)
	if got := caseLine(r.Cases); got != "slider SKIP" {
		t.Fatalf("without a switch: %s\n%s", got, out.String())
	}
	r.C.cfg.SKU.SoundSwitch = "Alert slider"
	r.Run("== slider\nrequire-sound-switch\nsound-switch vibrate\n")
	if got := caseLine(r.Cases); got != "slider PASS" || r.C.soundMode() != ui.SoundVibrate {
		t.Fatalf("with one: %s, %s\n%s", got, r.C.soundMode(), out.String())
	}
	r.C.soundSwitch.hw = ui.SoundSilent
	r.Run("== back\nsound-switch restore\n== wrong\nsound-switch middle\n")
	if got := caseLine(r.Cases); got != "back PASS, wrong FAIL" || r.C.soundMode() != ui.SoundSilent {
		t.Fatalf("restore, a wrong position: %s, %s\n%s", got, r.C.soundMode(), out.String())
	}
}
