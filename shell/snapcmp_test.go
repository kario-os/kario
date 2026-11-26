package shell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSnapMasks(t *testing.T) {
	v := &snapView{masks: []*regexp.Regexp{regexp.MustCompile(`(\d{4})/article`)}}
	for in, want := range map[string]string{
		"12:57":                            "00:00",
		"sent 9:05 PM":                     "sent 0:00 PM",
		"updated 13:05:13 ago":             "updated 00:00:00 ago",
		"11-21 13:02 paid":                 "00-00 00:00 paid",
		"open http://127.0.0.1:34745/x":    "open http://127.0.0.1:00000/x",
		"device PH-7e0435fb3c15 key":       "device PH-000000000000 key",
		"Kario-EDD5":                       "Kario-0000",
		"http://127.0.0.1:4\n0389/article": "http://127.0.0.1:0\n#/article",
		// What does not change from run to run stays.
		"Kario-Test 16:9 v9.0.0 50%": "Kario-Test 16:9 v9.0.0 50%",
	} {
		if got := v.text(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// A frame's text is masked before the renderer lays it out; the files and
// ids the renderer looks up, and the numbers, are not touched.
func TestSnapViewFrame(t *testing.T) {
	raw := []byte(`{"t":"frame","n":12345678901234567,"status":{"time":"13:57","battery":82},` +
		`"nodes":[{"id":"alarm:12:30","text":"at 12:30","icon":"/icons/12:30.png","src":"http://127.0.0.1:40389/a.png","frac":0.25}]}`)
	got, err := (&snapView{}).frame(raw)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	d := json.NewDecoder(bytes.NewReader(got))
	d.UseNumber()
	if err := d.Decode(&f); err != nil {
		t.Fatal(err)
	}
	node := f["nodes"].([]any)[0].(map[string]any)
	if f["status"].(map[string]any)["time"] != "00:00" || node["text"] != "at 00:00" {
		t.Errorf("text not masked: %s", got)
	}
	if node["icon"] != "/icons/12:30.png" || node["id"] != "alarm:12:30" || node["src"] != "http://127.0.0.1:40389/a.png" {
		t.Errorf("a file or an id masked: %s", got)
	}
	if f["n"].(json.Number) != "12345678901234567" || node["frac"].(json.Number) != "0.25" {
		t.Errorf("numbers changed: %s", got)
	}
}

func TestParseUnstable(t *testing.T) {
	rules, err := parseUnstable("# a comment\n\n41-* skip\n78-charging mask (\\d+)°\nqr skip-pixel\n")
	if err != nil || len(rules) != 3 {
		t.Fatalf("%v %v", rules, err)
	}
	b := &Baselines{rules: rules}
	if b.compared("41-weather", "text") || b.compared("qr", "pixel") || !b.compared("qr", "text") || !b.compared("78-charging", "text") {
		t.Error("skips")
	}
	if got := b.view("78-charging").text("57° 13:05"); got != "#° 00:00" {
		t.Errorf("mask: %q", got)
	}
	for _, bad := range []string{"x", "x frob", "x mask", "x mask (", "x skip extra", "[ skip"} {
		if _, err := parseUnstable(bad); err == nil {
			t.Errorf("%q taken", bad)
		}
	}
}

// Every line of the checked-in list names snapshots the script takes: one
// left behind would hide a new snapshot of the same name.
func TestUnstableNamesSnapshots(t *testing.T) {
	b, err := LoadBaselines(filepath.Join("testdata", "snapshots"), "bar-qwerty")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, line := range strings.Split(SelftestScript, "\n") {
		if n, ok := strings.CutPrefix(line, "snap "); ok {
			names = append(names, strings.TrimSpace(n))
		}
	}
	for _, r := range b.rules {
		found := false
		for _, n := range names {
			if ok, _ := path.Match(r.name, n); ok {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %q is no snapshot of the selftest", UnstableFile, r.name)
		}
	}
}

// The checked-in rules hide what the runs showed changing, and only that.
func TestUnstableMasks(t *testing.T) {
	b, err := LoadBaselines(filepath.Join("testdata", "snapshots"), "bar-qwerty")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, a, b, kept string }{
		{"52-video", " 0:00 / 0:00 ━━─────── playing", " 0:00 / 0:00 ━━━────── playing", "/ 0:00 # playing"},
		{"40-about", "  Kernel  4.18.0-553.72.1.el8_10.x86_64\n  Up      6m1s\n  Apps    21\n", "  Kernel             6.8.0-1017-azure\n  Up    1h2m3s\n  Apps    21\n", "Apps    21"},
		{"10-trusted-confirm", "Order P1763520000001", "Order P1763523600002", "Order P#"},
		{"76-pay-fingerprint", "Order: P1795263012345", "Order: P1795266612346", "Order: P#"},
		{"77-pay-invalidated", "Order: P1795263012345", "Order: P1795266612346", "Order: P#"},
		{"53-tutorial-reminder", "Push token\n  UEgtN2UwNDM1ZmIzYzE1AGNvbS5leGFtcGxl.q8-Zt_0vR2xkLm9TbWF-9J-Rw\n",
			"Push token\n  UEgtYTkxYzA0NGQ3ZTIyAGNvbS5leGFtcGxl.Xw3_Lp-0dGhlcmUtaGVyZQ\n", "Push token\n  #\n"},
		{"53-tutorial-reminder", "  UEgtN2UwNDM1ZmIzYzE1AGNvbS5leGFt…", "  UEgtYTkxYzA0NGQ3ZTIy-GNvbS5leGFt…", "  #…"},
		{"70-usb-trust", "Trust this computer?\n Weather: Beijing Sunny 57°F", "Trust this computer?\n Weather: Beijing Showers 59°F", "computer?\n Weather: #"},
		{"93-nfc-present", "Hold near the reader\n Weather: Beijing Sunny 57°F", "Hold near the reader\n Weather: Beijing Showers 59°F", "reader\n Weather: #"},
		{"47-sim-pin", "\u5df2\u62e8", "\u5df2\u62e8 · 00:01", "#"},
	} {
		v := b.view(c.name)
		if x, y := v.text(c.a), v.text(c.b); x != y || !strings.Contains(x, c.kept) {
			t.Errorf("%s: %q and %q", c.name, x, y)
		}
	}
}

// snapCore is a Shell without apps for the Runner to drive.
func snapCore(t *testing.T) *Core {
	t.Helper()
	root := t.TempDir()
	run, err := os.MkdirTemp("", "snapcmp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	app := filepath.Join(root, "apps", "test.still")
	_ = os.MkdirAll(app, 0o755)
	if err := os.WriteFile(filepath.Join(app, "manifest.json"), []byte(`{"id":"test.still","name":"Still","exec":"none"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{W: 40, H: 24, AppsDir: filepath.Join(root, "apps"), AppBinDir: filepath.Join(root, "bin"), RunDir: run,
		StateDir: filepath.Join(root, "state"), FreezeAfter: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Shutdown)
	return c
}

// While a compared snapshot is drawn, the time and date, the battery, the
// signal, the boot animation and the overlays' animation are the same
// whenever and wherever it runs.
func TestSnapshotHeldStill(t *testing.T) {
	c := snapCore(t)
	c.RestartBoot()
	draw := func() (status string, date string, boot float64, phase float64, period int) {
		status = strings.SplitN(c.Render().Plain(), "\n", 2)[0]
		phase, period = c.overlayPhase(time.Now().Add(-700*time.Millisecond), time.Second)
		return status, c.idleInfo().Date, c.BootProgress(), phase, period
	}
	c.snapping = &snapView{}
	s1, d1, b1, p1, n1 := draw()
	c.SetClockSkew(50 * time.Hour)
	time.Sleep(50 * time.Millisecond)
	s2, d2, b2, p2, n2 := draw()
	if s1 != s2 || d1 != d2 || b1 != snapBoot || b2 != snapBoot || p1 != 0 || p2 != 0 || n1 != 0 || n2 != 0 {
		t.Errorf("moved while snapping: %q %q %v %v %v / %q %q %v %v %v", s1, d1, b1, p1, n1, s2, d2, b2, p2, n2)
	}
	if p := c.shownPower(); p != simulatedPower {
		t.Errorf("battery %+v", p)
	}
	c.SetPower(Power{Level: 40})
	if p := c.shownPower(); p.Level != 40 {
		t.Errorf("the script's battery not shown: %+v", p)
	}
	c.snapping = nil
	if _, d3, _, _, _ := draw(); d3 == d1 {
		t.Errorf("the date is held still outside snapshots too: %q", d3)
	}
}

func runSnaps(t *testing.T, c *Core, b *Baselines, snapDir string) (bool, string, []CaseResult) {
	t.Helper()
	var out strings.Builder
	r := &Runner{C: c, SnapDir: snapDir, Out: &out, Baselines: b}
	ok := r.Run("== idle\nsleep 1600\nat sys.idle 8000\nsnap t-idle\n")
	return ok, out.String(), r.Cases
}

// A run records its text snapshots as the baselines, a later one at
// another time compares equal, a changed baseline fails the case with a
// diff, a skipped snapshot is not compared and a missing one fails.
func TestTextBaselines(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	dir, snaps := t.TempDir(), t.TempDir()
	base := filepath.Join(dir, "text", "bar-qwerty")
	_ = os.MkdirAll(base, 0o755)
	_ = os.WriteFile(filepath.Join(base, "gone.txt"), []byte("x"), 0o644)
	load := func(update bool) *Baselines {
		b, err := LoadBaselines(dir, "bar-qwerty")
		if err != nil {
			t.Fatal(err)
		}
		b.Update = update
		return b
	}

	if ok, out, _ := runSnaps(t, c, load(true), snaps); !ok || !strings.Contains(out, "text recorded 1") {
		t.Fatalf("recording:\n%s", out)
	}
	want, err := os.ReadFile(filepath.Join(base, "t-idle.txt"))
	if err != nil || !strings.HasPrefix(string(want), " 00:00 ") {
		t.Fatalf("baseline %q %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(base, "gone.txt")); !os.IsNotExist(err) {
		t.Error("the baseline of a snapshot the script no longer takes stayed")
	}

	c.SetClockSkew(26 * time.Hour)
	if ok, out, _ := runSnaps(t, c, load(false), snaps); !ok || !strings.Contains(out, "text same 1") {
		d, _ := os.ReadFile(filepath.Join(snaps, "diff", "t-idle.diff"))
		t.Fatalf("a day later:\n%s%s", out, d)
	}

	_ = os.WriteFile(filepath.Join(base, "t-idle.txt"), bytes.Replace(want, []byte("\n"), []byte("\nchanged\n"), 1), 0o644)
	ok, out, cases := runSnaps(t, c, load(false), snaps)
	if ok || len(cases) != 1 || cases[0].Status != "FAIL" || !strings.Contains(cases[0].Message, "text t-idle") ||
		!strings.Contains(out, "SNAPSHOT-DIFF: text t-idle") || !strings.Contains(out, "PHONE-SELFTEST-RESULT: FAIL") {
		t.Fatalf("a changed baseline: %v %+v\n%s", ok, cases, out)
	}
	if d, err := os.ReadFile(filepath.Join(snaps, "diff", "t-idle.diff")); err != nil || !strings.Contains(string(d), "@@ line 2\n-changed\n+") {
		t.Errorf("diff %q %v", d, err)
	}

	_ = os.WriteFile(filepath.Join(dir, UnstableFile), []byte("t-* skip\n"), 0o644)
	if ok, out, _ := runSnaps(t, c, load(false), snaps); !ok || !strings.Contains(out, "text not compared 1") {
		t.Fatalf("skipped:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(snaps, "diff")); !os.IsNotExist(err) {
		t.Error("the last run's diffs stayed")
	}

	os.Remove(filepath.Join(dir, UnstableFile))
	os.Remove(filepath.Join(base, "t-idle.txt"))
	if ok, out, _ := runSnaps(t, c, load(false), snaps); ok || !strings.Contains(out, "no baseline") {
		t.Fatalf("no baseline:\n%s", out)
	}
}

// A compared run's boot snapshot shows the animation even when the runner
// got to it after the animation ended (a loaded machine), but only before
// any other step and the first case; the live screen is not held.
func TestBootSnapshotWhenLate(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	dir, snaps := t.TempDir(), t.TempDir()
	b, err := LoadBaselines(dir, "bar-qwerty")
	if err != nil {
		t.Fatal(err)
	}
	b.Update = true
	late := (bootDuration + 300*time.Millisecond).Milliseconds()
	var out strings.Builder
	r := &Runner{C: c, SnapDir: snaps, Out: &out, Baselines: b}
	script := fmt.Sprintf("sleep %d\nsnap t-boot\nsnap t-after\n== idle\nat sys.idle 8000\n", late)
	if !r.Run(script) {
		t.Fatalf("run:\n%s", out.String())
	}
	if p := c.BootProgress(); p != -1 {
		t.Errorf("live boot progress %v after the animation", p)
	}
	brand := strings.Join(strings.Split(Brand, ""), " ")
	shows := func(name string) bool {
		raw, err := os.ReadFile(filepath.Join(snaps, name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(raw), brand)
	}
	if !shows("t-boot") {
		t.Error("the late boot snapshot missed the animation")
	}
	if shows("t-after") {
		t.Error("a snapshot after the boot one still shows the animation")
	}

	out.Reset()
	r = &Runner{C: c, SnapDir: snaps, Out: &out, Baselines: b}
	if !r.Run(fmt.Sprintf("sleep %d\nat sys.idle 8000\nsnap t-step\n", late)) {
		t.Fatalf("run:\n%s", out.String())
	}
	out.Reset()
	r = &Runner{C: c, SnapDir: snaps, Out: &out, Baselines: b}
	if !r.Run(fmt.Sprintf("== first\nsleep %d\nsnap t-case\n", late)) {
		t.Fatalf("run:\n%s", out.String())
	}
	if shows("t-step") || shows("t-case") {
		t.Error("held past another step or the first case")
	}
}

// A restart in place (the factory reset's cases) carries the comparison
// over to the next Shell: its counts, and a difference before the restart
// still fails the case.
func TestSnapshotsAcrossARestart(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	dir, snaps := t.TempDir(), t.TempDir()
	resume := filepath.Join(t.TempDir(), "resume.json")
	const script = "== restart\nsleep 1600\nat sys.idle 8000\nsnap t-before\nshell-restart\nsnap t-after\n"
	runner := func(update bool, out *strings.Builder) *Runner {
		b, err := LoadBaselines(dir, "bar-qwerty")
		if err != nil {
			t.Fatal(err)
		}
		b.Update = update
		return &Runner{C: c, SnapDir: snaps, Out: out, Baselines: b, ResumeFile: resume}
	}
	run := func(update bool) (bool, string, []CaseResult) {
		var out strings.Builder
		r := runner(update, &out)
		r.Restart = func(string) error { return os.ErrProcessDone }
		r.Run(script)
		res, err := LoadResume(resume)
		if err != nil {
			t.Fatal(err)
		}
		out.Reset()
		r = runner(update, &out)
		r.Resume = res
		ok := r.Run(script)
		return ok, out.String(), r.Cases
	}

	if ok, out, _ := run(true); !ok || !strings.Contains(out, "PHONE-SNAPSHOTS: text recorded 2\n") {
		t.Fatalf("recording:\n%s", out)
	}
	before := filepath.Join(dir, "text", "bar-qwerty", "t-before.txt")
	want, err := os.ReadFile(before)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(before, append([]byte("changed\n"), want...), 0o644)
	ok, out, cases := run(false)
	if ok || len(cases) != 1 || cases[0].Status != "FAIL" || !strings.Contains(cases[0].Message, "text t-before") ||
		!strings.Contains(out, "PHONE-SNAPSHOTS: text differ 1, text same 1\n") {
		t.Fatalf("a difference before the restart: %v %+v\n%s", ok, cases, out)
	}
}

func writeTestPNG(t *testing.T, file string, img image.Image, level png.CompressionLevel) {
	t.Helper()
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: level}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Pixel baselines are hashes of the pixels (another encoding of the same
// pixels is the same); a difference fails with an image of it when the
// baseline's PNG is kept; a form factor without baselines is not compared.
func TestPixelBaselines(t *testing.T) {
	dir, pix, imgs := t.TempDir(), t.TempDir(), t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var out strings.Builder
	r := &Runner{Out: &out, GUI: &GUI{info: RendererInfo{Name: "fake-renderer"}}}
	snap := func(device string, update bool, level png.CompressionLevel) {
		t.Helper()
		writeTestPNG(t, filepath.Join(pix, "a.png"), img, level)
		b := &Baselines{Dir: dir, Device: device, Update: update, Images: imgs, PixelDir: pix, counts: map[string]int{}}
		r.Baselines = b
		if err := b.pixelSnap(r, "a"); err != nil {
			t.Fatal(err)
		}
	}

	snap("bar-t9", true, png.DefaultCompression)
	raw, err := os.ReadFile(filepath.Join(dir, "pixel", "fake-renderer", "bar-t9.json"))
	if err != nil || !strings.Contains(string(raw), `"a": "`) {
		t.Fatalf("manifest %s %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(imgs, "a.png")); err != nil {
		t.Error("the baseline's image not kept")
	}

	snap("bar-t9", false, png.BestCompression)
	if len(r.snapDiffs) != 0 || r.Baselines.counts["pixel same"] != 1 {
		t.Fatalf("the same pixels: %v\n%s", r.snapDiffs, out.String())
	}

	img.SetNRGBA(1, 1, color.NRGBA{255, 255, 255, 255})
	snap("bar-t9", false, png.DefaultCompression)
	if len(r.snapDiffs) != 1 || !strings.Contains(out.String(), "1 pixels differ") {
		t.Fatalf("a changed pixel: %v\n%s", r.snapDiffs, out.String())
	}
	d, err := readPNG(filepath.Join(pix, "diff", "a.png"))
	if err != nil || d.Bounds().Dx() != 3*4+16 || d.Bounds().Dy() != 3 {
		t.Fatalf("diff image %v %v", d, err)
	}
	if c := color.NRGBAModel.Convert(d.At(2*(4+8)+1, 1)).(color.NRGBA); c != (color.NRGBA{255, 0, 0, 255}) {
		t.Errorf("the differing pixel is %v", c)
	}

	r.snapDiffs = nil
	out.Reset()
	snap("communicator", false, png.DefaultCompression)
	if len(r.snapDiffs) != 0 || !strings.Contains(out.String(), "no pixel baselines") {
		t.Fatalf("no baselines here: %v\n%s", r.snapDiffs, out.String())
	}
	if raw, _ := os.ReadFile(filepath.Join(pix, RunManifest)); !strings.Contains(string(raw), `"a": "`) {
		t.Errorf("run manifest %s", raw)
	}
}

// Another release of the fonts phone-paint finds is another set of pixel
// baselines.
func TestPaintFonts(t *testing.T) {
	if !alone(t) {
		return
	}
	dir := t.TempDir()
	t.Setenv("PHONE_PAINT_FONTS", dir)
	font := filepath.Join(dir, "NotoSansCJK-Regular.ttc")
	_ = os.WriteFile(font, []byte("one"), 0o644)
	a := paintFonts()
	if b := paintFonts(); a != b || len(a) != 8 {
		t.Fatalf("%q %q", a, b)
	}
	_ = os.WriteFile(font, []byte("two"), 0o644)
	if b := paintFonts(); b == a {
		t.Error("the same fingerprint for other fonts")
	}
}

// paintFonts looks for the fonts where phone-paint does.
func TestPaintFontsLookWherePaintDoes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "paint", "src", "canvas.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range append(append([]string{"PHONE_PAINT_FONTS", "NotoSansCJK-Regular", "NotoSansCJK-Bold"}, paintFontDirs...), paintFallbacks...) {
		if !strings.Contains(string(raw), strconv.Quote(s)) {
			t.Errorf("paint/src/canvas.rs no longer names %q", s)
		}
	}
}
