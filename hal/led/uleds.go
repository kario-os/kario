package led

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"time"
)

// Change is a brightness the kernel set on a userspace LED, and when.
type Change struct {
	At         time.Time
	Brightness int
}

// User is a userspace LED (uleds, CONFIG_LEDS_USER): a real LED class
// device whose brightness changes come to this process instead of to a
// driver. The emulator has no LEDs; the selftest gives it the board's
// notification LED this way and watches the timer trigger blink it.
type User struct {
	f       *os.File
	Changes chan Change
}

const uledsMaxName = 64

// NewUser creates the LED; it exists while the returned User is open.
func NewUser(name string, maxBrightness int) (*User, error) {
	f, err := os.OpenFile("/dev/uleds", os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoUleds
	}
	if err != nil {
		return nil, err
	}
	// struct uleds_user_dev { char name[LED_MAX_NAME_SIZE]; int max_brightness; }
	dev := make([]byte, uledsMaxName+4)
	copy(dev[:uledsMaxName-1], name)
	binary.NativeEndian.PutUint32(dev[uledsMaxName:], uint32(maxBrightness))
	if _, err := f.Write(dev); err != nil {
		f.Close()
		return nil, err
	}
	u := &User{f: f, Changes: make(chan Change, 64)}
	go u.read()
	return u, nil
}

func (u *User) read() {
	defer close(u.Changes)
	buf := make([]byte, 4)
	for {
		if _, err := io.ReadFull(u.f, buf); err != nil {
			return
		}
		c := Change{At: time.Now(), Brightness: int(int32(binary.NativeEndian.Uint32(buf)))}
		select {
		case u.Changes <- c:
		default:
		}
	}
}

// Close removes the LED.
func (u *User) Close() error { return u.f.Close() }
