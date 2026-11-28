package shell

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// Protocol compatibility (22-developer-api.md §2.1). testdata/compat holds
// what the system's apps sent during a selftest run, one transcript per app
// (TestCompatRecord makes them); every later Shell must keep accepting them
// (TestCompatTranscripts). make compat-old runs an older commit's app
// binaries against this Shell (TestOldApps).
const (
	compatDir = "testdata/compat"
	// PHONE_COMPAT_RECORD=1 go test ./shell -run '^TestCompatRecord$' -timeout 30m
	compatRecordEnv = "PHONE_COMPAT_RECORD"
	// The tee between the Shell and an app while recording: the program it
	// runs (and its first arguments), and where the lines go.
	teeRealEnv = "PHONE_TEE_REAL"
	teeOutEnv  = "PHONE_TEE_OUT"
	// The replaying fake apps read the transcripts from here and write what
	// the Shell answered to replayResult in their data directory.
	replayEnv    = "PHONE_REPLAY_DIR"
	replayPrefix = "replay."
	replayDone   = "replay-done"
	replayResult = "replay.json"
	// make compat-old: <dir>/src the old sources, <dir>/bin their apps.
	oldAppsEnv = "PHONE_OLD_APPS"
	// Lines kept per transcript, and the longest kept.
	compatMaxLines = 200
	compatMaxLine  = 256 << 10
)

// teeApp runs the app named by PHONE_TEE_REAL behind a socket of its own,
// passing everything through (files the Shell sends included) and appending
// each line the app sends to PHONE_TEE_OUT/<app id>.jsonl.
func teeApp() int {
	shellSide, err := net.FileConn(os.NewFile(3, "ui"))
	if err != nil {
		return 2
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return 2
	}
	theirs := os.NewFile(uintptr(fds[1]), "app")
	mine, err := net.FileConn(os.NewFile(uintptr(fds[0]), "tee"))
	if err != nil {
		return 2
	}
	argv := append(strings.Fields(os.Getenv(teeRealEnv)), flag.Args()...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.ExtraFiles, cmd.Stdout, cmd.Stderr = []*os.File{theirs}, os.Stdout, os.Stderr
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, teeRealEnv+"=") && !strings.HasPrefix(kv, fakeAppEnv+"=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if err := cmd.Start(); err != nil {
		return 2
	}
	theirs.Close()
	rec, _ := os.OpenFile(filepath.Join(os.Getenv(teeOutEnv), os.Getenv("PHONE_APP_ID")+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	go func() {
		rd := bufio.NewReaderSize(mine, 1<<20)
		for {
			line, err := rd.ReadBytes('\n')
			if len(line) > 0 {
				if len(line) <= compatMaxLine {
					_, _ = rec.Write(line)
				}
				if _, werr := shellSide.Write(line); werr != nil {
					return
				}
			}
			if err != nil {
				shellSide.Close()
				return
			}
		}
	}()
	go func() {
		in, out := shellSide.(*net.UnixConn), mine.(*net.UnixConn)
		buf, oob := make([]byte, 1<<16), make([]byte, syscall.CmsgSpace(4*16))
		for {
			n, oobn, _, _, err := in.ReadMsgUnix(buf, oob)
			if n > 0 || oobn > 0 {
				_, _, _ = out.WriteMsgUnix(buf[:n], oob[:oobn], nil)
				msgs, _ := syscall.ParseSocketControlMessage(oob[:oobn])
				for _, m := range msgs {
					fds, _ := syscall.ParseUnixRights(&m)
					for _, fd := range fds {
						syscall.Close(fd)
					}
				}
			}
			if err != nil {
				out.CloseWrite()
				return
			}
		}
	}()
	err = cmd.Wait()
	rec.Close()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 0
}

// shape names what a message is made of: the keys at every depth, and the
// values of the fields that name a type.
func shape(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	var parts []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				p := path + "." + k
				switch k {
				case "t", "cap", "kind", "op", "event", "style":
					if s, ok := e.(string); ok {
						p += "=" + s
					}
				}
				parts = append(parts, p)
				walk(p, e)
			}
		case []any:
			for _, e := range x {
				walk(path+"[]", e)
			}
		}
	}
	walk("", v)
	sort.Strings(parts)
	return strings.Join(slices.Compact(parts), " ")
}

// compactTranscript keeps the first line of each shape, hello first.
func compactTranscript(lines [][]byte) [][]byte {
	var out [][]byte
	seen := map[string]bool{}
	for _, l := range lines {
		var m struct{ T string }
		if json.Unmarshal(l, &m) != nil || m.T == ui.MsgHello && len(out) > 0 {
			continue
		}
		if len(out) == 0 && m.T != ui.MsgHello {
			continue
		}
		if s := shape(l); !seen[s] && len(out) < compatMaxLines {
			seen[s] = true
			out = append(out, l)
		}
	}
	return out
}

// TestCompatRecord runs the selftest with every app behind the tee and writes
// the transcripts to testdata/compat.
func TestCompatRecord(t *testing.T) {
	if os.Getenv(compatRecordEnv) == "" {
		t.Skip("records the transcripts: " + compatRecordEnv + "=1")
	}
	rec := t.TempDir()
	wrap := func(path string) {
		real := path + ".real"
		if err := os.Rename(path, real); err != nil {
			t.Fatal(err)
		}
		script := fmt.Sprintf("#!/bin/sh\nexport %s=%q %s=1 %s=%q\nexec %q -test.run='^TestFakeApp$' \"$@\"\n",
			teeRealEnv, real, fakeAppEnv, teeOutEnv, rec, os.Args[0])
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := e2eStackWith(t, func(prefix string, cfg *Config) {
		bins, _ := filepath.Glob(filepath.Join(cfg.AppBinDir, "phone-app-*"))
		for _, b := range append(bins, filepath.Join(cfg.AppBinDir, "kario-jsrt")) {
			if st, err := os.Stat(b); err == nil && !st.IsDir() {
				wrap(b)
			}
		}
		if py := filepath.Join(cfg.AppsDir, "hello-py", "hello.py"); fileExists(py) {
			wrap(py)
		}
	})
	var out strings.Builder
	if !(&Runner{C: c, Out: &out}).Run(SelftestScript) {
		t.Logf("the selftest behind the tee failed (the transcripts are kept):\n%s", out.String())
	}
	c.Shutdown()
	files, _ := filepath.Glob(filepath.Join(rec, "*.jsonl"))
	if err := os.MkdirAll(compatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		var lines [][]byte
		for _, l := range strings.Split(string(raw), "\n") {
			if l != "" {
				lines = append(lines, []byte(l))
			}
		}
		kept := compactTranscript(lines)
		if len(kept) < 2 {
			continue
		}
		var b strings.Builder
		for _, l := range kept {
			b.Write(l)
			b.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(compatDir, filepath.Base(f)), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d of %d lines", filepath.Base(f), len(kept), len(lines))
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

type replayOutcome struct {
	Sent, Answered int
	Unknown        []string
}

// replayApp sends a transcript's lines as they were recorded and writes how
// the Shell answered.
func replayApp(f *fakeConn, file string) int {
	raw, err := os.ReadFile(file)
	if err != nil {
		return 2
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	_ = f.raw(lines[0] + "\n")
	for {
		m, err := f.recv()
		if err != nil {
			return 0
		}
		if m.T == ui.MsgStart {
			break
		}
	}
	var mu sync.Mutex
	var out replayOutcome
	go func() {
		for {
			m, err := f.recv()
			if err != nil {
				return
			}
			switch m.T {
			case ui.MsgPing:
				_ = f.send(ui.Msg{T: ui.MsgPong, Value: m.Value})
			case ui.MsgRes:
				mu.Lock()
				out.Answered++
				if strings.HasPrefix(m.Err, "unknown capability") {
					out.Unknown = append(out.Unknown, m.Err)
				}
				mu.Unlock()
			}
		}
	}()
	for _, l := range lines[1:] {
		var m struct{ T string }
		_ = json.Unmarshal([]byte(l), &m)
		if m.T == ui.MsgReq {
			mu.Lock()
			out.Sent++
			mu.Unlock()
		}
		if f.raw(l+"\n") != nil {
			return 0
		}
	}
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		mu.Lock()
		done := out.Answered >= out.Sent
		mu.Unlock()
		if done {
			break
		}
	}
	mu.Lock()
	b, _ := json.Marshal(out)
	mu.Unlock()
	_ = os.WriteFile(dataFile(replayResult), b, 0o600)
	_ = f.send(ui.Msg{T: ui.MsgReset, Screen: &ui.Screen{ID: replayDone, Title: replayDone}})
	time.Sleep(time.Minute)
	return 0
}

// TestCompatTranscripts replays every recorded transcript to this Shell: no
// line may be malformed or unknown to it, no capability unknown, every
// request answered, and the app is not ended.
func TestCompatTranscripts(t *testing.T) {
	if !alone(t) {
		return
	}
	files, _ := filepath.Glob(filepath.Join(compatDir, "*.jsonl"))
	if len(files) == 0 {
		t.Fatal("no transcripts in " + compatDir)
	}
	apps := map[string]string{}
	var ids []string
	for _, f := range files {
		id := "test." + replayPrefix + strings.TrimSuffix(filepath.Base(f), ".jsonl")
		apps[id] = ""
		ids = append(ids, id)
	}
	r := newHostileRig(t, apps)
	abs, _ := filepath.Abs(compatDir)
	r.c.cfg.AppEnv = append(r.c.cfg.AppEnv, replayEnv+"="+abs)
	for _, id := range ids {
		t.Run(strings.TrimPrefix(id, "test."+replayPrefix), func(t *testing.T) {
			r.t = t
			a := r.launch(id)
			done := r.until(40*time.Second, func() bool {
				if len(r.c.overlays) > 0 {
					r.c.Key(Named(KeyEsc))
				}
				return !a.running || a.top() != nil && a.top().S.ID == replayDone
			})
			var out replayOutcome
			raw, _ := os.ReadFile(filepath.Join(r.state, "apps", id, replayResult))
			_ = json.Unmarshal(raw, &out)
			t.Logf("%d requests, %d answered; malformed %d, unknown types %v", out.Sent, out.Answered, a.guard.bad, a.guard.unknown)
			switch {
			case !done || !a.running:
				t.Fatalf("replay did not finish (running %v, crash %q)", a.running, r.crash(id))
			case a.guard.bad > 0 || len(a.guard.unknown) > 0 || a.guard.violation != "":
				t.Fatalf("the Shell no longer accepts the transcript: malformed %d, unknown %v, %q", a.guard.bad, a.guard.unknown, a.guard.violation)
			case len(out.Unknown) > 0:
				t.Fatalf("capabilities no longer known: %v", out.Unknown)
			case out.Answered < out.Sent:
				t.Fatalf("%d of %d requests answered", out.Answered, out.Sent)
			}
			r.c.kill(a)
			r.ended(a, 5*time.Second)
		})
	}
}

// Hello's version: any minor version of the system's major, or none, is
// accepted; another major (older or newer) ends the app. Start says which
// the system took.
func TestProtoVersions(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0)}
	for _, tc := range []struct {
		proto  string
		protos []string
		ok     bool
	}{
		{ui.Proto, nil, true}, {ui.Proto + ".3", nil, true}, {"", nil, true}, {"", []string{"phone.ui/1", ui.Proto}, true},
		{"phone.ui/1", nil, false}, {"phone.ui/1.0", nil, false}, {"phone.ui/-1", nil, false}, {"phone.ui/x", nil, false}, {"other/0", nil, false},
		{"phone.ui/00", nil, false}, {"phone.ui/1", []string{"phone.ui/2"}, false},
	} {
		a := &App{M: &Manifest{ID: "test.version", Name: "v"}, running: true}
		if got := c.speaks(a, ui.Msg{T: ui.MsgHello, Proto: tc.proto, Protos: tc.protos}); got != tc.ok || (a.guard.violation == "") != tc.ok {
			t.Errorf("hello %q %q: accepted %v, violation %q", tc.proto, tc.protos, got, a.guard.violation)
		}
		if tc.ok && (a.proto != nil || c.spoken(a) != ui.Proto) {
			t.Errorf("hello %q %q: speaks %s", tc.proto, tc.protos, c.spoken(a))
		}
	}
}

// TestOldApps runs the apps of an older commit (make compat-old) on this
// tree's Shell and services: each starts, shows a page and takes a few keys
// without a malformed or unknown message, a protocol violation or a crash.
func TestOldApps(t *testing.T) {
	old := os.Getenv(oldAppsEnv)
	if old == "" {
		t.Skip("make compat-old OLD_REV=<commit>")
	}
	var ids []string
	c, _ := e2eStackWith(t, func(prefix string, cfg *Config) {
		apps, bins := filepath.Join(old, "stage", "apps"), filepath.Join(old, "stage", "bin")
		_ = os.RemoveAll(filepath.Join(old, "stage"))
		for _, d := range []string{apps, bins} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		manifests, _ := filepath.Glob(filepath.Join(old, "src", "apps", "*", "manifest.json"))
		for _, mf := range manifests {
			var m Manifest
			if raw, err := os.ReadFile(mf); err != nil || json.Unmarshal(raw, &m) != nil || m.Runtime != "" {
				continue
			}
			src := filepath.Dir(mf)
			if name, ok := strings.CutPrefix(m.Exec, "phone-app-"); ok {
				bin := filepath.Join(old, "bin", name)
				if !fileExists(bin) {
					continue
				}
				if err := os.Symlink(bin, filepath.Join(bins, m.Exec)); err != nil {
					t.Fatal(err)
				}
			} else if !strings.HasPrefix(m.Exec, "./") {
				continue
			}
			dst := filepath.Join(apps, filepath.Base(src))
			if out, err := exec.Command("cp", "-r", src, dst).CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if strings.HasPrefix(m.Exec, "./") {
				// git keeps no mode for it; the image installs it executable.
				_ = os.Chmod(filepath.Join(dst, m.Exec), 0o755)
			}
			ids = append(ids, m.ID)
		}
		// The sandbox launcher and the TypeScript runtime stay this tree's.
		for _, keep := range []string{"phone-sandbox", "kario-jsrt"} {
			if p := filepath.Join(cfg.AppBinDir, keep); fileExists(p) {
				_ = os.Symlink(p, filepath.Join(bins, keep))
			}
		}
		cfg.AppsDir, cfg.AppBinDir = apps, bins
	})
	sort.Strings(ids)
	if len(ids) == 0 {
		t.Fatal("no old apps to run")
	}
	r := &hostileRig{t: t, c: c}
	c.SetScreenAuto(false)
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			r.t = t
			a := r.app(id)
			c.Launch(a, "")
			if !r.until(10*time.Second, func() bool { return a.ready && a.top() != nil || !a.running }) || !a.running {
				t.Fatalf("no page (ready %v, running %v, crash %q)", a.ready, a.running, r.crash(id))
			}
			for _, k := range []string{KeyDown, KeyDown, KeyEnter, KeyEsc, KeyEsc} {
				c.Key(Named(k))
				r.pump(400 * time.Millisecond)
				for range 3 {
					if len(c.overlays) == 0 {
						break
					}
					c.Key(Named(KeyEsc))
					r.pump(200 * time.Millisecond)
				}
			}
			top := ""
			if v := a.top(); v != nil {
				top = v.S.ID
			}
			t.Logf("top page %q; malformed %d, unknown %v", top, a.guard.bad, a.guard.unknown)
			if a.guard.bad > 0 || len(a.guard.unknown) > 0 || a.guard.violation != "" || r.crash(id) != "" {
				t.Fatalf("malformed %d, unknown %v, violation %q, crash %q", a.guard.bad, a.guard.unknown, a.guard.violation, r.crash(id))
			}
			c.GoHome()
			c.kill(a)
			r.ended(a, 5*time.Second)
		})
	}
}
