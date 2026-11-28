package factory

import (
	"slices"
	"strings"
)

// Kind: an automatic check measures by itself; an interactive one runs on
// the Shell's factory screen with the operator.
type Kind string

const (
	Auto        Kind = "auto"
	Interactive Kind = "interactive"
)

// Check is one check of a unit's test as its SKU calls for it. A check the
// SKU's hardware does not have is in the plan too, with Skip saying so: a
// report lists every check of the catalogue, whatever the SKU.
type Check struct {
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// Required: the unit fails unless it passes (the SKU has the hardware
	// and does not name it in its factory optional).
	Required bool `json:"required"`
	// Skip: why the check does not apply to this SKU, "" when it does.
	Skip string `json:"skip,omitempty"`
	// Keys: the keys check's keys, keys.toml's names.
	Keys []string `json:"keys,omitempty"`
}

type entry struct {
	id      string
	kind    Kind
	applies func(SKU) string
}

func need(ok bool, why string) string {
	if ok {
		return ""
	}
	return why
}

func part(kind, why string) func(SKU) string {
	return func(s SKU) string { return need(s.Has(kind), why) }
}

func always(SKU) string { return "" }

// catalogue is every check, automatic ones first, in the order they run;
// tools/sku.py's FACTORY_CHECKS lists the same ids (an SKU's optional
// names them).
var catalogue = []entry{
	{"input.keypad", Auto, func(s SKU) string {
		return need(s.Factory.Keypad != nil, "the keypad is not the phone's own (a Bluetooth or USB keyboard)")
	}},
	{"input.buttons", Auto, func(s SKU) string { return need(len(s.Factory.Buttons) > 0, "the SKU names no button devices") }},
	{"input.sound-switch", Auto, func(s SKU) string { return need(s.SoundSwitch != "", "the SKU has no sound switch") }},
	{"input.lid", Auto, func(s SKU) string { return need(s.Lid != "", "the SKU has no lid") }},
	{"input.touch", Auto, func(s SKU) string { return need(s.Touch, "the SKU has no touchscreen") }},
	{"display.mode", Auto, func(s SKU) string { return need(s.Factory.Panel[0] > 0, "the SKU gives no panel size") }},
	{"audio.card", Auto, func(s SKU) string {
		return need(s.Audio.Card != "" || s.Has("sound-card"), "the SKU has no sound card")
	}},
	{"audio.ucm", Auto, func(s SKU) string {
		return need(s.Audio.Card != "" && (s.Audio.Verb != "" || s.Audio.CallVerb != ""), "the board names no UCM verbs")
	}},
	{"vibrator", Auto, part("haptics", "the SKU has no vibrator")},
	{"sensor.light", Auto, part("light", "the SKU has no light sensor on IIO")},
	{"sensor.proximity", Auto, part("proximity", "the SKU has no proximity sensor on IIO")},
	{"sensor.accelerometer", Auto, part("accelerometer", "the SKU has no accelerometer on IIO")},
	{"sensor.gyroscope", Auto, part("gyroscope", "the SKU has no gyroscope on IIO")},
	{"sensor.magnetometer", Auto, part("magnetometer", "the SKU has no magnetometer on IIO")},
	{"battery", Auto, func(s SKU) string {
		return need(s.Has("fuel-gauge") || s.Has("charger"), "the SKU has no battery (no fuel gauge or charger)")
	}},
	{"rtc", Auto, always},
	{"storage", Auto, func(s SKU) string { return need(s.Factory.StorageGB > 0, "the SKU gives no storage size") }},
	{"memory", Auto, func(s SKU) string { return need(s.MemoryMB > 0, "the SKU gives no memory size") }},
	{"wifi", Auto, part("wifi", "the SKU has no Wi-Fi")},
	{"bluetooth", Auto, part("bluetooth", "the SKU has no Bluetooth")},
	{"modem", Auto, func(s SKU) string { return need(s.Modem != "", "the SKU has no modem") }},
	{"sim", Auto, func(s SKU) string { return need(s.Modem != "", "the SKU has no modem") }},
	{"keystore", Auto, always},
	{"keys", Interactive, func(s SKU) string { return need(len(s.Factory.Keys) > 0, "the SKU names no keys") }},
	{"display.patterns", Interactive, always},
	{"touch", Interactive, func(s SKU) string { return need(s.Touch, "the SKU has no touchscreen") }},
	{"speaker", Interactive, func(s SKU) string {
		return need(s.Audio.Card != "" || s.Has("sound-card"), "the SKU has no sound card")
	}},
	{"microphone", Interactive, func(s SKU) string { return need(s.Audio.CallMic != "", "the SKU names no microphone") }},
	{"vibrator.felt", Interactive, part("haptics", "the SKU has no vibrator")},
	{"camera.back", Interactive, part("camera-back", "the SKU has no back camera")},
	{"camera.front", Interactive, part("camera-front", "the SKU has no front camera")},
}

// CheckIDs are the catalogue's ids, in order.
func CheckIDs() []string {
	ids := make([]string, len(catalogue))
	for i, e := range catalogue {
		ids[i] = e.id
	}
	return ids
}

// Plan is the SKU's checks: the whole catalogue, each applying or not.
func Plan(s SKU) []Check {
	out := make([]Check, 0, len(catalogue))
	for _, e := range catalogue {
		c := Check{ID: e.id, Kind: e.kind, Skip: e.applies(s)}
		c.Required = c.Skip == "" && !optional(s.Factory.Optional, e.id)
		if e.id == "keys" && c.Skip == "" {
			c.Keys = slices.Clone(s.Factory.Keys)
		}
		out = append(out, c)
	}
	return out
}

// optional: the SKU names the check, or its kind ("camera.*").
func optional(names []string, id string) bool {
	for _, n := range names {
		if n == id || strings.HasSuffix(n, ".*") && strings.HasPrefix(id, strings.TrimSuffix(n, "*")) {
			return true
		}
	}
	return false
}

// Only is the plan's checks of one kind.
func Only(plan []Check, k Kind) []Check {
	var out []Check
	for _, c := range plan {
		if c.Kind == k {
			out = append(out, c)
		}
	}
	return out
}
