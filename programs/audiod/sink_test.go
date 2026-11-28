package audiod

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/subproc/subproctest"
)

// aplay ends with audiod however audiod ends: where a supervisor other
// than systemd restarts audiod (phone-shell --services spawn, the
// end-to-end tests) an aplay left over held the card, the probe's too (it
// waits for a card another program holds).
func TestAplayDiesWithAudiod(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(s *alsaSink)
	}{
		{"probing", func(s *alsaSink) { s.Caps() }},
		{"playing", func(s *alsaSink) { _ = s.open(s.device, "S16_LE", audio.Rate, audio.Channels) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			if subproctest.Child(t) {
				c.run(&alsaSink{device: "plughw:0,2", base: "plughw:0,2"})
				time.Sleep(time.Minute)
				return
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "aplay"), []byte(subproctest.StandIn), 0o755); err != nil {
				t.Fatal(err)
			}
			if pid := subproctest.Survivor(t, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH")); pid != 0 {
				t.Fatalf("aplay (pid %d) outlived audiod", pid)
			}
		})
	}
}

// The playing aplay outlives the thread that started it: Linux sends the
// parent-death signal when that thread ends, and Go ends a thread whose
// locked goroutine returns.
func TestAplayOutlivesTheThreadThatStartedIt(t *testing.T) {
	fakeAplay(t, "exec sleep 30\n")
	s := &alsaSink{device: "plughw:0,2", base: "plughw:0,2"}
	subproctest.OnEndingThread(t, func() {
		if err := s.open(s.device, "S16_LE", audio.Rate, audio.Channels); err != nil {
			t.Error(err)
		}
	})
	if s.cmd == nil {
		t.FailNow()
	}
	defer s.stop()
	if subproctest.Gone(s.cmd.Process.Pid, 300*time.Millisecond) {
		t.Fatal("aplay ended with the thread that started it")
	}
}
