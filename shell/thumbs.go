package shell

import (
	"fmt"
	"image"
	"os"
	"sync"
	"time"

	"github.com/kario-os/kario/media"
)

// Photo thumbnails in frames. A grid may name thousands of photos; only
// tiles within thumbRows rows of the focused one carry pixels, so a frame
// costs a screenful whatever the library holds. A renderer that takes
// surfaces gets each thumbnail in shared memory of the shell's, cut to the
// grid's cell, and the frame names it; thumbnails a frame no longer shows
// are closed here and dropped by the renderer. Others get the pixels
// inline, smaller.
//
// The surfaces are made off the loop (thumbLoader): decoding the rows a
// key brings into view took the frame of that key 50 ms on a PC, and the
// phone's cores are slower (report 008 §1.5). Until its surface is ready
// a tile goes without pixels, and the frame after it comes has them.

const (
	thumbRows   = 4  // rows either side of the focused one that get pixels
	inlineThumb = 96 // the side of inline thumbnails
)

// thumbJob is a thumbnail surface to make: its key, the stored thumbnail,
// the photo it is made again from when missing or smaller than minW, and
// the cell's side.
type thumbJob struct {
	key, thumb, photo string
	minW, side        int
}

// thumbLoader makes thumbnail surfaces one at a time on a goroutine of its
// own; wake is called after each.
type thumbLoader struct {
	mu      sync.Mutex
	queue   []thumbJob
	loading map[string]bool
	ready   map[string]*media.Surface
	running bool
}

// take is the surface made for key, if it is ready; the caller owns it.
func (l *thumbLoader) take(key string) *media.Surface {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.ready[key]
	delete(l.ready, key)
	return s
}

// load queues j unless it is queued, being made or ready.
func (l *thumbLoader) load(j thumbJob, wake func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.loading[j.key] || l.ready[j.key] != nil {
		return
	}
	if l.loading == nil {
		l.loading, l.ready = map[string]bool{}, map[string]*media.Surface{}
	}
	l.loading[j.key] = true
	l.queue = append(l.queue, j)
	if !l.running {
		l.running = true
		go l.run(wake)
	}
}

func (l *thumbLoader) run(wake func()) {
	for {
		l.mu.Lock()
		if len(l.queue) == 0 {
			l.running = false
			l.mu.Unlock()
			return
		}
		j := l.queue[0]
		l.queue = l.queue[1:]
		l.mu.Unlock()
		var surf *media.Surface
		if img := loadThumb(j.thumb, j.photo, j.minW); img != nil {
			surf, _ = rgbaSurface(scaleCrop(img, j.side, j.side))
		}
		l.mu.Lock()
		delete(l.loading, j.key)
		if surf != nil {
			l.ready[j.key] = surf
		}
		l.mu.Unlock()
		wake()
	}
}

// keep closes the surfaces made that want does not name: a frame came
// after them that no longer shows them.
func (l *thumbLoader) keep(want map[string]bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, s := range l.ready {
		if !want[k] {
			s.Close()
			delete(l.ready, k)
		}
	}
}

// snapSettle bounds a snapshot's wait for the thumbnails its frame asked
// for.
const snapSettle = 10 * time.Second

// settle waits up to d for the thumbnails queued or being made; whether a
// frame made now would have more of them (a snapshot shows the screen as
// it settles). A thumbnail made after the last frame asked for it but
// before settle looked counts too: no frame has it yet.
func (l *thumbLoader) settle(d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		l.mu.Lock()
		busy, made := len(l.loading) > 0, len(l.ready) > 0
		l.mu.Unlock()
		if !busy {
			return made
		}
	}
	return true
}

// thumbShown: tile k of a grid of cols columns whose focus is tile sel
// (-1: none, the grid's top) is near enough to carry pixels.
func thumbShown(k, sel, cols int) bool {
	cols = max(cols, 1)
	row, at := k/cols, max(sel, 0)/cols
	return row >= at-thumbRows && row <= at+thumbRows
}

// thumbFrame is a photo's thumbnail for a grid of cols columns.
func (c *Core) thumbFrame(it *mediaItem, cols int) *FrameImage {
	d := c.screenDev
	if c.renderer == nil || !c.renderer.Has("shm") || d.Width <= 0 {
		return c.thumbPixels(it, inlineThumb)
	}
	side := min(thumbSide, max(32, d.Width/max(cols, 1)))
	key := fmt.Sprintf("thumb:%s@%d", it.ID, side)
	c.frameSurfs[key] = true
	if c.thumbSurfs[key] == nil {
		surf := c.thumbs.take(key)
		if surf == nil {
			c.thumbs.load(thumbJob{key: key, thumb: c.media.thumb(it.ID), photo: c.media.path(it), minW: min(thumbSide, it.W), side: side}, c.postFrame)
			return nil
		}
		c.thumbSurfs[key] = surf
	}
	return &FrameImage{W: side, H: side, Surface: key, Seq: 1}
}

// dropThumbs closes the thumbnail surfaces the frame just made does not use.
func (c *Core) dropThumbs() {
	for k, s := range c.thumbSurfs {
		if !c.frameSurfs[k] {
			s.Close()
			delete(c.thumbSurfs, k)
		}
	}
	c.thumbs.keep(c.frameSurfs)
}

func (c *Core) thumbFile(id string) (*os.File, int, int, bool) {
	if s := c.thumbSurfs[id]; s != nil {
		return s.File(), s.W, s.H, true
	}
	return nil, 0, 0, false
}

// rgbaSurface publishes a picture once in a new surface.
func rgbaSurface(img *image.RGBA) (*media.Surface, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	f := media.NewFrame(w, h)
	for i := 0; i < w*h; i++ {
		copy(f.Pix[3*i:3*i+3], img.Pix[4*i:4*i+3])
	}
	f.Seq = 1
	surf, err := media.NewSurface(w, h)
	if err != nil {
		return nil, err
	}
	surf.Publish(f)
	return surf, nil
}
