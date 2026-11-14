package shell

import (
	"bytes"
	"errors"
	"image"
	"io"
	"os"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Video playback (video.open, video.control). The shell owns the videos:
// it opens the file and an audiod ring (category media, in the app's name,
// so audio focus treats it like the app's music) and hands both to mediad,
// which decodes and paces the picture by the sound. The app gets a stream
// name for a media node and controls; it never sees the file. A player
// plays only while its stream is on the app's top screen in the
// foreground, and closes with the last screen that names it.

const maxAppPlayers = 2

type videoPlayer struct {
	id      string
	app     *App
	media   string
	audioID string // the audiod stream, "" for a silent video
	w, h    int
	held    bool // paused by the user (the app), not by the system
	away    bool // paused because the stream is not on screen
	named   bool // a screen of the app has named its stream
	opened  time.Time
}

// playerGrace: how long a new player waits for the app to name its stream.
const playerGrace = 5 * time.Second

func (b *broker) rpcWith(service, method string, params any, files []*os.File, out any) error {
	cl, err := b.client(service)
	if err != nil {
		return err
	}
	return cl.CallWith(method, params, files, out)
}

func (c *Core) openVideo(a *App, args ui.VideoArgs, reply func(any, error)) {
	it := c.media.get(args.Media)
	if it == nil || !it.video() || !c.mediaAllowed(a, it) {
		reply(nil, errors.New("no such video"))
		return
	}
	n := 0
	for _, p := range c.players {
		if p.app == a {
			n++
		}
	}
	if n >= maxAppPlayers {
		reply(nil, errors.New("too many videos open"))
		return
	}
	f, err := os.Open(c.media.path(it))
	if err != nil {
		reply(nil, err)
		return
	}
	maxW, maxH := 320, 240
	if d := c.screenDev; d.Width > 0 && d.Height > 0 {
		maxW, maxH = d.Width, d.Height*3/5
	}
	b := c.broker
	go func() {
		defer f.Close()
		ring, stream := c.openVideoSound(a, it.Name)
		send := []*os.File{f}
		if ring != nil {
			send = append(send, ring)
			defer ring.Close()
		}
		var st ui.VideoState
		err := b.rpcWith(svc.Mediad, svc.MediaPlayerOpen, map[string]any{"app": a.M.ID, "title": it.Name, "max_w": maxW, "max_h": maxH,
			"at": float64(args.AtMS) / 1000}, send, &st)
		if stream != "" && (err != nil || !st.Sound) {
			_ = b.rpc(svc.Audiod, svc.AudioControl, map[string]string{"id": stream, "op": ui.OpStop}, nil)
			stream = ""
		}
		c.Post(func() {
			if err != nil {
				reply(nil, err)
				return
			}
			if !a.running {
				go func() { _ = b.rpc(svc.Mediad, svc.MediaPlayerClose, map[string]string{"player": st.Player}, nil) }()
				reply(nil, errors.New("app stopped"))
				return
			}
			c.players[st.Player] = &videoPlayer{id: st.Player, app: a, media: it.ID, audioID: stream, w: st.W, h: st.H, opened: time.Now()}
			c.log.Printf("%s: video %s as %s (%dx%d, sound %v)", a.M.ID, it.ID, st.Player, st.W, st.H, stream != "")
			reply(st, nil)
		})
	}()
}

// openVideoSound opens the ring a video's sound goes to (off the core loop).
func (c *Core) openVideoSound(a *App, name string) (*os.File, string) {
	var s audio.Stream
	files, err := c.broker.rpcFiles(svc.Audiod, svc.AudioOpen, map[string]string{"app": a.M.ID, "name": name, "category": "media", "dir": "play"}, &s)
	if err != nil || len(files) != 1 {
		closeFiles(files)
		return nil, ""
	}
	c.Post(func() { c.audioStream(s) })
	return files[0], s.ID
}

func (c *Core) controlVideo(a *App, args ui.VideoArgs, reply func(any, error)) {
	p := c.players[args.Player]
	if p == nil || p.app != a {
		reply(nil, errors.New("no such player"))
		return
	}
	switch args.Op {
	case ui.OpPause:
		p.held = true
	case ui.OpResume:
		p.held = false
		if p.away {
			// Off screen: it plays once the stream shows again.
			c.videoState(p, ui.OpState, reply)
			return
		}
	case ui.OpClose:
		c.closeVideo(p)
		reply(map[string]bool{"closed": true}, nil)
		return
	case ui.OpSeek:
		c.seekVideo(p, args.AtMS, reply)
		return
	case ui.OpState:
	default:
		reply(nil, errors.New("op is pause, resume, seek, state or close"))
		return
	}
	c.videoState(p, args.Op, reply)
}

// videoState sends op to mediad and the sound's stream to audiod.
func (c *Core) videoState(p *videoPlayer, op string, reply func(any, error)) {
	b := c.broker
	stream := p.audioID
	go func() {
		if stream != "" && (op == ui.OpPause || op == ui.OpResume) {
			_ = b.rpc(svc.Audiod, svc.AudioControl, map[string]string{"id": stream, "op": op}, nil)
		}
		var st ui.VideoState
		err := b.rpc(svc.Mediad, svc.MediaPlayerControl, map[string]any{"player": p.id, "op": op}, &st)
		c.Post(func() {
			if reply != nil {
				reply(st, err)
			}
		})
	}()
}

// seekVideo restarts decoding at a position: the sound already in the
// ring cannot be taken back, so it goes to a new ring.
func (c *Core) seekVideo(p *videoPlayer, atMS int64, reply func(any, error)) {
	b := c.broker
	old, name := p.audioID, c.media.get(p.media)
	title := ""
	if name != nil {
		title = name.Name
	}
	go func() {
		var send []*os.File
		stream := ""
		if old != "" {
			_ = b.rpc(svc.Audiod, svc.AudioControl, map[string]string{"id": old, "op": ui.OpStop}, nil)
			var ring *os.File
			if ring, stream = c.openVideoSound(p.app, title); ring != nil {
				send = append(send, ring)
				defer ring.Close()
			}
		}
		var st ui.VideoState
		err := b.rpcWith(svc.Mediad, svc.MediaPlayerControl, map[string]any{"player": p.id, "op": ui.OpSeek, "at": float64(atMS) / 1000}, send, &st)
		c.Post(func() {
			p.audioID = stream
			if p.held || p.away {
				c.videoState(p, ui.OpPause, nil)
				st.State = ui.PlayPaused
			}
			reply(st, err)
		})
	}()
}

func (c *Core) closeVideo(p *videoPlayer) {
	delete(c.players, p.id)
	b, stream := c.broker, p.audioID
	go func() {
		if stream != "" {
			_ = b.rpc(svc.Audiod, svc.AudioControl, map[string]string{"id": stream, "op": ui.OpStop}, nil)
		}
		_ = b.rpc(svc.Mediad, svc.MediaPlayerClose, map[string]string{"player": p.id}, nil)
	}()
}

// reconcileVideos (each render): pause players whose stream left the
// screen, resume those back on it, close those no screen of the app names.
func (c *Core) reconcileVideos() {
	for _, p := range c.players {
		named := appNames(p.app, p.id)
		p.named = p.named || named
		if !p.app.running || (!named && (p.named || time.Since(p.opened) > playerGrace)) {
			c.closeVideo(p)
			continue
		}
		top := p.app.top()
		shown := c.fg == p.app && top != nil && viewNames(top, p.id) && len(c.overlays) == 0 && !c.keylock
		switch {
		case !shown && !p.away:
			p.away = true
			if !p.held {
				c.videoState(p, ui.OpPause, nil)
			}
		case shown && p.away:
			p.away = false
			if !p.held {
				c.videoState(p, ui.OpResume, nil)
			}
		}
	}
}

func viewNames(v *View, stream string) bool {
	for _, n := range v.S.Body {
		if n.Kind == ui.KindMedia && (n.Stream == stream || n.Pip == stream) {
			return true
		}
	}
	return false
}

func appNames(a *App, stream string) bool {
	for _, v := range a.stack {
		if viewNames(v, stream) {
			return true
		}
	}
	return false
}

// streamShown: whether the app on screen may show a media stream there,
// its own calls' and players' only.
func (c *Core) streamShown(a *App, stream string) bool {
	if a == nil || a.system {
		return true
	}
	if p := c.players[stream]; p != nil {
		return p.app == a
	}
	for _, ci := range c.calls {
		if stream == ci.local || stream == ci.remote {
			return ci.app == a
		}
	}
	return false
}

// CanPlayVideo: mediad has read a video of the library (it has a decoder).
func (c *Core) CanPlayVideo() bool {
	for _, it := range c.media.items {
		if it.video() && it.W > 0 {
			return true
		}
	}
	return false
}

// probeVideos asks mediad for the size, length and a still of videos
// added without them (samples at first start).
func (c *Core) probeVideos() {
	for _, it := range c.media.items {
		if !it.video() || it.W > 0 {
			continue
		}
		f, err := os.Open(c.media.path(it))
		if err != nil {
			continue
		}
		go func(it *mediaItem, f *os.File) {
			defer f.Close()
			var info struct {
				W   int     `json:"w"`
				H   int     `json:"h"`
				Dur float64 `json:"duration"`
			}
			cl, err := c.broker.client(svc.Mediad)
			var files []*os.File
			if err == nil {
				files, err = cl.CallFilesWith(svc.MediaPlayerProbe, nil, []*os.File{f}, &info)
			}
			var still image.Image
			if len(files) == 1 {
				raw, _ := io.ReadAll(files[0])
				still, _, _ = image.Decode(bytes.NewReader(raw))
			}
			closeFiles(files)
			c.Post(func() {
				if err != nil {
					c.log.Printf("video %s: %v", it.ID, err)
					return
				}
				it.W, it.H, it.DurMS = info.W, info.H, int64(info.Dur*1000)
				if still != nil {
					if thumb, err := encodePNG(scaleCrop(still, thumbSide, thumbSide)); err == nil {
						_ = os.WriteFile(c.media.thumb(it.ID), thumb, 0o600)
					}
				}
				c.media.save()
			})
		}(it, f)
	}
}
