package shellcmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kario-os/kario/shell"
)

const parallelScript = "sleep 10\nsnap 00-boot\n#part a\n== a1\nsleep 10\n#part b\n== b1\nsleep 10\n#part a\n== a2\nsleep 10\n"

// fakePart stands in for a part's phone-shell: it records its arguments,
// writes the boot's and its own snapshot, a difference, its PNG hashes and
// a compatibility report, and leaves its cases (a2 failing when FAIL_A2 is
// set) for the parent.
const fakePart = `#!/bin/sh
echo "$*" > "$PHONE_PART_RESULT.args"
while [ $# -gt 0 ]; do
	case $1 in
	--parts) part=$2; shift ;;
	--snap-dir) snap=$2; shift ;;
	--pixel-dir) pix=$2; shift ;;
	--cts) cts=$2; shift ;;
	esac
	shift
done
mkdir -p "$snap/diff" "$pix"
echo boot > "$snap/00-boot.txt"
echo "$part" > "$snap/$part.txt"
echo "-$part" > "$snap/diff/$part.diff"
printf '{"renderer":"r","device":"d","snapshots":{"00-boot":"b","%s":"%s"}}' "$part" "$part" > "$pix/snapshots.json"
echo "{}" > "$cts"
echo "the part $part ran, see $snap/diff/$part.diff"
case $part in
a) a2=PASS; [ -n "${FAIL_A2:-}" ] && a2=FAIL
   echo '{"cases":[{"name":"a1","status":"PASS","seconds":1},{"name":"a2","status":"'$a2'","seconds":2,"message":"line 11"}],"snaps":{"text same":2,"text differ":1},"pixel_baselines":"base.json"}' > "$PHONE_PART_RESULT"
   [ "$a2" = PASS ] ;;
b) echo '{"cases":[{"name":"b1","status":"PASS","seconds":3}],"snaps":{"text same":1}}' > "$PHONE_PART_RESULT" ;;
esac
`

// --parallel: every part in a phone-shell of its own; their snapshots,
// PNG hashes and cases (in the script's order) come together in the run's
// directories and JUnit report, and their output names the run's
// directories; a compatibility report a part beside the run's path; a
// failed case fails the run; --only runs the parts with a case it selects.
func TestParallelParts(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "phone-shell")
	if err := os.WriteFile(self, []byte(fakePart), 0o755); err != nil {
		t.Fatal(err)
	}
	snap, pix, junit, cts := filepath.Join(dir, "snapshots"), filepath.Join(dir, "pixel"), filepath.Join(dir, "junit.xml"), filepath.Join(dir, "cts-report.json")
	if err := os.MkdirAll(filepath.Join(snap, "diff"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "diff", "old.diff"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	p := parallelRun{self: self, args: withoutFlag([]string{"--selftest", "--parallel", "--baselines", "b", "-parallel=true"}, "parallel"),
		script: parallelScript, snapDir: snap, pixelDir: pix, junit: junit, cts: cts, out: &out}
	t.Setenv("FAIL_A2", "")
	if !p.run() {
		t.Fatalf("the parts passed, the run failed:\n%s", out.String())
	}
	if want := "see " + filepath.Join(snap, "diff", "a.diff"); !strings.Contains(out.String(), want) || strings.Contains(out.String(), "/parts/") {
		t.Errorf("a part's paths are not the run's (%q):\n%s", want, out.String())
	}
	for _, f := range []string{"00-boot.txt", "a.txt", "b.txt", "diff/a.diff", "diff/b.diff"} {
		if _, err := os.Stat(filepath.Join(snap, f)); err != nil {
			t.Errorf("snapshots: %v", err)
		}
	}
	for _, gone := range []string{filepath.Join(snap, "diff", "old.diff"), filepath.Join(snap, "parts"), filepath.Join(pix, "parts")} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	var m struct{ Snapshots map[string]string }
	raw, _ := os.ReadFile(filepath.Join(pix, shell.RunManifest))
	if err := json.Unmarshal(raw, &m); err != nil || len(m.Snapshots) != 3 || m.Snapshots["b"] != "b" {
		t.Errorf("the run manifest %s (%v)", raw, err)
	}
	raw, _ = os.ReadFile(junit)
	if a1, b1, a2 := strings.Index(string(raw), `"a1"`), strings.Index(string(raw), `"b1"`), strings.Index(string(raw), `"a2"`); a1 < 0 || !(a1 < b1 && b1 < a2) {
		t.Errorf("JUnit not in the script's order:\n%s", raw)
	}
	for _, part := range []string{"a", "b"} {
		if _, err := os.Stat(partReport(cts, part)); err != nil {
			t.Errorf("compatibility report: %v", err)
		}
	}
	t.Setenv("FAIL_A2", "1")
	if p.run() {
		t.Error("a failed case passed the run")
	}

	p.only = regexp.MustCompile("^b")
	parts, err := p.selected()
	if err != nil || !slices.Equal(parts, []string{"b"}) {
		t.Errorf("--only ^b runs %v (%v)", parts, err)
	}
}

// A part's phone-shell gets the run's arguments but --parallel, its part,
// its own directories and report, and writes no JUnit report or PNGs of
// its own.
func TestParallelChildArgs(t *testing.T) {
	p := parallelRun{self: "/bin/true", args: withoutFlag([]string{"--cts", "r.json", "--parallel", "--gui", "auto", "--only", "x"}, "parallel"),
		snapDir: "s", pixelDir: "p", cts: "build/cts-report.json"}
	cmd, err := p.child("chat", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cmd.Args[1:], " ")
	want := "--cts r.json --gui auto --only x --parts chat --junit= --print-png= --snap-dir s/parts/chat --pixel-dir p/parts/chat --cts build/cts-report-chat.json"
	if got != want {
		t.Errorf("args %q\nwant %q", got, want)
	}
}
