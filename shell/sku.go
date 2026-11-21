package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kario-os/kario/hal/led"
)

// SKU is the hardware variant the image was built for (24-sku-framework.md):
// share/phone/sku.json, written by the SKU generator. Without one (a
// developer machine, the default QEMU image) the zero value applies and
// the command line chooses.
type SKU struct {
	ID    string `json:"id"`
	Board string `json:"board"`
	// Device is the form factor (Devices); Width and Height the panel in
	// physical pixels as mounted, 0 for the form factor's own; Scale its
	// physical pixels per logical one, 0 for the form factor's (a whole
	// phone's dense panel, 24-sku-framework.md §7.4).
	Device   string  `json:"device"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	Scale    float64 `json:"scale,omitempty"`
	Display  string  `json:"display"`
	Keypad   string  `json:"keypad"`
	MemoryMB int     `json:"memory_mb"`
	// WebGPU lets pages render on the GPU (its render node opened in their
	// sandbox) where the board has one.
	WebGPU bool `json:"web_gpu,omitempty"`
	// Modem is telephonyd's backend: sim, modemmanager, android-radio, vendor.
	Modem   string `json:"modem"`
	Region  string `json:"region"`
	Product string `json:"product"`
	// Fn is a full keyboard's second legends: the character typed with Fn
	// held, by the key's own character.
	Fn map[string]string `json:"fn,omitempty"`
	// Ctrl is how a keyboard without a Ctrl key presses it (Device.Ctrl).
	Ctrl string `json:"ctrl,omitempty"`
	// LEDs and Backlight name the board's notification light and panel
	// backlight (hal); empty where the board has none.
	LEDs struct {
		Notify led.Config `json:"notify"`
	} `json:"leds,omitempty"`
	Backlight string `json:"backlight,omitempty"`
	// KeyboardBacklight is the keyboard's backlight LED; empty: the first
	// LED named <device>::kbd_backlight, if any.
	KeyboardBacklight string `json:"keyboard_backlight,omitempty"`
	// LightSensor is the ambient light sensor's IIO name; empty: the first
	// IIO device that measures illuminance, if any.
	LightSensor string `json:"light_sensor,omitempty"`
	// Fingerprint is keystored's sensor backend (hal/fingerprint): "sim" on
	// the emulator, empty where the board has none it can drive.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Suspend: the board can be woken from suspend (its keys, RTC and modem
	// are wake-up sources); not the emulator, which nothing wakes.
	Suspend bool `json:"suspend,omitempty"`
	// PowerKey: the board has a power key of its own (a side key); without
	// one the end key is the power key too, the Nokia way.
	PowerKey bool `json:"power_key,omitempty"`
	// NFC is phone-nfcd's controller: "sim" on the emulator, "nci:<device>"
	// for a board's NCI controller, empty where the board has none.
	NFC string `json:"nfc,omitempty"`
	// GNSS is the location receiver (location.ParseSpec): "nmea:<device>"
	// (the kernel's /dev/gnssN, a serial port), "modemmanager" (the
	// modem's GPS), "fifo:<name>" (a named pipe in the run directory: the
	// emulator's, which the selftest writes NMEA into). Empty: a phone
	// without one, or on the developer machine (no id) the simulated
	// receiver.
	GNSS string `json:"gnss,omitempty"`
	// Keymap is a whole phone's own keypad (a KaiOS phone's matrix, its
	// device tree's codes): the input code the kernel reports for a key ->
	// the key's name in sku/keys.toml (Remap).
	Keymap map[string]string `json:"keymap,omitempty"`
}

// keyCodes are sku/keys.toml's keys and the input codes phone-paint reads
// them by (tools/sku_test.py compares).
var keyCodes = map[string]int{
	"soft-left": 59, "soft-right": 60, "ok": 28, "back": 1, "clear": 14, "call": 63, "end": 62,
	"up": 103, "down": 108, "left": 105, "right": 106, "ime": 64, "sym": 65,
	"vol-up": 115, "vol-down": 114, "power": 116,
	"0": 512, "1": 513, "2": 514, "3": 515, "4": 516, "5": 517, "6": 518, "7": 519, "8": 520, "9": 521,
	"star": 522, "pound": 523,
	"a": 30, "b": 48, "c": 46, "d": 32, "e": 18, "f": 33, "g": 34, "h": 35, "i": 23, "j": 36, "k": 37, "l": 38, "m": 50,
	"n": 49, "o": 24, "p": 25, "q": 16, "r": 19, "s": 31, "t": 20, "u": 22, "v": 47, "w": 17, "x": 45, "y": 21, "z": 44,
	"space": 57, "comma": 51, "dot": 52, "shift": 42, "ctrl": 29, "fn": 464,
}

// keyMax is the kernel's KEY_MAX.
const keyMax = 0x2ff

// Remap is the keymap as the renderer applies it: the code a key reports
// -> the code of the key it is. Codes not in it are read as they are.
func (s SKU) Remap() (map[int]int, error) {
	if len(s.Keymap) == 0 {
		return nil, nil
	}
	out := make(map[int]int, len(s.Keymap))
	for code, name := range s.Keymap {
		n, err := strconv.Atoi(code)
		if err != nil || n < 1 || n > keyMax || strconv.Itoa(n) != code {
			return nil, fmt.Errorf("keymap: %q is not an input code (1..%d)", code, keyMax)
		}
		to, ok := keyCodes[name]
		if !ok {
			return nil, fmt.Errorf("keymap: %s = %q is no key", code, name)
		}
		out[n] = to
	}
	return out, nil
}

// LoadSKU reads share/phone/sku.json; a missing file is the zero SKU.
func LoadSKU(shareDir string) (SKU, error) {
	s, err := LoadSKUFile(filepath.Join(shareDir, "sku.json"))
	if os.IsNotExist(err) {
		return SKU{}, nil
	}
	return s, err
}

// SimulatedSKU reads a sku.json for the developer machine's Shell to
// simulate (kario run -sku, phone-shell --sku) without its id: with one the
// Shell takes the machine it runs on for the phone, and powering off or
// restarting from the power menu would do it to the computer.
func SimulatedSKU(path string) (SKU, error) {
	s, err := LoadSKUFile(path)
	s.ID = ""
	return s, err
}

// LoadSKUFile reads a sku.json: the image's, or the one kario run -sku
// writes for the developer machine's Shell to simulate.
func LoadSKUFile(path string) (SKU, error) {
	var s SKU
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("sku.json: %w", err)
	}
	if s.Device != "" {
		if _, ok := DeviceByID(s.Device); !ok {
			return s, fmt.Errorf("sku.json: unknown device %q", s.Device)
		}
	}
	if s.Width < 0 || s.Height < 0 || (s.Width > 0) != (s.Height > 0) {
		return s, fmt.Errorf("sku.json: a panel of %dx%d", s.Width, s.Height)
	}
	if s.Scale != 0 && !ScaleOK(s.Scale) {
		return s, fmt.Errorf("sku.json: scale %g", s.Scale)
	}
	if _, err := s.Remap(); err != nil {
		return s, fmt.Errorf("sku.json: %w", err)
	}
	return s, nil
}

// SKUDevice is the SKU's form factor with its panel's size and scale, if it
// names one.
func (s SKU) SKUDevice() (Device, bool) {
	if s.Device == "" {
		return Device{}, false
	}
	d, ok := DeviceByID(s.Device)
	if ok && s.Width > 0 && s.Height > 0 {
		d.Width, d.Height = s.Width, s.Height
	}
	if ok && s.Scale > 0 {
		d = d.WithScale(s.Scale)
	}
	d.Fn, d.Ctrl = s.Fn, s.Ctrl
	d.Keymap, _ = s.Remap()
	return d, ok
}
