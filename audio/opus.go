package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/kazzmir/opus-go/ogg"
)

// IsOgg says whether a music file is Ogg by its name (.opus, .ogg, .oga);
// OggCodec tells Opus from Vorbis by its first packet. phone-audiod plays
// them through audio/opusfile and audio/vorbisfile.
func IsOgg(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".opus") || strings.HasSuffix(p, ".ogg") || strings.HasSuffix(p, ".oga")
}

// The codecs OggCodec tells apart.
const (
	CodecOpus   = "opus"
	CodecVorbis = "vorbis"
)

// OggCodec reads the stream's first packet (in the first page, 27 bytes of
// header and the segment table before it): "OpusHead" or "\x01vorbis".
func OggCodec(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 27+255+16)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if len(head) < 28 || !bytes.Equal(head[:4], []byte("OggS")) {
		return "", errors.New("not an Ogg file")
	}
	start := 27 + int(head[26])
	if len(head) < start+8 {
		return "", errors.New("Ogg file: its first page is cut short")
	}
	switch p := head[start:]; {
	case bytes.HasPrefix(p, []byte("OpusHead")):
		return CodecOpus, nil
	case bytes.HasPrefix(p, []byte("\x01vorbis")):
		return CodecVorbis, nil
	}
	return "", errors.New("Ogg file: neither Opus nor Vorbis")
}

// OpusLen is an Ogg Opus file's length in samples at Rate, from its last
// granule position, without decoding (the Ogg reader alone, no libopus).
func OpusLen(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r, err := ogg.NewOpusReader(f)
	if err != nil {
		return 0, err
	}
	n, err := r.TotalSamples()
	if err != nil {
		return 0, err
	}
	return int(max(n, 0)), nil
}

// VorbisLen is an Ogg Vorbis file's length in samples at Rate: the last
// page's granule position (samples at the stream's rate) over the rate in
// the identification header, without decoding.
func VorbisLen(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	start := 27 + int(raw[min(26, len(raw)-1)])
	if len(raw) < start+16 || !bytes.HasPrefix(raw[start:], []byte("\x01vorbis")) {
		return 0, errors.New("not an Ogg Vorbis file")
	}
	rate := int64(binary.LittleEndian.Uint32(raw[start+12:]))
	last := bytes.LastIndex(raw, []byte("OggS"))
	if rate <= 0 || last < 0 || len(raw) < last+14 {
		return 0, errors.New("Ogg Vorbis file without a last page")
	}
	granule := int64(binary.LittleEndian.Uint64(raw[last+6:]))
	return int(max(granule, 0) * Rate / rate), nil
}

// OggLen is an Ogg file's length in samples at Rate, whichever its codec.
func OggLen(path string) (int, error) {
	codec, err := OggCodec(path)
	if err != nil {
		return 0, err
	}
	if codec == CodecVorbis {
		return VorbisLen(path)
	}
	return OpusLen(path)
}
