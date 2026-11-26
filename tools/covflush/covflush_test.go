package covflush

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// prog builds testdata/prog as make coverage builds the system's programs.
func prog(t *testing.T) string {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH")
	}
	exe := filepath.Join(t.TempDir(), "prog")
	cmd := exec.Command(gobin, "build", "-cover", "-covermode=atomic",
		"-coverpkg=github.com/kario-os/kario/tools/covflush/...", "-o", exe, "./testdata/prog")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return exe
}

// funcs is covdata's coverage of each function in dir's runs.
func funcs(t *testing.T, dir string) map[string]string {
	t.Helper()
	out, err := exec.Command("go", "tool", "covdata", "func", "-i="+dir).CombinedOutput()
	if err != nil {
		t.Fatalf("covdata func: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) == 3 {
			got[f[1]] = f[2]
		}
	}
	return got
}

var counterFile = regexp.MustCompile(`^covcounters\.[0-9a-f]+\.(\d+)\.\d+$`)

// counterFiles are dir's counter files by process.
func counterFiles(t *testing.T, dir string) map[string][]string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string][]string{}
	for _, e := range ents {
		if m := counterFile.FindStringSubmatch(e.Name()); m != nil {
			by[m[1]] = append(by[m[1]], e.Name())
		} else if !strings.HasPrefix(e.Name(), "covmeta.") {
			t.Errorf("%s left in the directory", e.Name())
		}
	}
	return by
}

// A killed process keeps what it ran up to its last write, in one counter
// file however often it wrote.
func TestKilledKeepsItsCounters(t *testing.T) { killed(t, "wait", 3*Interval+Interval/2) }

// So does one that may no longer make a file, writing over the one it
// made while it still could, and more often: killed well within an
// Interval of its start.
func TestConfinedKeepsItsCounters(t *testing.T) { killed(t, "confined", 4*ConfinedInterval) }

func killed(t *testing.T, mode string, after time.Duration) {
	exe := prog(t)
	dir := t.TempDir()
	cmd := exec.Command(exe, mode)
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if l, _ := bufio.NewReader(stdout).ReadString('\n'); l != "ready\n" {
		t.Fatalf("prog said %q", l)
	}
	time.Sleep(after)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	files := counterFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("counter files %v, want one process's", files)
	}
	for pid, names := range files {
		if len(names) != 1 {
			t.Errorf("process %s wrote %v, want one file", pid, names)
		}
	}
	if got := funcs(t, dir)["reached"]; got != "100.0%" {
		t.Errorf("reached: %q covered, want 100.0%%", got)
	}
}

// Flush before an exec keeps what ran before it; without it the exec
// would lose it, the runtime writing only at exit.
func TestFlushBeforeExec(t *testing.T) {
	exe := prog(t)
	dir := t.TempDir()
	cmd := exec.Command(exe, "exec")
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "exec\n" {
		t.Fatalf("prog: %v %q", err, out)
	}
	if got := funcs(t, dir)["execed"]; got != "100.0%" {
		t.Errorf("execed: %q covered, want 100.0%%", got)
	}
}

// Every program make build compiles starts writing in a coverage build: it
// links a package whose cover.go calls Start.
func TestEveryProgramStarts(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH")
	}
	cmd := exec.Command(gobin, "list", "-tags", "kariocover", "-f",
		`{{if eq .Name "main"}}{{.ImportPath}}{{range .Deps}} {{.}}{{end}}{{end}}`,
		"./cmd/...", "./apps/...", "./cts/...", "./examples/...")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		n++
		if !slices.Contains(f[1:], "github.com/kario-os/kario/tools/covflush") {
			t.Errorf("%s does not link covflush under -tags kariocover", f[0])
		}
	}
	if n == 0 {
		t.Fatal("no programs listed")
	}
}

// Without GOCOVERDIR nothing is written and Flush does nothing.
func TestNoDirectory(t *testing.T) {
	t.Setenv("GOCOVERDIR", "")
	Start()
	Flush()
	if inPlace, err := write(); inPlace || err != nil {
		t.Fatal(inPlace, err)
	}
}
