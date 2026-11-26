package svc

// displayd: the panel's refresh rate (08-display-adaptive-refresh.md). The
// Shell is its only client: it passes on what moves (keys, the renderer's
// animations, the hints of the app in front, a video playing), the
// setting and the caps; displayd picks the rate, and the renderer, which
// holds the panel (DRM master), switches to the mode of that rate.
const (
	Displayd     = "displayd"
	DisplaydSock = Displayd + ".sock"
)

const (
	// DisplayState answers DisplayStatus.
	DisplayState = "state"
	// DisplaySetting {setting}: ui.RefreshAuto, ui.RefreshStandard or
	// ui.RefreshHigh (Settings › Display and brightness).
	DisplaySetting = "setting"
	// DisplayHint takes DisplayHintArgs.
	DisplayHint = "hint"
	// DisplayCap {source, hz}: the highest rate a source allows (battery,
	// thermal), 0 none.
	DisplayCap = "cap"
	// DisplayPanel takes DisplayPanelArgs: the renderer drives the panel
	// (on a phone displayd reads its modes then, never before: the first
	// to open the card would become its master), or the mode it switched
	// to.
	DisplayPanel = "panel"
	// DisplaySimThermal {hot}: the simulated thermal zone is over the cap
	// or not (tests; refused with real zones).
	DisplaySimThermal = "sim.thermal"

	// DisplayEventRate is a DisplayEvent: the rate chosen, or the panel's,
	// changed.
	DisplayEventRate = "rate"
)

// Hint kinds and the sources the Shell names.
const (
	// DisplayInteraction: a key went down; the high rate for the hold.
	DisplayInteraction = "interaction"
	// DisplayAnimating: something moves for MS (0: until DisplayEnd).
	DisplayAnimating = "animating"
	// DisplayEnd: it stopped moving; the hold starts now.
	DisplayEnd = "end"
	// DisplayContent: content of FPS frames a second shows for MS (0:
	// until cleared).
	DisplayContent = "content"
	// DisplayClear takes the source's hint back; a Source ending in "*"
	// the hints of every source that starts so ("app:*").
	DisplayClear = "clear"

	DisplaySourceInput    = "input"
	DisplaySourceRenderer = "renderer"
	DisplaySourceShell    = "shell"
	// DisplaySourceApp and DisplaySourceVideo prefix an app's ID and a
	// video player's.
	DisplaySourceApp   = "app:"
	DisplaySourceVideo = "video:"

	DisplayCapBattery = "battery"
	DisplayCapThermal = "thermal"
)

// DisplayHintArgs: a hint from Source of Kind.
type DisplayHintArgs struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	MS     int    `json:"ms,omitempty"`
	FPS    int    `json:"fps,omitempty"`
}

// DisplayCapArgs: Source allows at most Hz (0: no cap).
type DisplayCapArgs struct {
	Source string `json:"source"`
	Hz     int    `json:"hz"`
}

// DisplayPanelArgs: KMS, the renderer drives the panel through DRM/KMS
// (displayd may read the card now); Applied, the rate of the mode the
// renderer switched to, Err why it could not switch to the one asked for.
type DisplayPanelArgs struct {
	KMS     bool   `json:"kms,omitempty"`
	Applied int    `json:"applied,omitempty"`
	Err     string `json:"err,omitempty"`
}

// DisplayStatus: the panel (Panel: "sim", "kms:<connector>", or "" before
// its modes are read) and its rates, lowest first; Hz the rate it runs
// at, Want the one displayd chose (they differ until the renderer has
// switched, or where it could not) and Reason why: "idle", "setting",
// "cap:<source>", or the source of the hint that decided. Caps are the
// caps in force; Switches counts the rates chosen since the start. Seq
// grows with every change: an answer written after a newer event is older
// than it.
type DisplayStatus struct {
	Seq      uint64         `json:"seq"`
	Panel    string         `json:"panel"`
	Rates    []int          `json:"rates"`
	Hz       int            `json:"hz"`
	Want     int            `json:"want"`
	Reason   string         `json:"reason"`
	Setting  string         `json:"setting"`
	Caps     map[string]int `json:"caps,omitempty"`
	Switches int            `json:"switches"`
}

// DisplayEvent is a displayd event: Event, and the state after it.
type DisplayEvent struct {
	Event string `json:"event"`
	DisplayStatus
}
