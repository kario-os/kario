package shell

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/jsrt"
)

func TestCrashLoop(t *testing.T) {
	dir := t.TempDir()
	c := &Core{cfg: Config{StateDir: dir, RunDir: dir}, log: log.New(io.Discard, "", 0)}
	a := &App{M: &Manifest{ID: "com.example.a", Name: "A"}}
	b := &App{M: &Manifest{ID: "com.example.b", Name: "B"}}
	for i := 0; i < crashLoopCount-1; i++ {
		c.recordCrash(a, "exit status 2")
	}
	c.recordCrash(b, "signal: segmentation fault")
	if c.crashLooping(a) {
		t.Fatal("looping before the third crash")
	}
	c.recordCrash(a, "exit status 2")
	if !c.crashLooping(a) || c.crashLooping(b) {
		t.Fatal("crash loop not told apart")
	}
	c.clockSkew = crashLoopWindow + time.Minute
	if c.crashLooping(a) {
		t.Fatal("old crashes still count")
	}
	again := &Core{cfg: c.cfg, log: c.log}
	if n := len(again.Crashes()); n != crashLoopCount+1 {
		t.Fatalf("%d records after a restart", n)
	}
	for i := 0; i < maxCrashRecords+5; i++ {
		again.recordCrash(b, "exit status 1")
	}
	if n := len(again.Crashes()); n != maxCrashRecords {
		t.Fatalf("%d records kept", n)
	}
}

// A crash of native code keeps kario-jsrt's backtrace whole in the record:
// the one that ends the log and names the signal the app died of (or, as
// the first process of a pid namespace, exited with 128 + its number for),
// not one an earlier run left.
func TestCrashBacktrace(t *testing.T) {
	dir := t.TempDir()
	c := &Core{cfg: Config{StateDir: dir, RunDir: dir}, log: log.New(io.Discard, "", 0)}
	a := &App{M: &Manifest{ID: "com.example.native", Name: "N"}}
	block := func(sig string, frames int) []string {
		out := []string{"kario-jsrt: crashed in native:probe.crash: " + sig + " at 0x0"}
		for i := 0; i < frames; i++ {
			out = append(out, fmt.Sprintf("kario-jsrt: #%d native:probe +0x%x build-id 5e1d", i, 0x1100+i))
		}
		return append(out, jsrt.BacktraceEnd)
	}
	write := func(lines ...[]string) {
		var all []string
		for _, l := range lines {
			all = append(all, l...)
		}
		if err := os.WriteFile(filepath.Join(dir, a.M.ID+".log"), []byte(strings.Join(all, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	older, newer := block("SIGSEGV", 2), block("SIGSEGV", 30)
	for _, c2 := range []struct {
		name, reason string
		log          [][]string
		want         []string
	}{
		{"the last", "signal: segmentation fault", [][]string{{"run 1"}, older, {"run 2"}, newer}, newer},
		{"core dumped", "signal: segmentation fault (core dumped)", [][]string{newer}, newer},
		{"an earlier run's", "signal: segmentation fault", [][]string{older, {"run 2", "panic: x"}}, nil},
		{"another signal", "signal: aborted", [][]string{newer}, nil},
		{"not a signal", "exit status 1", [][]string{newer}, nil},
		{"aborted", "signal: aborted", [][]string{block("SIGABRT", 3)}, block("SIGABRT", 3)},
		{"aborted as the first process", "exit status 134", [][]string{block("SIGABRT", 3)}, block("SIGABRT", 3)},
		{"another exit status", "exit status 134", [][]string{newer}, nil},
	} {
		write(c2.log...)
		c.recordCrash(a, c2.reason)
		r := c.Crashes()[len(c.Crashes())-1]
		if !slices.Equal(r.Backtrace, c2.want) {
			t.Errorf("%s: %d lines, want %d", c2.name, len(r.Backtrace), len(c2.want))
		}
	}
	if len(newer) <= crashLogLines {
		t.Fatal("the backtrace fits the log's tail: the case tells nothing")
	}
}
