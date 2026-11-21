package shell

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Device is a hardware form factor. The shell picks the keypad behaviour
// from it and tells the pixel renderer the screen geometry; the text
// renderer keeps its own cell grid.
type Device struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Like    string  `json:"like"`   // the classic Nokia phones it follows
	Keypad  string  `json:"keypad"` // "t9" | "qwerty" | "full"
	Width   int     `json:"width"`  // physical pixels
	Height  int     `json:"height"`
	Scale   float64 `json:"scale"`    // physical pixels per logical pixel
	DocCols int     `json:"doc_cols"` // monospace columns for documents and diagrams
	// Fn: the keyboard's Fn layer from the SKU (the pixel renderer types
	// with it), none for the form factors themselves.
	Fn map[string]string `json:"fn,omitempty"`
	// Ctrl: how the SKU's keyboard without a Ctrl key presses Ctrl ("fn+sym":
	// Fn, then Chr, as on the E71); empty: the Ctrl key, if any.
	Ctrl string `json:"ctrl,omitempty"`
	// Keymap: a whole phone's own keypad (SKU.Remap): the input code a key
	// reports -> the code the pixel renderer reads it as.
	Keymap map[int]int `json:"keymap,omitempty"`
}

var Devices = []Device{
	{ID: "bar-t9", Name: "Candybar 12-key", Like: "Nokia 6300 · N73 · 8110 4G", Keypad: "t9",
		Width: 480, Height: 640, Scale: 2, DocCols: 32},
	{ID: "bar-qwerty", Name: "Candybar QWERTY", Like: "Nokia E71 · E63 · E72", Keypad: "qwerty",
		Width: 640, Height: 480, Scale: 2, DocCols: 43},
	{ID: "communicator", Name: "Landscape wide keyboard", Like: "Nokia E90 · 9300 · N900", Keypad: "full",
		Width: 800, Height: 352, Scale: 1.6, DocCols: 69},
}

// DeviceByID also accepts "<id>:<W>x<H>" to override the panel size, e.g. a
// taller screen to capture a whole document in one snapshot, and a scale
// after "@": "bar-t9:1080x2180@4.5" is the OnePlus 6T's panel as its SKU
// draws it (24-sku-framework.md §7.4).
func DeviceByID(id string) (Device, bool) {
	id, scale, scaled := strings.Cut(id, "@")
	base, size, _ := strings.Cut(id, ":")
	for _, d := range Devices {
		if d.ID == base {
			if size != "" {
				if _, err := fmt.Sscanf(size, "%dx%d", &d.Width, &d.Height); err != nil || d.Width <= 0 || d.Height <= 0 {
					return Device{}, false
				}
			}
			if scaled {
				s, err := strconv.ParseFloat(scale, 64)
				if err != nil || !ScaleOK(s) {
					return Device{}, false
				}
				d = d.WithScale(s)
			}
			return d, true
		}
	}
	return Device{}, false
}

// ScaleOK: physical pixels per logical one that a panel may be drawn at.
func ScaleOK(s float64) bool { return s >= 1 && s <= 8 }

// WithScale is the form factor on a panel of another density (a whole
// phone's): documents keep their characters' logical size, so their
// columns follow the logical width.
func (d Device) WithScale(scale float64) Device {
	if f, ok := DeviceByID(d.ID); ok && f.Width > 0 && f.Scale > 0 {
		d.DocCols = int(math.Round(float64(f.DocCols) * float64(d.Width) / scale / (float64(f.Width) / f.Scale)))
	}
	d.Scale = scale
	return d
}

// Brand is the system's name (boot animation, About phone, compatibility
// reports). It comes from share/phone/brand.json in the image so that the
// name changes without touching code.
var Brand = "Kario"

// loadBrand reads brand.json next to the apps directory, if present.
func loadBrand(shareDir string) {
	var b struct{ Name string }
	if raw, err := os.ReadFile(filepath.Join(shareDir, "brand.json")); err == nil && json.Unmarshal(raw, &b) == nil && b.Name != "" {
		Brand = b.Name
	}
}
