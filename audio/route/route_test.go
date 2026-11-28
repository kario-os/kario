package route

import (
	"slices"
	"testing"
)

var (
	headset = Output{ID: Wired, Kind: Wired, Mic: true, Modem: true}
	phones  = Output{ID: Wired, Kind: Wired, Modem: true}
	dac     = Output{ID: "usb:1", Kind: USB, Name: "KA3", HWVolume: true, PCM: "plughw:1,0", Key: "usb:2972:0047"}
	usbSet  = Output{ID: "usb:2", Kind: USB, Name: "USB headset", Mic: true, PCM: "plughw:2,0", Key: "usb:046d:0a8f"}
	buds    = Output{ID: "bt:aa", Kind: Bluetooth, Name: "Buds", Mic: true, Key: "bt:aa"}
)

func ids(os []Output) []string {
	var out []string
	for _, o := range os {
		out = append(out, o.ID)
	}
	return out
}

func call(t *testing.T, p *Policy, modem bool, out, mic string) {
	t.Helper()
	o, m := p.Call(modem)
	if o.ID != out || m != mic {
		t.Fatalf("call (modem %v) on %s with mic %s, want %s with %s", modem, o.ID, m, out, mic)
	}
}

// The owner's case: a 3.5 mm headset in, then a USB-C DAC, then
// Bluetooth; each newest one plays media, and the call follows it.
func TestNewestWins(t *testing.T) {
	p := New(true)
	if p.Media().ID != Speaker {
		t.Fatal(p.Media())
	}
	call(t, p, true, Earpiece, PhoneMic)
	if got := ids(p.Ring()); !slices.Equal(got, []string{Speaker}) {
		t.Fatal(got)
	}

	p.Plug(headset)
	if p.Media().ID != Wired {
		t.Fatal(p.Media())
	}
	call(t, p, true, Wired, Wired)
	call(t, p, false, Wired, Wired)
	if got := ids(p.Ring()); !slices.Equal(got, []string{Speaker, Wired}) {
		t.Fatal(got)
	}

	p.Plug(dac)
	if p.Media().ID != dac.ID {
		t.Fatal(p.Media())
	}
	// The DAC has no microphone: a VoIP call plays on it with the phone's.
	call(t, p, false, dac.ID, PhoneMic)
	// The modem's voice cannot reach a USB device: the jack carries it.
	call(t, p, true, Wired, Wired)
	if got := ids(p.Ring()); !slices.Equal(got, []string{Speaker, dac.ID}) {
		t.Fatal(got)
	}

	p.Plug(buds)
	call(t, p, false, buds.ID, buds.ID)
	call(t, p, true, Wired, Wired)
}

func TestModemCallWithoutJack(t *testing.T) {
	p := New(true)
	p.Plug(usbSet)
	call(t, p, false, usbSet.ID, usbSet.ID)
	call(t, p, true, Earpiece, PhoneMic)
	p.Plug(phones)
	// Headphones without a microphone: the call is heard in them, the
	// phone listens.
	call(t, p, true, Wired, PhoneMic)
	// A phone without an earpiece puts calls on the speaker.
	q := New(false)
	call(t, q, true, Speaker, PhoneMic)
}

func TestUnplugNoisy(t *testing.T) {
	p := New(true)
	p.Plug(headset)
	p.Plug(dac)
	// Not the output media plays on: nothing to pause.
	if p.Unplug(Wired) {
		t.Fatal("the headset was not playing media")
	}
	if p.Media().ID != dac.ID {
		t.Fatal(p.Media())
	}
	p.Plug(headset)
	if !p.Unplug(Wired) {
		t.Fatal("unplugging the output media plays on is noisy")
	}
	if p.Media().ID != dac.ID {
		t.Fatalf("media goes back to the DAC, on %s", p.Media().ID)
	}
	if !p.Unplug(dac.ID) || p.Media().ID != Speaker {
		t.Fatal(p.Media())
	}
	// The built-in outputs never go away; an unknown one is nothing.
	if p.Unplug(Speaker) || p.Unplug("usb:9") {
		t.Fatal("unplugged what cannot be")
	}
	if _, ok := p.Get(Speaker); !ok {
		t.Fatal("the speaker went")
	}
}

func TestPick(t *testing.T) {
	p := New(true)
	p.Plug(headset)
	p.Plug(dac)
	if err := p.Pick(Earpiece); err == nil {
		t.Fatal("the earpiece is for calls")
	}
	if err := p.Pick("usb:9"); err == nil {
		t.Fatal("picked what is not there")
	}
	if err := p.Pick(Wired); err != nil || p.Media().ID != Wired {
		t.Fatal(err, p.Media())
	}
	call(t, p, false, Wired, Wired)
	// Unplugging what was not picked is not noisy; the pick stays.
	if p.Unplug(dac.ID) || p.Media().ID != Wired {
		t.Fatal(p.Media())
	}
	// Media on the speaker by choice: calls go to the earpiece.
	if err := p.Pick(Speaker); err != nil {
		t.Fatal(err)
	}
	call(t, p, true, Earpiece, PhoneMic)
	if got := ids(p.Ring()); !slices.Equal(got, []string{Speaker}) {
		t.Fatal(got)
	}
	// A new device wins over the choice, as on Android.
	p.Plug(dac)
	if p.Media().ID != dac.ID || p.Picked() != "" {
		t.Fatal(p.Media(), p.Picked())
	}
	// Unplugging the picked output goes back to automatic.
	_ = p.Pick(Wired)
	if !p.Unplug(Wired) || p.Picked() != "" || p.Media().ID != dac.ID {
		t.Fatal(p.Media(), p.Picked())
	}
	if err := p.Pick(""); err != nil {
		t.Fatal(err)
	}
}

func TestCallSpeaker(t *testing.T) {
	p := New(true)
	p.Plug(headset)
	p.SetCallSpeaker(true)
	call(t, p, true, Speaker, PhoneMic)
	p.SetCallSpeaker(false)
	call(t, p, true, Wired, Wired)
}

// The jack's microphone shows up after its headphones (a second switch):
// the same output again, still the newest.
func TestReplug(t *testing.T) {
	p := New(true)
	p.Plug(phones)
	p.Plug(dac)
	_ = p.Pick(Wired)
	p.Plug(dac) // as it was: nothing changes
	if p.Picked() != Wired {
		t.Fatal("plugging the same device again dropped the pick")
	}
	p.Plug(headset)
	if p.Media().ID != Wired || len(p.Outputs()) != 4 {
		t.Fatal(p.Media(), p.Outputs())
	}
	call(t, p, true, Wired, Wired)
	d := p.Decide()
	if d.Media != Wired || d.ModemCall != Wired || d.ModemMic != Wired || d.Call != Wired || !slices.Equal(d.Ring, []string{Speaker, Wired}) {
		t.Fatalf("%+v", d)
	}
	if d.Outputs[0].Key != Speaker || d.Outputs[2].Key != "usb:2972:0047" || d.Outputs[3].Key != Wired {
		t.Fatalf("keys %+v", d.Outputs)
	}
}
