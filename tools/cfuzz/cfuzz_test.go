// Package cfuzz fuzzes the C and C++ parsers from Go: their harnesses
// (sdk/c/fuzz_lines.c, telephonyd/test/json_lines.cpp, jsrt/src/fuzz_ws.c),
// built with the address and undefined behaviour sanitizers (make fuzz
// builds them into /tmp/fuzz/bin), read the fuzzed input from a socket or
// a pipe; a sanitizer's report or a crash fails the input. The targets are
// skipped when the harnesses are not built (make test).
package cfuzz

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// harness is the built harness's path, from KARIO_CFUZZ_BIN (a directory).
func harness(t *testing.T, name string) string {
	dir := os.Getenv("KARIO_CFUZZ_BIN")
	if dir == "" {
		t.Skip("KARIO_CFUZZ_BIN is not set (make fuzz builds the C harnesses)")
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("no harness %s", p)
	}
	return p
}

// run runs cmd and fails on anything but a clean exit within the time.
func run(t *testing.T, cmd *exec.Cmd, stderr *bytes.Buffer) {
	t.Helper()
	err := cmd.Wait()
	var exit *exec.ExitError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || cmd.ProcessState != nil && !cmd.ProcessState.Exited():
		t.Fatalf("the harness hung or was killed: %v\n%s", err, stderr.Bytes())
	case errors.As(err, &exit):
		t.Fatalf("the harness failed (%v):\n%s", err, stderr.Bytes())
	case err != nil:
		t.Fatal(err)
	}
}

func lines(ls ...string) []byte {
	var b []byte
	for _, l := range ls {
		b = append(append(b, l...), '\n')
	}
	return b
}

// FuzzPhoneUI is what the Shell sends a C or C++ app: libphoneui's JSON
// reader takes every line apart.
func FuzzPhoneUI(f *testing.F) {
	f.Add(lines(`{"t":"start","value":"kario://notes/1"}`, `{"t":"ping","value":"7"}`,
		`{"t":"action","screen":"home","node":"q","action":"go","value":"x","fields":{"q":"caf\u00e9 \ud83d\ude00","n":[1,{"a":null}]}}`,
		`{"t":"event","event":"x"}`, `{"t":"res","req":1,"ok":true,"data":{"text":"hi"}}`, `{"t":"lifecycle","state":"background"}`))
	f.Add(lines(`{"t":"action","fields":{"q":"\u"}}`, `{"t":"res","req":1,"err":"no"}`, `{"t":"popped","screen":"a\\"}`))
	f.Fuzz(phoneUI)
}

// phoneUI runs the libphoneui harness on what a Shell sends it.
func phoneUI(t *testing.T, data []byte) {
	bin := harness(t, "phoneui-lines")
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	app := os.NewFile(uintptr(fds[1]), "app")
	nc, err := net.FileConn(os.NewFile(uintptr(fds[0]), "shell"))
	if err != nil {
		t.Fatal(err)
	}
	shell := nc.(*net.UnixConn)
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	var stderr bytes.Buffer
	cmd.Env = []string{"PHONE_UI_FD=3", "PHONE_APP_ID=com.example.c", "ASAN_OPTIONS=detect_leaks=0"}
	cmd.ExtraFiles, cmd.Stderr = []*os.File{app}, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	app.Close()
	go io.Copy(io.Discard, shell)
	shell.Write(data)
	shell.CloseWrite()
	run(t, cmd, &stderr)
}

// FuzzTelephonydJSON is a request line to phone-telephonyd's control
// socket, read by its own JSON parser.
func FuzzTelephonydJSON(f *testing.F) {
	f.Add(lines(`{"id":1,"method":"dial","params":{"number":"+8613800138000","sim":1}}`,
		`{"id":2,"method":"sms.send","params":{"to":"10086","text":"caf\u00e9 \ud83d\ude00 \"q\"","parts":[1,2.5,-3e2]}}`,
		`{"id":3,"ok":true,"result":null}`, `[true,false,{},[],""]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		bin := harness(t, "telephonyd-json")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin)
		var stderr bytes.Buffer
		cmd.Env = []string{"ASAN_OPTIONS=detect_leaks=0"}
		cmd.Stdin, cmd.Stderr = bytes.NewReader(data), &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		run(t, cmd, &stderr)
	})
}

// FuzzWebSocketFrames is what a WebSocket server sends a TypeScript app:
// kario-jsrt's frame reader (jsrt/src/wsframe.c) takes it apart in pieces
// and checks its invariants (fuzz_ws.c); the first byte picks the pieces.
func FuzzWebSocketFrames(f *testing.F) {
	frame := func(op byte, p string) string {
		b := []byte{0x80 | op}
		switch n := len(p); {
		case n < 126:
			b = append(b, byte(n))
		case n < 65536:
			b = append(b, 126, byte(n>>8), byte(n))
		default:
			b = append(b, 127, 0, 0, 0, 0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		}
		return string(append(b, p...))
	}
	f.Add([]byte("\x07" + frame(1, "hello") + frame(2, "\x00\x01") + frame(9, "p") + frame(10, "") + frame(8, "\x03\xe8bye")))
	f.Add([]byte("\x03" + "\x01\x03caf" + frame(9, "x") + "\x80\x02\xc3\xa9" + frame(1, strings.Repeat("y", 300))))
	f.Add([]byte("\x00" + frame(2, strings.Repeat("z", 70000)) + frame(8, "")))
	f.Add([]byte("\x05\x82\x7f\x7f\xff\xff\xff\xff\xff\xff\xff"))
	f.Add([]byte("\x02" + frame(1, "\xc3\x28") + frame(8, "\x03")))
	f.Fuzz(func(t *testing.T, data []byte) {
		bin := harness(t, "ws-frames")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin)
		var stderr bytes.Buffer
		cmd.Env = []string{"ASAN_OPTIONS=detect_leaks=1"}
		cmd.Stdin, cmd.Stderr = bytes.NewReader(data), &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		run(t, cmd, &stderr)
	})
}

// A line nested deeper than any message is refused, not followed down
// until the stack runs out.
func TestDeepNesting(t *testing.T) {
	deep := strings.Repeat("[", 300000)
	t.Run("telephonyd", func(t *testing.T) {
		bin := harness(t, "telephonyd-json")
		cmd := exec.Command(bin)
		var stderr bytes.Buffer
		cmd.Env = []string{"ASAN_OPTIONS=detect_leaks=0"}
		cmd.Stdin, cmd.Stderr = strings.NewReader(deep+"\n"), &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		run(t, cmd, &stderr)
	})
	t.Run("phoneui", func(t *testing.T) {
		phoneUI(t, []byte(`{"t":"action","fields":`+deep+"}\n"))
	})
}
