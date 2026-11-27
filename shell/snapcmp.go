package shell

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Snapshot baselines (docs/40-testing.md §2): a selftest's snapshots are
// compared with the ones checked in, the text renderer's as files, the
// pixel renderer's as a hash of each image's pixels (the PNGs are too big
// for git), and a difference fails the case it was taken in.

const (
	// UnstableFile lists the snapshots that differ from run to run however
	// the Shell is held still, in the baselines' directory.
	UnstableFile = "unstable.txt"
	// RunManifest: this run's pixel hashes, beside its PNGs; copied over a
	// baseline manifest it adopts the run (CI's artifact).
	RunManifest = "snapshots.json"
	// snapSignal: the signal bars a compared snapshot shows.
	snapSignal = 3
	// snapBoot: how far the boot animation a compared snapshot shows is.
	snapBoot = 0.6
)

// snapInstant is the time a compared snapshot's status bar and idle screen
// show, in the phone's zone.
var snapInstant = time.Date(2030, time.January, 9, 9, 41, 0, 0, time.UTC)

// snapMasks are what the apps draw differently in every run (the time of
// day, the date beside a time, the port a local server got, a device's
// random id, the hotspot's name): the letters and digits of a match, or of
// its first group, become 0, which keeps the layout.
var snapMasks = []*regexp.Regexp{
	regexp.MustCompile(`\b\d{2}-\d{2} \d{1,2}:\d{2}\b`),
	regexp.MustCompile(`\b\d{1,2}:\d{2}(?::\d{2})?\b`),
	regexp.MustCompile(`127\.0\.0\.1:(\d+)`),
	regexp.MustCompile(`\bPH-([0-9a-f]{12})\b`),
	regexp.MustCompile(`\bKario-([0-9A-F]{4})\b`),
}

// snapKeep are the frame members not masked: files, addresses, ids and
// styles, which the renderer looks up rather than draws.
var snapKeep = map[string]bool{
	"t": true, "id": true, "key": true, "kind": true, "screen": true, "style": true, "theme": true, "layout": true,
	"icon": true, "icon_image": true, "image": true, "photo": true, "pip": true, "surface": true, "wallpaper": true,
	"src": true, "stream": true,
	"pixels": true, "fg": true, "bg": true, "rgb": true, "fill": true, "box": true, "transition": true, "motion": true,
}

// snapView is what a compared snapshot hides; the Core holds it while the
// snapshot is drawn.
type snapView struct {
	masks []*regexp.Regexp
}

func (v *snapView) text(s string) string {
	for _, re := range v.masks {
		s = maskMatches(s, re, func(string) string { return "#" })
	}
	for _, re := range snapMasks {
		s = maskMatches(s, re, zeroed)
	}
	return s
}

// frame masks the strings of a frame (before the renderer lays them out,
// so a masked address wraps the same).
func (v *snapView) frame(raw []byte) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var doc any
	if err := d.Decode(&doc); err != nil {
		return nil, err
	}
	return json.Marshal(v.walk("", doc))
}

func (v *snapView) walk(key string, x any) any {
	switch t := x.(type) {
	case string:
		if !snapKeep[key] {
			return v.text(t)
		}
	case map[string]any:
		for k, e := range t {
			t[k] = v.walk(k, e)
		}
	case []any:
		for i, e := range t {
			t[i] = v.walk(key, e)
		}
	}
	return x
}

func maskMatches(s string, re *regexp.Regexp, with func(string) string) string {
	all := re.FindAllStringSubmatchIndex(s, -1)
	if all == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range all {
		from, to := m[0], m[1]
		if len(m) >= 4 && m[2] >= 0 {
			from, to = m[2], m[3]
		}
		b.WriteString(s[last:from])
		b.WriteString(with(s[from:to]))
		last = to
	}
	b.WriteString(s[last:])
	return b.String()
}

func zeroed(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x80 && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return '0'
		}
		return r
	}, s)
}

// shownNow is the time the status bar and the idle screen show.
func (c *Core) shownNow() time.Time {
	if c.snapping != nil {
		t := snapInstant
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, c.zone())
	}
	return c.now()
}

// shownPower is the battery the status bar shows: the simulated one in a
// compared snapshot, unless the script set it (a laptop's own would show).
func (c *Core) shownPower() Power {
	if c.snapping != nil && c.powerOverride == nil {
		return simulatedPower
	}
	return c.power()
}

func (c *Core) shownSignal() int {
	if c.snapping != nil {
		return snapSignal
	}
	return c.tel.Signal
}

// snapRule is a line of UnstableFile.
type snapRule struct {
	name string // a path.Match pattern
	verb string // skip, skip-text, skip-pixel, mask
	re   *regexp.Regexp
}

// Baselines compares a run's snapshots with Dir's: text/<device>/<name>.txt
// and pixel/<renderer>/<device>[-<fonts>].json.
type Baselines struct {
	Dir    string
	Device string
	// Update records this run's snapshots as the baselines instead.
	Update bool
	// Images keeps the PNGs of the pixel baselines (not in git) for diff
	// images: a run writes each one that matches, Update every one.
	Images string
	// PixelDir is where the renderer writes this run's PNGs.
	PixelDir string

	rules  []snapRule
	pixel  *pixelBaseline
	missed bool // said once that this environment has no pixel baselines
	counts map[string]int
}

type pixelBaseline struct {
	path string
	have bool // the file was there: the snapshots are compared
	m    pixelManifest
}

type pixelManifest struct {
	Renderer  string            `json:"renderer"`
	Device    string            `json:"device"`
	Fonts     string            `json:"fonts,omitempty"`
	Snapshots map[string]string `json:"snapshots"`
}

// LoadBaselines reads dir's UnstableFile.
func LoadBaselines(dir, device string) (*Baselines, error) {
	b := &Baselines{Dir: dir, Device: device, counts: map[string]int{}}
	raw, err := os.ReadFile(filepath.Join(dir, UnstableFile))
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	b.rules, err = parseUnstable(string(raw))
	return b, err
}

func parseUnstable(text string) ([]snapRule, error) {
	var rules []snapRule
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.SplitN(line, " ", 3)
		r := snapRule{name: f[0]}
		if len(f) > 1 {
			r.verb = f[1]
		}
		if _, err := path.Match(r.name, ""); err != nil {
			return nil, fmt.Errorf("%s:%d: %v", UnstableFile, n+1, err)
		}
		switch r.verb {
		case "skip", "skip-text", "skip-pixel":
			if len(f) > 2 {
				return nil, fmt.Errorf("%s:%d: %s takes no regexp", UnstableFile, n+1, r.verb)
			}
		case "mask":
			if len(f) < 3 {
				return nil, fmt.Errorf("%s:%d: mask needs a regexp", UnstableFile, n+1)
			}
			re, err := regexp.Compile(strings.TrimSpace(f[2]))
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", UnstableFile, n+1, err)
			}
			r.re = re
		default:
			return nil, fmt.Errorf("%s:%d: want <name> skip|skip-text|skip-pixel|mask <regexp>", UnstableFile, n+1)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func (b *Baselines) view(name string) *snapView {
	v := &snapView{}
	for _, r := range b.rules {
		if ok, _ := path.Match(r.name, name); ok && r.re != nil {
			v.masks = append(v.masks, r.re)
		}
	}
	return v
}

// compared: the snapshot's kind ("text", "pixel") is compared.
func (b *Baselines) compared(name, kind string) bool {
	for _, r := range b.rules {
		if ok, _ := path.Match(r.name, name); ok && (r.verb == "skip" || r.verb == "skip-"+kind) {
			return false
		}
	}
	return true
}

func (b *Baselines) textPath(name string) string {
	return filepath.Join(b.Dir, "text", b.Device, name+".txt")
}

// begin clears what a previous run left beside this one's snapshots.
func (b *Baselines) begin(r *Runner) {
	if r.SnapDir != "" {
		os.RemoveAll(filepath.Join(r.SnapDir, "diff"))
	}
	if b.PixelDir != "" && r.GUI != nil {
		os.RemoveAll(filepath.Join(b.PixelDir, "diff"))
		os.Remove(filepath.Join(b.PixelDir, RunManifest))
	}
}

// snapshot takes one snapshot of a compared run: held still, masked,
// compared (or recorded).
func (r *Runner) snapshot(name string) error {
	b := r.Baselines
	if b != nil {
		// The text renderer's idle screen is a document the tick rebuilds.
		r.C.snapping = b.view(name)
		r.C.refreshSystemTop()
		defer func() {
			r.C.snapping = nil
			r.C.refreshSystemTop()
		}()
	}
	if r.GUI != nil {
		r.C.Render()
		if err := r.GUI.Snap(name); err != nil {
			return err
		}
		if b != nil {
			if err := b.pixelSnap(r, name); err != nil {
				return err
			}
		}
	}
	if r.SnapDir == "" {
		return nil
	}
	_ = os.MkdirAll(r.SnapDir, 0o755)
	can := r.C.Render()
	text, ans := can.Plain(), can.ANSIMasked()
	if v := r.C.snapping; v != nil {
		text, ans = v.text(text), v.text(ans)
	}
	if err := os.WriteFile(filepath.Join(r.SnapDir, name+".txt"), []byte(text), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.SnapDir, name+".ans"), []byte(ans), 0o644); err != nil {
		return err
	}
	if b != nil {
		return b.textSnap(r, name, text)
	}
	return nil
}

// differs fails the running case at its end and says why now.
func (r *Runner) differs(kind, name, why string) {
	r.Baselines.counts[kind+" differ"]++
	r.snapDiffs = append(r.snapDiffs, kind+" "+name)
	fmt.Fprintf(r.Out, "  SNAPSHOT-DIFF: %s %s: %s\n", kind, name, why)
}

// withSnaps carries the comparison so far over a restart in place.
func (r *Runner) withSnaps(res Resume) Resume {
	if r.Baselines != nil {
		res.SnapCounts, res.SnapDiffs = r.Baselines.counts, r.snapDiffs
	}
	return res
}

func (b *Baselines) textSnap(r *Runner, name, text string) error {
	if !b.compared(name, "text") {
		b.counts["text not compared"]++
		return nil
	}
	file := b.textPath(name)
	if b.Update {
		b.counts["text recorded"]++
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		return os.WriteFile(file, []byte(text), 0o644)
	}
	want, err := os.ReadFile(file)
	if err != nil {
		r.differs("text", name, fmt.Sprintf("no baseline %s (make selftest-baselines)", file))
		return nil
	}
	if string(want) == text {
		b.counts["text same"]++
		return nil
	}
	out := filepath.Join(r.SnapDir, "diff", name+".diff")
	_ = os.MkdirAll(filepath.Dir(out), 0o755)
	if err := os.WriteFile(out, []byte(textDiff(file, filepath.Join(r.SnapDir, name+".txt"), string(want), text)), 0o644); err != nil {
		return err
	}
	r.differs("text", name, "differs from "+file+", see "+out)
	return nil
}

// textDiff lists the lines that differ, by line number (a snapshot is a
// screen: its lines do not move).
func textDiff(wantName, gotName, want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var s strings.Builder
	fmt.Fprintf(&s, "--- %s\n+++ %s\n", wantName, gotName)
	for i := 0; i < max(len(w), len(g)); i++ {
		var a, c string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			c = g[i]
		}
		if a != c {
			fmt.Fprintf(&s, "@@ line %d\n-%s\n+%s\n", i+1, a, c)
		}
	}
	return s.String()
}

func (b *Baselines) pixelSnap(r *Runner, name string) error {
	if b.PixelDir == "" {
		return nil
	}
	head := b.manifestFor(r)
	if !b.compared(name, "pixel") {
		b.counts["pixel not compared"]++
		return nil
	}
	file := filepath.Join(b.PixelDir, name+".png")
	img, err := readPNG(file)
	if err != nil {
		return fmt.Errorf("pixel snapshot %s: %v", name, err)
	}
	sum := pixelSum(img)
	if err := mergeManifest(filepath.Join(b.PixelDir, RunManifest), head, name, sum); err != nil {
		return err
	}
	base := b.pixel
	if b.Update {
		b.counts["pixel recorded"]++
		base.m.Snapshots[name] = sum
		base.have = true
		b.keepImage(name, file)
		return writeManifest(base.path, base.m)
	}
	if !base.have {
		b.counts["pixel without baselines"]++
		if !b.missed {
			b.missed = true
			fmt.Fprintf(r.Out, "  SNAPSHOT: no pixel baselines %s for this renderer, form factor and fonts: not compared (%s)\n", base.path, RunManifest)
		}
		return nil
	}
	want, ok := base.m.Snapshots[name]
	switch {
	case !ok:
		r.differs("pixel", name, fmt.Sprintf("no baseline in %s (make pixel-baselines)", base.path))
	case want != sum:
		r.differs("pixel", name, b.pixelDiff(name, want, img))
	default:
		b.counts["pixel same"]++
		b.keepImage(name, file)
	}
	return nil
}

// manifestFor loads the baseline manifest of the run's renderer, form
// factor and fonts once, and is the run manifest's header.
func (b *Baselines) manifestFor(r *Runner) pixelManifest {
	if b.pixel == nil {
		m := pixelManifest{Renderer: r.GUI.Info().Name, Device: b.Device, Snapshots: map[string]string{}}
		if m.Renderer == "phone-paint" {
			m.Fonts = paintFonts()
		}
		stem := m.Device
		if m.Fonts != "" {
			stem += "-" + m.Fonts
		}
		p := &pixelBaseline{path: filepath.Join(b.Dir, "pixel", m.Renderer, stem+".json"), m: m}
		if raw, err := os.ReadFile(p.path); err == nil {
			var have pixelManifest
			if json.Unmarshal(raw, &have) == nil && have.Snapshots != nil {
				p.have, p.m.Snapshots = true, have.Snapshots
			}
		}
		b.pixel = p
	}
	m := b.pixel.m
	m.Snapshots = nil
	return m
}

// pixelDiff writes the baseline beside this run's and their difference, if
// the baseline's image is kept here.
func (b *Baselines) pixelDiff(name, want string, img image.Image) string {
	why := fmt.Sprintf("hash %s, the baseline's %s", pixelSum(img), want)
	if b.Images == "" {
		return why
	}
	base, err := readPNG(filepath.Join(b.Images, name+".png"))
	if err != nil || pixelSum(base) != want {
		return why + "; no image of the baseline in " + b.Images + " to draw the difference"
	}
	out, n := diffImage(base, img)
	file := filepath.Join(b.PixelDir, "diff", name+".png")
	_ = os.MkdirAll(filepath.Dir(file), 0o755)
	if err := writePNG(file, out); err != nil {
		return why + "; " + err.Error()
	}
	return fmt.Sprintf("%d pixels differ, see %s (the baseline, this run, the difference in red)", n, file)
}

func (b *Baselines) keepImage(name, file string) {
	if b.Images == "" {
		return
	}
	dst := filepath.Join(b.Images, name+".png")
	src, err := os.ReadFile(file)
	if err != nil {
		return
	}
	if have, err := os.ReadFile(dst); err == nil && bytes.Equal(have, src) {
		return
	}
	if os.MkdirAll(b.Images, 0o755) == nil {
		_ = os.WriteFile(dst, src, 0o644)
	}
}

// end says what was compared and, after a whole run that recorded them,
// drops the baselines of snapshots the script no longer takes.
func (b *Baselines) end(r *Runner, script string) {
	if b.Update && r.Only == nil {
		names := map[string]bool{}
		for _, line := range strings.Split(script, "\n") {
			if n, ok := strings.CutPrefix(strings.TrimSpace(line), "snap "); ok {
				names[strings.TrimSpace(n)] = true
			}
		}
		keep := func(name, kind string) bool { return names[name] && b.compared(name, kind) }
		if r.SnapDir != "" {
			files, _ := filepath.Glob(filepath.Join(b.Dir, "text", b.Device, "*.txt"))
			for _, f := range files {
				if !keep(strings.TrimSuffix(filepath.Base(f), ".txt"), "text") {
					os.Remove(f)
					b.counts["text dropped"]++
				}
			}
		}
		if p := b.pixel; p != nil && p.have {
			for name := range p.m.Snapshots {
				if !keep(name, "pixel") {
					delete(p.m.Snapshots, name)
					b.counts["pixel dropped"]++
				}
			}
			if err := writeManifest(p.path, p.m); err != nil {
				fmt.Fprintln(r.Out, "SNAPSHOT:", err)
			}
		}
	}
	fmt.Fprintf(r.Out, "PHONE-SNAPSHOTS: %s\n", SnapCounts(b.counts))
	if b.pixel != nil {
		fmt.Fprintf(r.Out, "PHONE-SNAPSHOTS-PIXEL: baselines %s; this run's hashes %s\n", b.pixel.path, filepath.Join(b.PixelDir, RunManifest))
	}
}

// SnapCounts is the PHONE-SNAPSHOTS line's account of counts.
func SnapCounts(counts map[string]int) string {
	var parts []string
	for k, n := range counts {
		parts = append(parts, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// Counts are what the run compared so far, as PHONE-SNAPSHOTS counts them.
func (b *Baselines) Counts() map[string]int { return maps.Clone(b.counts) }

// PixelBaselines is the pixel baselines' file the run compared with ("" before
// its first pixel snapshot).
func (b *Baselines) PixelBaselines() string {
	if b.pixel == nil {
		return ""
	}
	return b.pixel.path
}

// MergeRunManifests writes into dst the pixel hashes of the run manifests
// several runs left (a run in parts, each part in a directory of its own).
func MergeRunManifests(dst string, files []string) error {
	var m pixelManifest
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var one pixelManifest
		if err := json.Unmarshal(raw, &one); err != nil {
			return fmt.Errorf("%s: %v", f, err)
		}
		if m.Snapshots == nil {
			m = one
			m.Snapshots = map[string]string{}
		}
		maps.Copy(m.Snapshots, one.Snapshots)
	}
	if m.Snapshots == nil {
		return nil
	}
	return writeManifest(dst, m)
}

func readPNG(file string) (image.Image, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func writePNG(file string, img image.Image) error {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(file, buf.Bytes(), 0o644)
}

// pixelSum is the hash of an image's pixels, not of its file: encoders and
// their versions write the same pixels differently.
func pixelSum(img image.Image) string {
	bnd := img.Bounds()
	rgba := image.NewNRGBA(image.Rect(0, 0, bnd.Dx(), bnd.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, bnd.Min, draw.Src)
	h := sha256.New()
	_ = binary.Write(h, binary.BigEndian, [2]uint32{uint32(bnd.Dx()), uint32(bnd.Dy())})
	h.Write(rgba.Pix)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// diffImage is the baseline, this run's and between them in red the pixels
// that differ over this run's dimmed; and how many differ.
func diffImage(base, cur image.Image) (*image.NRGBA, int) {
	bb, cb := base.Bounds(), cur.Bounds()
	w, h := max(bb.Dx(), cb.Dx()), max(bb.Dy(), cb.Dy())
	const gap = 8
	out := image.NewNRGBA(image.Rect(0, 0, 3*w+2*gap, h))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.NRGBA{128, 128, 128, 255}), image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(0, 0, bb.Dx(), bb.Dy()), base, bb.Min, draw.Src)
	draw.Draw(out, image.Rect(w+gap, 0, w+gap+cb.Dx(), cb.Dy()), cur, cb.Min, draw.Src)
	n := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			in := func(r image.Rectangle) bool { return x < r.Dx() && y < r.Dy() }
			var a, c color.NRGBA
			if in(bb) {
				a = color.NRGBAModel.Convert(base.At(bb.Min.X+x, bb.Min.Y+y)).(color.NRGBA)
			}
			if in(cb) {
				c = color.NRGBAModel.Convert(cur.At(cb.Min.X+x, cb.Min.Y+y)).(color.NRGBA)
			}
			px := color.NRGBA{255, 0, 0, 255}
			if a == c && in(bb) == in(cb) {
				g := uint8((uint32(c.R)*30 + uint32(c.G)*59 + uint32(c.B)*11) / 100 / 3)
				px = color.NRGBA{g, g, g, 255}
			} else {
				n++
			}
			out.SetNRGBA(2*(w+gap)+x, y, px)
		}
	}
	return out, n
}

func mergeManifest(file string, head pixelManifest, name, sum string) error {
	m := head
	if raw, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	if m.Snapshots == nil {
		m.Snapshots = map[string]string{}
	}
	m.Snapshots[name] = sum
	return writeManifest(file, m)
}

func writeManifest(file string, m pixelManifest) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(raw, '\n'), 0o644)
}

// paintFontDirs and paintFallbacks are where phone-paint looks for its
// fonts; they must stay as paint/src/canvas.rs Fonts::load has them.
var (
	paintFontDirs  = []string{"/usr/share/fonts/noto-cjk", "/usr/share/fonts/google-noto-cjk", "/usr/share/fonts/opentype/noto"}
	paintFallbacks = []string{
		"/usr/share/fonts/truetype/DejaVuSans.ttf",
		"/usr/share/fonts/dejavu/DejaVuSans.ttf",
		"/usr/share/fonts/dejavu-sans-fonts/DejaVuSans.ttf",
	}
)

// paintFonts is a fingerprint of the font files phone-paint draws with: the
// same frames are other pixels with another release of the fonts, so each
// set of fonts has its own baselines.
func paintFonts() string {
	dirs := paintFontDirs
	if d := os.Getenv("PHONE_PAINT_FONTS"); d != "" {
		dirs = append([]string{d}, dirs...)
	}
	find := func(stem string) string {
		for _, d := range dirs {
			ents, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			names := make([]string, 0, len(ents))
			for _, e := range ents {
				names = append(names, e.Name())
			}
			slices.Sort(names)
			for _, n := range names {
				if strings.HasPrefix(n, stem) && (strings.HasSuffix(n, ".ttc") || strings.HasSuffix(n, ".otc")) {
					return filepath.Join(d, n)
				}
			}
		}
		return ""
	}
	h := sha256.New()
	for _, f := range append([]string{find("NotoSansCJK-Regular"), find("NotoSansCJK-Bold")}, paintFallbacks...) {
		fh, err := os.Open(f)
		if err != nil {
			h.Write([]byte{0})
			continue
		}
		_, _ = io.Copy(h, fh)
		fh.Close()
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}
