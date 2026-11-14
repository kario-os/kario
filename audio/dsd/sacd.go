package dsd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/kario-os/kario/audio/internal/textenc"
)

// SACD disc images (.iso): 2048-byte sectors. The Scarlet Book that
// defines the disc is not public; this reader follows the public
// descriptions of its layout (written from those, no code taken from
// existing rippers):
//
//	sector 510      Master TOC "SACDMTOC": album and disc data, where the
//	                stereo ("2CH") and multichannel ("MC") area TOCs are
//	sector 511      Master text "SACDText": album and disc title, artist
//	area TOC        "TWOCHTOC" / "MULCHTOC": rate, frame format (DST,
//	                plain DSD), channels, track count, the track area;
//	                then in its following sectors the track lists
//	                "SACDTRL1" (start sector, sectors) and "SACDTRL2"
//	                (start time, length as mm:ss:ff, 75 frames a second)
//	                and the track text "SACDTTxt"
//	audio sectors   a header byte (DST or not, frame and packet counts),
//	                the packets' sizes and kinds, a timecode for each
//	                frame starting in the sector, then the packets; audio
//	                packets joined make the frames (plain DSD: 4704 bytes
//	                a channel, interleaved by byte; DST: one coded frame)
const (
	SectorSize   = 2048
	masterTOCLSN = 510
)

// Frame formats of an area.
const (
	formatDST    = 0
	formatDSD14  = 2
	formatDSD16  = 3
	dataAudio    = 2
	maxSACDFrame = 6 * 4704 * 2
)

// SACD is an opened disc image.
type SACD struct {
	f                   *os.File
	Title, Artist       string
	DiscTitle, Genre    string
	Year                int
	Areas               []*Area // the stereo area first
	charset             int
	stereoTOC, multiTOC uint32
}

// Area is the stereo or the multichannel programme.
type Area struct {
	Multichannel bool
	Channels     int
	// Speakers is the loudspeaker configuration: 0 stereo, 3 five
	// channels (L R C LS RS), 4 5.1 (L R C LFE LS RS).
	Speakers int
	Rate     int
	DST      bool
	Tracks   []SACDTrack
	first    uint32 // the track area's first and last sector
	last     uint32
	charset  int
}

// SACDTrack is a track of an area. Start and Frames are 1/75 s; start is
// the area's time, as the audio sectors' timecodes count.
type SACDTrack struct {
	Number           int
	Title, Performer string
	LSN, Sectors     uint32
	Start, Frames    int64
}

// FrameBytes is a plain DSD frame's bytes per channel (DSD64, 1/75 s).
const FrameBytes = Base / 8 / FramesPerSecond

// OpenSACD reads the TOCs of an SACD image.
func OpenSACD(path string) (*SACD, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	s := &SACD{f: f}
	if err := s.read(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

func (s *SACD) Close() error { return s.f.Close() }

func (s *SACD) sector(lsn uint32, b []byte) error {
	_, err := s.f.ReadAt(b[:SectorSize], int64(lsn)*SectorSize)
	return err
}

func (s *SACD) read() error {
	be := binary.BigEndian
	b := make([]byte, SectorSize)
	if err := s.sector(masterTOCLSN, b); err != nil || string(b[:8]) != "SACDMTOC" {
		return errors.New("not an SACD image (no master TOC at sector 510)")
	}
	s.stereoTOC, s.multiTOC = be.Uint32(b[64:]), be.Uint32(b[72:])
	s.Year = int(be.Uint16(b[120:]))
	s.charset = int(b[138])
	s.Genre = ""
	if err := s.sector(masterTOCLSN+1, b); err == nil && string(b[:8]) == "SACDText" {
		str := func(at int) string {
			p := int(be.Uint16(b[at:]))
			if p <= 0 || p >= SectorSize {
				return ""
			}
			end := bytes.IndexByte(b[p:], 0)
			if end < 0 {
				end = SectorSize - p
			}
			return textenc.Charset(b[p:p+end], s.charset)
		}
		s.Title, s.Artist, s.DiscTitle = str(16), str(18), str(32)
	}
	for _, at := range []uint32{s.stereoTOC, s.multiTOC} {
		if at == 0 {
			continue
		}
		a, err := s.readArea(at)
		if err != nil {
			return err
		}
		s.Areas = append(s.Areas, a)
	}
	if len(s.Areas) == 0 {
		return errors.New("SACD image without an audio area")
	}
	return nil
}

func (s *SACD) readArea(lsn uint32) (*Area, error) {
	be := binary.BigEndian
	b := make([]byte, SectorSize)
	if err := s.sector(lsn, b); err != nil {
		return nil, err
	}
	sig := string(b[:8])
	if sig != "TWOCHTOC" && sig != "MULCHTOC" {
		return nil, fmt.Errorf("SACD: no area TOC at sector %d", lsn)
	}
	a := &Area{Multichannel: sig == "MULCHTOC"}
	size := uint32(be.Uint16(b[10:]))
	if b[20] != 4 {
		return nil, fmt.Errorf("SACD: sample frequency code %d", b[20])
	}
	a.Rate = Base
	switch b[21] & 15 {
	case formatDST:
		a.DST = true
	case formatDSD14, formatDSD16:
	default:
		return nil, fmt.Errorf("SACD: frame format %d", b[21]&15)
	}
	a.Channels, a.Speakers = int(b[32]), int(b[33]>>3)
	if a.Channels < 1 || a.Channels > 6 {
		return nil, fmt.Errorf("SACD: %d channels", a.Channels)
	}
	offset, count := int(b[68]), int(b[69])
	a.first, a.last = be.Uint32(b[72:]), be.Uint32(b[76:])
	a.charset = int(b[90])
	if a.charset == 0 {
		a.charset = s.charset
	}
	a.Tracks = make([]SACDTrack, count)
	for i := range a.Tracks {
		a.Tracks[i].Number = offset + i + 1
	}
	text := make([]byte, 0, 4*SectorSize)
	for k := uint32(1); k < max(size, 2) && k < 64; k++ {
		if err := s.sector(lsn+k, b); err != nil {
			break
		}
		switch string(b[:8]) {
		case "SACDTRL1":
			for i := range a.Tracks {
				a.Tracks[i].LSN = be.Uint32(b[8+4*i:])
				a.Tracks[i].Sectors = be.Uint32(b[8+4*255+4*i:])
			}
		case "SACDTRL2":
			for i := range a.Tracks {
				a.Tracks[i].Start = timecode(b[8+4*i:])
				a.Tracks[i].Frames = timecode(b[8+4*255+4*i:])
			}
		case "SACDTTxt":
			text = append(text[:0], b...)
			for j := uint32(1); j < 4; j++ {
				nb := make([]byte, SectorSize)
				if s.sector(lsn+k+j, nb) != nil || bytes.HasPrefix(nb, []byte("SACD")) || nb[0] == 0 && bytes.Count(nb, []byte{0}) == SectorSize {
					break
				}
				text = append(text, nb...)
			}
		}
	}
	if len(text) > 0 {
		for i := range a.Tracks {
			if 16+2*i+2 > len(text) {
				break
			}
			p := int(be.Uint16(text[16+2*i:]))
			a.Tracks[i].Title, a.Tracks[i].Performer = trackText(text, p, a.charset)
		}
	}
	for i, t := range a.Tracks {
		if t.LSN < a.first || t.Sectors == 0 || t.Frames == 0 {
			return nil, fmt.Errorf("SACD: track %d has no audio", i+1)
		}
	}
	return a, nil
}

// trackText reads a track's text items at p: a count, three reserved
// bytes, then items of a type byte, a reserved byte and a NUL-terminated
// string (padding zeros between them).
func trackText(b []byte, p, charset int) (title, performer string) {
	if p <= 0 || p+4 > len(b) {
		return
	}
	n := int(b[p])
	p += 4
	for i := 0; i < n && p+2 < len(b); i++ {
		typ := b[p]
		p += 2
		end := bytes.IndexByte(b[p:], 0)
		if end < 0 {
			return
		}
		s := textenc.Charset(b[p:p+end], charset)
		p += end + 1
		for p < len(b) && b[p] == 0 {
			p++
		}
		switch typ {
		case 1:
			title = s
		case 2:
			performer = s
		}
	}
	return
}

func timecode(b []byte) int64 {
	return (int64(b[0])*60+int64(b[1]))*FramesPerSecond + int64(b[2])
}

// Stereo is the area to play: the stereo one when there is one.
func (s *SACD) Stereo() *Area { return s.Areas[0] }

// sacdReader assembles one track's frames from its sectors.
type sacdReader struct {
	s     *SACD
	a     *Area
	t     SACDTrack
	lsn   uint32 // next sector to read
	buf   []byte
	cur   []byte // the frame being assembled
	curAt int64  // its timecode, -1 before the first frame start
	ready [][]byte
	times []int64
	next  int64 // timecode of the next frame to hand out
	done  bool
}

func (s *SACD) reader(a *Area, track int) (*sacdReader, error) {
	if track < 1 || track > len(a.Tracks) {
		return nil, fmt.Errorf("SACD: no track %d", track)
	}
	t := a.Tracks[track-1]
	r := &sacdReader{s: s, a: a, t: t, buf: make([]byte, SectorSize)}
	r.seek(t.Start)
	return r, nil
}

func (r *sacdReader) end() int64 { return r.t.Start + r.t.Frames }

// seek starts reading at the frame with timecode at: from an estimate of
// its sector, backing off until the first frame found is not later.
func (r *sacdReader) seek(at int64) {
	t := r.t
	est := t.LSN
	if t.Frames > 0 {
		est += uint32(int64(t.Sectors) * (at - t.Start) / t.Frames)
	}
	for back := uint32(8); ; back *= 4 {
		lsn := t.LSN
		if est > t.LSN+back {
			lsn = est - back
		}
		r.lsn, r.cur, r.curAt, r.ready, r.times, r.done = lsn, r.cur[:0], -1, nil, nil, false
		first := r.peekStart()
		if first <= at || lsn <= t.LSN || lsn < r.a.first {
			break
		}
	}
	r.next = at
}

// peekStart is the timecode of the first frame starting at or after
// r.lsn (reading ahead without keeping anything); a large number if none.
func (r *sacdReader) peekStart() int64 {
	for lsn := r.lsn; lsn <= r.a.last && lsn < r.lsn+64; lsn++ {
		if r.s.sector(lsn, r.buf) != nil {
			break
		}
		if _, infos, ok := parseSector(r.buf); ok && len(infos) > 0 {
			return infos[0]
		}
	}
	return 1 << 40
}

type packet struct {
	start bool
	kind  int
	size  int
	data  []byte
}

// parseSector splits an audio sector into its packets and the timecodes
// of the frames that start in it.
func parseSector(b []byte) ([]packet, []int64, bool) {
	h := b[0]
	dst := h&0x80 != 0
	nframes, npackets := int(h>>3&7), int(h&7)
	p := 1
	if p+2*npackets > len(b) {
		return nil, nil, false
	}
	pk := make([]packet, npackets)
	for i := range pk {
		v := binary.BigEndian.Uint16(b[p:])
		pk[i] = packet{start: v&0x8000 != 0, kind: int(v >> 11 & 7), size: int(v & 0x7ff)}
		p += 2
	}
	infos := make([]int64, nframes)
	for i := range infos {
		if p+3 > len(b) {
			return nil, nil, false
		}
		infos[i] = timecode(b[p:])
		p += 3
		if dst {
			p++
		}
	}
	for i := range pk {
		n := pk[i].size
		if p+n > len(b) {
			return nil, nil, false
		}
		pk[i].data = b[p : p+n]
		p += n
	}
	return pk, infos, true
}

// frame returns the next frame of the track (timecode order), nil at the
// end.
func (r *sacdReader) frame() ([]byte, error) {
	for {
		for len(r.ready) > 0 {
			f, at := r.ready[0], r.times[0]
			r.ready, r.times = r.ready[1:], r.times[1:]
			if at >= r.end() {
				r.done = true
				return nil, nil
			}
			if at >= r.next {
				r.next = at + 1
				return f, nil
			}
		}
		if r.done || r.lsn > r.a.last || r.lsn >= r.t.LSN+r.t.Sectors+8 {
			if !r.done && r.curAt >= r.next && r.curAt < r.end() && len(r.cur) > 0 {
				r.done = true
				r.next = r.curAt + 1
				return r.cur, nil
			}
			return nil, nil
		}
		if err := r.s.sector(r.lsn, r.buf); err != nil {
			return nil, err
		}
		r.lsn++
		pk, infos, ok := parseSector(r.buf)
		if !ok {
			return nil, errors.New("SACD: bad audio sector")
		}
		k := 0
		for _, p := range pk {
			if p.kind != dataAudio {
				continue
			}
			if p.start {
				if r.curAt >= 0 && len(r.cur) > 0 {
					r.ready = append(r.ready, r.cur)
					r.times = append(r.times, r.curAt)
				}
				r.cur, r.curAt = nil, -1
				if k < len(infos) {
					r.curAt = infos[k]
					k++
				}
			}
			if r.curAt >= 0 && len(r.cur)+len(p.data) <= maxSACDFrame {
				r.cur = append(r.cur, p.data...)
			}
		}
	}
}

// SACDDSD is a plain-DSD track as a Stream.
type SACDDSD struct {
	r    *sacdReader
	have []byte // the rest of the current frame
	pos  int64  // byte frames handed out
}

// SACDDST is a DST track's frames.
type SACDDST struct {
	r *sacdReader
	n int64
}

// OpenTrack opens track (1-based) of an area: a *SACDDSD for plain DSD,
// a *SACDDST for DST.
func (s *SACD) OpenTrack(a *Area, track int) (any, error) {
	r, err := s.reader(a, track)
	if err != nil {
		return nil, err
	}
	if a.DST {
		return &SACDDST{r: r}, nil
	}
	return &SACDDSD{r: r}, nil
}

func (d *SACDDSD) Rate() int     { return d.r.a.Rate }
func (d *SACDDSD) Channels() int { return d.r.a.Channels }
func (d *SACDDSD) Len() int64    { return d.r.t.Frames * FrameBytes }
func (d *SACDDSD) Close() error  { return nil }

func (d *SACDDSD) SeekFrame(n int64) error {
	if n < 0 || n > d.Len() {
		return fmt.Errorf("SACD: seek to %d of %d", n, d.Len())
	}
	fr := n / FrameBytes
	d.r.seek(d.r.t.Start + fr)
	d.have, d.pos = nil, fr*FrameBytes
	if skip := n - d.pos; skip > 0 {
		f, err := d.r.frame()
		if err != nil {
			return err
		}
		ch := int64(d.r.a.Channels)
		if int64(len(f)) >= skip*ch {
			d.have = f[skip*ch:]
		}
		d.pos = n
	}
	return nil
}

func (d *SACDDSD) Read(p []byte) (int, error) {
	ch := d.r.a.Channels
	n := 0
	for n+ch <= len(p) && d.pos < d.Len() {
		if len(d.have) == 0 {
			f, err := d.r.frame()
			if err != nil {
				return n, err
			}
			if f == nil {
				break
			}
			if len(f) != FrameBytes*ch {
				// A short or damaged frame plays as silence of the right length.
				f = bytes.Repeat([]byte{Silence}, FrameBytes*ch)
			}
			d.have = f
		}
		k := min(len(d.have), (len(p)-n)/ch*ch, int(d.Len()-d.pos)*ch)
		copy(p[n:], d.have[:k])
		d.have = d.have[k:]
		n += k
		d.pos += int64(k / ch)
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (d *SACDDST) Rate() int     { return d.r.a.Rate }
func (d *SACDDST) Channels() int { return d.r.a.Channels }
func (d *SACDDST) Count() int64  { return d.r.t.Frames }
func (d *SACDDST) Close() error  { return nil }

func (d *SACDDST) Next() ([]byte, error) {
	f, err := d.r.frame()
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, io.EOF
	}
	d.n++
	return f, nil
}

func (d *SACDDST) SeekFrame(n int64) error {
	if n < 0 || n > d.r.t.Frames {
		return fmt.Errorf("SACD: seek to frame %d of %d", n, d.r.t.Frames)
	}
	d.r.seek(d.r.t.Start + n)
	d.n = n
	return nil
}
