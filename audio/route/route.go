// Package route is phone-audiod's policy for several outputs at once: the
// phone's own speaker and earpiece, a headset in the 3.5 mm jack, USB
// audio devices (a DAC dongle, a USB headset) and Bluetooth. It decides
// which output plays media, which plays calls (and with which
// microphone) and which play ringtones and alarms, and what a plug or an
// unplug does. It does no I/O: audiod tells it what is connected and
// plays where it says.
//
// The rules follow Android's: the output connected last plays media
// until the user picks another; calls follow that output, with its own
// microphone when it has one; ringtones and alarms sound on the speaker
// and on that output; unplugging the output media plays on pauses media
// (Android's "becoming noisy").
package route

import (
	"errors"
	"slices"
)

// Kinds of output.
const (
	Speaker   = "speaker"
	Earpiece  = "earpiece"
	Wired     = "wired" // the 3.5 mm jack: headphones, or a headset with a microphone
	USB       = "usb"
	Bluetooth = "bluetooth"
)

// PhoneMic is the phone's own microphone, the call's when its output has
// none.
const PhoneMic = "phone"

// Output is one place sound can go.
type Output struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Name is the device's own (a USB product, a Bluetooth alias); the
	// Shell names the built-in ones and the jack.
	Name string `json:"name,omitempty"`
	// Mic: it has its own microphone (a headset).
	Mic bool `json:"mic,omitempty"`
	// Modem: a cellular call's voice reaches it. The modem's voice goes
	// from the DSP to the phone's own codec (the card's UCM call set-ups),
	// so only the earpiece, the speaker and the jack have it; USB and
	// Bluetooth devices carry the calls whose sound goes through the
	// mixer (VoIP).
	Modem bool `json:"modem,omitempty"`
	// HWVolume: the device has its own volume control, which media's
	// volume sets (absolute volume) while the samples go out untouched.
	HWVolume bool `json:"hw_volume,omitempty"`
	// PCM is the ALSA device the mixer plays to for it; "" the built-in
	// card's (routed by its UCM profile).
	PCM string `json:"pcm,omitempty"`
	// Key is what its volume is remembered by: the kind for the built-in
	// outputs and the jack, a USB device's vendor:product, a Bluetooth
	// address.
	Key string `json:"key"`
	// Sim: a selftest's stand-in, not a device.
	Sim bool `json:"sim,omitempty"`
	seq int
}

// External outputs are the ones plugged in or paired.
func (o Output) External() bool { return o.Kind != Speaker && o.Kind != Earpiece }

// Policy holds what is connected and the user's choice.
type Policy struct {
	outs        []Output
	seq         int
	picked      string
	callSpeaker bool
}

// New starts with the phone's own outputs: the speaker, and the earpiece
// on a phone that has one.
func New(earpiece bool) *Policy {
	p := &Policy{}
	p.add(Output{ID: Speaker, Kind: Speaker, Key: Speaker, Modem: true})
	if earpiece {
		p.add(Output{ID: Earpiece, Kind: Earpiece, Key: Earpiece, Modem: true})
	}
	return p
}

func (p *Policy) add(o Output) {
	p.seq++
	o.seq = p.seq
	if o.Key == "" {
		o.Key = o.Kind
	}
	p.outs = append(p.outs, o)
}

func (p *Policy) index(id string) int {
	return slices.IndexFunc(p.outs, func(o Output) bool { return o.ID == id })
}

// Outputs lists what is connected, in the order it was.
func (p *Policy) Outputs() []Output { return slices.Clone(p.outs) }

// Get is the connected output id.
func (p *Policy) Get(id string) (Output, bool) {
	if i := p.index(id); i >= 0 {
		return p.outs[i], true
	}
	return Output{}, false
}

// Plug connects an output (again: a device that was there is replaced).
// It plays media from now on, the user's earlier choice put aside, as on
// Android. Plugging what is already connected as it is changes nothing.
func (p *Policy) Plug(o Output) {
	if i := p.index(o.ID); i >= 0 {
		old := p.outs[i]
		o.seq = old.seq
		if o.Key == "" {
			o.Key = o.Kind
		}
		if o == old {
			return
		}
		p.outs = slices.Delete(p.outs, i, i+1)
	}
	p.add(o)
	p.picked = ""
}

// Unplug disconnects an output. noisy: media played on it, and is to be
// paused rather than go on aloud elsewhere.
func (p *Policy) Unplug(id string) (noisy bool) {
	i := p.index(id)
	if i < 0 || !p.outs[i].External() {
		return false
	}
	noisy = p.Media().ID == id
	p.outs = slices.Delete(p.outs, i, i+1)
	if p.picked == id {
		p.picked = ""
	}
	return noisy
}

// Pick is the user's choice of the output for media; "" goes back to
// the one connected last.
func (p *Policy) Pick(id string) error {
	if id == "" {
		p.picked = ""
		return nil
	}
	o, ok := p.Get(id)
	switch {
	case !ok:
		return errors.New("no output " + id)
	case o.Kind == Earpiece:
		return errors.New("the earpiece plays calls only")
	}
	p.picked = id
	return nil
}

// Picked is the user's choice, "" automatic.
func (p *Policy) Picked() string { return p.picked }

// SetCallSpeaker is the call screen's speakerphone.
func (p *Policy) SetCallSpeaker(on bool) { p.callSpeaker = on }

// Media is the output media plays on: the user's pick, else the external
// output connected last, else the speaker.
func (p *Policy) Media() Output {
	if o, ok := p.Get(p.picked); ok {
		return o
	}
	best := -1
	for i, o := range p.outs {
		if o.External() && (best < 0 || o.seq > p.outs[best].seq) {
			best = i
		}
	}
	if best >= 0 {
		return p.outs[best]
	}
	o, _ := p.Get(Speaker)
	return o
}

// Call is the output and microphone of a call: the speaker with the
// speakerphone on; else the output media plays on when it is external
// and the call's sound reaches it (modem: a cellular call, which only
// the phone's codec carries), with its microphone if it has one; else
// the jack for a cellular call while media is on a USB or Bluetooth
// device; else the earpiece (the speaker on a phone without one).
func (p *Policy) Call(modem bool) (out Output, mic string) {
	builtin := func() Output {
		if o, ok := p.Get(Earpiece); ok {
			return o
		}
		o, _ := p.Get(Speaker)
		return o
	}
	if p.callSpeaker {
		o, _ := p.Get(Speaker)
		return o, PhoneMic
	}
	o := p.Media()
	if !o.External() {
		return builtin(), PhoneMic
	}
	if modem && !o.Modem {
		o = Output{}
		for _, c := range p.outs {
			if c.External() && c.Modem && c.seq > o.seq {
				o = c
			}
		}
		if o.ID == "" {
			return builtin(), PhoneMic
		}
	}
	if o.Mic {
		return o, o.ID
	}
	return o, PhoneMic
}

// Ring is where ringtones and alarms sound: the speaker, so that a phone
// put down is heard, and the output media plays on when that is
// external, so that someone wearing headphones hears it too.
func (p *Policy) Ring() []Output {
	sp, _ := p.Get(Speaker)
	out := []Output{sp}
	if o := p.Media(); o.External() {
		out = append(out, o)
	}
	return out
}

// Decision is the policy's answer for audiod's state and the Shell.
type Decision struct {
	Outputs []Output `json:"outputs"`
	Media   string   `json:"media"`
	Call    string   `json:"call"`
	CallMic string   `json:"call_mic"`
	// ModemCall and ModemMic: a cellular call's, where Call is a VoIP
	// call's.
	ModemCall string   `json:"modem_call"`
	ModemMic  string   `json:"modem_mic"`
	Ring      []string `json:"ring"`
	Picked    string   `json:"picked,omitempty"`
}

// Decide is the whole answer.
func (p *Policy) Decide() Decision {
	d := Decision{Outputs: p.Outputs(), Media: p.Media().ID, Picked: p.picked}
	c, mic := p.Call(false)
	d.Call, d.CallMic = c.ID, mic
	c, mic = p.Call(true)
	d.ModemCall, d.ModemMic = c.ID, mic
	for _, o := range p.Ring() {
		d.Ring = append(d.Ring, o.ID)
	}
	return d
}
