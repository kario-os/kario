package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// Sound goes through phone-audiod. The shell plays the system sounds itself
// (ringtone, ringback, call audio, alarm, notification) and relays app
// playback; audiod's focus policy decides what may sound together. Without
// audiod everything works silently.

type audioEvent struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Event  string          `json:"event"`
	Stream *audio.Stream   `json:"stream"`
}

func (c *Core) audioSock() string { return filepath.Join(c.cfg.RunDir, svc.AudiodSock) }

func (c *Core) startAudio() {
	go func() {
		for !c.stopping.Load() {
			nc, err := net.Dial("unix", c.audioSock())
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			conn := wire.New(nc)
			_ = conn.Send(wire.RPC{ID: 1, Method: wire.Subscribe})
			c.Post(func() { c.audioUp = true; c.applyVolumes() })
			for {
				var ev audioEvent
				if err := conn.Recv(&ev); err != nil {
					break
				}
				if ev.Event == svc.AudioEventStream && ev.Stream != nil {
					s := *ev.Stream
					c.Post(func() { c.audioStream(s) })
				}
			}
			nc.Close()
			c.Post(func() { c.audioUp = false })
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

func (c *Core) audioStream(s audio.Stream) {
	if c.streams == nil {
		c.streams = map[string]audio.Stream{}
	}
	if s.State == ui.PlayEnded {
		delete(c.streams, s.ID)
	} else {
		c.streams[s.ID] = s
	}
	if a := c.appByID(s.App); a != nil && a.running {
		raw, _ := json.Marshal(s)
		a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventAudio, Value: string(raw)})
	}
}

func (c *Core) audioRPC(method string, params, out any) error {
	if _, err := os.Stat(c.audioSock()); err != nil {
		return errors.New("no audio service")
	}
	return c.broker.rpc(svc.Audiod, method, params, out)
}

// sound starts a system sound under key unless it is already playing; the
// key's stream is stopped by quiet. Both may be called in any order.
func (c *Core) sound(key, tone, category string) {
	if c.sysWant == nil {
		c.sysWant, c.sysStream = map[string]bool{}, map[string]string{}
	}
	if c.sysWant[key] {
		return
	}
	c.sysWant[key] = true
	if c.silentSound(category) || c.silenced[key] {
		// Silent mode: the ringtone pulses the motor instead (vibrateRinging),
		// a notification vibrates once.
		if category == audio.Notify {
			c.vibrateMS(300)
		}
		return
	}
	go func() {
		var s audio.Stream
		err := c.audioRPC("play", map[string]string{"app": "system", "name": tone, "category": category, "source": "tone:" + tone}, &s)
		c.Post(func() {
			if err != nil {
				delete(c.sysWant, key)
				return
			}
			if !c.sysWant[key] {
				go func() { _ = c.audioRPC("control", map[string]string{"id": s.ID, "op": "stop"}, nil) }()
				return
			}
			c.sysStream[key] = s.ID
		})
	}()
}

func (c *Core) quiet(key string) {
	delete(c.silenced, key)
	if !c.sysWant[key] {
		return
	}
	delete(c.sysWant, key)
	if id := c.sysStream[key]; id != "" {
		delete(c.sysStream, key)
		go func() { _ = c.audioRPC("control", map[string]string{"id": id, "op": "stop"}, nil) }()
	}
}

const maxAppStreams = 4

// recording reports whether some app is recording (the status bar shows it).
func (c *Core) recording() bool {
	for _, s := range c.streams {
		if s.Category == "record" && s.State == ui.PlayPlaying {
			return true
		}
	}
	return false
}

// stopRecording ends an app's recordings when it leaves the foreground:
// apps cannot listen in the background (as on Android and iOS), except
// during a call.
func (c *Core) stopRecording(a *App) {
	if c.inCall(a) {
		return
	}
	for id, s := range c.streams {
		if s.App == a.M.ID && s.Category == "record" {
			c.log.Printf("%s left the foreground: recording %s stopped", a.M.ID, id)
			go func() { _ = c.audioRPC("control", map[string]string{"id": id, "op": "stop"}, nil) }()
		}
	}
}

// ping plays the notification sound, except during a call.
func (c *Core) ping() {
	if c.activeCell() != nil || len(c.calls) > 0 {
		return
	}
	c.notifySeq++
	c.sound(fmt.Sprintf("notify:%d", c.notifySeq), "notify", audio.Notify)
}

// cellSound follows a cellular call through its states.
func (c *Core) cellSound(call ui.TelCall) {
	id := call.ID
	if call.State != "incoming" {
		c.quiet("ring:" + id)
	}
	if call.State != "alerting" && call.State != "dialing" {
		c.quiet("ringback:" + id)
	}
	if call.State != "active" {
		c.quiet("voice:" + id)
	}
	switch call.State {
	case "incoming":
		c.sound("ring:"+id, "ringtone", audio.Ring)
	case "alerting":
		c.sound("ringback:"+id, "ringback", audio.Call)
	case "active":
		c.sound("voice:"+id, "voice", audio.Call)
	case ui.PlayEnded:
		if call.Reason == "busy" {
			c.sound("busy:"+id, "busy", audio.Call)
			// busy ends by itself; forget the key so a later call can reuse it
			go func() { time.Sleep(3 * time.Second); c.Post(func() { c.quiet("busy:" + id) }) }()
		}
	}
}

// appAudioPlaying reports whether the app has media playing (or paused only
// because something more important is sounding).
func (c *Core) appAudioPlaying(app string) bool {
	for _, s := range c.streams {
		if s.App == app && s.Category == audio.Media && (s.State == ui.PlayPlaying || s.ByFocus) {
			return true
		}
	}
	return false
}

// AudioState is what audiod reports (selftest).
func (c *Core) AudioState() ([]audio.Stream, error) {
	var st struct {
		Streams []audio.Stream `json:"streams"`
	}
	err := c.audioRPC("state", nil, &st)
	return st.Streams, err
}

func (c *Core) HasAudio() bool { return c.audioUp }

// musicDir holds the user's music, in folders (shell/music.go); apps see
// tracks as "music:<path in it>[#track]" and never get a path.
func (c *Core) musicDir() string { return filepath.Join(c.cfg.StateDir, "media", "music") }

// Media library: the music library's tracks, then the demo tracks audiod
// synthesises.
func (c *Core) mediaTracks() []musicTrack {
	out := c.music().scan()
	for _, t := range audio.DemoTracks {
		out = append(out, musicTrack{MediaItem: ui.MediaItem{ID: t.ID, Kind: ui.MediaAudio, Title: t.Title, Artist: t.Artist, LenMS: audio.TrackLenMS(t)}})
	}
	return out
}

func (b *broker) dispatchAudio(a *App, m ui.Msg, reply func(any, error)) {
	c := b.c
	switch m.Cap {
	case ui.CapMediaList:
		var args ui.MediaArgs
		_ = json.Unmarshal(m.Args, &args)
		if args.Kind != ui.MediaImage && args.Kind != ui.MediaVideo {
			// A first listing reads every file's headers: off the loop.
			go func() {
				out := c.mediaTracks()
				c.Post(func() { reply(out, nil) })
			}()
			return
		}
		out := []ui.MediaItem{}
		for _, it := range c.media.newestFirst() {
			if u := it.ui(); u.Kind == args.Kind {
				out = append(out, u)
			}
		}
		reply(out, nil)
	case ui.CapCameraCapture:
		c.capturePhoto(a, m.Origin, reply)
	case ui.CapMediaPick:
		c.pickPhoto(a, m.Origin, reply)
	case ui.CapMediaSave:
		var args ui.MediaSaveArgs
		_ = json.Unmarshal(m.Args, &args)
		c.savePhoto(a, m.Origin, args, reply)
	case ui.CapFileSave, ui.CapFilePick, ui.CapFileOpen:
		var args ui.FileArgs
		_ = json.Unmarshal(m.Args, &args)
		switch m.Cap {
		case ui.CapFileSave:
			c.saveFile(a, m.Origin, args, reply)
		case ui.CapFilePick:
			c.pickFile(a, m.Origin, args, reply)
		default:
			c.openFile(a, m.Origin, args.ID, reply)
		}
	case ui.CapMediaDelete:
		var args ui.MediaArgs
		_ = json.Unmarshal(m.Args, &args)
		c.deletePhoto(a, args.ID, reply)
	case ui.CapAudioPlay:
		var args ui.AudioPlayArgs
		_ = json.Unmarshal(m.Args, &args)
		app := a.M.ID
		go func() {
			var req any
			var err error
			if t, ok := audio.TrackByID(args.Media); ok {
				req = map[string]string{"app": app, "name": t.Title, "category": audio.Media, "source": t.ID}
			} else {
				req, _, err = c.musicPlay(app, args)
			}
			var s audio.Stream
			if err == nil {
				err = b.rpc(svc.Audiod, svc.AudioPlay, req, &s)
			}
			c.Post(func() {
				if err == nil {
					c.audioStream(s)
				}
				reply(s, err)
			})
		}()
	case ui.CapAudioControl:
		var args ui.AudioControlArgs
		_ = json.Unmarshal(m.Args, &args)
		if s, ok := c.streams[args.ID]; !ok || s.App != a.M.ID {
			reply(nil, errors.New("no such stream for this app"))
			return
		}
		go func() {
			req, err := c.musicControl(args)
			var s audio.Stream
			if err == nil {
				err = b.rpc(svc.Audiod, svc.AudioControl, req, &s)
			}
			c.Post(func() {
				if err == nil {
					c.audioStream(s)
				}
				reply(s, err)
			})
		}()
	case ui.CapAudioState:
		out := []audio.Stream{}
		var all struct {
			Streams []audio.Stream `json:"streams"`
		}
		b.call(svc.Audiod, svc.AudioState, nil, &all, func(err error) {
			for _, s := range all.Streams {
				if s.App == a.M.ID {
					out = append(out, s)
				}
			}
			reply(out, err)
		})
	case ui.CapAudioStream, ui.CapAudioRecord:
		// The shell decides what the stream is (call audio during a call,
		// media otherwise) and hands the ring to the app; samples then
		// flow between app and audiod directly.
		var args ui.AudioOpenArgs
		_ = json.Unmarshal(m.Args, &args)
		dir, cat := "play", audio.Media
		if c.inCall(a) {
			cat = audio.Call
		}
		if m.Cap == ui.CapAudioRecord {
			dir = "record"
			if c.fg != a && !c.inCall(a) {
				reply(nil, errors.New("only the foreground app or one in a call may record"))
				return
			}
		}
		// Each stream costs audiod a shared ring: bound what one app holds.
		streams, recs := 0, 0
		for _, s := range c.streams {
			if s.App == a.M.ID {
				streams++
				if s.Category == "record" {
					recs++
				}
			}
		}
		if streams >= maxAppStreams || (dir == "record" && recs > 0) {
			reply(nil, fmt.Errorf("too many audio streams (at most %d, one recording)", maxAppStreams))
			return
		}
		go func() {
			var s audio.Stream
			files, err := b.rpcFiles(svc.Audiod, svc.AudioOpen, map[string]string{"app": a.M.ID, "name": args.Name, "category": cat, "dir": dir}, &s)
			c.Post(func() {
				if err == nil {
					c.audioStream(s)
				}
				reply(withFiles{data: s, files: files}, err)
			})
		}()
	case ui.CapAudioRoute:
		var args ui.AudioRouteArgs
		_ = json.Unmarshal(m.Args, &args)
		b.call(svc.Audiod, svc.AudioRoute, args, nil, func(err error) { reply(map[string]string{"route": args.Route}, err) })
	default:
		b.dispatchPhone(a, m, reply)
	}
}
