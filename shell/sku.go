package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
	// physical pixels as mounted, 0 for the form factor's own.
	Device   string `json:"device"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Display  string `json:"display"`
	Keypad   string `json:"keypad"`
	MemoryMB int    `json:"memory_mb"`
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
	// LEDs and Backlight name the board's notification light and panel
	// backlight (hal); empty where the board has none.
	LEDs struct {
		Notify led.Config `json:"notify"`
	} `json:"leds,omitempty"`
	Backlight string `json:"backlight,omitempty"`
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
}

// LoadSKU reads share/phone/sku.json; a missing file is the zero SKU.
func LoadSKU(shareDir string) (SKU, error) {
	var s SKU
	raw, err := os.ReadFile(filepath.Join(shareDir, "sku.json"))
	if os.IsNotExist(err) {
		return s, nil
	}
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
	return s, nil
}

// SKUDevice is the SKU's form factor with its panel size, if it names one.
func (s SKU) SKUDevice() (Device, bool) {
	if s.Device == "" {
		return Device{}, false
	}
	d, ok := DeviceByID(s.Device)
	if ok && s.Width > 0 && s.Height > 0 {
		d.Width, d.Height = s.Width, s.Height
	}
	d.Fn = s.Fn
	return d, ok
}
