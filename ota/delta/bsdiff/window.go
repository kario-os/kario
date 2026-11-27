package bsdiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/kario-os/kario/ota/delta/xz"
)

// Pieces: a patch cut into pieces that each make a fixed number of bytes
// of the new data (the last fewer), so that it applies as it streams in,
// one piece in memory at a time, and an install can stop and resume
// between two pieces. A piece needs nothing of the others: it says where in
// the old data it starts, and carries its own control, diff and extra
// streams, each stored or xz-compressed as in a whole patch:
//
//	"KBSDPCE1" | old position (int64 LE) | 3 × (method, raw u32 LE, packed u32 LE) | the streams

const pieceMagic = "KBSDPCE1"

const pieceHeader = 8 + 8 + 3*9

// maxPos bounds positions and moves in the old data, far past any file, so
// that a damaged piece cannot overflow them.
const maxPos = 1 << 40

// MaxPiece bounds the new data one piece makes (what the phone holds of
// it at a time).
const MaxPiece = 16 << 20

// maxCtrl bounds a piece's control stream: a step makes at least one byte
// and takes at most 30 bytes to encode, but real patches have a step per
// stretch of matching code, far fewer.
func maxCtrl(want int) int { return 2*want + 1<<20 }

type pieceBuilder struct {
	compress          func([]byte) ([]byte, error)
	out               [][]byte
	open              bool
	old               int64
	made              int
	ctrl, diff, extra []byte
}

func (b *pieceBuilder) step(add, take int, seek int64, diff, extra []byte, oldPos int64) {
	if !b.open {
		b.open, b.old, b.made = true, oldPos, 0
	}
	b.ctrl = binary.AppendUvarint(b.ctrl, uint64(add))
	b.ctrl = binary.AppendUvarint(b.ctrl, uint64(take))
	b.ctrl = binary.AppendVarint(b.ctrl, seek)
	b.diff = append(b.diff, diff...)
	b.extra = append(b.extra, extra...)
	b.made += add + take
}

func (b *pieceBuilder) flush() error {
	if !b.open {
		return nil
	}
	p := make([]byte, pieceHeader, pieceHeader+len(b.diff)+len(b.extra))
	copy(p, pieceMagic)
	binary.LittleEndian.PutUint64(p[8:], uint64(b.old))
	for i, s := range [][]byte{b.ctrl, b.diff, b.extra} {
		method, data := byte(stored), s
		if b.compress != nil && len(s) > 0 {
			c, err := b.compress(s)
			if err != nil {
				return err
			}
			if len(c) < len(s) {
				method, data = xzed, c
			}
		}
		h := p[16+i*9:]
		h[0] = method
		binary.LittleEndian.PutUint32(h[1:], uint32(len(s)))
		binary.LittleEndian.PutUint32(h[5:], uint32(len(data)))
		p = append(p, data...)
	}
	b.out = append(b.out, p)
	b.open, b.ctrl, b.diff, b.extra = false, nil, nil, nil
	return nil
}

// Pieces cuts the delta into pieces of window bytes of new data each (the
// last fewer; none for no new data), each encoded as Encode encodes a
// patch's streams.
func (d Delta) Pieces(window int, compress func([]byte) ([]byte, error)) ([][]byte, error) {
	if window <= 0 || window > MaxPiece {
		return nil, fmt.Errorf("bsdiff: pieces of %d bytes", window)
	}
	b := &pieceBuilder{compress: compress}
	ctrl, diff, extra := bytes.NewReader(d.Ctrl), d.Diff, d.Extra
	var oldPos int64
	for ctrl.Len() > 0 {
		add64, err1 := binary.ReadUvarint(ctrl)
		take64, err2 := binary.ReadUvarint(ctrl)
		seek, err3 := binary.ReadVarint(ctrl)
		if err1 != nil || err2 != nil || err3 != nil || add64 > uint64(len(diff)) || take64 > uint64(len(extra)) {
			return nil, errors.New("bsdiff: a damaged delta")
		}
		add, take := int(add64), int(take64)
		if add == 0 && take == 0 {
			if b.open {
				b.step(0, 0, seek, nil, nil, oldPos)
			}
			oldPos += seek
			continue
		}
		for add > 0 || take > 0 {
			room := window
			if b.open {
				room -= b.made
			}
			ka := min(add, room)
			kt := min(take, room-ka)
			var s int64
			last := ka == add && kt == take
			if last {
				s = seek
			}
			b.step(ka, kt, s, diff[:ka], extra[:kt], oldPos)
			diff, extra = diff[ka:], extra[kt:]
			oldPos += int64(ka)
			add, take = add-ka, take-kt
			if last {
				oldPos += seek
			}
			if b.made == window {
				if err := b.flush(); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := b.flush(); err != nil {
		return nil, err
	}
	return b.out, nil
}

// ApplyPiece writes the want bytes of new data that piece makes from old
// to w. Nothing is written for a piece that is malformed before its steps
// start; a piece whose steps go wrong fails part way, and its result's
// hash (the payload's) catches anything else.
func ApplyPiece(old, piece []byte, want int, w io.Writer) error {
	return (&Patcher{old: inMemory(old)}).Apply(piece, want, w)
}

// Patcher applies a patch's pieces one after another, the old data read
// as they need it, and keeps for the next piece what one decodes and its
// window of the old data: applying a file's pieces holds one piece's
// streams and the window, whatever the size of the file.
type Patcher struct {
	old     *oldData
	reuse   bool
	decoded [3][]byte
	buf     []byte
}

// NewPatcher is a Patcher of the old data, size bytes, read from old. The
// streams of the pieces it applies are decoded into buffers of the sizes
// their headers give, which the caller vouches for (the payload's hash of
// each piece).
func NewPatcher(old io.ReaderAt, size int64) *Patcher {
	return &Patcher{old: through(old, size), reuse: true}
}

// Apply writes the want bytes of new data that piece makes to w, as
// ApplyPiece does.
func (p *Patcher) Apply(piece []byte, want int, w io.Writer) error {
	old := p.old
	if len(piece) < pieceHeader || string(piece[:8]) != pieceMagic || want < 0 || want > MaxPiece {
		return errors.New("bsdiff: not a patch piece")
	}
	oldPos := int64(binary.LittleEndian.Uint64(piece[8:]))
	if oldPos < -maxPos || oldPos > maxPos {
		return errors.New("bsdiff: piece reaches outside the data")
	}
	var streams [3][]byte
	limits := [3]int{maxCtrl(want), want, want}
	at := pieceHeader
	for i := range streams {
		h := piece[16+i*9:]
		raw, packed := int(binary.LittleEndian.Uint32(h[1:])), int(binary.LittleEndian.Uint32(h[5:]))
		if packed > len(piece)-at || raw > limits[i] {
			return errors.New("bsdiff: a truncated or oversized piece")
		}
		data := piece[at : at+packed]
		at += packed
		switch {
		case h[0] == stored && raw == packed:
			streams[i] = data
		case h[0] == xzed:
			var dst []byte
			if p.reuse {
				dst = slices.Grow(p.decoded[i][:0], raw)
			}
			var err error
			if streams[i], err = xz.DecodeTo(dst, data, raw); err != nil {
				return fmt.Errorf("bsdiff: %w", err)
			}
			if p.reuse {
				p.decoded[i] = streams[i]
			}
		default:
			return errors.New("bsdiff: bad stream header")
		}
	}
	if at != len(piece) {
		return errors.New("bsdiff: data after the piece")
	}
	ctrl, diff, extra := bytes.NewReader(streams[0]), streams[1], streams[2]
	if len(p.buf) < min(want, 64<<10) {
		p.buf = make([]byte, min(want, 64<<10))
	}
	buf := p.buf
	made := 0
	for ctrl.Len() > 0 {
		add, err1 := binary.ReadUvarint(ctrl)
		take, err2 := binary.ReadUvarint(ctrl)
		seek, err3 := binary.ReadVarint(ctrl)
		if err1 != nil || err2 != nil || err3 != nil {
			return errors.New("bsdiff: control stream ends early")
		}
		if add > uint64(want-made) || add > uint64(len(diff)) || add > 0 && (oldPos < 0 || oldPos > old.size || add > uint64(old.size-oldPos)) {
			return errors.New("bsdiff: piece reaches outside the data")
		}
		if err := old.add(diff, oldPos, int(add), buf, w); err != nil {
			return err
		}
		diff, oldPos = diff[add:], oldPos+int64(add)
		made += int(add)
		if take > uint64(want-made) || take > uint64(len(extra)) {
			return errors.New("bsdiff: piece reaches outside the data")
		}
		if _, err := w.Write(extra[:take]); err != nil {
			return err
		}
		extra = extra[take:]
		made += int(take)
		if seek < -maxPos || seek > maxPos {
			return errors.New("bsdiff: piece reaches outside the data")
		}
		if oldPos += seek; oldPos < -maxPos || oldPos > maxPos {
			return errors.New("bsdiff: piece reaches outside the data")
		}
	}
	if made != want || len(diff) != 0 || len(extra) != 0 {
		return errors.New("bsdiff: piece makes the wrong amount or has data left over")
	}
	return nil
}
