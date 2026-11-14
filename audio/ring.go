package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// RingBuf is a single-producer, single-consumer PCM ring (48 kHz mono, signed
// 16-bit) in a sealed memfd shared by an app and phone-audiod. The shell
// hands the fd to the app once (SCM_RIGHTS); after that the samples flow
// between the two processes without passing through the shell.
//
// Layout (little endian):
//
//	0   magic "PRNG"
//	4   version (1)
//	8   rate
//	12  capacity in frames
//	16  write position (u64, frames ever written; the producer's)
//	24  read position (u64, frames ever read; the consumer's)
//	32  flags (u32): 1 = the producer has finished
//	64  samples
//
// Neither side trusts the other's position: indices are taken modulo the
// capacity, so a misbehaving peer can garble its own audio but not make the
// other side read or write out of bounds. The memfd is sealed against
// shrinking, so a peer cannot truncate it under the other's mapping.
type RingBuf struct {
	mem  []byte
	data []int16
	cap  uint64
}

const (
	ringHeader  = 64
	ringMagic   = 0x474e5250
	ringEOF     = 1
	maxRingSecs = 10
)

// NewRingFile creates a sealed ring of capFrames frames.
func NewRingFile(name string, capFrames int) (*os.File, error) {
	if capFrames <= 0 || capFrames > Rate*maxRingSecs {
		return nil, fmt.Errorf("ring of %d frames", capFrames)
	}
	fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	size := ringHeader + 2*capFrames
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		f.Close()
		return nil, err
	}
	mem, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, err
	}
	binary.LittleEndian.PutUint32(mem[0:], ringMagic)
	binary.LittleEndian.PutUint32(mem[4:], 1)
	binary.LittleEndian.PutUint32(mem[8:], Rate)
	binary.LittleEndian.PutUint32(mem[12:], uint32(capFrames))
	_ = unix.Munmap(mem)
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_SEAL); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// MapRing maps a ring received from a peer, refusing one that could be
// resized under the mapping.
func MapRing(f *os.File) (*RingBuf, error) {
	seals, err := unix.FcntlInt(f.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&unix.F_SEAL_SHRINK == 0 {
		return nil, errors.New("audio ring is not sealed against shrinking")
	}
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size < ringHeader+2 || size > ringHeader+2*Rate*maxRingSecs {
		return nil, fmt.Errorf("audio ring of %d bytes", size)
	}
	mem, err := unix.Mmap(int(f.Fd()), 0, int(size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	frames := uint64(binary.LittleEndian.Uint32(mem[12:]))
	if binary.LittleEndian.Uint32(mem[0:]) != ringMagic || binary.LittleEndian.Uint32(mem[4:]) != 1 ||
		ringHeader+2*frames != uint64(size) {
		_ = unix.Munmap(mem)
		return nil, errors.New("not an audio ring")
	}
	return &RingBuf{mem: mem, cap: frames, data: unsafe.Slice((*int16)(unsafe.Pointer(&mem[ringHeader])), frames)}, nil
}

func (r *RingBuf) wpos() *uint64  { return (*uint64)(unsafe.Pointer(&r.mem[16])) }
func (r *RingBuf) rpos() *uint64  { return (*uint64)(unsafe.Pointer(&r.mem[24])) }
func (r *RingBuf) flags() *uint32 { return (*uint32)(unsafe.Pointer(&r.mem[32])) }

// Cap is the capacity in frames.
func (r *RingBuf) Cap() int { return int(r.cap) }

// Buffered is how many frames wait for the consumer.
func (r *RingBuf) Buffered() int {
	return int(min(atomic.LoadUint64(r.wpos())-atomic.LoadUint64(r.rpos()), r.cap))
}

// Write (producer) copies as many samples as fit and returns that count.
func (r *RingBuf) Write(p []int16) int {
	w, rd := atomic.LoadUint64(r.wpos()), atomic.LoadUint64(r.rpos())
	free := r.cap - min(w-rd, r.cap)
	n := min(uint64(len(p)), free)
	for i := uint64(0); i < n; i++ {
		r.data[(w+i)%r.cap] = p[i]
	}
	atomic.StoreUint64(r.wpos(), w+n)
	return int(n)
}

// Read (consumer) copies up to len(p) samples and returns that count.
func (r *RingBuf) Read(p []int16) int {
	w, rd := atomic.LoadUint64(r.wpos()), atomic.LoadUint64(r.rpos())
	if w-rd > r.cap {
		rd = w - r.cap
	}
	n := min(uint64(len(p)), w-rd)
	for i := uint64(0); i < n; i++ {
		p[i] = r.data[(rd+i)%r.cap]
	}
	atomic.StoreUint64(r.rpos(), rd+n)
	return int(n)
}

// Finish marks the end of the stream (producer).
func (r *RingBuf) Finish()        { atomic.StoreUint32(r.flags(), ringEOF) }
func (r *RingBuf) Finished() bool { return atomic.LoadUint32(r.flags())&ringEOF != 0 }

func (r *RingBuf) Close() error {
	if r.mem == nil {
		return nil
	}
	err := unix.Munmap(r.mem)
	r.mem, r.data = nil, nil
	return err
}

// RingSource plays what an app writes into a ring; it ends once the app has
// finished and everything was played. Gaps (the app writing too slowly)
// play as silence.
type RingSource struct {
	r   *RingBuf
	tmp []int16
	pos int
}

func NewRingSource(r *RingBuf) *RingSource { return &RingSource{r: r} }

func (s *RingSource) Fill(buf []float64) bool {
	if cap(s.tmp) < len(buf) {
		s.tmp = make([]int16, len(buf))
	}
	n := s.r.Read(s.tmp[:len(buf)])
	for i := range buf {
		if i < n {
			buf[i] = float64(s.tmp[i]) / 32768
		} else {
			buf[i] = 0
		}
	}
	s.pos += len(buf)
	return n > 0 || !s.r.Finished()
}

func (s *RingSource) Pos() int     { return s.pos }
func (s *RingSource) Len() int     { return 0 }
func (s *RingSource) Close() error { return s.r.Close() }

// Mic is the simulated microphone: "speech" as alternating 365 Hz and
// 475 Hz syllables. Neither is a system tone or a note of the demo tracks
// (which use natural notes only), so a recording can be recognised wherever
// it is played back.
type Mic struct{ n int }

func (m *Mic) Next(out []int16) {
	const syl = Rate * 3 / 10
	for i := range out {
		k := m.n % (2 * syl)
		f, on := 365.0, k < syl*6/10
		if k >= syl {
			f, on = 475.0, k-syl < syl*7/10
		}
		v := 0.0
		if on {
			v = 0.35 * math.Sin(2*math.Pi*f*float64(m.n)/Rate)
		}
		out[i] = int16(v * 32767)
		m.n++
	}
}
