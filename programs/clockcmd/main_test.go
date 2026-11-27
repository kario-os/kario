package clockcmd

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
	"github.com/kario-os/kario/hal/rtc"
)

// board is a phone whose sku.json has the rtc given, with the RTC, the
// system clock and the kernel's NTP state the test's: the RTC runs *behind
// the system clock (as the 6T's counts from 1970), whole seconds.
type board struct {
	sku, persist string
	behind       time.Duration
	synced, ours bool
	set, written []time.Time
}

func newBoard(t *testing.T, rtcJSON string) *board {
	b := &board{persist: t.TempDir(), behind: 56*365*24*time.Hour + 250*time.Millisecond, ours: true}
	b.sku = filepath.Join(t.TempDir(), "sku.json")
	if err := os.WriteFile(b.sku, []byte(`{"id": "oneplus-fajita-t9", "rtc": `+rtcJSON+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "class", "rtc", rtc.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hctosys"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sys, read, set, write, synced, ours := hal.SysRoot, rtc.Read, rtc.SetSystem, rtc.Write, rtc.Synced, rtc.Ours
	t.Cleanup(func() {
		hal.SysRoot, rtc.Read, rtc.SetSystem, rtc.Write, rtc.Synced, rtc.Ours = sys, read, set, write, synced, ours
	})
	hal.SysRoot = filepath.Dir(filepath.Dir(filepath.Dir(dir)))
	rtc.Read = func() (int64, error) { return time.Now().Add(-b.behind).Unix(), nil }
	rtc.SetSystem = func(t time.Time) error { b.set = append(b.set, t); return nil }
	rtc.Write = func(t time.Time) error { b.written = append(b.written, t); return nil }
	rtc.Synced = func() bool { return b.synced }
	rtc.Ours = func() bool { return b.ours }
	return b
}

func (b *board) run(t *testing.T, want int, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if got := run(append(args, "--persist-dir", b.persist), &out, b.sku); got != want {
		t.Fatalf("phone-clock %s: exit %d, want %d: %s", strings.Join(args, " "), got, want, out.String())
	}
	return out.String()
}

func (b *board) kept(t *testing.T) rtc.Offset {
	t.Helper()
	o, err := rtc.Load(filepath.Join(b.persist, rtc.OffsetFile))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// The 6T: nothing kept, nothing set; the time kept by hand (as the Shell
// keeps it), then the phone off for three days: the clock comes back from
// the RTC plus the offset; an RTC that started over is not trusted.
func TestRestoreFromTheOffset(t *testing.T) {
	b := newBoard(t, `{"read_only": true, "offset": true}`)
	if out := b.run(t, 0, "restore"); !strings.Contains(out, "no offset kept yet") || len(b.set) != 0 {
		t.Fatalf("without an offset: %s, set %v", out, b.set)
	}
	b.run(t, 0, "save")
	if o := b.kept(t); o.By != "save" || o.Duration()-b.behind < 0 || o.Duration()-b.behind > 50*time.Millisecond {
		t.Fatalf("kept %+v, the RTC %v behind", o, b.behind)
	}
	if out := b.run(t, 0, "restore"); !strings.Contains(out, "right already") || len(b.set) != 0 {
		t.Fatalf("the clock right already: %s, set %v", out, b.set)
	}
	kept := b.kept(t)
	b.behind -= 72 * time.Hour
	b.run(t, 0, "restore")
	if want := time.Now().Add(72 * time.Hour); len(b.set) != 1 || b.set[0].Sub(want).Abs() > 700*time.Millisecond {
		t.Fatalf("three days off: set %v, want about %v", b.set, want)
	}
	b.behind = time.Since(time.Unix(1000, 0))
	if out := b.run(t, 0, "restore"); !strings.Contains(out, "started over") || len(b.set) != 1 {
		t.Fatalf("an RTC that started over: %s, set %v", out, b.set)
	}
	if b.kept(t) != kept || len(b.written) != 0 {
		t.Errorf("restore changed the offset (%+v) or wrote the RTC (%v)", b.kept(t), b.written)
	}
}

// At shutdown the offset is kept again only while NTP keeps the clock, and
// only when the RTC drifted from it.
func TestSaveIfSynced(t *testing.T) {
	b := newBoard(t, `{"read_only": true, "offset": true}`)
	if out := b.run(t, 0, "save", "--if-synced"); !strings.Contains(out, "NTP does not keep the clock") {
		t.Fatalf("not synchronised: %s", out)
	}
	if _, err := os.Stat(filepath.Join(b.persist, rtc.OffsetFile)); !os.IsNotExist(err) {
		t.Fatal("kept without NTP")
	}
	b.synced = true
	b.run(t, 0, "save", "--if-synced")
	if o := b.kept(t); o.By != "ntp" {
		t.Fatalf("kept %+v", o)
	}
	if out := b.run(t, 0, "save", "--if-synced"); !strings.Contains(out, "still right") {
		t.Fatalf("unchanged: %s", out)
	}
	b.behind += 2 * time.Second
	b.run(t, 0, "save", "--if-synced")
	if o := b.kept(t); o.Duration()-b.behind < 0 || o.Duration()-b.behind > 50*time.Millisecond {
		t.Errorf("drifted by 2 s: kept %+v, the RTC %v behind", o, b.behind)
	}
}

// A settable RTC is the kernel's to read at boot and is written from the
// system clock (at shutdown while NTP keeps it); a read-only one without an
// offset is left alone.
func TestOtherBoards(t *testing.T) {
	b := newBoard(t, `{}`)
	if out := b.run(t, 0, "restore"); !strings.Contains(out, "keeps no offset") || len(b.set) != 0 {
		t.Fatalf("settable: %s", out)
	}
	b.run(t, 0, "save", "--if-synced")
	b.synced = true
	b.run(t, 0, "save", "--if-synced")
	if len(b.written) != 1 || time.Since(b.written[0]) > time.Minute {
		t.Fatalf("written %v", b.written)
	}
	b = newBoard(t, `{"read_only": true}`)
	b.run(t, 0, "save")
	b.run(t, 0, "restore")
	if _, err := os.Stat(filepath.Join(b.persist, rtc.OffsetFile)); !os.IsNotExist(err) || len(b.written)+len(b.set) != 0 {
		t.Errorf("read-only without an offset: written %v, set %v, offset %v", b.written, b.set, err)
	}
}

// Where the clock is not this system's (the chroot kit) it only shows.
func TestNotOurs(t *testing.T) {
	b := newBoard(t, `{"read_only": true, "offset": true}`)
	b.ours = false
	b.run(t, 1, "save")
	b.run(t, 1, "restore")
	if len(b.set)+len(b.written) != 0 {
		t.Fatalf("set %v, written %v", b.set, b.written)
	}
	out := b.run(t, 0, "show")
	for _, want := range []string{"read-only: the time is kept as an offset", "since_epoch", "offset: none kept", "this system's: false"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	b.ours = true
	b.run(t, 0, "save")
	if out := b.run(t, 0, "show"); !regexp.MustCompile(`plus the offset: \S+, [+-]0\.\d s from the system clock`).MatchString(out) {
		t.Errorf("show with an offset:\n%s", out)
	}
	for _, args := range [][]string{{}, {"set"}, {"restore", "--if-synced"}, {"show", "x"}} {
		b.run(t, 2, args...)
	}
	if err := os.WriteFile(b.sku, []byte(`{"rtc": {"offset": true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b.run(t, 1, "restore")
}

// The image runs it early at boot and at shutdown: kario-system has it,
// the image links it, and phone-fw installs and enables the unit, ordered
// before the clock's users.
func TestInstalled(t *testing.T) {
	read := func(p string) string {
		raw, err := os.ReadFile(filepath.Join("..", "..", p))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	unit := read("yocto/meta-phone/recipes-phone/phone-fw/files/phone-clock.service")
	for _, want := range []string{"ExecStart=/usr/bin/phone-clock restore\n", "ExecStop=/usr/bin/phone-clock save --if-synced\n",
		"DefaultDependencies=no\n", "RequiresMountsFor=/var/lib/slots\n", "WantedBy=sysinit.target\n", "RemainAfterExit=yes\n",
		"After=local-fs.target systemd-udev-trigger.service\n"} {
		if !strings.Contains(unit, want) {
			t.Errorf("phone-clock.service lacks %q", want)
		}
	}
	before := regexp.MustCompile(`(?m)^Before=(.*)$`).FindStringSubmatch(unit)
	for _, u := range []string{"time-set.target", "sysinit.target", "systemd-timesyncd.service", "shutdown.target"} {
		if before == nil || !strings.Contains(" "+before[1]+" ", " "+u+" ") {
			t.Errorf("phone-clock.service is not before %s", u)
		}
	}
	recipe := read("yocto/meta-phone/recipes-phone/phone-fw/phone-fw_0.1.bb")
	for _, want := range []string{"file://phone-clock.service", "${UNPACKDIR}/phone-clock.service ${D}${systemd_system_unitdir}/", `SYSTEMD_SERVICE:${PN} += "phone-clock.service"`} {
		if !strings.Contains(recipe, want) {
			t.Errorf("phone-fw lacks %q", want)
		}
	}
	if !regexp.MustCompile(`for b in [^;]* phone-clock[ ;]`).MatchString(read("scripts/stage.sh")) {
		t.Error("stage.sh does not link phone-clock to kario-system")
	}
	if !strings.Contains(read("cmd/kario-system/main.go"), `"phone-clock":      clockcmd.Main,`) {
		t.Error("kario-system does not run phone-clock")
	}
	if !strings.Contains(read("programs/shellcmd/main.go"), `"/var/lib/slots"`) {
		t.Error("the Shell's persistent area is not phone-clock's default")
	}
}
