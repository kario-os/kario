package audiod

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/route"
	"github.com/kario-os/kario/audio/ucm"
)

// outputs follows what is connected (the jack, USB audio devices, a
// selftest's stand-ins) and plays where the policy says (audio/route):
// the sinks that reach each output, media's volume on each (absolute on a
// device with its own control) and the jack's set-up on the built-in card.
type outputs struct {
	mu     sync.Mutex
	pol    *route.Policy
	m      *audio.Mixer
	base   sink            // the built-in card's (--sink)
	ext    map[string]sink // a USB device's own, by PCM
	dead   []sink          // the sinks of devices gone, to stop
	router *ucm.Router
	hw     hwVolume
	// ctl: a device's volume control by output ID ("" none); level what
	// it was last set to.
	ctl   map[string]string
	level map[string]float64
	vols  map[string]float64 // media's volume by output key
	// profile: how a USB device is set up, by output ID ("UCM HiFi",
	// "generic").
	profile map[string]string
	usb     map[string]usbCard
	// ucmVerb sets a USB card up through its UCM profile if it has one.
	ucmVerb func(card string) error
	media   string // the output media's volume was last applied for
	// changed tells the Shell (an outputs event); noisy is the output
	// whose going paused media.
	changed func(v outputsView)
	logf    func(string, ...any)
	sim     bool // takes output.sim (--sim-outputs)
}

// safeLevel is where a new headphone output's media volume starts:
// never louder than the speaker's, and at most half.
const safeLevel = 0.5

func newOutputs(m *audio.Mixer, base sink, router *ucm.Router) *outputs {
	return &outputs{pol: route.New(router != nil && router.Earpiece.Verb != ""), m: m, base: base, router: router, hw: amixer{},
		ext: map[string]sink{}, ctl: map[string]string{}, level: map[string]float64{}, vols: map[string]float64{},
		profile: map[string]string{}, usb: map[string]usbCard{}, logf: log.Printf}
}

// outputsView is the policy's answer with media's volume on its output.
type outputsView struct {
	route.Decision
	Volume   float64           `json:"volume"`
	Absolute bool              `json:"absolute,omitempty"`
	Profiles map[string]string `json:"profiles,omitempty"`
	Noisy    string            `json:"noisy,omitempty"`
	Sim      bool              `json:"sim,omitempty"`
}

func (o *outputs) view() outputsView {
	md := o.pol.Media()
	v := outputsView{Decision: o.pol.Decide(), Volume: o.levelOf(md), Absolute: o.ctl[md.ID] != "", Sim: o.sim}
	if len(o.profile) > 0 {
		v.Profiles = map[string]string{}
		for id, p := range o.profile {
			v.Profiles[id] = p
		}
	}
	return v
}

// View is what audiod's state and outputs method answer.
func (o *outputs) View() outputsView {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.view()
}

func (o *outputs) tell(noisy string) {
	v := o.view()
	v.Noisy = noisy
	if o.changed != nil {
		o.changed(v)
	}
}

// levelOf is media's volume on an output: as the Shell set it for the
// output's key, else the speaker's, no more than safeLevel for headphones.
func (o *outputs) levelOf(out route.Output) float64 {
	if v, ok := o.vols[out.Key]; ok {
		return v
	}
	sp, ok := o.vols[route.Speaker]
	if !ok {
		sp = audio.DefaultVolume(audio.Media)
	}
	if out.External() {
		return min(sp, safeLevel)
	}
	return sp
}

// applyVolume puts media's volume where its output takes it: the
// device's own control, the samples left at full scale, or the mixer.
func (o *outputs) applyVolume() {
	md := o.pol.Media()
	o.media = md.ID
	v := o.levelOf(md)
	ctl := o.ctl[md.ID]
	if ctl == "" {
		o.m.SetVolume(audio.Media, v)
		return
	}
	if o.level[md.ID] != v || !o.hasLevel(md.ID) {
		if err := o.setHW(md, ctl, v); err != nil {
			o.logf("phone-audiod: %s volume: %v: in the mixer instead", md.ID, err)
			o.ctl[md.ID] = ""
			o.m.SetVolume(audio.Media, v)
			return
		}
		o.level[md.ID] = v
	}
	o.m.SetVolume(audio.Media, 1)
}

func (o *outputs) hasLevel(id string) bool {
	_, ok := o.level[id]
	return ok
}

func (o *outputs) setHW(out route.Output, ctl string, v float64) error {
	if out.Sim {
		return nil
	}
	c, ok := o.usb[out.ID]
	if !ok {
		return errors.New("not a USB device")
	}
	return o.hw.Set(c.num, ctl, v)
}

// SetVolume is media's volume on the output of that key ("" the
// speaker's).
func (o *outputs) SetVolume(key string, v float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if key == "" {
		key = route.Speaker
	}
	o.vols[key] = max(0, min(1, v))
	o.applyVolume()
}

// plug and unplug change what is connected; unplugging the output media
// plays on pauses media.
func (o *outputs) plug(out route.Output) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if have, ok := o.pol.Get(out.ID); ok && have.Key == out.Key && have.Mic == out.Mic && have.HWVolume == out.HWVolume && have.Modem == out.Modem && have.Name == out.Name && have.PCM == out.PCM {
		return
	}
	o.pol.Plug(out)
	o.logf("phone-audiod: output %s (%s %q) connected: media plays on %s", out.ID, out.Kind, out.Name, o.pol.Media().ID)
	o.applyVolume()
	o.tell("")
}

func (o *outputs) unplug(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.pol.Get(id); !ok {
		return
	}
	noisy := o.pol.Unplug(id)
	if s, ok := o.ext[o.pcmOf(id)]; ok {
		// The output loop may be writing to it: it stops it.
		o.dead = append(o.dead, s)
		delete(o.ext, o.pcmOf(id))
	}
	delete(o.usb, id)
	delete(o.ctl, id)
	delete(o.level, id)
	delete(o.profile, id)
	why := ""
	// Before the next tick can play it elsewhere.
	if noisy && len(o.m.Noisy()) > 0 {
		why = id
	}
	o.logf("phone-audiod: output %s disconnected (media paused: %v): media plays on %s", id, noisy, o.pol.Media().ID)
	o.applyVolume()
	o.tell(why)
}

func (o *outputs) pcmOf(id string) string {
	if c, ok := o.usb[id]; ok {
		return usbPCM(c.num)
	}
	return ""
}

func usbPCM(num string) string { return "plughw:" + num + ",0" }

// Pick is the user's choice for media, "" automatic.
func (o *outputs) Pick(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.pol.Pick(id); err != nil {
		return err
	}
	o.applyVolume()
	o.tell("")
	return nil
}

// SetCallSpeaker follows the call screen's speakerphone.
func (o *outputs) SetCallSpeaker(on bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pol.SetCallSpeaker(on)
}

// jack follows the 3.5 mm jack.
func (o *outputs) jack(st jackState) {
	if !st.in() {
		o.unplug(route.Wired)
		return
	}
	modem := o.router != nil && o.router.Headset.Verb != ""
	o.plug(route.Output{ID: route.Wired, Kind: route.Wired, Mic: st.mic, Modem: modem, Key: route.Wired})
}

// usbCards takes a scan's USB devices: new ones are plugged (set up
// through their UCM profile if they have one, their volume control
// found), gone ones unplugged.
func (o *outputs) usbCards(cards []usbCard) {
	seen := map[string]bool{}
	for _, c := range cards {
		id := "usb:" + c.num
		if b, ok := o.base.(*alsaSink); ok && b.base == usbPCM(c.num) {
			// The sink's own card: a board whose only card is USB.
			continue
		}
		seen[id] = true
		o.mu.Lock()
		have, ok := o.usb[id]
		o.mu.Unlock()
		if ok && have == c {
			continue
		}
		if ok {
			o.unplug(id)
		}
		profile := "generic"
		if o.ucmVerb != nil && o.ucmVerb("hw:"+c.num) == nil {
			profile = "UCM HiFi"
		}
		ctl := ""
		if o.hw != nil {
			ctl = o.hw.Control(c.num)
		}
		o.mu.Lock()
		o.usb[id], o.ctl[id], o.profile[id] = c, ctl, profile
		o.mu.Unlock()
		o.logf("phone-audiod: USB audio %s %q (%s), %s profile, volume control %q, microphone %v", c.id, c.name, id, profile, ctl, c.mic)
		o.plug(route.Output{ID: id, Kind: route.USB, Name: c.name, Mic: c.mic, HWVolume: ctl != "", PCM: usbPCM(c.num), Key: "usb:" + c.id})
	}
	o.mu.Lock()
	var gone []string
	for _, out := range o.pol.Outputs() {
		if out.Kind == route.USB && !out.Sim && !seen[out.ID] {
			gone = append(gone, out.ID)
		}
	}
	o.mu.Unlock()
	for _, id := range gone {
		o.unplug(id)
	}
}

// watchUSB scans for USB audio devices every half second.
func (o *outputs) watchUSB(asound string) {
	for {
		o.usbCards(scanUSB(asound))
		time.Sleep(500 * time.Millisecond)
	}
}

// simArgs: output.sim, a selftest's stand-in for a device being plugged
// in or pulled out.
type simArgs struct {
	Op       string `json:"op"` // plug | unplug
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Mic      bool   `json:"mic"`
	HWVolume bool   `json:"hw_volume"`
}

func (o *outputs) Sim(a simArgs) error {
	if !o.sim {
		return errors.New("output.sim: audiod runs without --sim-outputs")
	}
	switch a.Op {
	case "unplug":
		o.mu.Lock()
		out, ok := o.pol.Get(a.ID)
		o.mu.Unlock()
		if !ok || !out.Sim {
			return fmt.Errorf("no stand-in output %q", a.ID)
		}
		o.unplug(a.ID)
		return nil
	case "plug":
	default:
		return fmt.Errorf("unknown op %q", a.Op)
	}
	if !slices.Contains([]string{route.Wired, route.USB, route.Bluetooth}, a.Kind) || a.ID == "" || (a.Kind == route.Wired) != (a.ID == route.Wired) {
		return errors.New("a stand-in is wired (id wired), usb or bluetooth, with an id")
	}
	out := route.Output{ID: a.ID, Kind: a.Kind, Name: a.Name, Mic: a.Mic, HWVolume: a.HWVolume, Key: a.Kind + ":" + strings.ToLower(a.Name), Sim: true}
	if a.Kind == route.Wired {
		out.Key, out.Modem = route.Wired, o.router != nil && o.router.Headset.Verb != ""
	}
	o.mu.Lock()
	if a.HWVolume {
		o.ctl[a.ID] = "PCM"
	}
	o.mu.Unlock()
	o.plug(out)
	return nil
}

// targets are the sinks the mixer plays to while top is the highest
// category sounding: a call's output, the ringtone's (the speaker and
// the headphones), else media's. modem: a cellular call is on, its voice
// in the DSP. The first is the one music alone may take at its own format.
func (o *outputs) targets(top string, modem bool) []sink {
	o.mu.Lock()
	defer o.mu.Unlock()
	var outs []route.Output
	jack := ucm.Jack{}
	for _, s := range o.dead {
		if a, ok := s.(*alsaSink); ok {
			a.stop()
		}
	}
	o.dead = nil
	switch top {
	case audio.Call:
		c, _ := o.pol.Call(modem)
		outs = []route.Output{c}
	case audio.Ring, audio.Alarm, audio.Alert:
		outs = o.pol.Ring()
	default:
		outs = []route.Output{o.pol.Media()}
	}
	if c, mic := o.pol.Call(true); c.Kind == route.Wired {
		jack.Call, jack.CallMic = true, mic == route.Wired
	}
	wired, speaker := false, false
	for _, out := range outs {
		wired = wired || out.Kind == route.Wired
		speaker = speaker || out.Kind == route.Speaker
	}
	switch {
	case wired && speaker:
		jack.Media = ucm.JackRing
	case wired:
		jack.Media = ucm.JackMedia
	}
	if o.router != nil {
		if err := o.router.SetJack(jack); err != nil {
			o.logf("phone-audiod: jack routing: %v", err)
		}
	}
	if o.media != o.pol.Media().ID {
		o.applyVolume()
	}
	var sinks []sink
	for _, out := range outs {
		s := o.sinkFor(out)
		if !slices.Contains(sinks, s) {
			sinks = append(sinks, s)
		}
	}
	// Release the cards nothing plays to.
	for _, s := range o.ext {
		if a, ok := s.(*alsaSink); ok && !slices.Contains(sinks, s) && a.w != nil {
			a.stop()
		}
	}
	if b, ok := o.base.(*alsaSink); ok && !slices.Contains(sinks, o.base) && b.w != nil {
		b.stop()
	}
	return sinks
}

// sinkFor is the sink that reaches an output: a USB device's own aplay
// when audiod plays through ALSA, else the built-in one (a stand-in, the
// jack and the built-in outputs; the file and null sinks take all).
func (o *outputs) sinkFor(out route.Output) sink {
	if out.PCM == "" || out.Sim {
		return o.base
	}
	if _, ok := o.base.(*alsaSink); !ok {
		return o.base
	}
	s, ok := o.ext[out.PCM]
	if !ok {
		s = &alsaSink{device: out.PCM, base: out.PCM}
		o.ext[out.PCM] = s
	}
	return s
}
