// Package media is the media plane: frames flow from the media service to the
// shell's compositor directly and never through app UI messages. The prototype
// uses RGB over a Unix socket; on hardware this is a dmabuf handed to a DRM plane.
package media

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
)

type Frame struct {
	W, H int
	Seq  uint32
	Pix  []byte
}

func NewFrame(w, h int) *Frame { return &Frame{W: w, H: h, Pix: make([]byte, w*h*3)} }

func (f *Frame) Set(x, y int, r, g, b uint8) {
	if x < 0 || y < 0 || x >= f.W || y >= f.H {
		return
	}
	i := (y*f.W + x) * 3
	f.Pix[i], f.Pix[i+1], f.Pix[i+2] = r, g, b
}

func (f *Frame) At(x, y int) (uint8, uint8, uint8) {
	if x < 0 || y < 0 || x >= f.W || y >= f.H {
		return 0, 0, 0
	}
	i := (y*f.W + x) * 3
	return f.Pix[i], f.Pix[i+1], f.Pix[i+2]
}

func WriteFrame(w io.Writer, f *Frame) error {
	var hdr [12]byte
	binary.BigEndian.PutUint32(hdr[0:], uint32(8+len(f.Pix)))
	binary.BigEndian.PutUint16(hdr[4:], uint16(f.W))
	binary.BigEndian.PutUint16(hdr[6:], uint16(f.H))
	binary.BigEndian.PutUint32(hdr[8:], f.Seq)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(f.Pix)
	return err
}

func ReadFrame(r io.Reader) (*Frame, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(hdr[0:])) - 8
	f := &Frame{
		W:   int(binary.BigEndian.Uint16(hdr[4:])),
		H:   int(binary.BigEndian.Uint16(hdr[6:])),
		Seq: binary.BigEndian.Uint32(hdr[8:]),
	}
	if n != f.W*f.H*3 || n < 0 || n > 16<<20 {
		return nil, fmt.Errorf("bad frame size %d for %dx%d", n, f.W, f.H)
	}
	f.Pix = make([]byte, n)
	_, err := io.ReadFull(r, f.Pix)
	return f, err
}

type SubscribeReq struct {
	T      string `json:"t"`
	Stream string `json:"stream"`
	W      int    `json:"w"`
	H      int    `json:"h"`
}

// Sub is a live subscription to one stream at a fixed size. The latest frame
// wins; the compositor never queues stale frames.
type Sub struct {
	Stream string
	W, H   int
	conn   net.Conn
	mu     sync.Mutex
	last   *Frame
	frames int
	closed bool
	Err    error
}

func Subscribe(sock, stream string, w, h int, onFrame func()) (*Sub, error) {
	nc, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(SubscribeReq{T: "subscribe", Stream: stream, W: w, H: h})
	if _, err := nc.Write(append(b, '\n')); err != nil {
		nc.Close()
		return nil, err
	}
	s := &Sub{Stream: stream, W: w, H: h, conn: nc}
	go func() {
		br := bufio.NewReaderSize(nc, 64<<10)
		for {
			f, err := ReadFrame(br)
			s.mu.Lock()
			if err != nil {
				s.Err = err
				s.closed = true
				s.mu.Unlock()
				return
			}
			s.last = f
			s.frames++
			s.mu.Unlock()
			if onFrame != nil {
				onFrame()
			}
		}
	}()
	return s, nil
}

func (s *Sub) Latest() (*Frame, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.frames
}

func (s *Sub) Ended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Sub) Close() { _ = s.conn.Close() }
