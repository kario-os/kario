package audiod

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/hifi"
)

// sink is where the mixer's output goes: 48 kHz interleaved stereo.
type sink interface {
	Write(pcm []int16) error
	Name() string
}

// direct sinks also take a track at its own format (bit-perfect, DoP,
// native DSD), set up as a plan says. Open sets the card up for the plan
// before the track's samples are taken for it.
type direct interface {
	Caps() hifi.Caps
	Open(p hifi.Plan) error
	WriteRaw(p hifi.Plan, b []byte) error
}

func le16(pcm []int16) []byte {
	buf := make([]byte, 2*len(pcm))
	for i, v := range pcm {
		buf[2*i], buf[2*i+1] = byte(v), byte(uint16(v)>>8)
	}
	return buf
}

// nullSink takes everything (the developer machine): plans come out as
// they would on a card that can do anything.
type nullSink struct{ plan hifi.Plan }

func (*nullSink) Write([]int16) error                    { return nil }
func (*nullSink) Name() string                           { return "null" }
func (*nullSink) Caps() hifi.Caps                        { return hifi.Caps{Any: true} }
func (*nullSink) Open(hifi.Plan) error                   { return nil }
func (n *nullSink) WriteRaw(p hifi.Plan, _ []byte) error { n.plan = p; return nil }

// fileSink writes a WAV file, keeping the header's sizes current. It
// only takes the mixer's output (one format a file).
type fileSink struct {
	f     *os.File
	bytes uint32
	path  string
	last  time.Time
}

func newFileSink(path string) (*fileSink, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(audio.WavHeaderFor(0, audio.Channels))
	return &fileSink{f: f, path: path}, err
}

func (s *fileSink) Write(pcm []int16) error {
	buf := le16(pcm)
	if _, err := s.f.Write(buf); err != nil {
		return err
	}
	s.bytes += uint32(len(buf))
	if time.Since(s.last) > time.Second {
		s.last = time.Now()
		_, _ = s.f.WriteAt(audio.WavHeaderFor(s.bytes, audio.Channels), 0)
	}
	return nil
}

func (s *fileSink) Name() string { return "file:" + s.path }

// alsaSink pipes to aplay, one aplay at a time: the mixer's set-up or a
// track's own. Changing the set-up stops the running aplay (what is still
// in its buffer, about 0.2 s, is lost) and starts another. In auto mode
// a USB sound card (a DAC) is used in place of the built-in one when one
// is plugged in. A set-up aplay could not play is not tried again for
// retryAfter: until then writing it fails at once, with aplay's reason.
type alsaSink struct {
	auto   bool
	device string // plughw:C,D
	base   string // the device it was opened on
	cmd    *exec.Cmd
	w      *os.File
	said   *tail
	conf   string
	caps   *hifi.Caps
	capsOf string
	failed map[string]failure
	now    func() time.Time
}

type failure struct {
	at  time.Time
	err error
}

const (
	// bufferUS is aplay's buffer: short, so a set-up change loses little.
	bufferUS = "200000"
	// retryAfter: how long a set-up that failed is left alone.
	retryAfter = 10 * time.Second
	// stallAfter: a write the card has not taken by then (aplay waiting
	// for a card another program holds, a DSP that stopped) fails. Well
	// above what a working card makes a write wait (aplay's period) and
	// a new aplay's start on the emulator.
	stallAfter = 2 * time.Second
)

func (s *alsaSink) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// tail keeps the end of what aplay prints: why it stopped.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 1024 {
		t.b = t.b[len(t.b)-1024:]
	}
	return len(p), nil
}

// aplayWhere is how aplay starts an error: its name and source position.
var aplayWhere = regexp.MustCompile(`^aplay: \w+:\d+: `)

func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	return aplayWhere.ReplaceAllString(strings.TrimSpace(lines[len(lines)-1]), "")
}

func (s *alsaSink) Name() string { return "alsa:" + s.device }

// pick chooses the card in auto mode: the last USB audio device with a
// playback interface, else card 0.
func (s *alsaSink) pick() {
	if !s.auto {
		return
	}
	dev := "plughw:0,0"
	streams, _ := filepath.Glob("/proc/asound/card*/stream0")
	sort.Strings(streams)
	for _, p := range streams {
		raw, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(raw), "Playback:") {
			continue
		}
		card := strings.TrimPrefix(filepath.Base(filepath.Dir(p)), "card")
		if _, err := strconv.Atoi(card); err == nil {
			dev = "plughw:" + card + ",0"
		}
	}
	if dev != s.device {
		log.Printf("phone-audiod: output %s", dev)
		s.device, s.caps = dev, nil
	}
}

// route moves the output to dev ("" the device the sink was opened on):
// a UCM profile's call set-ups play to other PCMs than media. Auto mode
// picks its card itself.
func (s *alsaSink) route(dev string) {
	if s.auto {
		return
	}
	dev = cmp.Or(dev, s.base)
	if dev == s.device {
		return
	}
	log.Printf("phone-audiod: output %s", dev)
	s.stop()
	s.device, s.caps = dev, nil
}

// hw is the device without ALSA's plug conversions.
func (s *alsaSink) hw() string { return strings.TrimPrefix(s.device, "plug") }

func (s *alsaSink) card() string {
	c, _, _ := strings.Cut(strings.TrimPrefix(s.hw(), "hw:"), ",")
	return c
}

// Caps probes the card while no aplay holds it: aplay's dump of the
// hardware parameters, refined by a USB device's exact rates.
func (s *alsaSink) Caps() hifi.Caps {
	if s.caps != nil && s.capsOf == s.device {
		return *s.caps
	}
	if s.w != nil {
		s.stop()
	}
	s.pick()
	var c hifi.Caps
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "aplay", "-D", s.hw(), "--dump-hw-params", "-t", "raw", "-f", "S16_LE", "-c", "2", "-r", "48000", "/dev/null").CombinedOutput()
	if pc, err := hifi.ParseHWParams(string(out)); err == nil {
		c = pc
	} else {
		log.Printf("phone-audiod: %s: no hardware parameters, music goes through the mixer", s.hw())
	}
	if raw, err := os.ReadFile(fmt.Sprintf("/proc/asound/card%s/stream0", s.card())); err == nil {
		if u := hifi.ParseUSBStream(string(raw)); len(u.Alts) > 0 {
			c = u
		}
	}
	s.caps, s.capsOf = &c, s.device
	return c
}

func (s *alsaSink) stop() {
	if s.w == nil {
		return
	}
	_ = s.w.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}
	_ = s.cmd.Wait()
	s.w, s.said, s.conf = nil, nil, ""
}

func (s *alsaSink) open(dev, format string, rate, ch int) error {
	conf := fmt.Sprintf("%s %s %d %d", dev, format, rate, ch)
	if s.w != nil && s.conf == conf {
		return nil
	}
	if f, ok := s.failed[conf]; ok && s.clock().Sub(f.at) < retryAfter {
		return f.err
	}
	s.stop()
	cmd := exec.Command("aplay", "-q", "-t", "raw", "-f", format, "-r", strconv.Itoa(rate), "-c", strconv.Itoa(ch), "-D", dev, "--buffer-time="+bufferUS)
	said := &tail{}
	cmd.Stderr = said
	// aplay goes with audiod (it restarts aplay, not the other way round).
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdin = r
	err = cmd.Start()
	r.Close()
	if err != nil {
		w.Close()
		return s.fail(conf, err)
	}
	s.cmd, s.w, s.said, s.conf = cmd, w, said, conf
	return nil
}

func (s *alsaSink) fail(conf string, err error) error {
	err = fmt.Errorf("aplay %s: %w", conf, err)
	if s.failed == nil {
		s.failed = map[string]failure{}
	}
	s.failed[conf] = failure{s.clock(), err}
	return err
}

func (s *alsaSink) write(b []byte) error {
	_ = s.w.SetWriteDeadline(time.Now().Add(stallAfter))
	_, err := s.w.Write(b)
	if err == nil {
		return nil
	}
	conf, said := s.conf, s.said
	s.stop()
	if errors.Is(err, os.ErrDeadlineExceeded) {
		err = fmt.Errorf("the card took nothing for %v", stallAfter)
	} else if why := said.last(); why != "" {
		err = errors.New(why)
	}
	return s.fail(conf, err)
}

func (s *alsaSink) Write(pcm []int16) error {
	if s.w == nil || !strings.HasSuffix(s.conf, fmt.Sprintf("S16_LE %d %d", audio.Rate, audio.Channels)) {
		s.pick()
	}
	if err := s.open(s.device, "S16_LE", audio.Rate, audio.Channels); err != nil {
		return err
	}
	return s.write(le16(pcm))
}

var errNotTaken = errors.New("the card does not take this set-up")

func (s *alsaSink) Open(p hifi.Plan) error {
	dev := s.device
	if p.Mode == hifi.ModeDSD || p.Mode == hifi.ModeDoP {
		dev = s.hw()
	}
	if err := s.open(dev, p.Format, p.Rate, p.Channels); err != nil {
		return fmt.Errorf("%w: %v", errNotTaken, err)
	}
	return nil
}

func (s *alsaSink) WriteRaw(p hifi.Plan, b []byte) error {
	if err := s.Open(p); err != nil {
		return err
	}
	if err := s.write(b); err != nil {
		return fmt.Errorf("%w: %v", errNotTaken, err)
	}
	return nil
}

func openSink(spec string) (sink, error) {
	switch {
	case spec == "null":
		return &nullSink{}, nil
	case strings.HasPrefix(spec, "file:"):
		return newFileSink(strings.TrimPrefix(spec, "file:"))
	case spec == "alsa" || strings.HasPrefix(spec, "alsa:"):
		dev := strings.TrimPrefix(strings.TrimPrefix(spec, "alsa"), ":")
		if dev == "" {
			dev = "plughw:0,0"
		}
		return &alsaSink{device: dev, base: dev}, nil
	case spec == "auto":
		if _, err := os.Stat("/dev/snd/pcmC0D0p"); err == nil {
			if _, err := exec.LookPath("aplay"); err == nil {
				s := &alsaSink{auto: true, device: "plughw:0,0"}
				s.pick()
				return s, nil
			}
		}
		return &nullSink{}, nil
	}
	return nil, fmt.Errorf("unknown sink %q", spec)
}
