// Package voice is the voice message codec: Opus (the VoIP mode, 16 kbit/s
// at most, 20 ms frames) in an Ogg file (RFC 7845) any player opens; about
// 1.5–2 KB a second against 96 KB for the 48 kHz PCM recorded. The encoder
// is libopus transpiled to Go (no cgo), kept out of package audio so only
// the apps that send voice link it.
package voice

import (
	"bytes"
	"errors"
	"io"

	"github.com/kazzmir/opus-go/ogg"
	"github.com/kazzmir/opus-go/opus"
)

const (
	rate    = 48000
	frame   = rate / 50
	bitrate = 16000
	// OPUS_APPLICATION_VOIP: tuned for speech intelligibility.
	appVoIP = 2048
	// Middle complexity: voice quality is close to 10 at a fraction of
	// the CPU, which matters on a phone's little cores.
	complexity = 5
	maxPacket  = 1275
)

// Encode turns 48 kHz mono PCM into an Ogg Opus file.
func Encode(pcm []int16) ([]byte, error) {
	enc, err := opus.NewEncoder(rate, 1, appVoIP)
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	if err := enc.SetBitrate(bitrate); err != nil {
		return nil, err
	}
	if err := enc.SetComplexity(complexity); err != nil {
		return nil, err
	}
	pre, err := enc.PreSkip()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	w := ogg.NewPageWriter(&out, 0x4a314a31)
	head, err := ogg.BuildOpusHeadPacket(ogg.OpusHead{Version: 1, Channels: 1, PreSkip: uint16(pre), InputSampleRate: rate})
	if err != nil {
		return nil, err
	}
	tags, err := ogg.BuildOpusTagsPacket(ogg.OpusTags{Vendor: "Kario"})
	if err != nil {
		return nil, err
	}
	if err := w.WriteHeaderPacket(head, true); err != nil {
		return nil, err
	}
	if err := w.WriteHeaderPacket(tags, false); err != nil {
		return nil, err
	}
	// The encoder's lookahead (pre-skip) delays the signal: feed that much
	// silence after the end so the last samples come out, and let the last
	// granule position tell the decoder where the message really ends.
	n := len(pcm)
	src := append(pcm[:n:n], make([]int16, pre+frame)...)
	pkt := make([]byte, maxPacket)
	buf := make([]int16, frame)
	var fed uint64
	for off := 0; fed < uint64(pre+n); off += frame {
		copy(buf, src[off:min(len(src), off+frame)])
		size, err := enc.Encode(buf, frame, pkt)
		if err != nil {
			return nil, err
		}
		fed += frame
		last := fed >= uint64(pre+n)
		gran := fed
		if last {
			gran = uint64(pre + n)
		}
		if err := w.WritePacket(pkt[:size], gran, last); err != nil {
			return nil, err
		}
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Decode reads a voice message back to 48 kHz mono PCM, without the
// pre-skip and cut where the last granule position says.
func Decode(raw []byte) ([]int16, error) {
	if len(raw) < 4 || string(raw[:4]) != "OggS" {
		return nil, errors.New("not an Ogg file")
	}
	r, err := ogg.NewOpusReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if r.Head.Channels != 1 {
		return nil, errors.New("not a mono voice message")
	}
	dec, err := opus.NewDecoder(rate, 1)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	var out []int16
	var end uint64
	buf := make([]int16, 120*rate/1000)
	for {
		p, err := r.ReadAudioPacket()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		n, err := dec.Decode(p.Data, buf, len(buf), false)
		if err != nil {
			return nil, err
		}
		out = append(out, buf[:n]...)
		if p.GranuleValid {
			end = p.GranulePos
		}
	}
	pre := uint64(r.Head.PreSkip)
	if end > 0 && end < uint64(len(out)) {
		out = out[:end]
	}
	if pre >= uint64(len(out)) {
		return nil, errors.New("empty voice message")
	}
	return out[pre:], nil
}
