// phone-audiod mixes every sound on the phone: system tones from the shell
// (ringtone, ringback, alarm, notification, call audio) and media from apps,
// under one focus policy (see package audio). Music that plays alone goes
// to the sound card at its own format instead (audio/hifi): bit-perfect,
// DoP or native DSD where the card takes it.
//
//	phone-audiod --run-dir DIR [--sink auto|null|alsa[:device]|file:PATH]
//
// The shell is its only client (socket mode 0600); apps reach it through the
// broker. Production replaces the output with PipeWire and keeps the policy.
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
	"sync"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/hifi"
	"github.com/kario-os/kario/audio/opusfile"
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
	flag.Parse()
	out, err := openSink(*sinkSpec)
	if err != nil {
		log.Fatal(err)
	}
	m := audio.NewMixer()
	var hub *wire.Hub
	// Recordings: the microphone feeds each one's ring, nothing is mixed.
	type capture struct {
		s    audio.Stream
		ring *audio.RingBuf
	}
	var capMu sync.Mutex
	captures := map[string]*capture{}
	capSeq := 0
	stopCapture := func(id string) (audio.Stream, bool) {
		capMu.Lock()
		c := captures[id]
		delete(captures, id)
		capMu.Unlock()
		if c == nil {
			return audio.Stream{}, false
		}
		c.ring.Finish()
		c.ring.Close()
		c.s.State = ui.PlayEnded
		hub.Broadcast(map[string]any{"event": svc.AudioEventStream, "stream": c.s})
		return c.s, true
	}
	m.OnChange = func(s audio.Stream) {
		if hub != nil {
			hub.Broadcast(map[string]any{"event": svc.AudioEventStream, "stream": s})
		}
	}
	music := func(id string) (*hifi.Stream, error) {
		if s, ok := m.Source(id).(*hifi.Stream); ok {
			return s, nil
		}
		return nil, fmt.Errorf("stream %s is not music", id)
	}
	hub, err = wire.ServeHub(filepath.Join(*runDir, svc.AudiodSock), func(method string, params json.RawMessage) (any, error) {
		switch method {
		case svc.AudioPlay:
			var a playArgs
			if err := json.Unmarshal(params, &a); err != nil {
				return nil, err
			}
			var src audio.Source
			if a.Track != nil {
				s, err := hifi.Open(*a.Track, hifi.Options{DSD: dsdMode(a.DSD)}, a.StartMS)
				if err != nil {
					return nil, err
				}
				if a.Next != nil {
					s.Queue(a.Next)
				}
				src = s
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
			capMu.Lock()
			open := len(captures)
			capMu.Unlock()
			if open+len(m.State()) >= maxStreams {
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
				capMu.Lock()
				capSeq++
				c := &capture{s: audio.Stream{ID: fmt.Sprintf("r%d", capSeq), App: a.App, Name: a.Name, Category: "record", State: ui.PlayPlaying}, ring: ring}
				captures[c.s.ID] = c
				capMu.Unlock()
				hub.Broadcast(map[string]any{"event": svc.AudioEventStream, "stream": c.s})
				return wire.Files{Result: c.s, Files: []*os.File{f}}, nil
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
				if s, ok := stopCapture(a.ID); ok {
					return s, nil
				}
			case ui.OpSeek, ui.OpQueue, ui.OpOptions:
				s, err := music(a.ID)
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
			streams := m.State()
			capMu.Lock()
			for _, c := range captures {
				streams = append(streams, c.s)
			}
			capMu.Unlock()
			return map[string]any{"streams": streams, "route": m.Route(), "sink": out.Name(), "frames": m.Frames}, nil
		case svc.AudioVolume:
			var a struct {
				Category string
				Level    float64
			}
			if err := json.Unmarshal(params, &a); err != nil {
				return nil, err
			}
			m.SetVolume(a.Category, a.Level)
			return map[string]bool{"ok": true}, nil
		case svc.AudioRoute:
			var a struct{ Route string }
			if err := json.Unmarshal(params, &a); err != nil {
				return nil, err
			}
			m.SetRoute(a.Route)
			return map[string]string{"route": a.Route}, nil
		}
		return nil, fmt.Errorf("unknown method %q", method)
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("phone-audiod: sink %s on %s/audiod.sock", out.Name(), *runDir)
	buf := make([]int16, audio.Channels*audio.Frame)
	raw := make([]byte, 0, 1<<16)
	mic, micBuf := &audio.Mic{}, make([]int16, audio.Frame)
	tick := time.NewTicker(time.Second / 50)
	for range tick.C {
		capMu.Lock()
		if len(captures) > 0 {
			mic.Next(micBuf)
			for _, c := range captures {
				c.ring.Write(micBuf)
				c.s.PosMS += 1000 / 50
			}
		}
		capMu.Unlock()
		if solo(m, out, &raw) {
			continue
		}
		if m.SetOutput(audio.MixedOutput) {
			touchMedia(m)
		}
		m.Mix(buf)
		if err := out.Write(buf); err != nil {
			log.Printf("sink %s: %v", out.Name(), err)
			time.Sleep(time.Second)
		}
	}
}

// solo plays 20 ms of the music that plays alone straight to the card,
// at the set-up its format gets there; false when the mixer has to play.
func solo(m *audio.Mixer, out sink, raw *[]byte) bool {
	d, ok := out.(direct)
	if !ok {
		return false
	}
	id, src, gain, ok := m.Solo()
	if !ok {
		return false
	}
	s, ok := src.(*hifi.Stream)
	if !ok {
		return false
	}
	for try := 0; try < 2; try++ {
		f := s.Native()
		plan := hifi.Negotiate(f, d.Caps(), gain*s.Gain(), s.Options().DSD)
		if plan.Mode == hifi.ModeMixer {
			return false
		}
		frames := plan.Rate / 50
		if need := frames * plan.BytesPerFrame(); cap(*raw) < need {
			*raw = make([]byte, need)
		}
		b := (*raw)[:frames*plan.BytesPerFrame()]
		n, more, err := s.Out(plan, gain, frames, b)
		if errors.Is(err, hifi.ErrFormatChanged) {
			continue
		}
		if err := d.WriteRaw(plan, b[:n]); err != nil {
			log.Printf("phone-audiod: %s not taken (%v): music goes through the mixer", plan, err)
			return false
		}
		changed := m.SetOutput(plan.String())
		if err := s.Err(); !more && err != nil {
			log.Printf("phone-audiod: music stopped: %v", err)
		}
		m.Played(id, more)
		if changed {
			log.Printf("phone-audiod: music %s → %s", f, plan)
			touchMedia(m)
		}
		return true
	}
	return false
}

// touchMedia reports the music streams again: their output changed.
func touchMedia(m *audio.Mixer) {
	for _, s := range m.State() {
		if s.Category == audio.Media && s.Format != "" {
			_, _ = m.Touch(s.ID)
		}
	}
}
