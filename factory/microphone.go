package factory

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

// The microphone check's levels (RMS, dBFS): a recording quieter than
// micQuiet heard nothing; with the speaker's tone playing (loopback) the
// level must rise by loopbackRise over the quiet one.
const (
	micQuiet     = -50.0
	loopbackRise = 10.0
	micSeconds   = "2"
	micRate      = 16000
)

// Microphone records from the SKU's card (its call verb and microphone
// set through UCM first) and measures the level. It records with arecord
// itself: audiod's recordings come from its simulated microphone.
func (e *Env) Microphone(ctx context.Context, s SKU, loopback bool) (string, error) {
	if s.Audio.Card == "" {
		return "", errors.New("the SKU names no sound card")
	}
	dev := "plug" + s.Audio.Card + ",0"
	if s.Audio.CallVerb != "" && s.Audio.CallMic != "" {
		out, err := e.run(ctx, "alsaucm", "-c", s.Audio.Card, "set", "_verb", s.Audio.CallVerb, "set", "_enadev", s.Audio.CallMic,
			"get", "CapturePCM/"+s.Audio.CallMic)
		if err != nil {
			return "", fmt.Errorf("UCM %s/%s: %w", s.Audio.CallVerb, s.Audio.CallMic, err)
		}
		if pcm := capturePCM(string(out)); pcm != "" {
			dev = pcm
		}
	}
	record := func() (float64, error) {
		raw, err := e.run(ctx, "arecord", "-q", "-D", dev, "-f", "S16_LE", "-c", "1", "-r", fmt.Sprint(micRate), "-d", micSeconds, "-t", "raw")
		if err != nil {
			return 0, fmt.Errorf("arecord: %w", err)
		}
		if len(raw) < micRate/10 {
			return 0, fmt.Errorf("arecord: %d bytes recorded", len(raw))
		}
		return levelDBFS(raw), nil
	}
	if !loopback {
		db, err := record()
		if err != nil {
			return "", err
		}
		v := fmt.Sprintf("%.1f dBFS", db)
		if db < micQuiet {
			return v, fmt.Errorf("heard nothing: %.1f dBFS is below %.0f", db, micQuiet)
		}
		return v, nil
	}
	quiet, err := record()
	if err != nil {
		return "", err
	}
	stop, err := e.tone()
	if err != nil {
		return "", fmt.Errorf("no tone for the loopback: %w", err)
	}
	loud, err := record()
	stop()
	if err != nil {
		return "", err
	}
	v := fmt.Sprintf("%.1f dBFS quiet, %.1f dBFS with the tone", quiet, loud)
	if loud < micQuiet {
		return v, fmt.Errorf("heard nothing with the tone: %.1f dBFS is below %.0f", loud, micQuiet)
	}
	if loud-quiet < loopbackRise {
		return v, fmt.Errorf("the speaker's tone raised the level by %.1f dB, less than %.0f", loud-quiet, loopbackRise)
	}
	return v, nil
}

// tone has audiod play the alarm tone (its category is not silenced by
// the sound mode) until stop.
func (e *Env) tone() (stop func(), err error) {
	cl, err := wire.Dial(filepath.Join(e.RunDir, svc.AudiodSock))
	if err != nil {
		return nil, err
	}
	var st audio.Stream
	if err := cl.Call(svc.AudioPlay, map[string]string{"app": "system", "name": "factory", "category": audio.Alarm, "source": "tone:alarm"}, &st); err != nil {
		cl.Close()
		return nil, err
	}
	return func() {
		_ = cl.Call(svc.AudioControl, map[string]string{"id": st.ID, "op": "stop"}, nil)
		cl.Close()
	}, nil
}

// capturePCM is the device alsaucm's get CapturePCM/<mic> names ("hw:0,1"
// on a line "CapturePCM/Mic=hw:0,1"), through the plug layer for the rate.
func capturePCM(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.HasPrefix(k, "CapturePCM/") {
			if strings.HasPrefix(v, "hw:") {
				return "plug" + v
			}
			return v
		}
	}
	return ""
}

// levelDBFS is 16-bit little-endian samples' RMS level in dBFS.
func levelDBFS(raw []byte) float64 {
	n := len(raw) / 2
	if n == 0 {
		return math.Inf(-1)
	}
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(raw[2*i:]))) / 32768
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(n))
	if rms == 0 {
		return -120
	}
	return math.Max(20*math.Log10(rms), -120)
}
