package mediad

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/media"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// Video playback. The shell hands over the file (mediad never opens the
// media library) and, when the video has sound, the audiod ring it opened
// for the app (category media). ffmpeg decodes, as a child of mediad:
// pictures as RGB at the stream rate into a short queue, sound as 48 kHz
// mono into the ring. The clock is the sound's, what audiod has played, so
// the picture follows the sound through pauses, gaps and audio focus; a
// silent video runs on the wall clock.

const (
	playerFPS   = 15 // the frame loop's rate (serveFrames)
	playerQueue = 8  // decoded pictures ahead of the clock
	maxPlayers  = 4
)

var errNoDecoder = errors.New("no video decoder on this device (ffmpeg)")

type vframe struct {
	pts float64
	pix []byte
}

type player struct {
	ID, App, Title string
	W, H           int
	Dur            float64
	file           *os.File

	mu       sync.Mutex
	cmd      *exec.Cmd
	frames   chan vframe
	cur      *vframe
	next     *vframe
	eof      bool // no more pictures in this run
	ring     *audio.RingBuf
	written  int64 // samples put in the ring this run
	soundEnd bool  // the sound of this run has all been written
	base     float64
	wall     time.Time // the wall clock's start, for silent runs
	paused   bool
	pausedAt time.Time
	held     time.Duration
	pos      float64
	state    string // playing | paused | ended | failed
	failure  string // why the decoder stopped before showing anything
}

type probeInfo struct {
	W     int     `json:"w"`
	H     int     `json:"h"`
	Dur   float64 `json:"duration"`
	Sound bool    `json:"sound"`
}

// probe reads a video's size, length and whether it has sound.
func probe(f *os.File) (probeInfo, error) {
	var out struct {
		Streams []struct {
			Type string `json:"codec_type"`
			W    int    `json:"width"`
			H    int    `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,width,height:format=duration", "-of", "json", "/dev/fd/3")
	cmd.ExtraFiles = []*os.File{f}
	raw, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return probeInfo{}, errNoDecoder
	}
	if err != nil || json.Unmarshal(raw, &out) != nil {
		return probeInfo{}, fmt.Errorf("not a video this device plays: %v", err)
	}
	var p probeInfo
	for _, s := range out.Streams {
		switch {
		case s.Type == "video" && p.W == 0:
			p.W, p.H = s.W, s.H
		case s.Type == "audio":
			p.Sound = true
		}
	}
	p.Dur, _ = strconv.ParseFloat(out.Format.Duration, 64)
	if p.W <= 0 || p.H <= 0 {
		return probeInfo{}, errors.New("no picture in the file")
	}
	return p, nil
}

// poster is a still from a third into the video (at most 1 s), as PNG in
// a memfd.
func poster(f *os.File, p probeInfo) (*os.File, error) {
	at := min(1.0, p.Dur/3)
	cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-ss", strconv.FormatFloat(at, 'f', 2, 64), "-i", "/dev/fd/3",
		"-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "pipe:1")
	cmd.ExtraFiles = []*os.File{f}
	raw, err := cmd.Output()
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("no still from the video: %v", err)
	}
	fd, err := unix.MemfdCreate("poster", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	out := os.NewFile(uintptr(fd), "poster")
	if _, err := out.Write(raw); err != nil {
		out.Close()
		return nil, err
	}
	_, _ = out.Seek(0, 0)
	return out, nil
}

// fit is the decoded size: the video's within maxW × maxH, even sides.
func fit(w, h, maxW, maxH int) (int, int) {
	s := min(1, float64(maxW)/float64(w), float64(maxH)/float64(h))
	return max(2, int(float64(w)*s)/2*2), max(2, int(float64(h)*s)/2*2)
}

// run starts decoding at the player's base position.
func (p *player) run() error {
	args := []string{"-nostdin", "-v", "error", "-ss", strconv.FormatFloat(p.base, 'f', 3, 64), "-i", "/dev/fd/3",
		"-map", "0:v:0", "-vf", fmt.Sprintf("scale=%d:%d", p.W, p.H), "-r", strconv.Itoa(playerFPS),
		"-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1"}
	var sound *os.File
	files := []*os.File{p.file}
	if p.ring != nil {
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		sound = r
		files = append(files, w)
		args = append(args, "-map", "0:a:0", "-ac", "1", "-ar", strconv.Itoa(audio.Rate), "-f", "s16le", "pipe:4")
		defer w.Close()
	}
	cmd := exec.Command("ffmpeg", args...)
	cmd.ExtraFiles = files
	errs := &tail{prefix: "player " + p.Title + ": ffmpeg: "}
	cmd.Stderr = errs
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if sound != nil {
			sound.Close()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return errNoDecoder
		}
		return err
	}
	frames := make(chan vframe, playerQueue)
	p.cmd, p.frames, p.cur, p.next, p.eof, p.failure = cmd, frames, nil, nil, false, ""
	p.written, p.soundEnd, p.wall, p.held = 0, p.ring == nil, time.Now(), 0
	if p.paused {
		p.pausedAt = p.wall
	}
	go func(base float64) {
		size := p.W * p.H * 3
		r := bufio.NewReaderSize(out, size)
		n := 0
		for ; ; n++ {
			pix := make([]byte, size)
			if _, err := io.ReadFull(r, pix); err != nil {
				break
			}
			frames <- vframe{pts: base + float64(n)/playerFPS, pix: pix}
		}
		err := cmd.Wait()
		if err != nil && n == 0 {
			p.mu.Lock()
			if p.cmd == cmd {
				p.failure = strings.TrimSpace(errs.last + " (" + err.Error() + ")")
			}
			p.mu.Unlock()
		}
		close(frames)
	}(p.base)
	if sound != nil {
		go p.pump(sound, p.ring)
	}
	return nil
}

// pump moves the decoded sound into the ring as fast as audiod takes it.
func (p *player) pump(r *os.File, ring *audio.RingBuf) {
	defer r.Close()
	br := bufio.NewReaderSize(r, 8192)
	buf := make([]byte, 4096)
	pcm := make([]int16, len(buf)/2)
	for {
		n, err := io.ReadFull(br, buf)
		n -= n % 2
		for i := 0; i < n/2; i++ {
			pcm[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
		}
		for rest := pcm[:n/2]; len(rest) > 0; {
			p.mu.Lock()
			if p.ring != ring {
				p.mu.Unlock()
				return
			}
			k := ring.Write(rest)
			p.written += int64(k)
			p.mu.Unlock()
			rest = rest[k:]
			if len(rest) > 0 {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if err != nil {
			p.mu.Lock()
			if p.ring == ring {
				ring.Finish()
				p.soundEnd = true
			}
			p.mu.Unlock()
			return
		}
	}
}

// clock is the position in seconds (mu held): what audiod has played of
// this run, or the wall clock without sound or once it has all played.
func (p *player) clock() float64 {
	if p.ring != nil {
		played := p.written - int64(p.ring.Buffered())
		if !p.soundEnd || p.ring.Buffered() > 0 {
			return p.base + float64(played)/audio.Rate
		}
		// The sound is over (a shorter track): the picture goes on alone.
		p.base += float64(played) / audio.Rate
		p.ring.Close()
		p.ring, p.wall, p.held = nil, time.Now(), 0
		if p.paused {
			p.pausedAt = p.wall
		}
	}
	at := time.Now()
	if p.paused {
		at = p.pausedAt
	}
	return p.base + (at.Sub(p.wall) - p.held).Seconds()
}

// source draws the picture for the clock into f, letterboxed.
func (p *player) source(_ float64, f *media.Frame) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	for !p.eof {
		if p.next == nil {
			select {
			case fr, ok := <-p.frames:
				if !ok {
					p.eof = true
					continue
				}
				p.next = &fr
			default:
			}
		}
		if p.next == nil || p.next.pts > now {
			break
		}
		p.cur, p.next = p.next, nil
	}
	p.pos = min(now, p.Dur)
	switch {
	case p.eof && p.failure != "" && p.state != ui.PlayFailed:
		p.state = ui.PlayFailed
		log.Printf("player %s: %s", p.ID, p.failure)
	case p.eof && p.next == nil && (p.ring == nil || p.ring.Buffered() == 0) && p.state == ui.PlayPlaying:
		p.state, p.pos = ui.PlayEnded, p.Dur
	}
	for i := range f.Pix {
		f.Pix[i] = 0
	}
	if p.cur != nil {
		blitFit(f, p.cur.pix, p.W, p.H)
	}
}

// blitFit scales a w × h RGB picture into f keeping its shape (nearest).
func blitFit(f *media.Frame, pix []byte, w, h int) {
	s := math.Min(float64(f.W)/float64(w), float64(f.H)/float64(h))
	dw, dh := min(f.W, max(1, int(float64(w)*s))), min(f.H, max(1, int(float64(h)*s)))
	ox, oy := (f.W-dw)/2, (f.H-dh)/2
	for y := 0; y < dh; y++ {
		sy := y * h / dh
		row := f.Pix[3*((oy+y)*f.W+ox):]
		for x := 0; x < dw; x++ {
			sx := x * w / dw
			copy(row[3*x:3*x+3], pix[3*(sy*w+sx):])
		}
	}
}

// tail logs a child's error output line by line and keeps the last line.
type tail struct {
	prefix string
	buf    []byte
	last   string
}

func (t *tail) Write(b []byte) (int, error) {
	t.buf = append(t.buf, b...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(t.buf[:i])); line != "" {
			t.last = line
			log.Print(t.prefix + line)
		}
		t.buf = t.buf[i+1:]
	}
	if len(t.buf) > 4096 {
		t.buf = t.buf[:0]
	}
	return len(b), nil
}

func (p *player) stopRun() {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	if p.frames != nil {
		go func(ch chan vframe) {
			for range ch {
			}
		}(p.frames)
	}
	if p.ring != nil {
		p.ring.Close()
		p.ring = nil
	}
}

type playerState struct {
	ID     string `json:"player"`
	Stream string `json:"stream"`
	State  string `json:"state"`
	PosMS  int64  `json:"pos_ms"`
	DurMS  int64  `json:"duration_ms"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	Sound  bool   `json:"sound"`
	Error  string `json:"error,omitempty"`
}

func (p *player) stateLocked() playerState {
	if p.state != ui.PlayEnded && p.state != ui.PlayFailed {
		p.pos = min(p.clock(), p.Dur)
	}
	st := playerState{ID: p.ID, Stream: p.ID, State: p.state, PosMS: int64(p.pos * 1000), DurMS: int64(p.Dur * 1000), W: p.W, H: p.H}
	if p.state == ui.PlayFailed {
		st.Error = p.failure
	}
	return st
}

type playerArgs struct {
	App    string  `json:"app"`
	Title  string  `json:"title"`
	Player string  `json:"player"`
	Op     string  `json:"op"`
	At     float64 `json:"at"`
	MaxW   int     `json:"max_w"`
	MaxH   int     `json:"max_h"`
}

// handlePlayer serves player.*; files: the video, then the ring if any.
func (s *server) handlePlayer(method string, params json.RawMessage, files []*os.File) (any, error) {
	var a playerArgs
	_ = json.Unmarshal(params, &a)
	take := func(i int) *os.File {
		if i < len(files) {
			f := files[i]
			files[i] = nil
			return f
		}
		return nil
	}
	defer func() {
		for _, f := range files {
			if f != nil {
				f.Close()
			}
		}
	}()
	switch method {
	case svc.MediaPlayerProbe:
		f := take(0)
		if f == nil {
			return nil, errors.New("no video")
		}
		defer f.Close()
		info, err := probe(f)
		if err != nil {
			return nil, err
		}
		still, err := poster(f, info)
		if err != nil {
			return info, nil
		}
		return wire.Files{Result: info, Files: []*os.File{still}}, nil
	case svc.MediaPlayerOpen:
		f := take(0)
		if f == nil {
			return nil, errors.New("no video")
		}
		info, err := probe(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		s.mu.Lock()
		n := len(s.players)
		s.mu.Unlock()
		if n >= maxPlayers {
			f.Close()
			return nil, fmt.Errorf("%d videos open", maxPlayers)
		}
		w, h := fit(info.W, info.H, max(16, a.MaxW), max(16, a.MaxH))
		p := &player{App: a.App, Title: a.Title, W: w, H: h, Dur: info.Dur, file: f, base: max(0, min(a.At, info.Dur)), state: ui.PlayPlaying}
		if rf := take(1); rf != nil && info.Sound {
			if ring, err := audio.MapRing(rf); err == nil {
				p.ring = ring
			}
			rf.Close()
		}
		p.mu.Lock()
		err = p.run()
		p.mu.Unlock()
		if err != nil {
			if p.ring != nil {
				p.ring.Close()
			}
			f.Close()
			return nil, err
		}
		s.mu.Lock()
		s.seq++
		p.ID = fmt.Sprintf("play%d", s.seq)
		s.players[p.ID] = p
		s.streams[p.ID] = p.source
		s.mu.Unlock()
		log.Printf("player %s: %s for %s, %dx%d, %.1f s, sound %v", p.ID, a.Title, a.App, w, h, info.Dur, p.ring != nil)
		p.mu.Lock()
		defer p.mu.Unlock()
		st := p.stateLocked()
		st.Sound = p.ring != nil
		return st, nil
	case svc.MediaPlayerControl:
		s.mu.Lock()
		p := s.players[a.Player]
		s.mu.Unlock()
		if p == nil {
			return nil, errors.New("no such player")
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		switch a.Op {
		case ui.OpPause:
			if !p.paused {
				p.clock()
				p.paused, p.pausedAt = true, time.Now()
			}
			if p.state == ui.PlayPlaying {
				p.state = ui.PlayPaused
			}
		case ui.OpResume:
			if p.paused {
				p.held += time.Since(p.pausedAt)
				p.paused = false
			}
			if p.state == ui.PlayPaused {
				p.state = ui.PlayPlaying
			}
		case ui.OpSeek:
			// The old ring cannot be emptied from this side: the shell
			// passes a new one (or none).
			p.stopRun()
			p.base = max(0, min(a.At, p.Dur))
			if rf := take(0); rf != nil {
				if ring, err := audio.MapRing(rf); err == nil {
					p.ring = ring
				}
				rf.Close()
			}
			if p.state == ui.PlayEnded {
				p.state = ui.PlayPlaying
				p.paused = false
			}
			if err := p.run(); err != nil {
				return nil, err
			}
		case ui.OpState:
		default:
			return nil, fmt.Errorf("unknown op %q", a.Op)
		}
		return p.stateLocked(), nil
	case svc.MediaPlayerClose:
		s.mu.Lock()
		p := s.players[a.Player]
		delete(s.players, a.Player)
		delete(s.streams, a.Player)
		s.mu.Unlock()
		if p == nil {
			return nil, errors.New("no such player")
		}
		p.mu.Lock()
		p.stopRun()
		p.file.Close()
		p.mu.Unlock()
		return map[string]bool{"closed": true}, nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}
