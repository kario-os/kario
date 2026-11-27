package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	// PaintGPU is the pixel renderer's GPU path by default
	// (42-gpu-rendering.md §3): "compose", "raster", or "off" (empty);
	// PHONE_PAINT_GPU in its environment overrides it.
	PaintGPU string `json:"paint_gpu,omitempty"`
	// GPURender is the kernel driver of the GPU's render node where the
	// display's DRM device cannot render (a display controller beside a
	// Mali or PowerVR): the renderer renders there and shows the frames on
	// the display's device.
	GPURender string `json:"gpu_render,omitempty"`
	// Modem is telephonyd's backend, one it is built with (tools/sku.py
	// MODEMS): sim or modemmanager.
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
	// Proximity: the board has a proximity sensor by the earpiece (one of
	// its parts), which can tell the ear from a hand in a call
	// (calltouch.go); nothing reads it yet.
	Proximity bool `json:"proximity,omitempty"`
	// Fingerprint is keystored's sensor backend (hal/fingerprint): "sim" on
	// the emulator, empty where the board has none it can drive.
	Fingerprint string `json:"fingerprint,omitempty"`
	// FingerprintAt is where the keypad has the sensor: ui.FingerprintOKKey,
	// or empty for a sensor of its own. keystored reads it from sku.json
	// itself; the Shell passes a simulated SKU's on (lock.go).
	FingerprintAt string `json:"fingerprint_at,omitempty"`
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
	// Touch: the board's display has a touchscreen ([display] touchscreen:
	// a whole phone's own, QEMU's virtio multitouch device), a second way
	// to the keys (touch.go).
	Touch bool `json:"touch,omitempty"`
	// SoundSwitch names the input device of the board's hardware sound
	// switch (a three-position slider reporting ABS_SND_PROFILE: the
	// 6T's "Alert slider"; or a two-position mute switch reporting
	// SW_MUTE_DEVICE: silent or ring), which the pixel renderer reads;
	// its position is the sound mode (soundmode.go). Empty: none.
	SoundSwitch string `json:"sound_switch,omitempty"`
	// Lid names the input device of a flip's lid switch (its Hall sensor,
	// reporting SW_LID: 1 while shut), which the pixel renderer reads
	// (lid.go). Empty: none.
	Lid string `json:"lid,omitempty"`
	// Outer is a second panel on the outside of a flip
	// (24-sku-framework.md §9.10); the zero value: none.
	Outer SKUOuter `json:"outer,omitempty"`
	// Audio is the board's sound card; empty where phone-audiod finds the
	// card itself.
	Audio SKUAudio `json:"audio,omitempty"`
	// RTC is how the board's real-time clock keeps time (hal/rtc); the
	// zero value is a settable RTC woken with "+N" alarms.
	RTC SKURTC `json:"rtc,omitempty"`
	// USB is the board's USB-C port as a host (usbhost.go); the zero
	// value: a USB device only.
	USB SKUUSB `json:"usb,omitempty"`
}

// SKUUSB is the board file's [usb].
type SKUUSB struct {
	// Host: the port can be a USB host (OTG): keyboards, mice, hubs.
	Host bool `json:"host,omitempty"`
	// VBUSMA: what the phone gives the devices on VBUS when it powers
	// them; a device that would draw more stays unconfigured.
	VBUSMA int `json:"vbus_ma,omitempty"`
	// Controller: a word of the sysfs path of the port's host controller
	// where the board has another inside; empty: every one.
	Controller string `json:"controller,omitempty"`
}

// usbVBUSMax is the most a board may say its port gives (tools/sku.py).
const usbVBUSMax = 3000

func (u SKUUSB) check() error {
	switch {
	case u.VBUSMA != 0 && (u.VBUSMA < 100 || u.VBUSMA > usbVBUSMax):
		return fmt.Errorf("usb: vbus_ma %d is 100 to %d mA", u.VBUSMA, usbVBUSMax)
	case !u.Host && (u.VBUSMA != 0 || u.Controller != ""):
		return errors.New("usb: vbus_ma or controller without host: a device powers nothing")
	}
	return nil
}

// SKURTC is the board file's [rtc].
type SKURTC struct {
	// ReadOnly: setting the RTC fails (the 6T's PMIC RTC on mainline), so
	// setting the time does not write it.
	ReadOnly bool `json:"read_only,omitempty"`
	// Offset: the system time is kept as an offset from the read-only RTC
	// (hal/rtc), and phone-clock sets the clock from both at boot.
	Offset bool `json:"offset,omitempty"`
	// Alarm is how the wake alarm is written: "relative" ("+N" seconds
	// from the RTC's own time, the default, right on any RTC) or
	// "absolute" (the wake time itself: only an RTC on the system's clock).
	Alarm string `json:"alarm,omitempty"`
}

// check refuses what cannot hold: an offset from an RTC that is set
// instead, an absolute alarm on an RTC whose time is not the system's.
func (r SKURTC) check() error {
	switch {
	case r.Alarm != "" && r.Alarm != "relative" && r.Alarm != "absolute":
		return fmt.Errorf("rtc: alarm %q is relative or absolute", r.Alarm)
	case r.Offset && !r.ReadOnly:
		return errors.New("rtc: an offset is kept from a read-only RTC only (a settable one is set)")
	case r.ReadOnly && r.Alarm == "absolute":
		return errors.New("rtc: a read-only RTC's time is not the system's: its alarm is relative")
	}
	return nil
}

// SKUOuter is the SKU's outer display (tools/sku.py writes it from the
// display file): its pixels, what lights it and holds its picture (Kind:
// "lcd", "oled", "memory-lcd" or "e-paper"), what a pixel shows (Color:
// "rgb", "gray" or "mono") and the DRM driver of its device, by which the
// renderer tells it from the main panel.
type SKUOuter struct {
	Display   string  `json:"display"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	WidthMM   float64 `json:"width_mm,omitempty"`
	HeightMM  float64 `json:"height_mm,omitempty"`
	Kind      string  `json:"kind"`
	Color     string  `json:"color"`
	DRMDriver string  `json:"drm_driver"`
}

// check refuses an outer display without its pixels, kind, color or
// driver.
func (o SKUOuter) check() error {
	if o == (SKUOuter{}) {
		return nil
	}
	switch {
	case o.Display == "":
		return errors.New("outer: no display")
	case o.Width <= 0 || o.Height <= 0:
		return fmt.Errorf("outer: a panel of %dx%d", o.Width, o.Height)
	case o.WidthMM < 0 || o.HeightMM < 0 || (o.WidthMM > 0) != (o.HeightMM > 0):
		return fmt.Errorf("outer: %gx%g mm", o.WidthMM, o.HeightMM)
	case !driverName.MatchString(o.DRMDriver):
		return fmt.Errorf("outer: drm_driver %q is not a kernel driver's name", o.DRMDriver)
	}
	switch o.Kind {
	case "lcd", "oled", "memory-lcd", "e-paper":
	default:
		return fmt.Errorf("outer: kind %q", o.Kind)
	}
	switch o.Color {
	case "rgb", "gray", "mono":
	default:
		return fmt.Errorf("outer: color %q", o.Color)
	}
	return nil
}

// SKUAudio is a board's sound card as phone-audiod plays to it and sets it
// up through its ALSA UCM profile (20-audio.md §10): the board file's
// [audio], which a Yocto machine also passes to the image's audiod
// (tools/sku.py's AUDIO names each value's PHONE_AUDIO_* variable).
type SKUAudio struct {
	PCM            string `json:"pcm"`
	Card           string `json:"card,omitempty"`
	Verb           string `json:"verb,omitempty"`
	Device         string `json:"device,omitempty"`
	CallVerb       string `json:"call_verb,omitempty"`
	Earpiece       string `json:"earpiece,omitempty"`
	EarpiecePCM    string `json:"earpiece_pcm,omitempty"`
	CallSpeaker    string `json:"call_speaker,omitempty"`
	CallSpeakerPCM string `json:"call_speaker_pcm,omitempty"`
	CallMic        string `json:"call_mic,omitempty"`
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
	if err := s.RTC.check(); err != nil {
		return s, fmt.Errorf("sku.json: %w", err)
	}
	if err := s.Outer.check(); err != nil {
		return s, fmt.Errorf("sku.json: %w", err)
	}
	if err := s.USB.check(); err != nil {
		return s, fmt.Errorf("sku.json: %w", err)
	}
	if s.Lid != "" && s.Lid == s.SoundSwitch {
		return s, fmt.Errorf("sku.json: %q is the sound switch and the lid", s.Lid)
	}
	switch s.PaintGPU {
	case "", "off", "compose", "raster":
	default:
		return s, fmt.Errorf("sku.json: paint_gpu %q is not off, compose or raster", s.PaintGPU)
	}
	if s.GPURender != "" && !driverName.MatchString(s.GPURender) {
		return s, fmt.Errorf("sku.json: gpu_render %q is not a kernel driver's name", s.GPURender)
	}
	return s, nil
}

// driverName is a kernel DRM driver's name (tools/sku.py's DRIVER).
var driverName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// GPU is what the pixel renderer takes from the board for its GPU path,
// whatever form factor it draws: the path by default and the render node.
func (s SKU) GPU(d Device) Device {
	d.GPU, d.GPURender = s.PaintGPU, s.GPURender
	return d
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
	d.Fn, d.Ctrl, d.Touch, d.SoundSwitch, d.Lid = s.Fn, s.Ctrl, s.Touch, s.SoundSwitch, s.Lid
	d.Keymap, _ = s.Remap()
	return s.GPU(d), ok
}
