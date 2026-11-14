package media

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// A Surface is a stream's frames in shared memory (a memfd), so pixels go
// from the media service to the renderer without passing through the
// shell, which forwards the fd once and then only a sequence number per
// frame. Two slots: the writer fills the one not shown, then flips.
//
//	header (32 bytes, little endian): "PHSURF1\x00", width, height,
//	latest slot (0 or 1), the two slots' frame numbers
//	slot 0, slot 1: width × height × 3 bytes of RGB
//
// A reader takes the latest slot and checks its frame number did not change
// while it copied (the writer would have to flip twice in that time).
const (
	SurfaceMagic  = "PHSURF1\x00"
	surfaceHeader = 32
)

type Surface struct {
	W, H int
	file *os.File
	mem  []byte
}

func surfaceSize(w, h int) int { return surfaceHeader + 2*w*h*3 }

// NewSurface makes a w×h surface (the writer's side).
func NewSurface(w, h int) (*Surface, error) {
	if w <= 0 || h <= 0 || w*h > 1920*1080 {
		return nil, fmt.Errorf("bad surface size %dx%d", w, h)
	}
	fd, err := unix.MemfdCreate("phone-surface", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "phone-surface")
	if err := f.Truncate(int64(surfaceSize(w, h))); err != nil {
		f.Close()
		return nil, err
	}
	// The size is fixed from now on, so a reader's mapping stays valid.
	_, _ = unix.FcntlInt(f.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_SEAL)
	mem, err := unix.Mmap(int(f.Fd()), 0, surfaceSize(w, h), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, err
	}
	copy(mem, SurfaceMagic)
	binary.LittleEndian.PutUint32(mem[8:], uint32(w))
	binary.LittleEndian.PutUint32(mem[12:], uint32(h))
	return &Surface{W: w, H: h, file: f, mem: mem}, nil
}

// File is the memfd to hand to readers.
func (s *Surface) File() *os.File { return s.file }

func (s *Surface) word(at int) *uint32 { return (*uint32)(unsafe.Pointer(&s.mem[at])) }

// Publish writes a frame into the slot not shown and makes it the latest.
func (s *Surface) Publish(f *Frame) {
	if f.W != s.W || f.H != s.H {
		return
	}
	next := 1 - atomic.LoadUint32(s.word(16))
	copy(s.mem[surfaceHeader+int(next)*s.W*s.H*3:], f.Pix)
	atomic.StoreUint32(s.word(20+4*int(next)), f.Seq)
	atomic.StoreUint32(s.word(16), next)
}

func (s *Surface) Close() {
	_ = unix.Munmap(s.mem)
	_ = s.file.Close()
}

// OpenSurface maps a surface someone handed over (the reader's side).
func OpenSurface(f *os.File) (*Surface, error) {
	st, err := f.Stat()
	if err != nil || st.Size() < surfaceHeader {
		return nil, errors.New("not a surface")
	}
	mem, err := unix.Mmap(int(f.Fd()), 0, int(st.Size()), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	w, h := int(binary.LittleEndian.Uint32(mem[8:])), int(binary.LittleEndian.Uint32(mem[12:]))
	if string(mem[:8]) != SurfaceMagic || surfaceSize(w, h) > len(mem) {
		_ = unix.Munmap(mem)
		return nil, errors.New("not a surface")
	}
	return &Surface{W: w, H: h, file: f, mem: mem}, nil
}

// Latest copies the newest complete frame out.
func (s *Surface) Latest() *Frame {
	for try := 0; try < 3; try++ {
		slot := atomic.LoadUint32(s.word(16)) & 1
		seq := atomic.LoadUint32(s.word(20 + 4*int(slot)))
		f := NewFrame(s.W, s.H)
		copy(f.Pix, s.mem[surfaceHeader+int(slot)*s.W*s.H*3:])
		if atomic.LoadUint32(s.word(20+4*int(slot))) == seq {
			f.Seq = seq
			return f
		}
	}
	return nil
}

// SurfaceSub is a subscription by surface: the fd once, then a frame
// number per frame (the pixels are in the shared memory).
type SurfaceSub struct {
	Stream string
	W, H   int
	File   *os.File
	conn   *net.UnixConn
	mu     sync.Mutex
	seq    uint32
	frames int
	closed bool
}

func SubscribeSurface(sock, stream string, w, h int, onFrame func()) (*SurfaceSub, error) {
	nc, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(SubscribeReq{T: "surface", Stream: stream, W: w, H: h})
	if _, err := nc.Write(append(b, '\n')); err != nil {
		nc.Close()
		return nil, err
	}
	buf, oob := make([]byte, 64), make([]byte, unix.CmsgSpace(4))
	n, oobn, _, _, err := nc.ReadMsgUnix(buf, oob)
	if err != nil || n == 0 {
		nc.Close()
		return nil, fmt.Errorf("surface: %v", err)
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	var fds []int
	if err == nil && len(msgs) == 1 {
		fds, err = unix.ParseUnixRights(&msgs[0])
	}
	if err != nil || len(fds) != 1 {
		nc.Close()
		return nil, errors.New("surface: no fd from the media service")
	}
	s := &SurfaceSub{Stream: stream, W: w, H: h, File: os.NewFile(uintptr(fds[0]), "surface:"+stream), conn: nc}
	go func() {
		br := bufio.NewReader(nc)
		var b [4]byte
		for {
			if _, err := io.ReadFull(br, b[:]); err != nil {
				s.mu.Lock()
				s.closed = true
				s.mu.Unlock()
				return
			}
			s.mu.Lock()
			s.seq = binary.LittleEndian.Uint32(b[:])
			s.frames++
			s.mu.Unlock()
			if onFrame != nil {
				onFrame()
			}
		}
	}()
	return s, nil
}

// Seq is the latest frame's number and how many frames came.
func (s *SurfaceSub) Seq() (uint32, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq, s.frames
}

func (s *SurfaceSub) Ended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *SurfaceSub) Close() {
	_ = s.conn.Close()
	_ = s.File.Close()
}

// ServeSurface is the media service's side: it sends the surface's fd,
// then for each frame publishes it and sends its number.
func ServeSurface(nc *net.UnixConn, surf *Surface) error {
	_, _, err := nc.WriteMsgUnix([]byte("surface"), unix.UnixRights(int(surf.File().Fd())), nil)
	return err
}

// Notify tells the subscriber a frame is in the surface.
func Notify(w io.Writer, seq uint32) error {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], seq)
	_, err := w.Write(b[:])
	return err
}
