package xz

import (
	"encoding/binary"
	"errors"
)

// The LZMA decoder of an LZMA2 filter (the LZMA specification, Igor Pavlov,
// public domain), decoding into one flat buffer: the output is the
// dictionary, so no window is kept apart from it.

var errCorrupt = errors.New("xz: corrupt data")

const (
	numStates    = 12
	probInit     = 1 << 10
	minMatchLen  = 2
	endPosModel  = 14
	numFullDists = 128
	alignBits    = 4
)

type prob uint16

type rangeDec struct {
	in   []byte
	pos  int
	rng  uint32
	code uint32
	past bool // a read beyond the chunk
}

func (r *rangeDec) init(in []byte) error {
	if len(in) < 5 || in[0] != 0 {
		return errCorrupt
	}
	*r = rangeDec{in: in, pos: 5, rng: 0xFFFFFFFF, code: binary.BigEndian.Uint32(in[1:5])}
	return nil
}

// fill takes the next input byte; a bit is decoded only with the range at
// 2^24 or more (normalize before, as liblzma's decoder).
func (r *rangeDec) fill() {
	var b byte
	if r.pos < len(r.in) {
		b = r.in[r.pos]
	} else {
		r.past = true
	}
	r.pos++
	r.rng <<= 8
	r.code = r.code<<8 | uint32(b)
}

func (r *rangeDec) bit(p *prob) uint32 {
	if r.rng < 1<<24 {
		r.fill()
	}
	bound := (r.rng >> 11) * uint32(*p)
	if r.code < bound {
		r.rng = bound
		*p += (1<<11 - *p) >> 5
		return 0
	}
	r.rng -= bound
	r.code -= bound
	*p -= *p >> 5
	return 1
}

func (r *rangeDec) direct(n uint) uint32 {
	var v uint32
	for ; n > 0; n-- {
		if r.rng < 1<<24 {
			r.fill()
		}
		r.rng >>= 1
		r.code -= r.rng
		t := 0 - (r.code >> 31)
		r.code += r.rng & t
		v = v<<1 + t + 1
	}
	return v
}

func (r *rangeDec) tree(probs []prob, bits uint) uint32 {
	m := uint32(1)
	for i := uint(0); i < bits; i++ {
		m = m<<1 | r.bit(&probs[m])
	}
	return m - 1<<bits
}

func (r *rangeDec) reverse(probs []prob, bits uint) uint32 {
	m, v := uint32(1), uint32(0)
	for i := uint(0); i < bits; i++ {
		b := r.bit(&probs[m])
		m = m<<1 | b
		v |= b << i
	}
	return v
}

type lenDec struct {
	choice, choice2 prob
	low, mid        [16][8]prob
	high            [256]prob
}

func (l *lenDec) decode(r *rangeDec, posState uint32) uint32 {
	if r.bit(&l.choice) == 0 {
		return r.tree(l.low[posState][:], 3)
	}
	if r.bit(&l.choice2) == 0 {
		return 8 + r.tree(l.mid[posState][:], 3)
	}
	return 16 + r.tree(l.high[:], 8)
}

type lzma struct {
	lc, lp, pb uint
	literal    []prob
	isMatch    [numStates << 4]prob
	isRep      [numStates]prob
	isRepG0    [numStates]prob
	isRepG1    [numStates]prob
	isRepG2    [numStates]prob
	isRep0Long [numStates << 4]prob
	posSlot    [4][64]prob
	posSpecial [1 + numFullDists - endPosModel]prob
	align      [1 << alignBits]prob
	matchLen   lenDec
	repLen     lenDec
	state      uint32
	rep        [4]uint32
}

// setProps takes an LZMA2 chunk's properties byte (lc + lp at most 4).
func (s *lzma) setProps(b byte) error {
	if b >= 9*5*5 {
		return errCorrupt
	}
	lc, lp, pb := uint(b%9), uint(b/9%5), uint(b/45)
	if lc+lp > 4 {
		return errCorrupt
	}
	s.lc, s.lp, s.pb = lc, lp, pb
	if n := 0x300 << (lc + lp); cap(s.literal) >= n {
		s.literal = s.literal[:n]
	} else {
		s.literal = make([]prob, n)
	}
	return nil
}

func fill(p []prob) {
	for i := range p {
		p[i] = probInit
	}
}

func (l *lenDec) reset() {
	l.choice, l.choice2 = probInit, probInit
	for i := range l.low {
		fill(l.low[i][:])
		fill(l.mid[i][:])
	}
	fill(l.high[:])
}

func (s *lzma) reset() {
	fill(s.literal)
	fill(s.isMatch[:])
	fill(s.isRep[:])
	fill(s.isRepG0[:])
	fill(s.isRepG1[:])
	fill(s.isRepG2[:])
	fill(s.isRep0Long[:])
	for i := range s.posSlot {
		fill(s.posSlot[i][:])
	}
	fill(s.posSpecial[:])
	fill(s.align[:])
	s.matchLen.reset()
	s.repLen.reset()
	s.state = 0
	s.rep = [4]uint32{}
}

func (s *lzma) distance(r *rangeDec, length uint32) uint32 {
	slot := r.tree(s.posSlot[min(length, 3)][:], 6)
	if slot < 4 {
		return slot
	}
	n := uint(slot>>1) - 1
	dist := (2 | slot&1) << n
	if slot < endPosModel {
		return dist + r.reverse(s.posSpecial[dist-slot:], n)
	}
	dist += r.direct(n-alignBits) << alignBits
	return dist + r.reverse(s.align[:], alignBits)
}

// out is the decoded data so far; start is where the dictionary was last
// reset (matches reach no further back); limit is the size expected.
type out struct {
	b     []byte
	start int
	limit int
}

// chunk decodes one LZMA chunk: unpacked bytes from all of packed.
func (s *lzma) chunk(o *out, packed []byte, unpacked int) error {
	var r rangeDec
	if err := r.init(packed); err != nil {
		return err
	}
	end := len(o.b) + unpacked
	if end > o.limit {
		return errors.New("xz: more data than expected")
	}
	pbMask := uint32(1)<<s.pb - 1
	lpMask := uint32(1)<<s.lp - 1
	for len(o.b) < end {
		pos := uint32(len(o.b) - o.start)
		posState := pos & pbMask
		st := s.state
		if r.bit(&s.isMatch[st<<4|posState]) == 0 {
			var prev uint32
			if pos > 0 {
				prev = uint32(o.b[len(o.b)-1])
			}
			lit := s.literal[0x300*((pos&lpMask)<<s.lc+prev>>(8-s.lc)):]
			sym := uint32(1)
			if st >= 7 {
				if s.rep[0] >= pos {
					return errCorrupt
				}
				match := uint32(o.b[len(o.b)-int(s.rep[0])-1])
				for sym < 0x100 {
					mb := match >> 7 & 1
					match <<= 1
					b := r.bit(&lit[(1+mb)<<8+sym])
					sym = sym<<1 | b
					if mb != b {
						break
					}
				}
			}
			for sym < 0x100 {
				sym = sym<<1 | r.bit(&lit[sym])
			}
			o.b = append(o.b, byte(sym))
			switch {
			case st < 4:
				s.state = 0
			case st < 10:
				s.state = st - 3
			default:
				s.state = st - 6
			}
			continue
		}
		var length uint32
		if r.bit(&s.isRep[st]) != 0 {
			if pos == 0 {
				return errCorrupt
			}
			if r.bit(&s.isRepG0[st]) == 0 {
				if r.bit(&s.isRep0Long[st<<4|posState]) == 0 {
					if s.rep[0] >= pos {
						return errCorrupt
					}
					s.state = 9
					if st >= 7 {
						s.state = 11
					}
					o.b = append(o.b, o.b[len(o.b)-int(s.rep[0])-1])
					continue
				}
			} else {
				var dist uint32
				if r.bit(&s.isRepG1[st]) == 0 {
					dist = s.rep[1]
				} else {
					if r.bit(&s.isRepG2[st]) == 0 {
						dist = s.rep[2]
					} else {
						dist = s.rep[3]
						s.rep[3] = s.rep[2]
					}
					s.rep[2] = s.rep[1]
				}
				s.rep[1] = s.rep[0]
				s.rep[0] = dist
			}
			length = s.repLen.decode(&r, posState)
			s.state = 8
			if st >= 7 {
				s.state = 11
			}
		} else {
			s.rep[3], s.rep[2], s.rep[1] = s.rep[2], s.rep[1], s.rep[0]
			length = s.matchLen.decode(&r, posState)
			s.state = 7
			if st >= 7 {
				s.state = 10
			}
			s.rep[0] = s.distance(&r, length)
		}
		if s.rep[0] >= pos {
			return errCorrupt
		}
		n := int(length) + minMatchLen
		if len(o.b)+n > end {
			return errCorrupt
		}
		from := len(o.b) - int(s.rep[0]) - 1
		if from+n <= len(o.b) {
			o.b = append(o.b, o.b[from:from+n]...)
		} else {
			for i := 0; i < n; i++ {
				o.b = append(o.b, o.b[from+i])
			}
		}
	}
	if r.rng < 1<<24 {
		r.fill()
	}
	if r.past || r.pos != len(packed) || r.code != 0 {
		return errCorrupt
	}
	return nil
}

// lzma2 decodes LZMA2 chunks up to their end marker and says how many
// bytes of in they took.
func lzma2(in []byte, o *out) (int, error) {
	var s lzma
	needDictReset, needProps := true, true
	p := 0
	for {
		if p >= len(in) {
			return 0, errCorrupt
		}
		c := in[p]
		p++
		if c == 0 {
			return p, nil
		}
		if c >= 0xE0 || c == 0x01 {
			needProps, needDictReset = true, false
			o.start = len(o.b)
		} else if needDictReset {
			return 0, errCorrupt
		}
		if c < 0x80 {
			if c > 0x02 || p+2 > len(in) {
				return 0, errCorrupt
			}
			n := int(in[p])<<8 | int(in[p+1]) + 1
			p += 2
			if p+n > len(in) || len(o.b)+n > o.limit {
				return 0, errCorrupt
			}
			o.b = append(o.b, in[p:p+n]...)
			p += n
			continue
		}
		if p+4 > len(in) {
			return 0, errCorrupt
		}
		unpacked := int(c&0x1F)<<16 | int(in[p])<<8 | int(in[p+1]) + 1
		packed := int(in[p+2])<<8 | int(in[p+3]) + 1
		p += 4
		switch {
		case c >= 0xC0:
			if p >= len(in) {
				return 0, errCorrupt
			}
			if err := s.setProps(in[p]); err != nil {
				return 0, err
			}
			p++
			needProps = false
			s.reset()
		case needProps:
			return 0, errCorrupt
		case c >= 0xA0:
			s.reset()
		}
		if p+packed > len(in) {
			return 0, errCorrupt
		}
		if err := s.chunk(o, in[p:p+packed], unpacked); err != nil {
			return 0, err
		}
		p += packed
	}
}
