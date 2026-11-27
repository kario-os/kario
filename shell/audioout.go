package shell

import (
	"fmt"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/route"
	"github.com/kario-os/kario/svc"
)

// Several outputs at once (20-audio.md §12): phone-audiod follows the
// jack, USB audio devices and the routing policy (audio/route); the Shell
// names the outputs, says when one takes over or goes, keeps media's
// volume for each and offers the picker.

// outputsView is audiod's outputs event and method.
type outputsView struct {
	route.Decision
	Volume   float64 `json:"volume"`
	Absolute bool    `json:"absolute"`
	Noisy    string  `json:"noisy"`
	Sim      bool    `json:"sim"`
}

// audioOutputs takes audiod's word on the outputs: a toast when a device
// connected takes media, or media paused because its output went.
func (c *Core) audioOutputs(v outputsView) {
	prev := c.outputs
	c.outputs = v
	switch {
	case v.Noisy != "":
		name := tr.T("The output")
		if o, ok := outputIn(prev, v.Noisy); ok {
			name = c.outputName(o)
		}
		c.log.Printf("audio output %s went: media paused", v.Noisy)
		c.showToast(tr.T("%s disconnected: paused", name))
	case v.Media != prev.Media:
		o, _ := outputIn(v, v.Media)
		if _, was := outputIn(prev, v.Media); !was && o.External() && prev.Media != "" {
			c.showToast(tr.T("Playing on %s", c.outputName(o)))
		}
		c.log.Printf("audio output: media on %s", v.Media)
	}
}

func outputIn(v outputsView, id string) (route.Output, bool) {
	for _, o := range v.Outputs {
		if o.ID == id {
			return o, true
		}
	}
	return route.Output{}, false
}

// outputName is what the user calls an output.
func (c *Core) outputName(o route.Output) string {
	switch o.Kind {
	case route.Speaker:
		return tr.T("Phone speaker")
	case route.Earpiece:
		return tr.T("Earpiece")
	case route.Wired:
		if o.Mic {
			return tr.T("Wired headset")
		}
		return tr.T("Wired headphones")
	}
	if o.Name != "" {
		return o.Name
	}
	if o.Kind == route.Bluetooth {
		return tr.T("Bluetooth audio")
	}
	return tr.T("USB audio")
}

// mediaOutput is the output media plays on (the speaker until audiod
// says).
func (c *Core) mediaOutput() route.Output {
	if o, ok := outputIn(c.outputs, c.outputs.Media); ok {
		return o
	}
	return route.Output{ID: route.Speaker, Kind: route.Speaker, Key: route.Speaker}
}

// mediaVolume is media's volume on its output: the speaker's setting, or
// the one kept for the device (audiod's starting level until the user
// sets one).
func (c *Core) mediaVolume() float64 {
	o := c.mediaOutput()
	if !o.External() {
		return c.settings.MediaVolume
	}
	if v, ok := c.settings.OutputVolumes[o.Key]; ok {
		return v
	}
	return c.outputs.Volume
}

// setMediaVolume sets media's volume on its output and keeps it for the
// output.
func (c *Core) setMediaVolume(v float64) {
	o := c.mediaOutput()
	if !o.External() {
		c.settings.MediaVolume = v
	} else {
		if c.settings.OutputVolumes == nil {
			c.settings.OutputVolumes = map[string]float64{}
		}
		c.settings.OutputVolumes[o.Key] = v
		c.outputs.Volume = v
	}
	c.saveSettings()
	key := map[bool]string{true: o.Key}[o.External()]
	go func() {
		_ = c.audioRPC(svc.AudioVolume, map[string]any{"category": audio.Media, "level": v, "output": key}, nil)
	}()
}

// pickableOutputs are the outputs media can be sent to; the picker is
// offered once there is more than the speaker.
func (c *Core) pickableOutputs() []route.Output {
	var out []route.Output
	for _, o := range c.outputs.Outputs {
		if o.Kind != route.Earpiece {
			out = append(out, o)
		}
	}
	return out
}

// outputEntry is the options menu's and the command palette's way to
// the picker.
func (c *Core) outputEntry() (menuEntry, bool) {
	if len(c.pickableOutputs()) < 2 {
		return menuEntry{}, false
	}
	return menuEntry{group: tr.T("System"), label: tr.T("Audio output: %s", c.outputName(c.mediaOutput())), name: tr.T("Audio output"),
		find: tr.T("Audio output"), run: c.openOutputPicker}, true
}

// openOutputPicker lists the outputs for media: automatic (the device
// connected last) first, then each, the one in use marked.
func (c *Core) openOutputPicker() {
	pick := func(id string) func() {
		return func() {
			go func() {
				var v outputsView
				err := c.audioRPC(svc.AudioOutputPick, map[string]string{"id": id}, &v)
				c.Post(func() {
					if err != nil {
						c.showToast(err.Error())
						return
					}
					c.audioOutputs(v)
				})
			}()
		}
	}
	auto := tr.T("Automatic: the device connected last")
	if c.outputs.Picked == "" {
		auto = tr.T("%s · in use", auto)
	}
	es := []menuEntry{{label: auto, run: pick("")}}
	for _, o := range c.pickableOutputs() {
		label := c.outputName(o)
		if o.ID == c.outputs.Media {
			label = tr.T("%s · in use", label)
		}
		es = append(es, menuEntry{label: label, run: pick(o.ID)})
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Audio output"), entries: es})
}

// mediaVolumeShown is the volume toast's text for media.
func (c *Core) mediaVolumeShown(v float64) string {
	pct := int(v*100 + 0.5)
	if o := c.mediaOutput(); o.External() {
		return fmt.Sprintf(tr.T("Media volume %d%% · %s"), pct, c.outputName(o))
	}
	return fmt.Sprintf(tr.T("Media volume %d%%"), pct)
}
