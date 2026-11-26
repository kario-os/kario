package shell

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/ui"
)

// The sound mode, as Android's ringer modes: ring; vibrate (calls and
// notifications vibrate instead of sounding); silent (they do neither).
// Alarms sound in all three, as on OnePlus phones, and emergency alerts
// too. Without a hardware sound switch the user chooses ring or vibrate
// (Settings › Sound and vibration › Silent mode, the setting "silent").
// A phone with one (SKU sound_switch: the 6T's alert slider) has the mode
// where the switch is: the pixel renderer reports its position when it
// starts and at every move ("sound_switch", 23-renderer-protocol.md), and
// Settings shows that mode instead of offering a choice.

// soundSwitch is the hardware sound switch's position: pos, the one in
// force, "" until a renderer has reported one; hw, the one a renderer
// reported last (the selftest's sound-switch step moves pos alone).
type soundSwitch struct {
	pos, hw string
}

var soundModes = []string{ui.SoundRing, ui.SoundVibrate, ui.SoundSilent}

// soundMarks are the text status bar's signs of the modes but ring.
var soundMarks = map[string]string{ui.SoundVibrate: "📳", ui.SoundSilent: "🔕"}

// errSoundSwitch refuses the silent mode setting while the switch sets the mode.
var errSoundSwitch = errors.New("the sound mode follows the phone's sound switch")

// soundMode is the mode in force (ui.SoundRing, ui.SoundVibrate, ui.SoundSilent).
func (c *Core) soundMode() string {
	if p := c.soundSwitch.pos; p != "" {
		return p
	}
	if c.settings.Silent {
		return ui.SoundVibrate
	}
	return ui.SoundRing
}

// soundSwitched takes the switch's position as the renderer reports it.
func (c *Core) soundSwitched(pos string) {
	if !slices.Contains(soundModes, pos) {
		c.log.Printf("sound switch: unknown position %q", pos)
		return
	}
	c.soundSwitch.hw = pos
	c.moveSoundSwitch(pos)
}

// moveSoundSwitch puts the mode where the switch is: quietly at the first
// position (the one the phone started with), with a toast at a move.
func (c *Core) moveSoundSwitch(pos string) {
	was, before := c.soundSwitch.pos, c.soundMode()
	if pos == was {
		return
	}
	c.soundSwitch.pos = pos
	c.log.Printf("sound switch: %s", firstNonEmpty(pos, "none"))
	c.soundModeChanged(before)
	if was != "" && pos != "" {
		c.showToast(soundModeName(pos))
	}
}

// SetSoundSwitch moves the sound switch for the selftest: to a position,
// as the renderer reports one, or ("restore") back where the renderer last
// said it is (nowhere where none has).
func (c *Core) SetSoundSwitch(pos string) error {
	switch {
	case pos == "restore":
		c.moveSoundSwitch(c.soundSwitch.hw)
	case slices.Contains(soundModes, pos):
		c.moveSoundSwitch(pos)
	default:
		return errors.New("sound-switch ring|vibrate|silent|restore")
	}
	return nil
}

// soundModeName is a mode as the switch's toast and Settings name it.
func soundModeName(mode string) string {
	switch mode {
	case ui.SoundVibrate:
		return tr.T("Vibrate")
	case ui.SoundSilent:
		return tr.T("Silent")
	}
	return tr.T("Ring")
}

// soundModeChanged follows a change of the mode from before: a call
// ringing stops sounding (vibrating on in vibrate mode) or sounds again;
// Settings, if it runs, shows the new mode.
func (c *Core) soundModeChanged(before string) {
	now := c.soundMode()
	if now == before {
		return
	}
	var rings []string
	for k := range c.sysWant {
		if isRing(k) && !c.silenced[k] {
			rings = append(rings, k)
		}
	}
	for _, k := range rings {
		id := c.sysStream[k]
		switch {
		case now != ui.SoundRing && id != "":
			delete(c.sysStream, k)
			go func() { _ = c.audioRPC("control", map[string]string{"id": id, "op": "stop"}, nil) }()
		case now == ui.SoundRing && id == "":
			delete(c.sysWant, k)
			c.sound(k, "ringtone", audio.Ring)
		}
	}
	if a := c.appByID(appSettings); a != nil && a.running {
		raw, _ := json.Marshal(map[string]string{"key": "sound", "value": now})
		a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventSettings, Value: string(raw)})
	}
}

// soundView is settings.get's "sound": the mode in force and whether the
// switch sets it.
func (c *Core) soundView() map[string]any {
	return map[string]any{"mode": c.soundMode(), "switch": c.soundSwitch.pos != ""}
}

// notifyVibrate is a notification's vibration where it has no sound of its
// own (a class 0 message): none in silent mode.
func (c *Core) notifyVibrate(ms int) {
	if c.soundMode() != ui.SoundSilent {
		c.vibrateMS(ms)
	}
}
