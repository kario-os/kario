package shell

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The media library (Android's MediaStore): photos belong to the system,
// not to the app that took them, and live where no app can read them. An
// app sees a photo when the user hands it over (the picker, the camera
// viewfinder) or when it holds the photos permission.

type mediaItem struct {
	ID     string   `json:"id"` // p<N>
	Name   string   `json:"name"`
	W      int      `json:"w"`
	H      int      `json:"h"`
	At     int64    `json:"at"`
	Size   int64    `json:"size"`
	Ext    string   `json:"ext,omitempty"`    // png | jpg | webm | mp4 ("" from before JPEG support: png)
	DurMS  int64    `json:"dur_ms,omitempty"` // videos
	Grants []string `json:"grants,omitempty"` // apps the user handed it to
}

// video: a video, not a photo. Its size, length and still (the thumbnail)
// come from mediad (probeVideos); W is 0 until then.
func (it *mediaItem) video() bool { return it.Ext == "webm" || it.Ext == "mp4" }

func (it *mediaItem) ext() string {
	if it.Ext == "" {
		return "png"
	}
	return it.Ext
}

type mediaStore struct {
	dir   string
	items []*mediaItem
	seq   int
	cache map[string]*FrameImage
}

const (
	maxPhotoBytes = 8 << 20
	maxPhotoSide  = 4096
	thumbSide     = 240 // a photo grid's cell; older, smaller thumbnails are made again
)

// sampleVersion: libraries seeded before it get the samples added since,
// and lose the drawn placeholders that stood in for photos before.
const sampleVersion = 2

// openMedia opens the library in dir. It starts with the image's sample
// photos and videos (examples: share/phone/examples, its photos and videos
// folders), or drawn photos without them.
func openMedia(dir, examples string) *mediaStore {
	m := &mediaStore{dir: dir, cache: map[string]*FrameImage{}}
	_ = os.MkdirAll(filepath.Join(dir, "thumbs"), 0o700)
	if raw, err := os.ReadFile(filepath.Join(dir, "index.json")); err == nil {
		_ = json.Unmarshal(raw, &m.items)
	}
	for _, it := range m.items {
		var n int
		if _, err := fmt.Sscanf(it.ID, "p%d", &n); err == nil {
			m.seq = max(m.seq, n)
		}
	}
	mark := filepath.Join(dir, "samples")
	if raw, _ := os.ReadFile(mark); len(m.items) == 0 || strings.TrimSpace(string(raw)) != fmt.Sprint(sampleVersion) {
		m.seed(examples)
		_ = os.WriteFile(mark, []byte(fmt.Sprint(sampleVersion)), 0o600)
	}
	return m
}

// sampleName is the library name of a sample photo or video.
func sampleName(base string) string { return tr.T("Sample: %s", base) }

func (m *mediaStore) seed(examples string) {
	var files []string
	if examples != "" {
		photos, _ := filepath.Glob(filepath.Join(examples, "photos", "*.jpg"))
		videos, _ := filepath.Glob(filepath.Join(examples, "videos", "*.webm"))
		files = append(photos, videos...)
	}
	have := map[string]bool{}
	for _, it := range append([]*mediaItem(nil), m.items...) {
		if strings.HasPrefix(it.Name, sampleName("")) && it.ext() == "png" && len(files) > 0 {
			m.remove(it)
			continue
		}
		have[it.Name] = true
	}
	// Newest first in the library: the first file in name order last.
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	for i, f := range files {
		ext := filepath.Ext(f)
		name := sampleName(strings.TrimSuffix(filepath.Base(f), ext))
		if have[name] {
			continue
		}
		at := time.Now().Add(-time.Duration(len(files)-i) * time.Hour)
		if ext == ".webm" {
			_, _ = m.addVideo(f, name, strings.TrimPrefix(filepath.Ext(f), "."), at)
		} else if raw, err := os.ReadFile(f); err == nil {
			_, _ = m.add(raw, name, at)
		}
	}
	if len(m.items) > 0 {
		return
	}
	for i, name := range []string{tr.T("Seaside"), tr.T("Valley"), tr.T("Evening")} {
		if raw, err := encodePNG(samplePhoto(fmt.Sprintf("gen:sample%d", i), 480, 360)); err == nil {
			_, _ = m.add(raw, sampleName(name), time.Now().Add(-time.Duration(3-i)*time.Hour))
		}
	}
}

func (m *mediaStore) save() {
	raw, _ := json.MarshalIndent(m.items, "", "  ")
	_ = os.WriteFile(filepath.Join(m.dir, "index.json"), raw, 0o600)
}

// add stores a PNG or JPEG (the format is read from the data, its header
// checked before decoding) and makes its thumbnail.
func (m *mediaStore) add(raw []byte, name string, at time.Time) (*mediaItem, error) {
	if len(raw) > maxPhotoBytes {
		return nil, errors.New("photo too large")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "png" && format != "jpeg") || cfg.Width <= 0 || cfg.Height <= 0 ||
		cfg.Width > maxPhotoSide || cfg.Height > maxPhotoSide {
		return nil, errors.New("not a usable PNG or JPEG")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	m.seq++
	it := &mediaItem{ID: fmt.Sprintf("p%d", m.seq), Name: name, W: cfg.Width, H: cfg.Height, At: at.Unix(), Size: int64(len(raw)),
		Ext: map[string]string{"png": "png", "jpeg": "jpg"}[format]}
	if err := os.WriteFile(m.path(it), raw, 0o600); err != nil {
		return nil, err
	}
	if thumb, err := encodePNG(scaleCrop(img, thumbSide, thumbSide)); err == nil {
		_ = os.WriteFile(m.thumb(it.ID), thumb, 0o600)
	}
	m.items = append(m.items, it)
	m.save()
	return it, nil
}

// addVideo copies a video into the library; probeVideos fills in the rest.
// ext is its container (webm), the file at path may have no extension.
func (m *mediaStore) addVideo(path, name, ext string, at time.Time) (*mediaItem, error) {
	src, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	m.seq++
	it := &mediaItem{ID: fmt.Sprintf("p%d", m.seq), Name: name, At: at.Unix(), Ext: ext}
	dst, err := os.OpenFile(m.path(it), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(m.path(it))
		return nil, err
	}
	it.Size = n
	m.items = append(m.items, it)
	m.save()
	return it, nil
}

func (m *mediaStore) get(id string) *mediaItem {
	id = strings.TrimPrefix(id, ui.MediaPrefix)
	for _, it := range m.items {
		if it.ID == id {
			return it
		}
	}
	return nil
}

func (m *mediaStore) path(it *mediaItem) string { return filepath.Join(m.dir, it.ID+"."+it.ext()) }

// remove deletes a photo, its thumbnail and every grant to it.
func (m *mediaStore) remove(it *mediaItem) {
	_ = os.Remove(m.path(it))
	_ = os.Remove(m.thumb(it.ID))
	for i, x := range m.items {
		if x == it {
			m.items = append(m.items[:i], m.items[i+1:]...)
			break
		}
	}
	m.cache = map[string]*FrameImage{}
	m.save()
}
func (m *mediaStore) thumb(id string) string { return filepath.Join(m.dir, "thumbs", id+".png") }

// remakeThumb makes a photo's thumbnail again from the photo.
func (m *mediaStore) remakeThumb(it *mediaItem) image.Image {
	raw, err := os.ReadFile(m.path(it))
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	t := scaleCrop(img, thumbSide, thumbSide)
	if b, err := encodePNG(t); err == nil {
		_ = os.WriteFile(m.thumb(it.ID), b, 0o600)
	}
	return t
}

func (m *mediaStore) grant(app string, it *mediaItem) {
	for _, g := range it.Grants {
		if g == app {
			return
		}
	}
	it.Grants = append(it.Grants, app)
	m.save()
}

// newestFirst lists the photos, latest first.
func (m *mediaStore) newestFirst() []*mediaItem {
	out := make([]*mediaItem, 0, len(m.items))
	for i := len(m.items) - 1; i >= 0; i-- {
		out = append(out, m.items[i])
	}
	return out
}

func (it *mediaItem) ui() ui.MediaItem {
	kind := "image"
	if it.video() {
		kind = "video"
	}
	return ui.MediaItem{ID: ui.MediaPrefix + it.ID, Kind: kind, Title: it.Name, W: it.W, H: it.H, Size: it.Size, At: it.At, LenMS: int(it.DurMS),
		Mime: map[string]string{"png": "image/png", "jpg": "image/jpeg", "webm": "video/webm", "mp4": "video/mp4"}[it.ext()]}
}

// mediaAllowed: system screens see every photo; an app the photos it was
// handed, or all of them with the photos permission.
func (c *Core) mediaAllowed(a *App, it *mediaItem) bool {
	if a == nil || a.system {
		return true
	}
	if c.broker.holds(a, ui.PermPhotos) {
		return true
	}
	for _, g := range it.Grants {
		if g == a.M.ID {
			return true
		}
	}
	return false
}

// photoImage decodes and scales a photo to fit maxW x maxH (cached).
func (c *Core) photoImage(src string, maxW, maxH int, secure bool) *FrameImage {
	it := c.media.get(src)
	if it == nil || !c.mediaAllowed(c.current(), it) {
		return nil
	}
	key := fmt.Sprintf("%s@%dx%d", it.ID, maxW, maxH)
	if f := c.media.cache[key]; f != nil {
		return &FrameImage{W: f.W, H: f.H, RGB: f.RGB, Secure: secure}
	}
	raw, err := os.ReadFile(c.media.path(it))
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	scale := math.Min(float64(maxW)/float64(it.W), float64(maxH)/float64(it.H))
	w, h := max(1, int(float64(it.W)*scale)), max(1, int(float64(it.H)*scale))
	small := scaleCrop(img, w, h)
	pix := make([]byte, w*h*3)
	for i := 0; i < w*h; i++ {
		copy(pix[3*i:3*i+3], small.Pix[4*i:4*i+3])
	}
	f := encodeRGB(w, h, pix, false, false)
	if len(c.media.cache) > 64 {
		c.media.cache = map[string]*FrameImage{}
	}
	c.media.cache[key] = f
	return &FrameImage{W: f.W, H: f.H, RGB: f.RGB, Secure: secure}
}

// thumbImage is a photo's stored thumbnail, made again when missing or
// from before thumbnails were this large.
func (c *Core) thumbImage(it *mediaItem) image.Image {
	raw, err := os.ReadFile(c.media.thumb(it.ID))
	var img image.Image
	if err == nil {
		img, _, err = image.Decode(bytes.NewReader(raw))
	}
	if err != nil || img.Bounds().Dx() < min(thumbSide, it.W) {
		img = c.media.remakeThumb(it)
	}
	return img
}

// thumbPixels is a photo's thumbnail as inline frame pixels, side × side,
// for renderers without surfaces: the renderer gets what it draws, not
// access to the library.
func (c *Core) thumbPixels(it *mediaItem, side int) *FrameImage {
	key := fmt.Sprintf("thumb:%s@%d", it.ID, side)
	if f := c.media.cache[key]; f != nil {
		return f
	}
	img := c.thumbImage(it)
	if img == nil {
		return nil
	}
	w, h := side, side
	small := scaleCrop(img, w, h)
	pix := make([]byte, w*h*3)
	for i := 0; i < w*h; i++ {
		copy(pix[3*i:3*i+3], small.Pix[4*i:4*i+3])
	}
	f := encodeRGB(w, h, pix, false, false)
	if len(c.media.cache) > 64 {
		c.media.cache = map[string]*FrameImage{}
	}
	c.media.cache[key] = f
	return f
}

// scaleCrop fills w x h from the centre of img (area averaging).
func scaleCrop(img image.Image, w, h int) *image.RGBA {
	b := img.Bounds()
	sw, sh := float64(b.Dx()), float64(b.Dy())
	s := math.Max(float64(w)/sw, float64(h)/sh)
	ox, oy := (sw-float64(w)/s)/2, (sh-float64(h)/s)/2
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := int(oy+float64(y)/s), int(oy+float64(y+1)/s)
		for x := 0; x < w; x++ {
			x0, x1 := int(ox+float64(x)/s), int(ox+float64(x+1)/s)
			var r, g, bl, n uint32
			for yy := y0; yy <= max(y0, y1-1); yy++ {
				for xx := x0; xx <= max(x0, x1-1); xx++ {
					cr, cg, cb, _ := img.At(b.Min.X+min(xx, b.Dx()-1), b.Min.Y+min(yy, b.Dy()-1)).RGBA()
					r, g, bl, n = r+cr>>8, g+cg>>8, bl+cb>>8, n+1
				}
			}
			i := out.PixOffset(x, y)
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return out
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

// genPixel is the procedural landscape standing in for photos ("gen:" images
// and the sample photos).
func genPixel(src string, u, v float64) cv.RGB {
	h := fnv.New32a()
	h.Write([]byte(src))
	seed := float64(h.Sum32()%1000) / 1000
	switch {
	case v < 0.55+0.1*math.Sin(u*6+seed*9):
		return cv.RGB{R: uint8(80 + 100*seed), G: uint8(140 + 60*v), B: 230}
	case math.Hypot(u-0.7-0.2*seed, v-0.3) < 0.12:
		return cv.RGB{R: 250, G: 220, B: 90}
	}
	return cv.RGB{R: uint8(40 + 80*seed), G: uint8(120 + 60*u), B: uint8(60 + 40*v)}
}

func samplePhoto(src string, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := genPixel(src, float64(x)/float64(w), float64(y)/float64(h))
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
		}
	}
	return img
}

// revokeApp drops an uninstalled app from every photo it was handed.
func (m *mediaStore) revokeApp(app string) {
	for _, it := range m.items {
		for i, g := range it.Grants {
			if g == app {
				it.Grants = append(it.Grants[:i], it.Grants[i+1:]...)
				break
			}
		}
	}
	m.save()
}

// MediaInfo is what phone-mediad says about its camera (selftest).
func (c *Core) MediaInfo() map[string]any {
	info := map[string]any{"camera": ""}
	_ = c.broker.rpc(svc.Mediad, svc.MediaInfo, nil, &info)
	if _, ok := info["camera"].(string); !ok {
		info["camera"] = ""
	}
	return info
}
