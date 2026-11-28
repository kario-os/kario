package shell

import "time"

// The device mode's loop (phone-shell --gui): the Shell's events run, and
// the renderer is sent a frame when something changed. A frame goes out
// as soon as an event has run, so an input is drawn without waiting, and
// at most one every framePeriod since the last one sent: events come in
// bursts (a held key, an app's pages, a video's frames) and the renderer
// draws no more than the panel shows (it merges frames that queue). A
// push that changed nothing sends nothing and does not count, so an app's
// answer to a key goes out at once as well. An idle phone, and one with
// its screen off, does not wake up to draw; the boot animation ticks
// while it runs.

// framePeriod is the shortest time between two frames sent (60 Hz).
const framePeriod = 16 * time.Millisecond

// framePacer says when the next frame may go: at once, or framePeriod
// after the last one sent.
type framePacer struct{ sent time.Time }

func (p *framePacer) delay(now time.Time) time.Duration {
	return max(0, framePeriod-now.Sub(p.sent))
}

// RunFrames is the device mode's loop until the Shell quits. A renderer
// that exits is started again (Restart) and told about through restarted,
// with the error that ends the loop when it could not be.
func (w *RendererWatch) RunFrames(restarted func(error)) {
	c := w.c
	var pace framePacer
	var frame <-chan time.Time
	soon := func() {
		if frame == nil {
			frame = time.After(pace.delay(time.Now()))
		}
	}
	soon()
	for !c.Quit() {
		select {
		case f := <-c.Events():
			f()
			soon()
		case <-frame:
			frame = nil
			if !c.ScreenOff() {
				n := w.G.stats.Frames
				c.Render()
				_ = w.G.Push()
				if w.G.stats.Frames != n {
					pace.sent = time.Now()
				}
			}
			if c.BootProgress() >= 0 {
				soon()
			}
		case <-w.G.Done():
			// The renderer died; the shell has every piece of state, so a
			// new one only needs the current frame.
			err := w.Restart()
			restarted(err)
			if err != nil {
				return
			}
			soon()
		}
	}
}
