// Package input finds the kernel's input devices by the keys they report
// and reads their key events (evdev), and a touchscreen's finger
// (touch.go). The keys a person presses and the touches reach the Shell
// through the renderer, whose input helper opens every keyboard, button
// device and touchscreen (paint/src/input.rs); this is for checking a
// board's own buttons and touchscreen where no renderer reads them: the
// selftest's button and touchscreen steps.
package input

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/hal"
)

// DevRoot is where the event device nodes are (input/eventN); tests point
// it at a directory of their own.
var DevRoot = "/dev"

// KeyPower is the kernel's KEY_POWER.
const KeyPower = 116

const evKey = 1

// WithKey is the event device nodes whose device reports the key code,
// in the order of their names.
func WithKey(code int) []string {
	caps, _ := filepath.Glob(filepath.Join(hal.SysRoot, "class", "input", "event*", "device", "capabilities", "key"))
	var out []string
	for _, p := range caps {
		raw, err := os.ReadFile(p)
		if err != nil || !hasBit(string(raw), code) {
			continue
		}
		event := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(p))))
		out = append(out, filepath.Join(DevRoot, "input", event))
	}
	sort.Strings(out)
	return out
}

// hasBit reads a sysfs bitmap: hexadecimal words of the kernel's long, the
// most significant first, its leading zero words left out.
func hasBit(bitmap string, bit int) bool {
	words := strings.Fields(bitmap)
	size := strconv.IntSize
	i := len(words) - 1 - bit/size
	if bit < 0 || i < 0 {
		return false
	}
	w, err := strconv.ParseUint(words[i], 16, size)
	return err == nil && w&(1<<(bit%size)) != 0
}

// A Key is a key going down (Down) or up; auto-repeats are left out.
type Key struct {
	Code int
	Down bool
}

// Watcher reads one event device's keys into C until Close.
type Watcher struct {
	f *os.File
	C chan Key
}

// Watch opens an event device node and reads its keys.
func Watch(node string) (*Watcher, error) {
	f, err := os.Open(node)
	if err != nil {
		return nil, err
	}
	w := &Watcher{f: f, C: make(chan Key, 16)}
	go w.read()
	return w, nil
}

// eventSize is struct input_event: a struct timeval (two longs), then the
// type and code (16 bits each) and the value (32 bits).
const eventSize = 2*strconv.IntSize/8 + 8

func (w *Watcher) read() {
	defer close(w.C)
	buf := make([]byte, 64*eventSize)
	for {
		n, err := w.f.Read(buf)
		for off := 0; off+eventSize <= n; off += eventSize {
			if k, ok := parse(buf[off : off+eventSize]); ok {
				select {
				case w.C <- k:
				default:
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func parse(ev []byte) (Key, bool) {
	at := len(ev) - 8
	typ, code := binary.NativeEndian.Uint16(ev[at:]), binary.NativeEndian.Uint16(ev[at+2:])
	value := int32(binary.NativeEndian.Uint32(ev[at+4:]))
	if typ != evKey || value > 1 {
		return Key{}, false
	}
	return Key{Code: int(code), Down: value == 1}, true
}

// keyMax is the kernel's KEY_MAX: EVIOCGKEY fills a bit for each code up
// to it.
const keyMax = 0x2ff

// Down is whether the kernel has the key down now (EVIOCGKEY): the key's
// state for a reader that may have missed its release.
func (w *Watcher) Down(code int) (bool, error) {
	if code < 0 || code > keyMax {
		return false, errors.New("no such key")
	}
	var bits [keyMax/8 + 1]byte
	// _IOC(_IOC_READ, 'E', 0x18, len): the same on every architecture Kario runs on.
	req := uintptr(2)<<30 | uintptr(len(bits))<<16 | uintptr('E')<<8 | 0x18
	raw, err := w.f.SyscallConn()
	if err != nil {
		return false, err
	}
	var errno unix.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&bits[0])))
	}); err != nil {
		return false, err
	}
	if errno != 0 {
		return false, errno
	}
	return bits[code/8]&(1<<(code%8)) != 0, nil
}

// Close stops reading; C is closed once the reader has returned.
func (w *Watcher) Close() error {
	err := w.f.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}
