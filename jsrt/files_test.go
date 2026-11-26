package jsrt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/ui"
)

// Files that come with a capability's reply reach JavaScript: KarioFile
// objects on the result's "files" (read, readAll, size, close, with their
// limits), an audio stream's ring mapped as "ring"; files the app is not
// given, drops or cannot have are closed.
func TestReplyFiles(t *testing.T) { testReplyFiles(t, jsrtEngine(t), true) }

// sendFiles answers with files attached (SCM_RIGHTS), as the Shell's
// sendFiles does.
func (s *shell) sendFiles(m ui.Msg, files ...*os.File) {
	s.t.Helper()
	m.FDs = len(files)
	if err := s.conn.SendFiles(m, files...); err != nil {
		s.t.Fatalf("send %s: %v", m.T, err)
	}
	for _, f := range files {
		f.Close()
	}
}

// fileWith is a read-only file holding data.
func fileWith(t *testing.T, data string) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// openFDs counts the app process's descriptors.
func (s *shell) openFDs() int {
	s.t.Helper()
	es, err := os.ReadDir(filepath.Join("/proc", itoa(s.cmd.Process.Pid), "fd"))
	if err != nil {
		s.t.Fatal(err)
	}
	return len(es)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// fdsBack waits for the app's descriptors to come back to want.
func (s *shell) fdsBack(want int) {
	s.t.Helper()
	for end := time.Now().Add(5 * time.Second); s.openFDs() != want; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			s.t.Fatalf("the app holds %d descriptors, %d before", s.openFDs(), want)
		}
	}
}

func testReplyFiles(t *testing.T, start engine, collected bool) {
	dir := appCopy(t, "files")
	s := start(t, t.TempDir(), "en", filepath.Join(dir, "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	base := s.openFDs()

	answer := func(act, cap string, data string, files ...*os.File) ui.Msg {
		t.Helper()
		s.send(action(act))
		req := s.expect(ui.MsgReq)
		if req.Cap != cap {
			t.Fatalf("%s: request %+v", act, req)
		}
		s.sendFiles(ui.Msg{T: ui.MsgRes, Req: req.Req, OK: true, Data: json.RawMessage(data)}, files...)
		return req
	}

	answer("pick", ui.CapMediaPick, `{"id":"media:p1","mime":"image/png"}`, fileWith(t, "PNG!hello"))
	s.screenText(`picked 1 9 PNG! 5 PNG!hello {"id":"media:p1","mime":"image/png"} true TypeError: KarioFile.read: the file is closed`)
	s.fdsBack(base)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	answer("many", ui.CapFilePick, `{"id":"f1"}`, fileWith(t, "a"), r, fileWith(t, "ccc"))
	s.screenText("many a,ccc")
	if !s.logHas("a file that is not a regular file, closed") {
		t.Fatalf("log: %s", s.log())
	}
	s.fdsBack(base)

	big := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(big, nil, 0o600); err != nil || os.Truncate(big, 17<<20) != nil {
		t.Fatal(err)
	}
	bf, err := os.Open(big)
	if err != nil {
		t.Fatal(err)
	}
	answer("big", ui.CapFileOpen, `{"id":"f1"}`, bf)
	s.screenText("big 17825792 RangeError: KarioFile.readAll: the file is over 16 MB; read it in parts (read()) 1048576 " +
		"RangeError: KarioFile.read: from 1 to 1048576 bytes at a time RangeError: KarioFile.read: from 1 to 1048576 bytes at a time")
	s.fdsBack(base)

	// The app keeps at most 64 at a time; closing them makes room again.
	for i := 1; i <= 8; i++ {
		var fs []*os.File
		for j := 0; j < 8; j++ {
			fs = append(fs, fileWith(t, "k"))
		}
		answer("keep", ui.CapFilePick, `{"id":"f1"}`, fs...)
		s.screenText("kept " + itoa(i*8))
	}
	answer("keep", ui.CapFilePick, `{"id":"f1"}`, fileWith(t, "k"))
	s.screenText("err too many files open from replies (at most 64): close() the ones done with")
	s.send(action("release"))
	s.screenText("released")
	s.fdsBack(base)

	// audio.stream: the app writes into the shared ring; audio.record: it
	// reads what the microphone side wrote.
	ring, err := audio.NewRingFile("beep", 4800)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := audio.MapRing(ring)
	if err != nil {
		t.Fatal(err)
	}
	defer mine.Close()
	answer("play", ui.CapAudioStream, `{"id":"s1","state":"playing"}`, ring)
	s.screenText(`play s1 48000 4800 480 4320 4800 true {"id":"s1","state":"playing"}`)
	s.screenText("play closed true TypeError: AudioRing.write: the ring is closed")
	got := make([]int16, 480)
	if n := mine.Read(got); n != 480 || got[0] != -240 || got[479] != 239 || !mine.Finished() {
		t.Fatalf("ring: %d %v %v", n, got[:3], mine.Finished())
	}

	rec, err := audio.NewRingFile("memo", 4800)
	if err != nil {
		t.Fatal(err)
	}
	mic, err := audio.MapRing(rec)
	if err != nil {
		t.Fatal(err)
	}
	defer mic.Close()
	samples := make([]int16, 300)
	for i := range samples {
		samples[i] = int16(1000 + i)
	}
	mic.Write(samples)
	answer("record", ui.CapAudioRecord, `{"id":"r1"}`, rec)
	s.screenText("record 300 1000 1299 0 TypeError: AudioRing.read: the samples are an Int16Array")

	// A ring that could shrink under the mapping is refused.
	answer("play", ui.CapAudioStream, `{"id":"s2"}`, fileWith(t, strings.Repeat("\x00", 128)))
	s.screenText("err the audio ring is not sealed against shrinking")
	s.fdsBack(base)

	// A failed call's files and a result the app drops are closed.
	s.send(action("ignore"))
	req := s.expect(ui.MsgReq)
	s.sendFiles(ui.Msg{T: ui.MsgRes, Req: req.Req, Err: ui.ErrCancelled}, fileWith(t, "x"))
	s.screenText("err cancelled")
	s.fdsBack(base)
	answer("ignore", ui.CapFilePick, `{"id":"f1"}`, fileWith(t, "x"), fileWith(t, "y"))
	s.screenText("ignored")
	if collected {
		s.fdsBack(base)
	}
}
