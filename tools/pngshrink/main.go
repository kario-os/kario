// pngshrink makes the pixel renderer's snapshots small enough to commit with
// the documents (scripts/guideline-shots.sh): each PNG scaled down by a
// whole factor (box filter) and reduced to a palette of at most -colors
// colours chosen from the image itself, without dithering, which flat
// interface colours do not need. The same input gives the same bytes.
//
//	go run ./tools/pngshrink -scale 2 -colors 16 -suffix .en -o docs/img/33 build/shots/*.png
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	out := flag.String("o", ".", "where the small PNGs go")
	scale := flag.Int("scale", 2, "divide width and height by this")
	colors := flag.Int("colors", 16, "at most this many colours (2-256)")
	suffix := flag.String("suffix", "", "added to each file name before .png")
	flag.Parse()
	if *scale < 1 || *colors < 2 || *colors > 256 || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: pngshrink [-scale n] [-colors n] [-suffix s] -o dir file.png...")
		os.Exit(2)
	}
	for _, f := range flag.Args() {
		dst := filepath.Join(*out, strings.TrimSuffix(filepath.Base(f), ".png")+*suffix+".png")
		if err := shrink(f, dst, *scale, *colors); err != nil {
			fmt.Fprintln(os.Stderr, "pngshrink:", err)
			os.Exit(1)
		}
	}
}

func shrink(src, dst string, scale, colors int) error {
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	img, err := png.Decode(r)
	if err != nil {
		return fmt.Errorf("%s: %v", src, err)
	}
	small := down(img, scale)
	pal := palette(small, colors)
	p := image.NewPaletted(small.Bounds(), pal)
	cache := map[color.RGBA]uint8{}
	for y := 0; y < small.Rect.Dy(); y++ {
		for x := 0; x < small.Rect.Dx(); x++ {
			c := small.RGBAAt(x, y)
			i, ok := cache[c]
			if !ok {
				i = uint8(pal.Index(c))
				cache[c] = i
			}
			p.SetColorIndex(x, y, i)
		}
	}
	w, err := os.Create(dst)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(w, p); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// down averages each scale×scale block into one opaque pixel.
func down(img image.Image, scale int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx()/scale, b.Dy()/scale
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	n := uint32(scale * scale)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sr, sg, sb uint32
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					r, g, bl, _ := img.At(b.Min.X+x*scale+dx, b.Min.Y+y*scale+dy).RGBA()
					sr, sg, sb = sr+r>>8, sg+g>>8, sb+bl>>8
				}
			}
			out.SetRGBA(x, y, color.RGBA{uint8(sr / n), uint8(sg / n), uint8(sb / n), 255})
		}
	}
	return out
}

// palette picks the most common colours (by 4-bit cells, each the mean of
// its pixels), skipping one too close to a colour already taken, so that
// the background's shades do not use up the palette before the text's.
func palette(img *image.RGBA, colors int) color.Palette {
	type cell struct{ n, r, g, b int }
	cells := map[uint16]*cell{}
	for i := 0; i+3 < len(img.Pix); i += 4 {
		r, g, b := int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])
		k := uint16(r>>4)<<8 | uint16(g>>4)<<4 | uint16(b>>4)
		c := cells[k]
		if c == nil {
			c = &cell{}
			cells[k] = c
		}
		c.n++
		c.r, c.g, c.b = c.r+r, c.g+g, c.b+b
	}
	keys := make([]uint16, 0, len(cells))
	for k := range cells {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := cells[keys[i]], cells[keys[j]]
		if a.n != b.n {
			return a.n > b.n
		}
		return keys[i] < keys[j]
	})
	var pal color.Palette
	for _, minDist := range []int{48, 24, 0} {
		for _, k := range keys {
			if len(pal) == colors {
				return pal
			}
			c := cells[k]
			m := color.RGBA{uint8(c.r / c.n), uint8(c.g / c.n), uint8(c.b / c.n), 255}
			if !far(pal, m, minDist) {
				continue
			}
			pal = append(pal, m)
		}
	}
	return pal
}

func far(pal color.Palette, c color.RGBA, min int) bool {
	for _, p := range pal {
		q := p.(color.RGBA)
		dr, dg, db := int(q.R)-int(c.R), int(q.G)-int(c.G), int(q.B)-int(c.B)
		if dr*dr+dg*dg+db*db <= min*min {
			return false
		}
	}
	return true
}
