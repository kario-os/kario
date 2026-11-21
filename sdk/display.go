package sdk

import "github.com/kario-os/kario/ui"

// HintDisplay says what the app shows, for the refresh rate the system
// picks (display.hint, 08-display-adaptive-refresh.md): AnimatingMS while
// it animates on its own (ask again before it runs out to go on), FPS for
// content of that frame rate until cleared, Clear to take it back. The
// screen's own scrolling and transitions need none. Only in the
// foreground, at most ui.DisplayHintBurst at once and then
// ui.DisplayHintPerSecond a second; done (may be nil) gets the rate the
// panel runs at now, on the dispatch goroutine.
func (a *App) HintDisplay(h ui.DisplayHintArgs, done func(ui.DisplayHintRes, error)) {
	res := new(ui.DisplayHintRes)
	a.Go(ui.CapDisplayHint, h, res, func(err error) {
		if done != nil {
			done(*res, err)
		}
	})
}
