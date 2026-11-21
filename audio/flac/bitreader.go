package flac

import (
	"io"
	"math/bits"
)

// bitReader reads MSB-first bits from a buffered stream. acc holds the
// pending bits left-aligned; the bytes they came from stay in buf (more
// keeps the last 8), so unread can give them back for byte-level work
// (finding a frame header).
type bitReader struct {
	r    io.Reader
	buf  []byte
	i, n int   // next byte, valid bytes
	base int64 // file offset of buf[0]
	acc  uint64
	nacc uint
	err  error
}

const bufSize = 64 << 10

func (b *bitReader) reset(r io.Reader, off int64) {
	if b.buf == nil {
		b.buf = make([]byte, bufSize)
	}
	b.r, b.i, b.n, b.base, b.acc, b.nacc, b.err = r, 0, 0, off, 0, 0, nil
}

// more reads the next chunk, keeping the last 8 bytes of the old one.
func (b *bitReader) more() bool {
	keep := min(8, b.n)
	copy(b.buf, b.buf[b.n-keep:b.n])
	b.base += int64(b.n - keep)
	b.i, b.n = keep, keep
	k, _ := io.ReadAtLeast(b.r, b.buf[keep:], 1)
	b.n += k
	return k > 0
}

func (b *bitReader) fill() {
	for b.nacc <= 56 {
		if b.i >= b.n && !b.more() {
			return
		}
		b.acc |= uint64(b.buf[b.i]) << (56 - b.nacc)
		b.i++
		b.nacc += 8
	}
}

// bits reads n <= 57 bits.
func (b *bitReader) bits(n uint) uint64 {
	if n == 0 {
		return 0
	}
	if b.nacc < n {
		b.fill()
		if b.nacc < n {
			if b.err == nil {
				b.err = io.ErrUnexpectedEOF
			}
			return 0
		}
	}
	v := b.acc >> (64 - n)
	b.acc <<= n
	b.nacc -= n
	return v
}

// sbits reads an n-bit two's complement number.
func (b *bitReader) sbits(n uint) int64 {
	v := b.bits(n)
	return int64(v<<(64-n)) >> (64 - n)
}

// unary counts zero bits up to the next one bit.
func (b *bitReader) unary() uint {
	n := uint(0)
	for {
		if b.nacc == 0 {
			b.fill()
			if b.nacc == 0 {
				if b.err == nil {
					b.err = io.ErrUnexpectedEOF
				}
				return n
			}
		}
		z := uint(bits.LeadingZeros64(b.acc))
		if z < b.nacc {
			b.acc <<= z + 1
			b.nacc -= z + 1
			return n + z
		}
		n += b.nacc
		b.acc, b.nacc = 0, 0
	}
}

func (b *bitReader) align() {
	r := b.nacc % 8
	b.acc <<= r
	b.nacc -= r
}

// unread hands the whole bytes still in acc back to buf.
func (b *bitReader) unread() {
	b.align()
	b.i -= int(b.nacc / 8)
	b.acc, b.nacc = 0, 0
}

// ensure makes buf[i:i+m] valid if the stream has that much (acc empty).
func (b *bitReader) ensure(m int) bool {
	if b.n-b.i >= m {
		return true
	}
	copy(b.buf, b.buf[b.i:b.n])
	b.base += int64(b.i)
	b.n -= b.i
	b.i = 0
	for b.n < m {
		k, err := b.r.Read(b.buf[b.n:])
		b.n += k
		if k == 0 && err != nil {
			break
		}
	}
	return b.n-b.i >= m
}
