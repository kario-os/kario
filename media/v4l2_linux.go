package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// A V4L2 camera (Video4Linux2, what every Linux camera driver speaks):
// YUYV frames from memory-mapped driver buffers, the latest kept for the
// camera stream. On the emulator the kernel's vivid driver (a virtual
// camera with test patterns) stands in for the sensor; a phone's camera
// pipeline offers the same interface.
//
// The ioctl structures are written out for 64-bit kernels (arm64 and
// x86-64 lay them out alike).

const (
	vidiocQuerycap = 0x80685600
	vidiocSFmt     = 0xc0d05605
	vidiocReqbufs  = 0xc0145608
	vidiocQuerybuf = 0xc0585609
	vidiocQbuf     = 0xc058560f
	vidiocDqbuf    = 0xc0585611
	vidiocStreamon = 0x40045612

	bufTypeCapture = 1
	memoryMmap     = 1
	capCapture     = 0x00000001
	capStreaming   = 0x04000000
	capDeviceCaps  = 0x80000000
	fourccYUYV     = 'Y' | 'U'<<8 | 'Y'<<16 | 'V'<<24
)

type V4L2 struct {
	Driver, Card string
	fd           int
	w, h, stride int
	bufs         [][]byte
	mu           sync.Mutex
	latest       []byte
	frames       int
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// OpenCamera starts capturing from dev at about w×h (the driver picks the
// nearest size it supports).
func OpenCamera(dev string, w, h int) (*V4L2, error) {
	fd, err := syscall.Open(dev, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	c := &V4L2{fd: fd}
	if err := c.setup(w, h); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("%s: %w", dev, err)
	}
	go c.loop()
	return c, nil
}

func (c *V4L2) setup(w, h int) error {
	var capb [104]byte
	if err := ioctl(c.fd, vidiocQuerycap, unsafe.Pointer(&capb[0])); err != nil {
		return fmt.Errorf("not a V4L2 device: %w", err)
	}
	c.Driver, c.Card = cstr(capb[0:16]), cstr(capb[16:48])
	caps := binary.LittleEndian.Uint32(capb[84:])
	if caps&capDeviceCaps != 0 {
		caps = binary.LittleEndian.Uint32(capb[88:])
	}
	if caps&capCapture == 0 || caps&capStreaming == 0 {
		return errors.New("no streaming video capture")
	}
	// struct v4l2_format: type, then the pix format at offset 8.
	var f [208]byte
	binary.LittleEndian.PutUint32(f[0:], bufTypeCapture)
	binary.LittleEndian.PutUint32(f[8:], uint32(w))
	binary.LittleEndian.PutUint32(f[12:], uint32(h))
	binary.LittleEndian.PutUint32(f[16:], fourccYUYV)
	binary.LittleEndian.PutUint32(f[20:], 1) // V4L2_FIELD_NONE
	if err := ioctl(c.fd, vidiocSFmt, unsafe.Pointer(&f[0])); err != nil {
		return fmt.Errorf("set format: %w", err)
	}
	if binary.LittleEndian.Uint32(f[16:]) != fourccYUYV {
		return errors.New("the camera does not offer YUYV")
	}
	c.w, c.h = int(binary.LittleEndian.Uint32(f[8:])), int(binary.LittleEndian.Uint32(f[12:]))
	c.stride = int(binary.LittleEndian.Uint32(f[24:]))
	if c.stride < 2*c.w {
		c.stride = 2 * c.w
	}
	// struct v4l2_requestbuffers: count, type, memory, ...
	var rb [20]byte
	binary.LittleEndian.PutUint32(rb[0:], 3)
	binary.LittleEndian.PutUint32(rb[4:], bufTypeCapture)
	binary.LittleEndian.PutUint32(rb[8:], memoryMmap)
	if err := ioctl(c.fd, vidiocReqbufs, unsafe.Pointer(&rb[0])); err != nil {
		return fmt.Errorf("request buffers: %w", err)
	}
	n := int(binary.LittleEndian.Uint32(rb[0:]))
	if n == 0 {
		return errors.New("no capture buffers")
	}
	for i := 0; i < n; i++ {
		b := c.buf(i)
		if err := ioctl(c.fd, vidiocQuerybuf, unsafe.Pointer(&b[0])); err != nil {
			return fmt.Errorf("query buffer: %w", err)
		}
		// m.offset at 64, length at 72.
		off, length := binary.LittleEndian.Uint32(b[64:]), binary.LittleEndian.Uint32(b[72:])
		mem, err := syscall.Mmap(c.fd, int64(off), int(length), syscall.PROT_READ, syscall.MAP_SHARED)
		if err != nil {
			return fmt.Errorf("map buffer: %w", err)
		}
		c.bufs = append(c.bufs, mem)
		if err := ioctl(c.fd, vidiocQbuf, unsafe.Pointer(&b[0])); err != nil {
			return fmt.Errorf("queue buffer: %w", err)
		}
	}
	t := uint32(bufTypeCapture)
	return ioctl(c.fd, vidiocStreamon, unsafe.Pointer(&t))
}

// buf is a struct v4l2_buffer (88 bytes) for buffer i.
func (c *V4L2) buf(i int) []byte {
	b := make([]byte, 88)
	binary.LittleEndian.PutUint32(b[0:], uint32(i))
	binary.LittleEndian.PutUint32(b[4:], bufTypeCapture)
	binary.LittleEndian.PutUint32(b[60:], memoryMmap)
	return b
}

func (c *V4L2) loop() {
	for {
		b := c.buf(0)
		if err := ioctl(c.fd, vidiocDqbuf, unsafe.Pointer(&b[0])); err != nil {
			fmt.Fprintln(os.Stderr, "camera:", err)
			return
		}
		i := int(binary.LittleEndian.Uint32(b[0:]))
		used := int(binary.LittleEndian.Uint32(b[8:]))
		if i < len(c.bufs) {
			c.mu.Lock()
			c.latest = append(c.latest[:0], c.bufs[i][:min(used, len(c.bufs[i]))]...)
			c.frames++
			c.mu.Unlock()
		}
		if err := ioctl(c.fd, vidiocQbuf, unsafe.Pointer(&b[0])); err != nil {
			return
		}
	}
}

// Frames counts the frames captured so far.
func (c *V4L2) Frames() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frames
}

// Source draws the latest captured frame, scaled to the frame asked for.
func (c *V4L2) Source() Source {
	return func(_ float64, f *Frame) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.latest) < c.stride*c.h {
			Off(0, f)
			return
		}
		for y := 0; y < f.H; y++ {
			sy := y * c.h / f.H
			row := c.latest[sy*c.stride:]
			for x := 0; x < f.W; x++ {
				sx := x * c.w / f.W
				p := row[(sx&^1)*2:]
				yy := p[0]
				if sx&1 == 1 {
					yy = p[2]
				}
				r, g, b := yuvToRGB(yy, p[1], p[3])
				f.Set(x, y, r, g, b)
			}
		}
	}
}

func yuvToRGB(y, u, v byte) (uint8, uint8, uint8) {
	c, d, e := float64(y)-16, float64(u)-128, float64(v)-128
	return clamp(1.164*c + 1.596*e), clamp(1.164*c - 0.392*d - 0.813*e), clamp(1.164*c + 2.017*d)
}
