// snap2png renders phone-shell ANSI snapshots (*.ans, secure cells already
// masked) to PNG for documentation. Dev-only tool, not part of the image.
//
//	go run . -out ../../../img ../../build/snapshots/*.ans
package main

import (
	"flag"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

const (
	cellW, cellH = 12, 26
	baseline     = 20
	pad          = 18
)

type cell struct {
	r      rune
	fg, bg color.RGBA
	bold   bool
	wide   bool
}

type fonts struct {
	latin, cjk         font.Face
	latinFont, cjkFont *sfnt.Font
}

func loadFace(f *sfnt.Font, size float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		log.Fatal(err)
	}
	return face
}

func loadFonts(latinPath, cjkPath string) *fonts {
	lb, err := os.ReadFile(latinPath)
	if err != nil {
		log.Fatal(err)
	}
	lf, err := opentype.Parse(lb)
	if err != nil {
		log.Fatal(err)
	}
	cb, err := os.ReadFile(cjkPath)
	if err != nil {
		log.Fatal(err)
	}
	coll, err := opentype.ParseCollection(cb)
	if err != nil {
		log.Fatal(err)
	}
	var cf *sfnt.Font
	for i := 0; i < coll.NumFonts(); i++ {
		f, _ := coll.Font(i)
		name, _ := f.Name(nil, sfnt.NameIDFamily)
		if name == "Noto Sans Mono CJK SC" {
			cf = f
		}
	}
	if cf == nil {
		cf, _ = coll.Font(0)
	}
	return &fonts{latin: loadFace(lf, 19), cjk: loadFace(cf, 22), latinFont: lf, cjkFont: cf}
}

func parse(data string) [][]cell {
	var rows [][]cell
	for _, line := range strings.Split(strings.TrimRight(data, "\n"), "\n") {
		var row []cell
		cur := cell{fg: color.RGBA{220, 220, 220, 255}, bg: color.RGBA{0, 0, 0, 255}}
		for i := 0; i < len(line); {
			if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
				end := strings.IndexByte(line[i:], 'm')
				if end < 0 {
					break
				}
				applySGR(&cur, line[i+2:i+end])
				i += end + 1
				continue
			}
			r, size := []rune(line[i:])[0], len(string([]rune(line[i:])[0]))
			c := cur
			c.r = r
			c.wide = runewidth.RuneWidth(r) == 2
			row = append(row, c)
			i += size
		}
		rows = append(rows, row)
	}
	return rows
}

func applySGR(c *cell, params string) {
	p := strings.Split(params, ";")
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case "0", "":
			c.bold = false
		case "1":
			c.bold = true
		case "38", "48":
			if i+4 < len(p) && p[i+1] == "2" {
				r, _ := strconv.Atoi(p[i+2])
				g, _ := strconv.Atoi(p[i+3])
				b, _ := strconv.Atoi(p[i+4])
				col := color.RGBA{uint8(r), uint8(g), uint8(b), 255}
				if p[i] == "38" {
					c.fg = col
				} else {
					c.bg = col
				}
				i += 4
			}
		}
	}
}

func fill(img *image.RGBA, x, y, w, h int, col color.RGBA) {
	draw.Draw(img, image.Rect(x, y, x+w, y+h), image.NewUniform(col), image.Point{}, draw.Src)
}

// procedural draws block and box glyphs exactly to the cell so borders and
// pixels line up regardless of font metrics.
func procedural(img *image.RGBA, x, y, w int, c cell) bool {
	fg := c.fg
	mx, my, t := x+w/2-1, y+cellH/2-1, 2
	hline := func(x0, x1 int) { fill(img, x0, my, x1-x0, t, fg) }
	vline := func(y0, y1 int) { fill(img, mx, y0, t, y1-y0, fg) }
	switch c.r {
	case '▀':
		fill(img, x, y, w, cellH/2, fg)
	case '▌':
		fill(img, x, y, w/2, cellH, fg)
	case '▏':
		fill(img, x, y+3, 2, cellH-6, fg)
	case '▂':
		fill(img, x+1, y+cellH*3/4, w-2, cellH/4, fg)
	case '▄':
		fill(img, x+1, y+cellH/2, w-2, cellH/2, fg)
	case '▆':
		fill(img, x+1, y+cellH/4, w-2, cellH*3/4, fg)
	case '▒':
		for j := 0; j < cellH; j += 2 {
			for i := (j / 2) % 2; i < w; i += 2 {
				img.Set(x+i, y+j, fg)
			}
		}
	case '─', '═':
		hline(x, x+w)
	case '│', '║', '┃':
		if c.r == '┃' {
			t = 3
		}
		vline(y, y+cellH)
	case '┌', '╔':
		hline(mx, x+w)
		vline(my, y+cellH)
	case '┐', '╗':
		hline(x, mx+t)
		vline(my, y+cellH)
	case '└', '╚':
		hline(mx, x+w)
		vline(y, my+t)
	case '┘', '╝':
		hline(x, mx+t)
		vline(y, my+t)
	default:
		return false
	}
	return true
}

func render(rows [][]cell, f *fonts) *image.RGBA {
	maxCols := 0
	for _, r := range rows {
		n := 0
		for _, c := range r {
			n++
			if c.wide {
				n++
			}
		}
		maxCols = max(maxCols, n)
	}
	img := image.NewRGBA(image.Rect(0, 0, maxCols*cellW+2*pad, len(rows)*cellH+2*pad))
	fill(img, 0, 0, img.Bounds().Dx(), img.Bounds().Dy(), color.RGBA{10, 10, 12, 255})
	for j, row := range rows {
		x := pad
		y := pad + j*cellH
		for _, c := range row {
			w := cellW
			if c.wide {
				w = 2 * cellW
			}
			fill(img, x, y, w, cellH, c.bg)
			if c.r != ' ' && !procedural(img, x, y, w, c) {
				face := f.latin
				if c.wide {
					face = f.cjk
				} else if idx, _ := f.latinFont.GlyphIndex(nil, c.r); idx == 0 {
					face = f.cjk
				}
				for k := 0; k <= btoi(c.bold); k++ {
					d := font.Drawer{Dst: img, Src: image.NewUniform(c.fg), Face: face, Dot: fixed.P(x+k, y+baseline)}
					d.DrawString(string(c.r))
				}
			}
			x += w
		}
	}
	return img
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func main() {
	home, _ := os.UserHomeDir()
	out := flag.String("out", ".", "output directory")
	latin := flag.String("latin", filepath.Join(home, ".local/share/fonts/JetBrainsMonoNerdFontMono-Regular.ttf"), "monospace font")
	cjk := flag.String("cjk", "/usr/share/fonts/google-noto-cjk/NotoSansCJK-Regular.ttc", "CJK font collection")
	flag.Parse()
	f := loadFonts(*latin, *cjk)
	_ = os.MkdirAll(*out, 0o755)
	for _, in := range flag.Args() {
		data, err := os.ReadFile(in)
		if err != nil {
			log.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in)) + ".png"
		fh, err := os.Create(filepath.Join(*out, name))
		if err != nil {
			log.Fatal(err)
		}
		if err := png.Encode(fh, render(parse(string(data)), f)); err != nil {
			log.Fatal(err)
		}
		fh.Close()
		log.Printf("wrote %s", filepath.Join(*out, name))
	}
}
