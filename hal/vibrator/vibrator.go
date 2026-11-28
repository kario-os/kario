// Package vibrator drives the vibration motor. Android kernels expose it as
// the LED-class "vibrator" (a duration, then activate); mainline kernels as
// an input device with force feedback (FF_RUMBLE: the PMIC haptics,
// gpio-vibrator and regulator-haptic drivers), played by uploading an
// effect (EVIOCSFF) and writing an EV_FF event.
package vibrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/kario-os/kario/hal"
)

// Motor is one vibration motor.
type Motor interface {
	Vibrate(d time.Duration) error
	Name() string
}

// DevRoot is where the input device nodes are (tests point it elsewhere).
var DevRoot = "/dev"

// Open is the machine's motor: the LED-class vibrator, else the first input
// device that can rumble; nil when there is none.
func Open() Motor {
	if l := OpenLED("vibrator"); l != nil {
		return l
	}
	if r := OpenRumble(); r != nil {
		return r
	}
	return nil
}

// LED is the LED-class motor of Android kernels.
type LED struct{ name string }

// OpenLED is the LED-class motor called name; nil when there is none.
func OpenLED(name string) *LED {
	if _, err := os.Stat(filepath.Join(hal.SysRoot, "class", "leds", name, "activate")); err != nil {
		return nil
	}
	return &LED{name: name}
}

func (l *LED) Name() string { return "led:" + l.name }

func (l *LED) Vibrate(d time.Duration) error {
	dir := filepath.Join(hal.SysRoot, "class", "leds", l.name)
	if err := hal.WriteAttr(filepath.Join(dir, "duration"), []byte(strconv.Itoa(int(d.Milliseconds())))); err != nil {
		return err
	}
	return hal.WriteAttr(filepath.Join(dir, "activate"), []byte("1"))
}

// The input subsystem's force feedback (linux/input.h).
const (
	evFF      = 0x15
	ffRumble  = 0x50
	iocWrite  = 1
	maxLength = 0xffff // replay.length is a __u16 of milliseconds
)

// ffPeriodic is struct ff_periodic_effect, the largest member of
// ff_effect's union: its pointer gives the union the C alignment. A rumble
// effect uses its first two fields as strong_magnitude, weak_magnitude.
type ffPeriodic struct {
	Waveform, Period  uint16
	Magnitude, Offset int16
	Phase             uint16
	Envelope          [4]uint16
	CustomLen         uint32
	CustomData        uintptr
}

// ffEffect is struct ff_effect.
type ffEffect struct {
	Type      uint16
	ID        int16
	Direction uint16
	Trigger   [2]uint16
	Replay    [2]uint16 // length, delay (ms)
	U         ffPeriodic
}

// inputEvent is struct input_event (a timeval of two longs).
type inputEvent struct {
	Sec, Usec int
	Type      uint16
	Code      uint16
	Value     int32
}

func ioc(dir, typ, nr, size uintptr) uintptr { return dir<<30 | size<<16 | typ<<8 | nr }

var eviocsff = ioc(iocWrite, 'E', 0x80, unsafe.Sizeof(ffEffect{}))

// hasRumble reads FF_RUMBLE from a capabilities/ff bitmask: hex words of
// the kernel's long, the most significant first.
func hasRumble(mask string, wordBits int) bool {
	words := strings.Fields(mask)
	i := len(words) - 1 - ffRumble/wordBits
	if i < 0 {
		return false
	}
	w, err := strconv.ParseUint(words[i], 16, 64)
	return err == nil && w&(1<<(ffRumble%wordBits)) != 0
}

// Rumble is a force-feedback input device.
type Rumble struct {
	node string
	mu   sync.Mutex
	f    *os.File
	id   int16
}

// OpenRumble is the first input device that can rumble; nil when there is
// none. Its node is opened on the first Vibrate.
func OpenRumble() *Rumble {
	ents, _ := os.ReadDir(filepath.Join(hal.SysRoot, "class", "input"))
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "event") {
			continue
		}
		raw, err := hal.ReadAttr(filepath.Join(hal.SysRoot, "class", "input", e.Name(), "device", "capabilities", "ff"))
		if err == nil && hasRumble(string(raw), strconv.IntSize) {
			return &Rumble{node: filepath.Join(DevRoot, "input", e.Name()), id: -1}
		}
	}
	return nil
}

func (r *Rumble) Name() string { return "rumble:" + r.node }

func (r *Rumble) Vibrate(d time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		f, err := os.OpenFile(r.node, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		r.f = f
	}
	ms := min(max(d.Milliseconds(), 1), maxLength)
	eff := ffEffect{Type: ffRumble, ID: r.id, Replay: [2]uint16{uint16(ms), 0}}
	eff.U.Waveform, eff.U.Period = 0xffff, 0xffff
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, r.f.Fd(), eviocsff, uintptr(unsafe.Pointer(&eff))); errno != 0 {
		r.f.Close()
		r.f, r.id = nil, -1
		return fmt.Errorf("%s: uploading the effect: %w", r.node, errno)
	}
	r.id = eff.ID
	ev := inputEvent{Type: evFF, Code: uint16(r.id), Value: 1}
	_, err := r.f.Write(unsafe.Slice((*byte)(unsafe.Pointer(&ev)), unsafe.Sizeof(ev)))
	return err
}

// Sim is a motor that only counts.
type Sim struct {
	mu    sync.Mutex
	n     int
	total time.Duration
}

func (s *Sim) Name() string { return "sim" }

func (s *Sim) Vibrate(d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	s.total += d
	return nil
}

// Count is how many times it vibrated and for how long in all.
func (s *Sim) Count() (int, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n, s.total
}
