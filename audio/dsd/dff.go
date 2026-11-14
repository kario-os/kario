package dsd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// DFFInfo is what a DSDIFF file (Philips' DSD Interchange File Format,
// big-endian IFF chunks) says about itself.
type DFFInfo struct {
	Rate, Channels int
	ChannelIDs     []string
	DST            bool  // DST-compressed ("DST " chunk) rather than plain
	Data           int64 // plain: first byte frame; DST: first chunk inside "DST "
	DataBytes      int64 // plain: bytes of sound; DST: size of the "DST " chunk's body
	DSTFrames      int64 // DST: frames (1/75 s each)
	Index          []DSTEntry
	Title, Artist  string // from the edited master information chunk
	ID3, ID3Size   int64  // an "ID3 " chunk some writers add
}

// DSTEntry is where a DST frame's data is in the file.
type DSTEntry struct {
	Offset int64
	Size   int64
}

const maxDFFChunks = 1 << 16

// ReadDFF reads the chunks before (and after) the sound.
func ReadDFF(r io.ReadSeeker) (DFFInfo, error) {
	var d DFFInfo
	be := binary.BigEndian
	var h [16]byte
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return d, err
	}
	if _, err := io.ReadFull(r, h[:]); err != nil || string(h[:4]) != "FRM8" || string(h[12:16]) != "DSD " {
		return d, errors.New("not a DSDIFF file")
	}
	end := 12 + int64(be.Uint64(h[4:]))
	var raw []DSTEntry
	err := chunks(r, 16, end, func(id string, at, size int64) error {
		switch id {
		case "PROP":
			var t [4]byte
			if _, err := io.ReadFull(r, t[:]); err != nil || string(t[:]) != "SND " {
				return errors.New("DSDIFF: PROP is not SND")
			}
			return chunks(r, at+4, at+size, func(id string, at, size int64) error {
				b := make([]byte, min(size, 4096))
				if _, err := io.ReadFull(r, b); err != nil {
					return err
				}
				switch id {
				case "FS  ":
					if len(b) >= 4 {
						d.Rate = int(be.Uint32(b))
					}
				case "CHNL":
					if len(b) >= 2 {
						d.Channels = int(be.Uint16(b))
						for i := 0; i < d.Channels && 2+4*i+4 <= len(b); i++ {
							d.ChannelIDs = append(d.ChannelIDs, string(b[2+4*i:6+4*i]))
						}
					}
				case "CMPR":
					if len(b) >= 4 {
						switch string(b[:4]) {
						case "DSD ":
						case "DST ":
							d.DST = true
						default:
							return fmt.Errorf("DSDIFF: compression %q", b[:4])
						}
					}
				}
				return nil
			})
		case "DSD ":
			d.Data, d.DataBytes = at, size
		case "DST ":
			d.Data, d.DataBytes = at, size
			return chunks(r, at, at+size, func(id string, at, size int64) error {
				if id == "FRTE" {
					var b [6]byte
					if _, err := io.ReadFull(r, b[:]); err != nil {
						return err
					}
					d.DSTFrames = int64(be.Uint32(b[:]))
					if fr := be.Uint16(b[4:]); fr != FramesPerSecond {
						return fmt.Errorf("DSDIFF: %d DST frames a second", fr)
					}
				}
				return errStop
			})
		case "DSTI":
			n := size / 12
			b := make([]byte, n*12)
			if _, err := io.ReadFull(r, b); err != nil {
				return err
			}
			raw = make([]DSTEntry, n)
			for i := range raw {
				raw[i] = DSTEntry{int64(be.Uint64(b[12*i:])), int64(be.Uint32(b[12*i+8:]))}
			}
		case "DIIN":
			return chunks(r, at, at+size, func(id string, at, size int64) error {
				if id != "DIAR" && id != "DITI" || size < 4 || size > 4096 {
					return nil
				}
				b := make([]byte, size)
				if _, err := io.ReadFull(r, b); err != nil {
					return err
				}
				n := int(be.Uint32(b))
				s := strings.TrimRight(string(b[4:min(len(b), 4+n)]), "\x00")
				if id == "DIAR" {
					d.Artist = s
				} else {
					d.Title = s
				}
				return nil
			})
		case "ID3 ":
			d.ID3, d.ID3Size = at, size
		}
		return nil
	})
	if err != nil {
		return d, err
	}
	if d.Rate < Base || d.Channels < 1 || d.Channels > 6 || d.DataBytes == 0 {
		return d, fmt.Errorf("DSDIFF: %d channels at %d Hz", d.Channels, d.Rate)
	}
	if d.DST && validIndex(r, raw) {
		d.Index = raw
	}
	return d, nil
}

var errStop = errors.New("stop")

// chunks walks the chunks in [from, to), calling f with each one's body
// position and size (the reader stands at the body).
func chunks(r io.ReadSeeker, from, to int64, f func(id string, at, size int64) error) error {
	be := binary.BigEndian
	for n := 0; from+12 <= to && n < maxDFFChunks; n++ {
		if _, err := r.Seek(from, io.SeekStart); err != nil {
			return err
		}
		var h [12]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return err
		}
		size := int64(be.Uint64(h[4:]))
		if size < 0 || from+12+size > to {
			size = to - from - 12
		}
		if err := f(string(h[:4]), from+12, size); err == errStop {
			return nil
		} else if err != nil {
			return err
		}
		from += 12 + size + size&1
	}
	return nil
}

// validIndex checks a DSTI index against the frames' own chunk headers.
func validIndex(r io.ReadSeeker, idx []DSTEntry) bool {
	if len(idx) == 0 {
		return false
	}
	for _, i := range []int{0, len(idx) / 2, len(idx) - 1} {
		e := idx[i]
		var h [12]byte
		if _, err := r.Seek(e.Offset-12, io.SeekStart); err != nil {
			return false
		}
		if _, err := io.ReadFull(r, h[:]); err != nil || string(h[:4]) != "DSTF" || int64(binary.BigEndian.Uint64(h[4:])) != e.Size {
			return false
		}
	}
	return true
}

// DFF is a plain DSDIFF file as a Stream.
type DFF struct {
	Info DFFInfo
	f    *os.File
	next int64
}

// OpenDFF opens a plain (uncompressed) DSDIFF file.
func OpenDFF(path string) (*DFF, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := ReadDFF(f)
	if err == nil && info.DST {
		err = errors.New("DSDIFF: DST-compressed")
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &DFF{Info: info, f: f}, nil
}

func (d *DFF) Rate() int     { return d.Info.Rate }
func (d *DFF) Channels() int { return d.Info.Channels }
func (d *DFF) Len() int64    { return d.Info.DataBytes / int64(d.Info.Channels) }
func (d *DFF) Close() error  { return d.f.Close() }

func (d *DFF) SeekFrame(n int64) error {
	if n < 0 || n > d.Len() {
		return fmt.Errorf("DSDIFF: seek to %d of %d", n, d.Len())
	}
	d.next = n
	return nil
}

func (d *DFF) Read(p []byte) (int, error) {
	ch := int64(d.Info.Channels)
	k := min(int64(len(p))/ch, d.Len()-d.next)
	if k <= 0 {
		return 0, io.EOF
	}
	n, err := d.f.ReadAt(p[:k*ch], d.Info.Data+d.next*ch)
	n -= n % int(ch)
	d.next += int64(n) / ch
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

// DFFDST is a DST-compressed DSDIFF file's frames.
type DFFDST struct {
	Info DFFInfo
	f    *os.File
	pos  int64 // next chunk to look at inside "DST "
	n    int64 // index of the next frame
}

// OpenDFFDST opens a DST-compressed DSDIFF file.
func OpenDFFDST(path string) (*DFFDST, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := ReadDFF(f)
	if err == nil && !info.DST {
		err = errors.New("DSDIFF: not DST-compressed")
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &DFFDST{Info: info, f: f, pos: info.Data}, nil
}

func (d *DFFDST) Rate() int     { return d.Info.Rate }
func (d *DFFDST) Channels() int { return d.Info.Channels }
func (d *DFFDST) Count() int64  { return d.Info.DSTFrames }
func (d *DFFDST) Close() error  { return d.f.Close() }

// maxDSTFrame bounds one coded frame (plain DSD of six channels is
// 28 KB a frame; DST only shrinks it).
const maxDSTFrame = 1 << 20

func (d *DFFDST) Next() ([]byte, error) {
	if d.Info.Index != nil {
		if d.n >= int64(len(d.Info.Index)) {
			return nil, io.EOF
		}
		e := d.Info.Index[d.n]
		if e.Size > maxDSTFrame {
			return nil, errors.New("DSDIFF: DST frame too large")
		}
		b := make([]byte, e.Size)
		if _, err := d.f.ReadAt(b, e.Offset); err != nil {
			return nil, err
		}
		d.n++
		return b, nil
	}
	end := d.Info.Data + d.Info.DataBytes
	for d.pos+12 <= end {
		var h [12]byte
		if _, err := d.f.ReadAt(h[:], d.pos); err != nil {
			return nil, err
		}
		size := int64(binary.BigEndian.Uint64(h[4:]))
		at := d.pos + 12
		d.pos = at + size + size&1
		if string(h[:4]) != "DSTF" {
			continue
		}
		if size > maxDSTFrame || at+size > end {
			return nil, errors.New("DSDIFF: bad DST frame")
		}
		b := make([]byte, size)
		if _, err := d.f.ReadAt(b, at); err != nil {
			return nil, err
		}
		d.n++
		return b, nil
	}
	return nil, io.EOF
}

func (d *DFFDST) SeekFrame(n int64) error {
	if n < 0 || n > d.Info.DSTFrames {
		return fmt.Errorf("DSDIFF: seek to frame %d of %d", n, d.Info.DSTFrames)
	}
	if d.Info.Index != nil {
		d.n = n
		return nil
	}
	if n < d.n {
		d.pos, d.n = d.Info.Data, 0
	}
	// Without an index: walk the chunk headers.
	end := d.Info.Data + d.Info.DataBytes
	for d.n < n && d.pos+12 <= end {
		var h [12]byte
		if _, err := d.f.ReadAt(h[:], d.pos); err != nil {
			return err
		}
		size := int64(binary.BigEndian.Uint64(h[4:]))
		d.pos += 12 + size + size&1
		if string(h[:4]) == "DSTF" {
			d.n++
		}
	}
	return nil
}
