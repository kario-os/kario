package shell

import (
	"fmt"
	"image"
	"os"

	"github.com/kario-os/kario/media"
)

// Photo thumbnails in frames. A grid may name thousands of photos; only
// tiles within thumbRows rows of the focused one carry pixels, so a frame
// costs a screenful whatever the library holds. A renderer that takes
// surfaces gets each thumbnail in shared memory of the shell's, cut to the
// grid's cell, and the frame names it; thumbnails a frame no longer shows
// are closed here and dropped by the renderer. Others get the pixels
// inline, smaller.

const (
	thumbRows   = 4  // rows either side of the focused one that get pixels
	inlineThumb = 96 // the side of inline thumbnails
)

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
	if c.thumbSurfs[key] == nil {
		img := c.thumbImage(it)
		if img == nil {
			return nil
		}
		surf, err := rgbaSurface(scaleCrop(img, side, side))
		if err != nil {
			c.log.Printf("thumbnail %s: %v", it.ID, err)
			return nil
		}
		c.thumbSurfs[key] = surf
	}
	c.frameSurfs[key] = true
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
