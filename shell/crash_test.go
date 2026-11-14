package shell

import (
	"io"
	"log"
	"testing"
	"time"
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
