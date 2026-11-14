// Package flac decodes FLAC (RFC 9639) in pure Go: 4 to 32 bits per
// sample, every rate the format can say (192 kHz and beyond), one to eight
// channels, with sample-accurate seeking (from the seek table when there is
// one, otherwise by bisecting over frame headers). Samples come out
// interleaved and left-justified in int32, so a 16-bit sample is v<<16 and
// a 24-bit one v<<8: what an S32 sound card takes unchanged.
package flac

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// StreamInfo is the STREAMINFO block.
type StreamInfo struct {
	MinBlock, MaxBlock int
	MinFrame, MaxFrame int
	Rate               int
	Channels           int
	Bits               int
	Samples            int64 // per channel; 0 when the encoder did not know
	MD5                [16]byte
}

// SeekPoint is a SEEKTABLE entry; Offset counts from the first frame.
type SeekPoint struct {
	Sample int64
	Offset int64
	Frames int
}

// Picture is a PICTURE block; the image itself stays in the file at
// Offset (Size bytes) until someone reads it.
type Picture struct {
	Type   int // 3 is the front cover
	MIME   string
	Offset int64
	Size   int64
}

// Metadata is what precedes the audio.
type Metadata struct {
	Info     StreamInfo
	Seek     []SeekPoint
	Comments []byte // the raw VORBIS_COMMENT block (see audio/tags)
	Pictures []Picture
	FramesAt int64 // offset of the first frame
}

const maxCommentBlock = 1 << 20

var errNotFLAC = errors.New("not a FLAC file")

// ReadMetadata reads the metadata blocks of the FLAC stream at the start
// of r (after an ID3v2 tag some taggers put before it).
func ReadMetadata(r io.ReadSeeker) (Metadata, error) {
	var m Metadata
	start, err := skipID3(r)
	if err != nil {
		return m, err
	}
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != "fLaC" {
		return m, errNotFLAC
	}
	off := start + 4
	sawInfo := false
	for {
		var h [4]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return m, fmt.Errorf("FLAC metadata: %w", err)
		}
		last, typ := h[0]&0x80 != 0, h[0]&0x7f
		size := int64(h[1])<<16 | int64(h[2])<<8 | int64(h[3])
		off += 4
		body := off
		switch typ {
		case 0:
			if size < 34 {
				return m, errors.New("FLAC: short STREAMINFO")
			}
			var b [34]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return m, err
			}
			m.Info = parseStreamInfo(b[:])
			sawInfo = true
		case 3:
			raw := make([]byte, size)
			if _, err := io.ReadFull(r, raw); err != nil {
				return m, err
			}
			for i := 0; i+18 <= len(raw); i += 18 {
				s := int64(binary.BigEndian.Uint64(raw[i:]))
				if s == -1 {
					continue
				}
				m.Seek = append(m.Seek, SeekPoint{Sample: s, Offset: int64(binary.BigEndian.Uint64(raw[i+8:])), Frames: int(binary.BigEndian.Uint16(raw[i+16:]))})
			}
		case 4:
			if size <= maxCommentBlock {
				m.Comments = make([]byte, size)
				if _, err := io.ReadFull(r, m.Comments); err != nil {
					return m, err
				}
			}
		case 6:
			p, err := readPicture(r, body, size)
			if err == nil {
				m.Pictures = append(m.Pictures, p)
			}
		}
		off = body + size
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			return m, err
		}
		if last {
			break
		}
	}
	if !sawInfo || m.Info.Rate == 0 || m.Info.Channels == 0 || m.Info.Bits < 4 || m.Info.Bits > 32 {
		return m, errors.New("FLAC: no usable STREAMINFO")
	}
	m.FramesAt = off
	return m, nil
}

func parseStreamInfo(b []byte) StreamInfo {
	var s StreamInfo
	s.MinBlock = int(binary.BigEndian.Uint16(b[0:]))
	s.MaxBlock = int(binary.BigEndian.Uint16(b[2:]))
	s.MinFrame = int(b[4])<<16 | int(b[5])<<8 | int(b[6])
	s.MaxFrame = int(b[7])<<16 | int(b[8])<<8 | int(b[9])
	v := binary.BigEndian.Uint64(b[10:])
	s.Rate = int(v >> 44)
	s.Channels = int(v>>41&7) + 1
	s.Bits = int(v>>36&31) + 1
	s.Samples = int64(v & (1<<36 - 1))
	copy(s.MD5[:], b[18:34])
	return s
}

func readPicture(r io.Reader, body, size int64) (Picture, error) {
	var p Picture
	var u [4]byte
	read32 := func() (uint32, error) {
		_, err := io.ReadFull(r, u[:])
		return binary.BigEndian.Uint32(u[:]), err
	}
	typ, err := read32()
	if err != nil {
		return p, err
	}
	n, err := read32()
	if err != nil || int64(n) > size {
		return p, errors.New("FLAC: bad PICTURE")
	}
	mime := make([]byte, n)
	if _, err := io.ReadFull(r, mime); err != nil {
		return p, err
	}
	d, err := read32()
	if err != nil || int64(d) > size {
		return p, errors.New("FLAC: bad PICTURE")
	}
	if _, err := io.CopyN(io.Discard, r, int64(d)+16); err != nil {
		return p, err
	}
	l, err := read32()
	if err != nil {
		return p, err
	}
	p.Type, p.MIME = int(typ), string(mime)
	p.Offset = body + 4 + 4 + int64(n) + 4 + int64(d) + 16 + 4
	p.Size = int64(l)
	if p.Offset+p.Size > body+size {
		return p, errors.New("FLAC: PICTURE overruns its block")
	}
	return p, nil
}

// skipID3 steps over an ID3v2 tag at the start of r and returns where the
// stream begins.
func skipID3(r io.ReadSeeker) (int64, error) {
	var h [10]byte
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if _, err := io.ReadFull(r, h[:]); err != nil || string(h[:3]) != "ID3" {
		_, err := r.Seek(0, io.SeekStart)
		return 0, err
	}
	size := int64(h[6]&0x7f)<<21 | int64(h[7]&0x7f)<<14 | int64(h[8]&0x7f)<<7 | int64(h[9]&0x7f)
	start := 10 + size
	if h[5]&0x10 != 0 {
		start += 10
	}
	_, err := r.Seek(start, io.SeekStart)
	return start, err
}

// Decoder reads a FLAC stream's samples.
type Decoder struct {
	Meta  Metadata
	r     io.ReadSeeker
	br    bitReader
	size  int64     // of the file, for bisection
	sub   [][]int64 // per channel, one block
	block []int32   // the current frame, interleaved
	have  int       // samples in block
	off   int       // next sample in block
	first int64     // the current frame's first sample
	skip  int       // samples per channel to drop after a seek
}

// Open reads the metadata and stands at the first sample.
func Open(r io.ReadSeeker) (*Decoder, error) {
	m, err := ReadMetadata(r)
	if err != nil {
		return nil, err
	}
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	d := &Decoder{Meta: m, r: r, size: size}
	d.sub = make([][]int64, m.Info.Channels)
	if err := d.reset(m.FramesAt); err != nil {
		return nil, err
	}
	return d, nil
}

// Channels, Rate and Bits describe the samples Read returns.
func (d *Decoder) Channels() int { return d.Meta.Info.Channels }
func (d *Decoder) Rate() int     { return d.Meta.Info.Rate }
func (d *Decoder) Bits() int     { return d.Meta.Info.Bits }

// Samples is the length per channel (0 if the file does not say).
func (d *Decoder) Samples() int64 { return d.Meta.Info.Samples }

// Pos is the next sample (per channel) Read returns.
func (d *Decoder) Pos() int64 {
	ch := d.Meta.Info.Channels
	return d.first + int64(d.off/ch) + int64(d.skip)
}

func (d *Decoder) reset(off int64) error {
	if _, err := d.r.Seek(off, io.SeekStart); err != nil {
		return err
	}
	d.br.reset(d.r, off)
	d.have, d.off = 0, 0
	return nil
}

// Read fills dst (a multiple of the channel count) with interleaved
// samples and returns how many it wrote; io.EOF once the stream is done.
func (d *Decoder) Read(dst []int32) (int, error) {
	ch := d.Meta.Info.Channels
	dst = dst[:len(dst)/ch*ch]
	n := 0
	for n < len(dst) {
		if d.off >= d.have {
			if err := d.frame(); err != nil {
				if n > 0 && err == io.EOF {
					return n, nil
				}
				return n, err
			}
			if d.skip > 0 {
				k := min(d.skip, d.have/ch)
				d.off = k * ch
				d.skip -= k
				continue
			}
		}
		c := copy(dst[n:], d.block[d.off:d.have])
		n += c
		d.off += c
	}
	return n, nil
}

// SeekSample moves to sample (per channel) of the stream.
func (d *Decoder) SeekSample(sample int64) error {
	if sample < 0 || d.Meta.Info.Samples > 0 && sample > d.Meta.Info.Samples {
		return fmt.Errorf("FLAC: seek to %d of %d samples", sample, d.Meta.Info.Samples)
	}
	if sample == d.Meta.Info.Samples {
		d.skip, d.first = 0, sample
		return d.reset(d.size)
	}
	start, at := d.Meta.FramesAt, int64(0)
	for _, p := range d.Meta.Seek {
		if p.Sample <= sample && p.Sample >= at {
			start, at = d.Meta.FramesAt+p.Offset, p.Sample
		}
	}
	// Far from a seek point (or none): bisect over frame headers.
	if rate := int64(d.Meta.Info.Rate); sample-at > 10*rate {
		lo, hi := start, d.size
		for hi-lo > 64<<10 {
			mid := lo + (hi-lo)/2
			off, first, err := d.syncAt(mid)
			if err != nil || first > sample || off >= hi {
				hi = mid
				continue
			}
			lo = off
		}
		start = lo
	}
	if err := d.reset(start); err != nil {
		return err
	}
	for {
		h, err := d.header()
		if err != nil {
			return err
		}
		if h.first+int64(h.block) > sample {
			if err := d.reset(h.offset); err != nil {
				return err
			}
			d.skip = int(sample - h.first)
			d.first = h.first
			return nil
		}
		if err := d.skipFrame(h); err != nil {
			return err
		}
	}
}

// syncAt finds the first frame at or after off: its offset and first
// sample.
func (d *Decoder) syncAt(off int64) (int64, int64, error) {
	if err := d.reset(off); err != nil {
		return 0, 0, err
	}
	h, err := d.header()
	if err != nil {
		return 0, 0, err
	}
	return h.offset, h.first, nil
}

// skipFrame decodes the frame whose header was just read, for its length.
func (d *Decoder) skipFrame(h header) error {
	return d.body(h)
}

// frame decodes the next frame into block.
func (d *Decoder) frame() error {
	h, err := d.header()
	if err != nil {
		return err
	}
	if err := d.body(h); err != nil {
		return err
	}
	ch := h.channels
	need := h.block * ch
	if cap(d.block) < need {
		d.block = make([]int32, need)
	}
	d.block = d.block[:need]
	shift := uint(32 - h.bits)
	a, b := d.sub[0][:h.block], []int64(nil)
	if ch > 1 {
		b = d.sub[1][:h.block]
	}
	switch h.assign {
	case leftSide:
		for i := range a {
			b[i] = a[i] - b[i]
		}
	case sideRight:
		for i := range a {
			a[i] += b[i]
		}
	case midSide:
		for i := range a {
			mid := a[i]<<1 | b[i]&1
			a[i], b[i] = (mid+b[i])>>1, (mid-b[i])>>1
		}
	}
	for c := 0; c < ch; c++ {
		s := d.sub[c][:h.block]
		for i, v := range s {
			d.block[i*ch+c] = int32(v) << shift
		}
	}
	d.have, d.off, d.first = need, 0, h.first
	return nil
}

const (
	independent = iota
	leftSide
	sideRight
	midSide
)

type header struct {
	block, rate, channels, bits int
	assign                      int
	first                       int64 // first sample of the frame
	offset                      int64 // of the frame in the file
}

var blockSizes = [16]int{0, 192, 576, 1152, 2304, 4608, -1, -2, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768}
var rates = [12]int{0, 88200, 176400, 192000, 8000, 16000, 22050, 24000, 32000, 44100, 48000, 96000}
var sizes = [8]int{0, 8, 12, -1, 16, 20, 24, 32}

// header finds the next frame header (sync code, valid CRC-8 and in
// keeping with STREAMINFO) and reads it.
func (d *Decoder) header() (header, error) {
	b := &d.br
	b.unread()
	for {
		if !b.ensure(2) {
			return header{}, io.EOF
		}
		if b.buf[b.i] == 0xff && b.buf[b.i+1]&0xfe == 0xf8 {
			b.ensure(16)
			if h, n, ok := d.parseHeader(b.buf[b.i:b.n]); ok {
				h.offset = b.base + int64(b.i)
				b.i += n
				return h, nil
			}
		}
		b.i++
	}
}

func (d *Decoder) parseHeader(p []byte) (header, int, bool) {
	var h header
	if len(p) < 6 || p[3]&1 != 0 {
		return h, 0, false
	}
	info := d.Meta.Info
	variable := p[1]&1 == 1
	bs, rc := int(p[2]>>4), int(p[2]&15)
	ca, sc := int(p[3]>>4), int(p[3]>>1&7)
	if bs == 0 || rc == 15 || ca > 10 || sc == 3 {
		return h, 0, false
	}
	i := 4
	c := p[i]
	var num uint64
	extra := 0
	switch {
	case c < 0x80:
		num = uint64(c)
	case c&0xe0 == 0xc0:
		num, extra = uint64(c&0x1f), 1
	case c&0xf0 == 0xe0:
		num, extra = uint64(c&0x0f), 2
	case c&0xf8 == 0xf0:
		num, extra = uint64(c&0x07), 3
	case c&0xfc == 0xf8:
		num, extra = uint64(c&0x03), 4
	case c&0xfe == 0xfc:
		num, extra = uint64(c&0x01), 5
	case c == 0xfe:
		num, extra = 0, 6
	default:
		return h, 0, false
	}
	i++
	for k := 0; k < extra; k++ {
		if i >= len(p) || p[i]&0xc0 != 0x80 {
			return h, 0, false
		}
		num = num<<6 | uint64(p[i]&0x3f)
		i++
	}
	h.block = blockSizes[bs]
	switch h.block {
	case -1:
		if i >= len(p) {
			return h, 0, false
		}
		h.block = int(p[i]) + 1
		i++
	case -2:
		if i+1 >= len(p) {
			return h, 0, false
		}
		h.block = int(binary.BigEndian.Uint16(p[i:])) + 1
		i += 2
	}
	switch {
	case rc == 0:
		h.rate = info.Rate
	case rc < 12:
		h.rate = rates[rc]
	case rc == 12:
		if i >= len(p) {
			return h, 0, false
		}
		h.rate = int(p[i]) * 1000
		i++
	default:
		if i+1 >= len(p) {
			return h, 0, false
		}
		h.rate = int(binary.BigEndian.Uint16(p[i:]))
		if rc == 14 {
			h.rate *= 10
		}
		i += 2
	}
	if i >= len(p) || crc8(p[:i]) != p[i] {
		return h, 0, false
	}
	i++
	h.bits = sizes[sc]
	if sc == 0 {
		h.bits = info.Bits
	}
	h.channels, h.assign = ca+1, independent
	if ca >= 8 {
		h.channels, h.assign = 2, ca-7
	}
	if h.channels != info.Channels || h.rate != info.Rate || h.bits != info.Bits || h.block > max(info.MaxBlock, 16) && info.MaxBlock > 0 {
		return h, 0, false
	}
	if variable {
		h.first = int64(num)
	} else {
		bsz := int64(h.block)
		if info.MinBlock == info.MaxBlock && info.MaxBlock > 0 {
			bsz = int64(info.MaxBlock)
		}
		h.first = int64(num) * bsz
	}
	return h, i, true
}

// body decodes the subframes after a header into sub, then steps over the
// footer.
func (d *Decoder) body(h header) error {
	b := &d.br
	for c := 0; c < h.channels; c++ {
		if cap(d.sub[c]) < h.block {
			d.sub[c] = make([]int64, max(h.block, d.Meta.Info.MaxBlock))
		}
		bits := h.bits
		if h.assign == leftSide && c == 1 || h.assign == sideRight && c == 0 || h.assign == midSide && c == 1 {
			bits++
		}
		if err := d.subframe(d.sub[c][:h.block], bits); err != nil {
			return err
		}
	}
	b.align()
	b.bits(16)
	return b.err
}

func (d *Decoder) subframe(out []int64, bits int) error {
	b := &d.br
	hdr := b.bits(8)
	if hdr&0x80 != 0 {
		return errors.New("FLAC: bad subframe header")
	}
	wasted := 0
	if hdr&1 != 0 {
		wasted = int(b.unary()) + 1
		bits -= wasted
	}
	if bits <= 0 {
		return errors.New("FLAC: wasted bits exceed the sample size")
	}
	typ := int(hdr >> 1 & 0x3f)
	switch {
	case typ == 0:
		v := b.sbits(uint(bits))
		for i := range out {
			out[i] = v
		}
	case typ == 1:
		for i := range out {
			out[i] = b.sbits(uint(bits))
		}
	case typ >= 8 && typ <= 12:
		order := typ - 8
		if order > len(out) {
			return errors.New("FLAC: predictor longer than the block")
		}
		for i := 0; i < order; i++ {
			out[i] = b.sbits(uint(bits))
		}
		if err := d.residual(out, order); err != nil {
			return err
		}
		fixed(out, order)
	case typ >= 32:
		order := typ - 31
		if order > len(out) {
			return errors.New("FLAC: predictor longer than the block")
		}
		for i := 0; i < order; i++ {
			out[i] = b.sbits(uint(bits))
		}
		prec := int(b.bits(4)) + 1
		if prec == 16 {
			return errors.New("FLAC: bad LPC precision")
		}
		shift := int(b.sbits(5))
		if shift < 0 {
			return errors.New("FLAC: negative LPC shift")
		}
		var coef [32]int64
		for i := 0; i < order; i++ {
			coef[i] = b.sbits(uint(prec))
		}
		if err := d.residual(out, order); err != nil {
			return err
		}
		lpc(out, coef[:order], uint(shift))
	default:
		return fmt.Errorf("FLAC: reserved subframe type %d", typ)
	}
	if wasted > 0 {
		for i := range out {
			out[i] <<= uint(wasted)
		}
	}
	return b.err
}

// residual reads the coded residual into out[order:].
func (d *Decoder) residual(out []int64, order int) error {
	b := &d.br
	method := b.bits(2)
	if method > 1 {
		return errors.New("FLAC: reserved residual coding")
	}
	pbits, escape := uint(4), uint64(15)
	if method == 1 {
		pbits, escape = 5, 31
	}
	porder := uint(b.bits(4))
	parts := 1 << porder
	n := len(out) >> porder
	if n<<porder != len(out) || n < order {
		return errors.New("FLAC: bad residual partitions")
	}
	i := order
	for p := 0; p < parts; p++ {
		end := (p + 1) * n
		k := b.bits(pbits)
		if k == escape {
			nb := uint(b.bits(5))
			for ; i < end; i++ {
				if nb == 0 {
					out[i] = 0
				} else {
					out[i] = b.sbits(nb)
				}
			}
			continue
		}
		for ; i < end; i++ {
			q := uint64(b.unary())
			u := q<<k | b.bits(uint(k))
			out[i] = int64(u>>1) ^ -int64(u&1)
		}
		if b.err != nil {
			return b.err
		}
	}
	return nil
}

func fixed(s []int64, order int) {
	switch order {
	case 1:
		for i := 1; i < len(s); i++ {
			s[i] += s[i-1]
		}
	case 2:
		for i := 2; i < len(s); i++ {
			s[i] += 2*s[i-1] - s[i-2]
		}
	case 3:
		for i := 3; i < len(s); i++ {
			s[i] += 3*s[i-1] - 3*s[i-2] + s[i-3]
		}
	case 4:
		for i := 4; i < len(s); i++ {
			s[i] += 4*s[i-1] - 6*s[i-2] + 4*s[i-3] - s[i-4]
		}
	}
}

func lpc(s []int64, coef []int64, shift uint) {
	order := len(coef)
	for i := order; i < len(s); i++ {
		var sum int64
		for j, c := range coef {
			sum += c * s[i-1-j]
		}
		s[i] += sum >> shift
	}
}

func crc8(p []byte) byte {
	var c byte
	for _, v := range p {
		c ^= v
		for k := 0; k < 8; k++ {
			if c&0x80 != 0 {
				c = c<<1 ^ 0x07
			} else {
				c <<= 1
			}
		}
	}
	return c
}
