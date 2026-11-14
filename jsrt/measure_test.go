package jsrt

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestMeasure is the start time to the first screen and the resident
// memory after it, of a TypeScript app (from source and from bytecode)
// against native ones, started as the Shell starts apps (29-app-development.md §5):
//
//	KARIO_MEASURE=<built app dir with main.js and main.jsc> [KARIO_MEASURE_NODE=1] \
//	KARIO_MEASURE_NATIVE="notes=/path/to/notes calc=/path/to/calc" go test -run Measure -v ./jsrt
//
// KARIO_MEASURE_NODE adds the same module under Node.js (kario run's engine).
func TestMeasure(t *testing.T) {
	app := os.Getenv("KARIO_MEASURE")
	if app == "" {
		t.Skip("KARIO_MEASURE is not set")
	}
	bin := binary(t)
	main := filepath.Join(app, DefaultMain)
	type subject struct {
		name string
		argv []string
	}
	subjects := []subject{
		{"ts (main.js)", []string{bin, "--no-bytecode", main}},
		{"ts (main.jsc)", []string{bin, main}},
	}
	if os.Getenv("KARIO_MEASURE_NODE") != "" {
		node, run := nodeRunner(t)
		subjects = append(subjects, subject{"ts (node)", []string{node, run, main}})
	}
	for _, kv := range strings.Fields(os.Getenv("KARIO_MEASURE_NATIVE")) {
		name, path, _ := strings.Cut(kv, "=")
		subjects = append(subjects, subject{name, []string{path}})
	}
	const runs = 20
	for _, s := range subjects {
		var starts []time.Duration
		var rss, pss []int
		for i := 0; i < runs; i++ {
			t0 := time.Now()
			sh := launch(t, t.TempDir(), "zh-Hans", s.argv[0], s.argv[1:]...)
			sh.hello("", nil)
			first := sh.expectPage()
			starts = append(starts, first.Sub(t0))
			time.Sleep(300 * time.Millisecond)
			r, p := memory(t, sh.cmd.Process.Pid)
			rss, pss = append(rss, r), append(pss, p)
			_ = sh.cmd.Process.Kill()
			sh.exit()
		}
		t.Logf("%-14s start %6.1f ms (median of %d)  Rss %5d KB  Pss %5d KB", s.name,
			float64(median(starts))/float64(time.Millisecond), runs, median(rss), median(pss))
	}
	for _, f := range []string{DefaultMain, Bytecode(DefaultMain)} {
		if st, err := os.Stat(filepath.Join(app, f)); err == nil {
			t.Logf("%s: %d bytes", f, st.Size())
		}
	}
}

// expectPage waits for the first page the app sends and says when.
func (s *shell) expectPage() time.Time {
	s.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-s.msgs:
			if !ok {
				s.t.Fatalf("app exited before its first page; log:\n%s", s.log())
			}
			if m.Screen != nil {
				return time.Now()
			}
		case <-timeout:
			s.t.Fatalf("no page; log:\n%s", s.log())
		}
	}
}

// memory is a process's Rss and Pss in KB, from smaps_rollup, which walks
// the page tables: status's VmRSS lags behind in a process with threads
// (the kernel counts per thread and folds the counts in now and then).
func memory(t testing.TB, pid int) (rss, pss int) {
	read := func(file, key string) int {
		f, err := os.Open(fmt.Sprintf("/proc/%d/%s", pid, file))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), key+":"); ok {
				var n int
				fmt.Sscan(strings.TrimSpace(v), &n)
				return n
			}
		}
		return 0
	}
	return read("smaps_rollup", "Rss"), read("smaps_rollup", "Pss")
}

func median[T int | time.Duration](xs []T) T {
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}
