// Package musicfile tells what a music file is without decoding it: the
// format, rate, depth, channels, exact length and tags, from headers only
// (cheap enough to index a library). Tracks also expands cue sheets and
// SACD images into their tracks. Decoding is audio/hifi's.
package musicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/audio/dsd"
	"github.com/kario-os/kario/audio/tags"
)

// Codecs.
const (
	FLAC    = "FLAC"
	PCM     = "PCM" // WAV, AIFF
	ALAC    = "ALAC"
	APE     = "APE"
	WavPack = "WavPack"
	MP3     = "MP3"
	Vorbis  = "Vorbis"
	Opus    = "Opus"
	DSD     = "DSD" // DSF, DFF, SACD
	DST     = "DST" // DST-compressed DFF, SACD
)

// Info describes one audio file (or an SACD area).
type Info struct {
	Container string // WAV, AIFF, FLAC, M4A, APE, WV, MP3, Ogg, DSF, DFF, SACD
	Codec     string
	Rate      int // frames a second; DSD: 1-bit samples a second per channel
	Bits      int // PCM bits per sample (1 for DSD)
	Channels  int
	Float     bool
	Frames    int64 // length in frames at Rate; 0 if unknown
	Tags      tags.Tags
	PCM       *Layout // where WAV and AIFF keep their samples
	// MP3: frames of encoder delay and padding to drop (LAME's header).
	Delay, Padding int
}

// Layout is uncompressed PCM inside a container.
type Layout struct {
	Offset, Size   int64
	BytesPerSample int // in the file (24-bit: 3)
	BigEndian      bool
}

// DSD says whether the audio is 1-bit.
func (i Info) DSD() bool { return i.Codec == DSD || i.Codec == DST }

// LenMS is the length in milliseconds.
func (i Info) LenMS() int64 {
	if i.Rate == 0 {
		return 0
	}
	return i.Frames * 1000 / int64(i.Rate)
}

// Quality is what players show: "16/44.1", "24/96", "DSD64".
func (i Info) Quality() string {
	if i.DSD() {
		return dsd.Name(i.Rate)
	}
	k := float64(i.Rate) / 1000
	rate := fmt.Sprintf("%g", k)
	if i.Codec == MP3 || i.Codec == Vorbis || i.Codec == Opus {
		return rate + " kHz"
	}
	bits := fmt.Sprint(i.Bits)
	if i.Float {
		bits += "f"
	}
	return bits + "/" + rate
}

// Label is the format and quality: "FLAC 24/96", "DSD64", "SACD DSD64".
func (i Info) Label() string {
	name := i.Codec
	switch {
	case i.Container == "SACD":
		name = "SACD"
	case i.Codec == DSD && i.Container == "DSF":
		name = "DSF"
	case i.Codec == DSD && i.Container == "DFF":
		name = "DFF"
	case i.Codec == DST:
		name = "DFF DST"
	case i.Codec == PCM:
		name = i.Container
	}
	return name + " " + i.Quality()
}

// IsMusic says whether a name is one this package reads (cue sheets and
// SACD images included).
func IsMusic(name string) bool {
	_, ok := byExt[strings.ToLower(filepath.Ext(name))]
	return ok || IsSheet(name) || IsImage(name)
}

// IsSheet: a cue sheet. IsImage: an SACD disc image.
func IsSheet(name string) bool { return strings.EqualFold(filepath.Ext(name), ".cue") }
func IsImage(name string) bool { return strings.EqualFold(filepath.Ext(name), ".iso") }

var byExt = map[string]func(*os.File) (Info, error){
	".flac": probeFLAC, ".wav": probeWAV, ".wave": probeWAV, ".aif": probeAIFF, ".aiff": probeAIFF, ".aifc": probeAIFF,
	".m4a": probeMP4, ".mp4": probeMP4, ".alac": probeMP4, ".ape": probeAPE, ".wv": probeWV, ".mp3": probeMP3,
	".ogg": probeOgg, ".oga": probeOgg, ".opus": probeOgg, ".dsf": probeDSF, ".dff": probeDFF,
}

// ErrUnsupported marks a file of a known kind in a codec not played here
// (AAC in .m4a, for one).
var ErrUnsupported = errors.New("not a format this phone plays")

// Probe reads an audio file's headers and tags.
func Probe(path string) (Info, error) {
	p, ok := byExt[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return Info{}, fmt.Errorf("%s: %w", filepath.Base(path), ErrUnsupported)
	}
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	i, err := p(f)
	if err != nil {
		return Info{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if i.Rate <= 0 || i.Channels <= 0 {
		return Info{}, fmt.Errorf("%s: no audio format in its header", filepath.Base(path))
	}
	return i, nil
}

func probeDSF(f *os.File) (Info, error) {
	d, err := dsd.ReadDSF(f)
	if err != nil {
		return Info{}, err
	}
	i := Info{Container: "DSF", Codec: DSD, Rate: d.Rate, Bits: 1, Channels: d.Channels, Frames: d.Samples}
	if d.Meta > 0 {
		if _, err := f.Seek(d.Meta, 0); err == nil {
			i.Tags, _, _ = tags.ReadID3v2(f)
		}
	}
	return i, nil
}

func probeDFF(f *os.File) (Info, error) {
	d, err := dsd.ReadDFF(f)
	if err != nil {
		return Info{}, err
	}
	i := Info{Container: "DFF", Codec: DSD, Rate: d.Rate, Bits: 1, Channels: d.Channels, Frames: d.DataBytes / int64(d.Channels) * 8}
	if d.DST {
		i.Codec, i.Frames = DST, d.DSTFrames*int64(d.Rate)/dsd.FramesPerSecond
	}
	if d.ID3 > 0 {
		if _, err := f.Seek(d.ID3, 0); err == nil {
			i.Tags, _, _ = tags.ReadID3v2(f)
		}
	}
	i.Tags.Merge(tags.Tags{Title: d.Title, Artist: d.Artist})
	return i, nil
}
