package audiod

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The 3.5 mm jack: the sound card's jack detection (a codec's MBHC) is an
// input device with switches, "<card> Headset Jack".
const (
	evSyn         = 0
	evSW          = 5
	swHeadphone   = 0x02
	swMicrophone  = 0x04
	swLineout     = 0x06
	inputEventLen = 24 // struct input_event on a 64-bit kernel
)

// jackState is what is in the jack.
type jackState struct{ phones, lineout, mic bool }

func (j jackState) in() bool { return j.phones || j.lineout }

// findJack is the input device that reports headphones in a jack: its
// sysfs switch capabilities have SW_HEADPHONE_INSERT. sys is
// /sys/class/input; "" when there is none.
func findJack(sys string) (event, name string) {
	dirs, _ := filepath.Glob(filepath.Join(sys, "event[0-9]*"))
	for _, d := range dirs {
		raw, err := os.ReadFile(filepath.Join(d, "device", "capabilities", "sw"))
		if err != nil {
			continue
		}
		words := strings.Fields(string(raw))
		if len(words) == 0 {
			continue
		}
		low, err := strconv.ParseUint(words[len(words)-1], 16, 64)
		if err != nil || low&(1<<swHeadphone) == 0 {
			continue
		}
		n, _ := os.ReadFile(filepath.Join(d, "device", "name"))
		return filepath.Base(d), strings.TrimSpace(string(n))
	}
	return "", ""
}

// jackSwitches reads the switches' present state (EVIOCGSW).
func jackSwitches(f *os.File) (jackState, error) {
	var bits [8]byte
	req := uintptr(2<<30 | len(bits)<<16 | 'E'<<8 | 0x1b)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(&bits[0]))); e != 0 {
		return jackState{}, e
	}
	on := func(sw int) bool { return bits[sw/8]&(1<<(sw%8)) != 0 }
	return jackState{phones: on(swHeadphone), lineout: on(swLineout), mic: on(swMicrophone)}, nil
}

// jackEvents reads the jack's events until r ends, telling each change
// once its report is complete (EV_SYN).
func jackEvents(r io.Reader, st jackState, tell func(jackState)) error {
	buf := make([]byte, inputEventLen)
	cur := st
	le := binary.LittleEndian
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		typ, code, val := le.Uint16(buf[16:]), le.Uint16(buf[18:]), int32(le.Uint32(buf[20:]))
		switch typ {
		case evSW:
			switch code {
			case swHeadphone:
				cur.phones = val != 0
			case swLineout:
				cur.lineout = val != 0
			case swMicrophone:
				cur.mic = val != 0
			}
		case evSyn:
			if cur != st {
				st = cur
				tell(st)
			}
		}
	}
}

// watchJack follows the jack, if the phone has one, telling o.
func watchJack(sys, dev string, o *outputs) {
	event, name := findJack(sys)
	if event == "" {
		return
	}
	f, err := os.Open(filepath.Join(dev, event))
	if err != nil {
		o.logf("phone-audiod: jack %s: %v", name, err)
		return
	}
	defer f.Close()
	st, err := jackSwitches(f)
	if err != nil {
		o.logf("phone-audiod: jack %s: %v", name, err)
	}
	o.logf("phone-audiod: jack %s (%s)", name, event)
	o.jack(st)
	if err := jackEvents(f, st, o.jack); err != nil {
		o.logf("phone-audiod: jack %s: %v", name, err)
	}
}
