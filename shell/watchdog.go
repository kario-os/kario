package shell

import (
	"fmt"
	"os"
	"time"
)

// RendererWatch keeps a pixel renderer on the panel: a renderer that dies is
// started again and gets the whole current frame (the shell owns all state,
// so nothing is lost); after maxCrashes within crashWindow the shell moves to
// the fallback renderer when the image has one. Without one it keeps
// restarting the same renderer, a little slower.
type RendererWatch struct {
	c       *Core
	dev     Device
	paths   []string
	args    []string
	idx     int
	crashes []time.Time
	G       *GUI
}

const (
	maxCrashes   = 3
	crashWindow  = time.Minute
	crashBackoff = 2 * time.Second
)

// WatchRenderer starts the primary renderer (or the fallback when the
// primary does not start); args go to every renderer it starts.
func WatchRenderer(c *Core, dev Device, primary, fallback string, args ...string) (*RendererWatch, error) {
	w := &RendererWatch{c: c, dev: dev, paths: []string{primary}, args: args}
	if fallback != "" && fallback != primary {
		if _, err := os.Stat(fallback); err == nil {
			w.paths = append(w.paths, fallback)
		}
	}
	var err error
	for w.idx = range w.paths {
		if w.G, err = StartGUI(c, w.paths[w.idx], dev, args...); err == nil {
			return w, nil
		}
		c.log.Printf("renderer %s: %v", w.paths[w.idx], err)
	}
	return nil, err
}

// Path is the renderer in use.
func (w *RendererWatch) Path() string { return w.paths[w.idx] }

// Restart replaces a renderer that exited; call it when G.Done() fires.
func (w *RendererWatch) Restart() error {
	now := time.Now()
	// A renderer asked to quit for a language change has not crashed.
	if w.G == nil || !w.G.Recycled() {
		w.crashes = append(w.crashes, now)
	}
	recent := w.crashes[:0]
	for _, t := range w.crashes {
		if now.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	w.crashes = recent
	if len(w.crashes) >= maxCrashes {
		if w.idx+1 < len(w.paths) {
			w.c.log.Printf("renderer %s crashed %d times in %s; switching to %s", w.Path(), len(w.crashes), crashWindow, w.paths[w.idx+1])
			w.idx++
			w.crashes = nil
		} else {
			time.Sleep(crashBackoff)
		}
	}
	g, err := StartGUI(w.c, w.Path(), w.dev, w.args...)
	if err != nil {
		return fmt.Errorf("restarting renderer %s: %w", w.Path(), err)
	}
	g.stats = w.G.stats
	w.G = g
	w.c.log.Printf("renderer restarted: %s (%s)", g.Info(), w.Path())
	return nil
}
