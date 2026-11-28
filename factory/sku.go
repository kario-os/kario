// Package factory is the line's hardware test (docs/40-testing.md §10): the
// checks a phone's SKU calls for, the automatic ones run against the
// system's files and services, the report the station reads, and factory
// mode, the boot it runs in on the line (docs/31-lock-and-biometrics.md
// §13), with the station's side of it over USB (docs/27-usb-typec.md §3.4).
package factory

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

// SKUPath is where the image keeps its SKU (phone-sku).
const SKUPath = "/usr/share/phone/sku.json"

// SKU is what the test reads of sku.json (tools/sku.py writes it; the Shell
// reads the rest, shell/sku.go).
type SKU struct {
	ID          string   `json:"id"`
	Board       string   `json:"board"`
	Device      string   `json:"device"`
	MemoryMB    int      `json:"memory_mb"`
	Modem       string   `json:"modem"`
	Touch       bool     `json:"touch,omitempty"`
	SoundSwitch string   `json:"sound_switch,omitempty"`
	Lid         string   `json:"lid,omitempty"`
	Audio       Audio    `json:"audio,omitzero"`
	Factory     Hardware `json:"factory"`
}

// Audio is the board's sound card as sku.json names it ([audio]).
type Audio struct {
	PCM      string `json:"pcm,omitempty"`
	Card     string `json:"card,omitempty"`
	Verb     string `json:"verb,omitempty"`
	CallVerb string `json:"call_verb,omitempty"`
	CallMic  string `json:"call_mic,omitempty"`
}

// Hardware is sku.json's factory (tools/sku.py's factory_json): keys, each
// pressed once; keypad, the phone's own keypad (nil for a whole phone's
// Bluetooth or USB keyboard); buttons, the board's button input devices;
// parts, the board's part kinds (a camera as camera-<facing>); panel, the
// main panel's pixels; storage_gb, the least the internal disks hold;
// optional, the checks whose failure does not fail the unit.
type Hardware struct {
	Keys      []string `json:"keys,omitempty"`
	Keypad    *Keypad  `json:"keypad,omitempty"`
	Buttons   []string `json:"buttons,omitempty"`
	Parts     []string `json:"parts,omitempty"`
	Panel     [2]int   `json:"panel"`
	StorageGB int      `json:"storage_gb,omitempty"`
	Optional  []string `json:"optional,omitempty"`
}

// Keypad is the phone's keypad: its chip ("usb" for the emulator's
// keyboard) and the input codes its device reports (none for "usb").
type Keypad struct {
	Chip  string `json:"chip"`
	Codes []int  `json:"codes,omitempty"`
}

// LoadSKU reads a sku.json.
func LoadSKU(path string) (SKU, error) {
	var s SKU
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Has: the board lists a part of this kind.
func (s SKU) Has(kind string) bool { return slices.Contains(s.Factory.Parts, kind) }
