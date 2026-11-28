package shell

import (
	"fmt"
	"testing"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/route"
	"github.com/kario-os/kario/ui"
)

func outputsOf(media, noisy string, outs ...route.Output) outputsView {
	return outputsView{Decision: route.Decision{Outputs: outs, Media: media}, Volume: 0.5, Noisy: noisy}
}

// What audiod says of the outputs reaches the user: a device that takes
// media says so, media's volume is the device's own and kept for it,
// pulling out the device media played on says media paused, and the
// picker lists the outputs once there is more than the speaker.
func TestAudioOutputs(t *testing.T) {
	c := menuCore(t)
	speaker := route.Output{ID: route.Speaker, Kind: route.Speaker, Key: route.Speaker}
	headset := route.Output{ID: route.Wired, Kind: route.Wired, Mic: true, Key: route.Wired}
	dac := route.Output{ID: "usb:1", Kind: route.USB, Name: "KA3", HWVolume: true, Key: "usb:2972:0047"}
	if _, ok := c.outputEntry(); ok {
		t.Fatal("a picker with only the speaker")
	}
	c.audioOutputs(outputsOf(route.Speaker, "", speaker))
	c.audioOutputs(outputsOf(route.Wired, "", speaker, headset))
	if c.toast == nil || c.toast.body != tr.T("Playing on %s", tr.T("Wired headset")) {
		t.Fatalf("toast %+v", c.toast)
	}
	c.audioOutputs(outputsOf("usb:1", "", speaker, headset, dac))
	if c.toast.body != tr.T("Playing on %s", "KA3") {
		t.Fatalf("toast %+v", c.toast)
	}

	c.streams = map[string]audio.Stream{"a1": {ID: "a1", App: "com.phone.music", Category: audio.Media, State: ui.PlayPlaying}}
	c.volumeKey(true)
	if v := c.settings.OutputVolumes[dac.Key]; v != 0.6 || c.settings.MediaVolume != 0.7 {
		t.Fatalf("the DAC's volume %v, the speaker's %v", v, c.settings.MediaVolume)
	}
	if c.toast.body != fmt.Sprintf(tr.T("Media volume %d%% · %s"), 60, "KA3") {
		t.Fatalf("toast %+v", c.toast)
	}
	if s := c.settingsGet()["settings"].(sysSettings); s.MediaVolume != 0.6 {
		t.Fatalf("settings.get shows %v", s.MediaVolume)
	}

	c.audioOutputs(outputsOf(route.Wired, "usb:1", speaker, headset))
	if c.toast.body != tr.T("%s disconnected: paused", "KA3") {
		t.Fatalf("toast %+v", c.toast)
	}
	// The headset has no volume of its own yet: audiod's starting level.
	if v := c.mediaVolume(); v != 0.5 {
		t.Fatal(v)
	}
	e, ok := c.outputEntry()
	if !ok || e.label != tr.T("Audio output: %s", tr.T("Wired headset")) {
		t.Fatalf("%+v", e)
	}
	c.openOutputPicker()
	m, ok := c.overlays[len(c.overlays)-1].(*menuOverlay)
	if !ok || m.title != tr.T("Audio output") || len(m.entries) != 3 {
		t.Fatalf("%+v", c.overlays)
	}
	want := []string{tr.T("%s · in use", tr.T("Automatic: the device connected last")), tr.T("Phone speaker"), tr.T("%s · in use", tr.T("Wired headset"))}
	for i, e := range m.entries {
		if e.label != want[i] {
			t.Errorf("entry %d: %q, want %q", i, e.label, want[i])
		}
	}

	c.audioOutputs(outputsOf(route.Speaker, "", speaker))
	c.volumeKey(false)
	if c.settings.MediaVolume != 0.6 || c.toast.body != fmt.Sprintf(tr.T("Media volume %d%%"), 60) {
		t.Fatalf("the speaker's volume %v, toast %+v", c.settings.MediaVolume, c.toast)
	}
}
