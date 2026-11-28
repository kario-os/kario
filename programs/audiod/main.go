// phone-audiod mixes every sound on the phone: system tones from the shell
// (ringtone, ringback, alarm, notification, call audio) and media from apps,
// under one focus policy (see package audio). Music that plays alone goes
// to the sound card at its own format instead (audio/hifi): bit-perfect,
// DoP or native DSD where the card takes it.
//
//	phone-audiod --run-dir DIR [--sink auto|null|alsa[:device]|file:PATH] [--ucm-card CARD ...]
//
// The shell is its only client (socket mode 0600); apps reach it through the
// broker. Production replaces the output with PipeWire and keeps the policy.
// It also runs the speech service the screen reader talks to (audio/tts).
//
// On a phone whose sound card is routed by an ALSA UCM profile the device
// configuration adds the --ucm-* flags: audiod then sets the card up for
// media, and, following telephonyd's calls, for the call on the earpiece or
// the speaker (audio/ucm).
package audiod

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/hifi"
	"github.com/kario-os/kario/audio/opusfile"
	"github.com/kario-os/kario/audio/tts"
	"github.com/kario-os/kario/audio/ucm"
	"github.com/kario-os/kario/audio/vorbisfile"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

type playArgs struct {
	App      string `json:"app"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Source   string `json:"source"` // tone:<name> | demo:<id> | file:<path>
	// Music: the track (in place of Source), the one after it, where to
	// start and the DSD output setting.
	Track   *hifi.Spec `json:"track,omitempty"`
	Next    *hifi.Spec `json:"next,omitempty"`
	StartMS int64      `json:"start_ms,omitempty"`
	DSD     string     `json:"dsd,omitempty"`
}

// controlArgs: op is pause | resume | stop, and for music seek (AtMS),
// queue (Next, nil to clear) and options (DSD).
type controlArgs struct {
	ID   string     `json:"id"`
	Op   string     `json:"op"`
	AtMS int64      `json:"at_ms"`
	Next *hifi.Spec `json:"next"`
	DSD  string     `json:"dsd"`
}

func source(spec string) (audio.Source, error) {
	switch {
	case strings.HasPrefix(spec, "tone:"):
		mk, ok := audio.SystemTones[strings.TrimPrefix(spec, "tone:")]
		if !ok {
			return nil, fmt.Errorf("unknown tone %q", spec)
		}
		return mk(), nil
	case strings.HasPrefix(spec, "demo:"):
		t, ok := audio.TrackByID(spec)
		if !ok {
			return nil, fmt.Errorf("unknown track %q", spec)
		}
		return audio.NewSynth(t)
	case strings.HasPrefix(spec, "file:"):
		path := strings.TrimPrefix(spec, "file:")
		switch {
		case strings.EqualFold(filepath.Ext(path), ".mp3"):
			return audio.OpenMP3(path)
		case audio.IsOgg(path):
			if codec, err := audio.OggCodec(path); err != nil {
				return nil, err
			} else if codec == audio.CodecVorbis {
				return vorbisfile.Open(path)
			}
			return opusfile.Open(path)
		}
		return audio.OpenWav(path)
	}
	return nil, errors.New("source must be tone:, demo: or file:")
}

func dsdMode(m string) string {
	switch m {
	case hifi.DSDDoP, hifi.DSDPCM:
		return m
	}
	return hifi.DSDAuto
}

// maxStreams bounds the shared memory audiod hands out, whatever the shell
// allows each app.
const maxStreams = 32

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	runDir := flag.String("run-dir", "/run/phone", "directory for audiod.sock")
	sinkSpec := flag.String("sink", "auto", "auto | null | alsa[:device] | file:PATH")
	simOutputs := flag.Bool("sim-outputs", false, "take output.sim: a selftest's stand-ins for headsets, USB and Bluetooth devices")
	var uc ucmConfig
	uc.flags()
	flag.Parse()
	out, err := openSink(*sinkSpec)
	if err != nil {
		log.Fatal(err)
	}
	router, err := uc.router(*runDir)
	if err != nil {
		log.Fatal(err)
	}
	// The modem carries a call's sound when the card has a call set-up.
	modemVoice := router != nil && router.Earpiece.Verb != ""
	if router != nil {
		if err := router.Start(); err != nil {
			log.Printf("phone-audiod: %v", err)
		}
		if modemVoice {
			go followCalls(filepath.Join(*runDir, svc.TelephonydSock), router)
		}
	}
	m := audio.NewMixer()
	rec := &recordings{}
	d := &daemon{m: m, out: out, router: router, modemVoice: modemVoice, rec: rec,
		play: newPlayer(m, out, router, rec), speech: tts.New(tts.Simulated{Log: log.Printf})}
	m.OnChange = func(s audio.Stream) {
		if d.hub != nil {
			d.hub.Broadcast(map[string]any{"event": svc.AudioEventStream, "stream": s})
		}
	}
	outs := newOutputs(m, out, router)
	outs.sim = *simOutputs
	outs.ucmVerb = func(card string) error { return ucm.Alsaucm{Card: card}.Run([]string{"set", "_verb", "HiFi"}) }
	outs.changed = func(v outputsView) {
		if d.hub != nil {
			d.hub.Broadcast(map[string]any{"event": svc.AudioEventOutputs, "outputs": v})
		}
	}
	d.outs = outs
	d.play.outs = outs
	if _, ok := out.(*alsaSink); ok {
		go outs.watchUSB("/proc/asound")
	}
	go watchJack("/sys/class/input", "/dev/input", outs)
	d.hub, err = wire.ServeHub(filepath.Join(*runDir, svc.AudiodSock), d.handle)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("phone-audiod: sink %s on %s/audiod.sock", out.Name(), *runDir)
	d.play.run(time.NewTicker(time.Second / 50).C)
}

// daemon is what audiod.sock's requests act on.
type daemon struct {
	m          *audio.Mixer
	out        sink
	router     *ucm.Router
	modemVoice bool
	rec        *recordings
	play       *player
	speech     *tts.Service
	hub        *wire.Hub
	outs       *outputs
}

func (d *daemon) music(id string) (*hifi.Stream, error) {
	if s, ok := d.m.Source(id).(*hifi.Stream); ok {
		return s, nil
	}
	return nil, fmt.Errorf("stream %s is not music", id)
}

func (d *daemon) handle(method string, params json.RawMessage) (any, error) {
	m, rec := d.m, d.rec
	switch method {
	case svc.AudioPlay:
		var a playArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		var src audio.Source
		var err error
		if a.Track != nil {
			s, err := hifi.Open(*a.Track, hifi.Options{DSD: dsdMode(a.DSD)}, a.StartMS)
			if err != nil {
				return nil, err
			}
			if a.Next != nil {
				s.Queue(a.Next)
			}
			src = s
		} else if d.modemVoice && a.Source == "tone:voice" {
			src = &silence{}
		} else if src, err = source(a.Source); err != nil {
			return nil, err
		}
		return m.Play(a.App, a.Name, a.Category, src)
	case svc.AudioOpen:
		// An app's own stream: a sealed shared ring the caller passes on
		// to the app. "play": the app writes, the mixer plays it in the
		// given category. "record": the microphone writes, the app reads.
		var a struct{ App, Name, Category, Dir string }
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if rec.count()+len(m.State()) >= maxStreams {
			return nil, fmt.Errorf("audiod: %d streams open", maxStreams)
		}
		f, err := audio.NewRingFile("phone-audio-"+a.App, audio.Rate*2)
		if err != nil {
			return nil, err
		}
		ring, err := audio.MapRing(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		switch a.Dir {
		case svc.AudioDirPlay:
			s, err := m.Play(a.App, a.Name, a.Category, audio.NewRingSource(ring))
			if err != nil {
				ring.Close()
				f.Close()
				return nil, err
			}
			return wire.Files{Result: s, Files: []*os.File{f}}, nil
		case svc.AudioDirRecord:
			s := rec.start(a.App, a.Name, ring)
			d.hub.Broadcast(map[string]any{"event": svc.AudioEventStream, "stream": s})
			return wire.Files{Result: s, Files: []*os.File{f}}, nil
		}
		ring.Close()
		f.Close()
		return nil, fmt.Errorf("unknown direction %q", a.Dir)
	case svc.AudioControl:
		var a controlArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		switch a.Op {
		case "stop":
			if s, ok := rec.stop(a.ID); ok {
				d.hub.Broadcast(map[string]any{"event": svc.AudioEventStream, "stream": s})
				return s, nil
			}
		case ui.OpSeek, ui.OpQueue, ui.OpOptions:
			s, err := d.music(a.ID)
			if err != nil {
				return nil, err
			}
			switch a.Op {
			case ui.OpSeek:
				if err := s.SeekMS(a.AtMS); err != nil {
					return nil, err
				}
			case ui.OpQueue:
				s.Queue(a.Next)
			case ui.OpOptions:
				s.SetOptions(hifi.Options{DSD: dsdMode(a.DSD)})
			}
			return m.Touch(a.ID)
		}
		return m.Control(a.ID, a.Op)
	case svc.AudioState:
		streams := append(m.State(), rec.streams()...)
		mono, balance := m.Channels()
		st := map[string]any{"streams": streams, "route": m.Route(), "sink": d.out.Name(), "frames": m.Frames, "mono": mono, "balance": balance}
		// No sound device: the mixer runs on, its output lost.
		if e := d.play.sink.failure(); e != "" {
			st["sink_error"] = e
		}
		if d.router != nil {
			st["ucm"] = d.router.Setup()
		}
		st["outputs"] = d.outs.View()
		return st, nil
	case svc.AudioOutputs:
		return d.outs.View(), nil
	case svc.AudioOutputPick:
		var a struct{ ID string }
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if err := d.outs.Pick(a.ID); err != nil {
			return nil, err
		}
		return d.outs.View(), nil
	case svc.AudioOutputSim:
		var a simArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if err := d.outs.Sim(a); err != nil {
			return nil, err
		}
		return d.outs.View(), nil
	case svc.AudioVolume:
		// Media's volume is an output's ("" the speaker's: key).
		var a struct {
			Category string
			Level    float64
			Output   string
		}
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if a.Category == audio.Media {
			d.outs.SetVolume(a.Output, a.Level)
		} else {
			m.SetVolume(a.Category, a.Level)
		}
		return map[string]bool{"ok": true}, nil
	case svc.AudioRoute:
		var a struct{ Route string }
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		m.SetRoute(a.Route)
		d.outs.SetCallSpeaker(a.Route == ui.RouteSpeaker)
		if d.router != nil {
			if err := d.router.SetRoute(a.Route); err != nil {
				log.Printf("phone-audiod: call routing: %v", err)
				return nil, err
			}
		}
		return map[string]string{"route": a.Route}, nil
	case svc.AudioChannels:
		var a struct {
			Mono    bool
			Balance float64
		}
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		m.SetChannels(a.Mono, a.Balance)
		mono, balance := m.Channels()
		return map[string]any{"mono": mono, "balance": balance}, nil
	case svc.SpeechSay:
		var a struct {
			Text, Lang string
			Interrupt  bool
		}
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if err := d.speech.Say(a.Text, a.Lang, a.Interrupt); err != nil {
			return nil, err
		}
		return d.speech.State(), nil
	case svc.SpeechStop:
		d.speech.Stop()
		return d.speech.State(), nil
	case svc.SpeechState:
		return d.speech.State(), nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

// touchMedia reports the music streams again: their output changed.
func touchMedia(m *audio.Mixer) {
	for _, s := range m.State() {
		if s.Category == audio.Media && s.Format != "" {
			_, _ = m.Touch(s.ID)
		}
	}
}
