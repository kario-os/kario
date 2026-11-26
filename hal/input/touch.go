package input

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/hal"
)

// The kernel's codes a touchscreen's events have.
const (
	evSyn         = 0
	evAbs         = 3
	synReport     = 0
	synDropped    = 3
	btnTouch      = 0x14a
	absX          = 0x00
	absY          = 0x01
	absMTSlot     = 0x2f
	absMTX        = 0x35
	absMTY        = 0x36
	absMTTracking = 0x39
	propDirect    = 1
)

// Touchscreens is the event device nodes of touchscreens: devices whose
// axes are the screen's (INPUT_PROP_DIRECT) with a position, of
// multitouch slots or of one contact, in the order of their names.
func Touchscreens() []string {
	props, _ := filepath.Glob(filepath.Join(hal.SysRoot, "class", "input", "event*", "device", "properties"))
	var out []string
	for _, p := range props {
		raw, err := os.ReadFile(p)
		if err != nil || !hasBit(string(raw), propDirect) {
			continue
		}
		abs, err := os.ReadFile(filepath.Join(filepath.Dir(p), "capabilities", "abs"))
		if err != nil || !hasBit(string(abs), absMTX) && !hasBit(string(abs), absX) {
			continue
		}
		event := filepath.Base(filepath.Dir(filepath.Dir(p)))
		out = append(out, filepath.Join(DevRoot, "input", event))
	}
	sort.Strings(out)
	return out
}

// Phase is what the finger followed did.
type Phase int

const (
	Down Phase = iota
	Move
	Up
	// Cancel: events were lost with the finger down.
	Cancel
)

func (p Phase) String() string { return [...]string{"down", "move", "up", "cancel"}[p] }

// A Contact is the finger followed at a report of the device, in its
// units (Range).
type Contact struct {
	Phase Phase
	X, Y  int32
}

// Range is an axis's span, as the kernel's absinfo says.
type Range struct{ Min, Max int32 }

// Contacts follows a touchscreen's fingers as the renderer does
// (paint/src/touch.rs): the multitouch protocol's slots, each with a
// tracking id that is -1 once lifted, or one contact (ABS_X, ABS_Y,
// BTN_TOUCH) on a device without slots; the first finger down while none
// is followed, until it lifts, the others left alone (and those still
// down after it).
type Contacts struct {
	Multi    bool
	slot     int
	slots    [16]slot
	single   slot
	followed int // the slot followed, -1 for none
	id       int32
	at       [2]int32
	others   bool
	dropped  bool
	started  bool
}

type slot struct{ id, x, y int32 }

func (c *Contacts) init() {
	if c.started {
		return
	}
	c.started, c.followed = true, -1
	for i := range c.slots {
		c.slots[i].id = -1
	}
	c.single.id = -1
}

// Event takes one of the device's events; at a report, what the finger
// followed did, if anything.
func (c *Contacts) Event(typ, code uint16, value int32) (Contact, bool) {
	c.init()
	switch {
	case typ == evSyn && code == synDropped:
		c.dropped = true
	case typ == evSyn && code == synReport:
		return c.report()
	case c.dropped:
	case typ == evKey && code == btnTouch && !c.Multi:
		c.single.id = map[bool]int32{true: 0, false: -1}[value != 0]
	case typ == evAbs && !c.Multi && code == absX:
		c.single.x = value
	case typ == evAbs && !c.Multi && code == absY:
		c.single.y = value
	case typ == evAbs && c.Multi && code == absMTSlot:
		c.slot = int(value)
	case typ == evAbs && c.Multi && c.slot >= 0 && c.slot < len(c.slots):
		s := &c.slots[c.slot]
		switch code {
		case absMTTracking:
			s.id = value
		case absMTX:
			s.x = value
		case absMTY:
			s.y = value
		}
	}
	return Contact{}, false
}

func (c *Contacts) report() (Contact, bool) {
	slots := c.slots[:]
	if !c.Multi {
		slots = []slot{c.single}
	}
	anyDown := func() bool {
		for _, s := range slots {
			if s.id >= 0 {
				return true
			}
		}
		return false
	}
	if c.dropped {
		c.dropped, c.others = false, false
		for i := range c.slots {
			c.slots[i].id = -1
		}
		c.single.id = -1
		if c.followed >= 0 {
			c.followed = -1
			return Contact{Cancel, c.at[0], c.at[1]}, true
		}
		return Contact{}, false
	}
	if c.followed >= 0 {
		s := slots[c.followed]
		if s.id != c.id {
			c.followed, c.others = -1, anyDown()
			return Contact{Up, c.at[0], c.at[1]}, true
		}
		if [2]int32{s.x, s.y} == c.at {
			return Contact{}, false
		}
		c.at = [2]int32{s.x, s.y}
		return Contact{Move, s.x, s.y}, true
	}
	if c.others {
		c.others = anyDown()
		return Contact{}, false
	}
	for i, s := range slots {
		if s.id >= 0 {
			c.followed, c.id, c.at = i, s.id, [2]int32{s.x, s.y}
			return Contact{Down, s.x, s.y}, true
		}
	}
	return Contact{}, false
}

// TouchWatcher reads one touchscreen's finger into C until Close; X and Y
// are its axes' spans.
type TouchWatcher struct {
	f    *os.File
	C    chan Contact
	X, Y Range
}

// WatchTouch opens a touchscreen's event device node and follows its
// finger.
func WatchTouch(node string) (*TouchWatcher, error) {
	f, err := os.Open(node)
	if err != nil {
		return nil, err
	}
	multi := false
	if caps, err := os.ReadFile(filepath.Join(hal.SysRoot, "class", "input", filepath.Base(node), "device", "capabilities", "abs")); err == nil {
		multi = hasBit(strings.TrimSpace(string(caps)), absMTX)
	}
	ax, ay := uint16(absX), uint16(absY)
	if multi {
		ax, ay = absMTX, absMTY
	}
	w := &TouchWatcher{f: f, C: make(chan Contact, 64)}
	if w.X, err = absRange(f, ax); err == nil {
		w.Y, err = absRange(f, ay)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	go w.read(&Contacts{Multi: multi})
	return w, nil
}

func (w *TouchWatcher) read(c *Contacts) {
	defer close(w.C)
	buf := make([]byte, 64*eventSize)
	for {
		n, err := w.f.Read(buf)
		for off := 0; off+eventSize <= n; off += eventSize {
			ev := buf[off : off+eventSize]
			at := len(ev) - 8
			typ, code := binary.NativeEndian.Uint16(ev[at:]), binary.NativeEndian.Uint16(ev[at+2:])
			if ct, ok := c.Event(typ, code, int32(binary.NativeEndian.Uint32(ev[at+4:]))); ok {
				select {
				case w.C <- ct:
				default:
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// absRange asks the kernel for an axis's span (EVIOCGABS: struct
// input_absinfo, six 32-bit values: value, minimum, maximum, fuzz, flat,
// resolution).
func absRange(f *os.File, axis uint16) (Range, error) {
	var info [6]int32
	// _IOR('E', 0x40 + axis, struct input_absinfo): the same on every
	// architecture Kario runs on.
	req := uintptr(2)<<30 | uintptr(unsafe.Sizeof(info))<<16 | uintptr('E')<<8 | uintptr(0x40+axis)
	raw, err := f.SyscallConn()
	if err != nil {
		return Range{}, err
	}
	var errno unix.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&info[0])))
	}); err != nil {
		return Range{}, err
	}
	if errno != 0 {
		return Range{}, errno
	}
	return Range{info[1], info[2]}, nil
}

// Close stops reading; C is closed once the reader has returned.
func (w *TouchWatcher) Close() error { return w.f.Close() }
