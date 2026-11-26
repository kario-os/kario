package ui

// The panel's refresh rate (08-display-adaptive-refresh.md): the system
// picks it, high while something moves and lower when nothing does. An
// app only says what it shows with display.hint: {animating_ms} while it
// animates on its own (a game, a chart drawn frame by frame) asks for the
// high rate that long, {fps} for a rate its content's frame rate divides
// (a video at 24, 30 or 60 frames a second), {clear} takes its hint back.
// Only the app in front; DisplayHintBurst hints at once, then
// DisplayHintPerSecond a second; its hint goes when it leaves the front.
// The answer is the rate now.
const (
	CapDisplayHint = "display.hint"

	DisplayHintMaxMS     = 3000
	DisplayHintMaxFPS    = 240
	DisplayHintBurst     = 8
	DisplayHintPerSecond = 4

	ErrDisplayHintRate = "too many display hints"
	ErrDisplayNotFront = "only the app in front hints the display"
)

// DisplayHintArgs: animate for AnimatingMS (at most DisplayHintMaxMS; ask
// again to go on), or content of FPS frames a second until cleared, or
// Clear.
type DisplayHintArgs struct {
	AnimatingMS int  `json:"animating_ms,omitempty"`
	FPS         int  `json:"fps,omitempty"`
	Clear       bool `json:"clear,omitempty"`
}

// DisplayHintRes: the rate the panel runs at now and the ones it has,
// lowest first.
type DisplayHintRes struct {
	Hz    int   `json:"hz"`
	Rates []int `json:"rates"`
}

// Settings › Display and brightness › Refresh rate: settings.set
// refresh_rate takes the index in RefreshRates: automatic, always the
// standard StandardHz, or always the panel's highest. The row is there
// only when the panel has more than one rate; settings.get's "display"
// has them, the rate now and why.
const (
	SettingRefreshRate = "refresh_rate"
	RefreshAuto        = "auto"
	RefreshStandard    = "standard"
	RefreshHigh        = "high"

	StandardHz = 60
)

// RefreshRates are the refresh rate's choices in order.
var RefreshRates = []string{RefreshAuto, RefreshStandard, RefreshHigh}
