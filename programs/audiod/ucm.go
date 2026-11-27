package audiod

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"path/filepath"
	"slices"
	"time"

	"github.com/kario-os/kario/audio/ucm"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// ucmConfig turns on routing through the card's ALSA UCM profile: the
// device configuration's flags (the OnePlus 6T's drop-in from
// phone-qcom-audio). Without --ucm-card audiod leaves the card alone.
type ucmConfig struct {
	card, verb, device              string
	callVerb, earpiece, earpiecePCM string
	speaker, speakerPCM, mic        string
	// The 3.5 mm jack's devices: in the media verb, in the call verb and
	// the headset's microphone there.
	headphones, headphonesPCM  string
	callHeadphones, headsetMic string
}

func (c *ucmConfig) flags() {
	flag.StringVar(&c.card, "ucm-card", "", "sound card whose ALSA UCM profile audiod sets up (hw:0); none when empty")
	flag.StringVar(&c.verb, "ucm-verb", "", "UCM verb for media (HiFi)")
	flag.StringVar(&c.device, "ucm-device", "", "UCM device for media (Speaker)")
	flag.StringVar(&c.callVerb, "ucm-call-verb", "", "UCM verb during a call (Voice Call); calls change nothing when empty")
	flag.StringVar(&c.earpiece, "ucm-earpiece", "", "UCM device of the earpiece during a call")
	flag.StringVar(&c.earpiecePCM, "ucm-earpiece-pcm", "", "PCM the mixer plays to while the call is on the earpiece (the sink's when empty)")
	flag.StringVar(&c.speaker, "ucm-speaker", "", "UCM device of the speaker during a call; calls stay on the earpiece when empty")
	flag.StringVar(&c.speakerPCM, "ucm-speaker-pcm", "", "PCM the mixer plays to while the call is on the speaker (the sink's when empty)")
	flag.StringVar(&c.mic, "ucm-mic", "", "UCM device of the microphone during a call, left out while the call is muted")
	flag.StringVar(&c.headphones, "ucm-headphones", "", "UCM device of the 3.5 mm jack's headphones for media (Headphones); media stays on the speaker when empty")
	flag.StringVar(&c.headphonesPCM, "ucm-headphones-pcm", "", "PCM the mixer plays to while media is on the headphones (the sink's when empty)")
	flag.StringVar(&c.callHeadphones, "ucm-call-headphones", "", "UCM device of the jack's headphones during a call; calls stay off the jack when empty")
	flag.StringVar(&c.headsetMic, "ucm-call-headset-mic", "", "UCM device of a headset's microphone during a call, in place of --ucm-mic")
}

func (c *ucmConfig) router(runDir string) (*ucm.Router, error) {
	if c.card == "" {
		return nil, nil
	}
	if c.verb == "" {
		return nil, errors.New("--ucm-card needs --ucm-verb")
	}
	r := &ucm.Router{Tool: ucm.Alsaucm{Card: c.card}, Media: ucm.Setup{Verb: c.verb, Devices: devices(c.device)},
		Mic: c.mic, State: filepath.Join(runDir, "audiod-ucm.json")}
	if c.headphones != "" {
		r.Headphones = ucm.Setup{Verb: c.verb, Devices: devices(c.headphones), PCM: c.headphonesPCM}
	}
	if c.callVerb == "" {
		return r, nil
	}
	if c.earpiece == "" {
		return nil, errors.New("--ucm-call-verb needs --ucm-earpiece")
	}
	r.Earpiece = ucm.Setup{Verb: c.callVerb, Devices: devices(c.earpiece, c.mic), PCM: c.earpiecePCM}
	if c.speaker != "" {
		r.Speaker = ucm.Setup{Verb: c.callVerb, Devices: devices(c.speaker, c.mic), PCM: c.speakerPCM}
	}
	if c.callHeadphones != "" {
		r.Headset = ucm.Setup{Verb: c.callVerb, Devices: devices(c.callHeadphones, c.mic), PCM: c.headphonesPCM}
		r.HeadsetMic = c.headsetMic
	}
	return r, nil
}

func devices(names ...string) []string {
	return slices.DeleteFunc(names, func(s string) bool { return s == "" })
}

type telEvent struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Event  string          `json:"event"`
	Call   *ui.TelCall     `json:"call"`
}

const callsReq = 2

// followCalls tells the router telephonyd's calls, as q6voiced follows
// ModemManager's: the list on each connection, then every change. While
// telephonyd is away the card stays as it is.
func followCalls(sock string, r *ucm.Router) {
	for {
		if nc, err := net.Dial("unix", sock); err == nil {
			followSession(nc, r)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func followSession(nc net.Conn, r *ucm.Router) {
	conn := wire.New(nc)
	defer conn.Close()
	if conn.Send(wire.RPC{ID: 1, Method: wire.Subscribe}) != nil || conn.Send(wire.RPC{ID: callsReq, Method: svc.TelCalls}) != nil {
		return
	}
	for {
		var ev telEvent
		if conn.Recv(&ev) != nil {
			return
		}
		var err error
		switch {
		case ev.ID == callsReq && ev.OK:
			var calls []ui.TelCall
			if err = json.Unmarshal(ev.Result, &calls); err == nil {
				err = r.SetCalls(calls)
			}
		case ev.Event == svc.TelEventCall && ev.Call != nil:
			err = r.Call(*ev.Call)
		}
		if err != nil {
			log.Printf("phone-audiod: call routing: %v", err)
		}
	}
}

// silence stands in for the simulated far end of a call when the modem
// carries the call's sound: its stream still holds the focus that keeps
// media paused.
type silence struct{ n int }

func (s *silence) Fill(buf []float64) bool {
	clear(buf)
	s.n += len(buf)
	return true
}

func (s *silence) Pos() int { return s.n }
func (s *silence) Len() int { return 0 }

// routeSink plays to the PCM of the card's set-up.
func routeSink(out sink, pcm string) {
	if a, ok := out.(*alsaSink); ok {
		a.route(pcm)
	}
}
