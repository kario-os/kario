// Package bsdiff makes and applies binary patches with the method of Colin
// Percival's bsdiff ("Naive differences of executable code", 2003; BSD
// licence): exact matches found through a suffix array of the old data are
// stretched into approximate ones while at least half their bytes agree,
// and stored as byte-wise differences, which for code that only moved or
// had its addresses changed are mostly zeros and compress well; what
// matches nothing is stored as is. This implementation and its patch format
// are Kario's own.
//
// A patch is three streams: control (per step: the bytes to add from the
// diff stream to the old data, the bytes to take from the extra stream, and
// how far to move in the old data), diff and extra, each stored or
// xz-compressed.
package bsdiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/kario-os/kario/ota/delta/xz"
)

const magic = "KBSDIFF1"

// Delta is a patch before encoding.
type Delta struct {
	Ctrl, Diff, Extra []byte
	NewSize           int
}

// Make finds the patch from old to new.
func Make(old, new []byte) Delta {
	m := matcher{old: old, new: new, sa: suffixArray(old)}
	m.run()
	return Delta{Ctrl: m.ctrl, Diff: m.diff, Extra: m.extra, NewSize: len(new)}
}

type matcher struct {
	old, new []byte
	sa       []int32

	ctrl, diff, extra []byte
	// The previous approximate match ends at new[lastScan], old[lastPos];
	// lastOffset is old minus new position along it.
	lastScan, lastPos, lastOffset int
}

// longest is the old position whose suffix shares the longest prefix with
// b, and the length shared: a binary search of the suffix array narrows to
// the two suffixes around where b would sort.
func (m *matcher) longest(b []byte) (int, int) {
	if len(m.sa) == 0 {
		return 0, 0
	}
	lo, hi := 0, len(m.sa)-1
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if bytes.Compare(m.old[m.sa[mid]:], b) < 0 {
			lo = mid
		} else {
			hi = mid
		}
	}
	x, y := common(m.old[m.sa[lo]:], b), common(m.old[m.sa[hi]:], b)
	if x > y {
		return int(m.sa[lo]), x
	}
	return int(m.sa[hi]), y
}

func common(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// same: old and new agree at new position i shifted by off.
func (m *matcher) same(i, off int) bool {
	return i+off >= 0 && i+off < len(m.old) && m.old[i+off] == m.new[i]
}

func (m *matcher) run() {
	scan, pos, n := 0, 0, 0
	for scan < len(m.new) {
		// Move on to the next exact match that the previous match's offset
		// does not already explain (it would be no better as a new match).
		scan += n
		explained := 0
		for counted := scan; scan < len(m.new); scan++ {
			pos, n = m.longest(m.new[scan:])
			for ; counted < scan+n; counted++ {
				if m.same(counted, m.lastOffset) {
					explained++
				}
			}
			if (n == explained && n != 0) || n > explained+8 {
				break
			}
			if m.same(scan, m.lastOffset) {
				explained--
			}
		}
		if n == explained && scan != len(m.new) {
			continue
		}
		m.emit(scan, pos)
	}
}

// emit closes the previous match at the new one (new[scan], old[pos]):
// the previous one stretched forward and this one backward as long as half
// their bytes agree, what lies between as extra bytes.
func (m *matcher) emit(scan, pos int) {
	fwd := 0
	for i, agree, best := 0, 0, 0; m.lastScan+i < scan && m.lastPos+i < len(m.old); {
		if m.old[m.lastPos+i] == m.new[m.lastScan+i] {
			agree++
		}
		i++
		if agree*2-i > best*2-fwd {
			best, fwd = agree, i
		}
	}
	back := 0
	if scan < len(m.new) {
		for i, agree, best := 1, 0, 0; scan >= m.lastScan+i && pos >= i; i++ {
			if m.old[pos-i] == m.new[scan-i] {
				agree++
			}
			if agree*2-i > best*2-back {
				best, back = agree, i
			}
		}
	}
	// Where the two stretches overlap, split them where the agreements on
	// each side add up best.
	if over := m.lastScan + fwd - (scan - back); over > 0 {
		gain, best, cut := 0, 0, 0
		for i := 0; i < over; i++ {
			if m.new[m.lastScan+fwd-over+i] == m.old[m.lastPos+fwd-over+i] {
				gain++
			}
			if m.new[scan-back+i] == m.old[pos-back+i] {
				gain--
			}
			if gain > best {
				best, cut = gain, i+1
			}
		}
		fwd += cut - over
		back -= cut
	}
	for i := 0; i < fwd; i++ {
		m.diff = append(m.diff, m.new[m.lastScan+i]-m.old[m.lastPos+i])
	}
	extra := scan - back - (m.lastScan + fwd)
	m.extra = append(m.extra, m.new[m.lastScan+fwd:m.lastScan+fwd+extra]...)
	m.ctrl = binary.AppendUvarint(m.ctrl, uint64(fwd))
	m.ctrl = binary.AppendUvarint(m.ctrl, uint64(extra))
	m.ctrl = binary.AppendVarint(m.ctrl, int64(pos-back-(m.lastPos+fwd)))
	m.lastScan, m.lastPos, m.lastOffset = scan-back, pos-back, pos-scan
}

const (
	stored = 0
	xzed   = 1
)

// Encode packs the delta, each stream compressed by compress (nil: stored)
// when that makes it smaller.
func (d Delta) Encode(compress func([]byte) ([]byte, error)) ([]byte, error) {
	out := append([]byte(magic), make([]byte, 8+3*17)...)
	binary.LittleEndian.PutUint64(out[8:], uint64(d.NewSize))
	var body []byte
	for i, s := range [][]byte{d.Ctrl, d.Diff, d.Extra} {
		method, data := byte(stored), s
		if compress != nil && len(s) > 0 {
			c, err := compress(s)
			if err != nil {
				return nil, err
			}
			if len(c) < len(s) {
				method, data = xzed, c
			}
		}
		h := out[16+i*17:]
		h[0] = method
		binary.LittleEndian.PutUint64(h[1:], uint64(len(s)))
		binary.LittleEndian.PutUint64(h[9:], uint64(len(data)))
		body = append(body, data...)
	}
	return append(out, body...), nil
}

// Size is the size of the data the patch makes.
func Size(patch []byte) (int64, error) {
	if len(patch) < 16 || string(patch[:8]) != magic || int64(binary.LittleEndian.Uint64(patch[8:])) < 0 {
		return 0, errors.New("bsdiff: not a patch")
	}
	return int64(binary.LittleEndian.Uint64(patch[8:])), nil
}

// A patch applied through a reader holds oldWindows windows of the old
// data, each at most oldWindow bytes. A patch's steps read the old data
// forward from where their seeks put them, mostly near the step before,
// now and then far off and back (code that moved): a window read where
// one ends reads oldWindow ahead, one read elsewhere only what the step
// needs (at least oldBlock), so that far steps neither read much nor
// push out the window the next step comes back to.
const (
	oldWindow  = 256 << 10
	oldWindows = 4
	oldBlock   = 4 << 10
)

// oldData is the old data a patch applies to: in memory whole, or read
// through windows from a reader (ApplyAt, Patcher), so that a large file
// is never held whole.
type oldData struct {
	mem  []byte
	r    io.ReaderAt
	size int64
	win  [oldWindows]struct {
		at   int64
		b    []byte
		used int
	}
	uses int
}

func inMemory(old []byte) *oldData { return &oldData{mem: old, size: int64(len(old))} }

func through(r io.ReaderAt, size int64) *oldData { return &oldData{r: r, size: size} }

// get is up to n bytes of the old data from pos on, at least one; the
// caller keeps pos and n inside the data.
func (o *oldData) get(pos int64, n int) ([]byte, error) {
	if pos < 0 || pos >= o.size || n <= 0 {
		return nil, errors.New("bsdiff: patch reaches outside the data")
	}
	if o.r == nil {
		return o.mem[pos : pos+min(int64(n), o.size-pos)], nil
	}
	o.uses++
	pick, want := 0, max(oldBlock, min(n, oldWindow))
	for i := range o.win {
		w := &o.win[i]
		if pos >= w.at && pos < w.at+int64(len(w.b)) {
			w.used = o.uses
			b := w.b[pos-w.at:]
			return b[:min(n, len(b))], nil
		}
		if w.used < o.win[pick].used {
			pick = i
		}
	}
	for i := range o.win {
		if w := &o.win[i]; len(w.b) > 0 && w.at+int64(len(w.b)) == pos {
			pick, want = i, oldWindow
		}
	}
	w := &o.win[pick]
	if w.b == nil {
		w.b = make([]byte, 0, min(oldWindow, o.size))
	}
	b := w.b[:min(int64(want), int64(cap(w.b)), o.size-pos)]
	if k, err := o.r.ReadAt(b, pos); k < len(b) {
		w.b = w.b[:0]
		if err == nil || err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("bsdiff: reading the old data: %w", err)
	}
	w.at, w.b, w.used = pos, b, o.uses
	return b[:min(n, len(b))], nil
}

// add writes n bytes of diff added to the old data from pos on to w,
// through buf.
func (o *oldData) add(diff []byte, pos int64, n int, buf []byte, w io.Writer) error {
	for n > 0 {
		b, err := o.get(pos, min(n, len(buf)))
		if err != nil {
			return err
		}
		k := len(b)
		for i := range k {
			buf[i] = diff[i] + b[i]
		}
		if _, err := w.Write(buf[:k]); err != nil {
			return err
		}
		diff, pos, n = diff[k:], pos+int64(k), n-k
	}
	return nil
}

// Apply writes the data the patch makes from old to w.
func Apply(old, patch []byte, w io.Writer) error {
	return apply(inMemory(old), patch, w)
}

// ApplyAt is Apply with the old data, size bytes, read from old as the
// patch needs it.
func ApplyAt(old io.ReaderAt, size int64, patch []byte, w io.Writer) error {
	return apply(through(old, size), patch, w)
}

func apply(old *oldData, patch []byte, w io.Writer) error {
	size, err := Size(patch)
	if err != nil || len(patch) < 16+3*17 {
		return errors.New("bsdiff: not a patch")
	}
	var streams [3][]byte
	at := uint64(16 + 3*17)
	for i := range streams {
		h := patch[16+i*17:]
		raw, packed := binary.LittleEndian.Uint64(h[1:]), binary.LittleEndian.Uint64(h[9:])
		if packed > uint64(len(patch))-at || raw > uint64(size)+64<<20 {
			return errors.New("bsdiff: truncated patch")
		}
		data := patch[at : at+packed]
		at += packed
		switch {
		case h[0] == stored && raw == packed:
			streams[i] = data
		case h[0] == xzed:
			if streams[i], err = xz.Decode(data, int(raw)); err != nil {
				return fmt.Errorf("bsdiff: %w", err)
			}
		default:
			return errors.New("bsdiff: bad stream header")
		}
	}
	if at != uint64(len(patch)) {
		return errors.New("bsdiff: data after the patch")
	}
	ctrl, diff, extra := bytes.NewReader(streams[0]), streams[1], streams[2]
	buf := make([]byte, 64<<10)
	var made, oldPos int64
	for made < size {
		add, err1 := binary.ReadUvarint(ctrl)
		take, err2 := binary.ReadUvarint(ctrl)
		seek, err3 := binary.ReadVarint(ctrl)
		if err1 != nil || err2 != nil || err3 != nil {
			return errors.New("bsdiff: control stream ends early")
		}
		if add > uint64(size-made) || add > uint64(len(diff)) || oldPos < 0 || add > uint64(old.size-min(oldPos, old.size)) {
			return errors.New("bsdiff: patch reaches outside the data")
		}
		if err := old.add(diff, oldPos, int(add), buf, w); err != nil {
			return err
		}
		diff, oldPos = diff[add:], oldPos+int64(add)
		made += int64(add)
		if take > uint64(size-made) || take > uint64(len(extra)) {
			return errors.New("bsdiff: patch reaches outside the data")
		}
		if _, err := w.Write(extra[:take]); err != nil {
			return err
		}
		extra = extra[take:]
		made += int64(take)
		oldPos += seek
	}
	if ctrl.Len() != 0 || len(diff) != 0 || len(extra) != 0 {
		return errors.New("bsdiff: patch has data left over")
	}
	return nil
}
