package audiod

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
// native DSD), set up as a plan says.
type direct interface {
	Caps() hifi.Caps
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
// is plugged in.
type alsaSink struct {
	auto   bool
	device string // plughw:C,D
	cmd    *exec.Cmd
	w      io.WriteCloser
	conf   string
	caps   *hifi.Caps
	capsOf string
	failed map[string]time.Time
}

// bufferUS is aplay's buffer: short, so a set-up change loses little.
const bufferUS = "200000"

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
	s.w, s.conf = nil, ""
}

func (s *alsaSink) open(dev, format string, rate, ch int) error {
	conf := fmt.Sprintf("%s %s %d %d", dev, format, rate, ch)
	if s.w != nil && s.conf == conf {
		return nil
	}
	if t, ok := s.failed[conf]; ok && time.Since(t) < 10*time.Second {
		return fmt.Errorf("aplay %s failed", conf)
	}
	s.stop()
	cmd := exec.Command("aplay", "-q", "-t", "raw", "-f", format, "-r", strconv.Itoa(rate), "-c", strconv.Itoa(ch), "-D", dev, "--buffer-time="+bufferUS)
	cmd.Stderr = os.Stderr
	// aplay goes with audiod (it restarts aplay, not the other way round).
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	w, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd, s.w, s.conf = cmd, w, conf
	return nil
}

func (s *alsaSink) write(b []byte) error {
	if _, err := s.w.Write(b); err != nil {
		if s.failed == nil {
			s.failed = map[string]time.Time{}
		}
		s.failed[s.conf] = time.Now()
		s.stop()
		return err
	}
	return nil
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

func (s *alsaSink) WriteRaw(p hifi.Plan, b []byte) error {
	dev := s.device
	if p.Mode == hifi.ModeDSD || p.Mode == hifi.ModeDoP {
		dev = s.hw()
	}
	if err := s.open(dev, p.Format, p.Rate, p.Channels); err != nil {
		return errNotTaken
	}
	if err := s.write(b); err != nil {
		return errNotTaken
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
		return &alsaSink{device: dev}, nil
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
