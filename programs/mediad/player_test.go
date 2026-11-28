package mediad

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/media"
	"github.com/kario-os/kario/subproc/subproctest"
	"github.com/kario-os/kario/wire"
)

// sampleVideo is one of the media library's sample videos (examples/videos:
// 15 seconds at 480 pixels wide, with sound; tools/samples.py).
func sampleVideo(t *testing.T) string {
	t.Helper()
	m, _ := filepath.Glob("../../examples/videos/*.webm")
	if len(m) == 0 {
		t.Fatal("no sample video in examples/videos")
	}
	return m[0]
}

func TestPlayerFollowsTheSound(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	sample := sampleVideo(t)
	s := &server{started: time.Now(), streams: map[string]media.Source{}, players: map[string]*player{}}
	open := func(name string) *os.File {
		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	res, err := s.handlePlayer("player.probe", nil, []*os.File{open(sample)})
	if err != nil {
		t.Fatal(err)
	}
	fr := res.(wire.Files)
	if info := fr.Result.(probeInfo); info.W != 480 || !info.Sound || info.Dur < 14 {
		t.Fatalf("probe: %+v", info)
	}
	fr.Files[0].Close()

	ringFile, err := audio.NewRingFile("test", audio.Rate*2)
	if err != nil {
		t.Fatal(err)
	}
	mixer, err := audio.MapRing(ringFile)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(ringFile.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	dup := os.NewFile(uintptr(fd), "ring")
	args, _ := json.Marshal(playerArgs{App: "t", Title: "Wetland", MaxW: 240, MaxH: 240})
	res, err = s.handlePlayer("player.open", args, []*os.File{open(sample), dup})
	if err != nil {
		t.Fatal(err)
	}
	st := res.(playerState)
	if st.W != 240 || st.H != 134 || !st.Sound {
		t.Fatalf("open: %+v", st)
	}
	// The mixer's side: take half a second of sound, then stop taking.
	buf := make([]int16, audio.Rate/2)
	deadline := time.Now().Add(5 * time.Second)
	for got := 0; got < len(buf) && time.Now().Before(deadline); {
		got += mixer.Read(buf[got:])
		time.Sleep(10 * time.Millisecond)
	}
	f := media.NewFrame(240, 134)
	src := s.streams[st.ID]
	p := s.players[st.ID]
	// The picture has a decoder of its own: on a loaded machine its first
	// frame can come after the half second of sound.
	var at float64
	shown := false
	for end := time.Now().Add(5 * time.Second); !shown && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		src(0, f)
		p.mu.Lock()
		at, shown = p.clock(), p.cur != nil
		p.mu.Unlock()
	}
	if at < 0.45 || at > 0.55 || !shown {
		t.Fatalf("after half a second of sound the clock is %.2f s, picture %v", at, shown)
	}
	time.Sleep(300 * time.Millisecond)
	p.mu.Lock()
	still := p.clock()
	p.mu.Unlock()
	if still != at {
		t.Fatalf("the clock ran on (%.2f → %.2f s) while no sound was played", at, still)
	}
	ctl, _ := json.Marshal(playerArgs{Player: st.ID, Op: "seek", At: 10})
	res, err = s.handlePlayer("player.control", ctl, nil)
	if err != nil || res.(playerState).PosMS < 9900 {
		t.Fatalf("seek: %+v %v", res, err)
	}
	cl, _ := json.Marshal(playerArgs{Player: st.ID})
	if _, err := s.handlePlayer("player.close", cl, nil); err != nil {
		t.Fatal(err)
	}
	mixer.Close()
	ringFile.Close()
}

// A decoder that dies before the first picture is a failure with its
// reason, not the end of the video.
func TestPlayerReportsDecoderFailure(t *testing.T) {
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	bin := t.TempDir()
	fake := "#!/bin/sh\necho \"Requested output format 's16le' is not known.\" >&2\nexit 234\n"
	if err := os.WriteFile(bin+"/ffmpeg", []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(probe, bin+"/ffprobe"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	s := &server{started: time.Now(), streams: map[string]media.Source{}, players: map[string]*player{}}
	f, err := os.Open(sampleVideo(t))
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(playerArgs{App: "t", Title: "Wetland", MaxW: 160, MaxH: 120})
	res, err := s.handlePlayer("player.open", args, []*os.File{f})
	if err != nil {
		t.Fatal(err)
	}
	id := res.(playerState).ID
	frame := media.NewFrame(160, 90)
	var st playerState
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		s.streams[id](0, frame)
		p := s.players[id]
		p.mu.Lock()
		st = p.stateLocked()
		p.mu.Unlock()
		if st.State != "playing" {
			break
		}
	}
	if st.State != "failed" || !strings.Contains(st.Error, "s16le") {
		t.Fatalf("decoder failure reported as %+v", st)
	}
}

// decoderPidEnv makes the test binary a mediad probing with an ffprobe that
// never ends, which writes its pid to the file it names.
const decoderPidEnv = "MEDIAD_TEST_DECODER_PID"

// A decoder dies with mediad. Killed while it probed the sample video the
// Shell had just put in the library (TestScenarioPartEnds ends a stack 1–2
// seconds after its start), mediad left ffprobe running with the tests'
// environment, which that test's check that nothing outlives a stack
// counted (2026-11-27).
func TestDecodersDieWithMediad(t *testing.T) {
	if pidFile := os.Getenv(decoderPidEnv); pidFile != "" {
		f, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		_, err = probe(f)
		t.Fatalf("the probe ended: %v", err)
	}
	bin := t.TempDir()
	fake := "#!/bin/sh\necho $$ > \"$" + decoderPidEnv + "\"\nexec sleep 600\n"
	if err := os.WriteFile(filepath.Join(bin, "ffprobe"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDecodersDieWithMediad$", "-test.count=1")
	cmd.Env = append(os.Environ(), decoderPidEnv+"="+pidFile, "PATH="+bin+":"+os.Getenv("PATH"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	pid := 0
	for deadline := time.Now().Add(10 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		raw, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	if pid == 0 {
		t.Fatal("ffprobe did not start")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	alive := func() bool {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		s := string(raw)
		i := strings.LastIndexByte(s, ')')
		return err == nil && i > 0 && i+2 < len(s) && s[i+2] != 'Z'
	}
	for deadline := time.Now().Add(5 * time.Second); alive() && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if alive() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("ffprobe outlived mediad")
	}
}

// The player's decoder outlives the thread that started it: Linux sends the
// parent-death signal when that thread ends, and Go ends a thread whose
// locked goroutine returns.
func TestPlayerDecoderOutlivesTheThreadThatStartedIt(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := &player{Title: "t", W: 16, H: 16, file: f}
	subproctest.OnEndingThread(t, func() {
		if err := p.run(); err != nil {
			t.Error(err)
		}
	})
	if p.cmd == nil {
		t.FailNow()
	}
	defer func() {
		_ = p.cmd.Process.Kill()
		for range p.frames {
		}
	}()
	if subproctest.Gone(p.cmd.Process.Pid, 300*time.Millisecond) {
		t.Fatal("ffmpeg ended with the thread that started it")
	}
}
