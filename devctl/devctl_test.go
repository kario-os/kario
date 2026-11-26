package devctl

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestViaAddr(t *testing.T) {
	for via, want := range map[string]string{
		"10.71.0.10 40000 10.71.0.1 22":               "10.71.0.1",
		"::ffff:10.71.0.10 40000 ::ffff:10.71.0.1 22": "10.71.0.1",
		"fe80::2 40000 fe80::1 22":                    "fe80::1",
		"10.71.0.10 40000 not-an-address 22":          "",
		"10.71.0.1 22":                                "",
		"":                                            "",
	} {
		if got := ViaAddr(via); got != want {
			t.Errorf("ViaAddr(%q) = %q, want %q", via, got, want)
		}
	}
}

// Each operation carries what it needs and nothing a remote command line
// or the Shell could misread.
func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		req Request
		ok  bool
	}{
		{Request{Op: OpPush, Path: "notes.db", Size: 10}, true},
		{Request{Op: OpPush, Path: "com.example.notes:files/a b.txt"}, true},
		{Request{Op: OpPush, Path: ""}, false},
		{Request{Op: OpPush, Path: "a\nb"}, false},
		{Request{Op: OpPush, Path: "x", Size: -1}, false},
		{Request{Op: OpPull, Path: strings.Repeat("a", MaxPath+1)}, false},
		{Request{Op: OpScreenshot}, true},
		{Request{Op: OpBugreport}, true},
		{Request{Op: OpKey, Keys: []string{"enter", "down"}}, true},
		{Request{Op: OpKey}, false},
		{Request{Op: OpKey, Keys: make([]string, MaxKeys+1)}, false},
		{Request{Op: OpType, Text: "hello"}, true},
		{Request{Op: OpType}, false},
		{Request{Op: OpType, Text: "\xff"}, false},
		{Request{Op: OpSyslog, Unit: "phone-pushd.service", Lines: 50}, true},
		{Request{Op: OpSyslog, Unit: "shell"}, true},
		{Request{Op: OpSyslog, Unit: "x; reboot"}, false},
		{Request{Op: OpSyslog, Lines: -1}, false},
		{Request{Op: OpForward, Port: 80}, true},
		{Request{Op: OpForward, Port: 0}, false},
		{Request{Op: OpForward, Port: 65536}, false},
		{Request{Op: OpForward, Port: 8080, Reverse: true}, true},
		{Request{Op: OpForward, Port: 80, Reverse: true}, false},
		{Request{Op: OpReboot}, true},
		{Request{Op: OpReboot, Target: RebootBootloader}, true},
		{Request{Op: OpReboot, Target: RebootRecovery}, true},
		{Request{Op: OpReboot, Target: "edl"}, false},
		{Request{Op: OpUpdate, Size: 1}, true},
		{Request{Op: OpUpdate}, false},
	} {
		if err := tc.req.Check(); (err == nil) != tc.ok {
			t.Errorf("%+v: %v, want ok %v", tc.req, err, tc.ok)
		}
	}
}

// fakeShell answers one request on a socket as serve says, and hands the
// test what it read.
func fakeShell(t *testing.T, serve func(conn net.Conn, r *bufio.Reader, req Request)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), Sock)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		req, err := ReadRequest(r)
		if err != nil {
			_ = WriteReply(conn, Reply{Error: err.Error()})
			return
		}
		serve(conn, r, req)
	}()
	return path
}

// Bytes after a reply are exactly Size, never the reply's text, whose
// note goes beside them; a reply cut short is an error, not a short file.
func TestCallBinary(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n...")
	path := fakeShell(t, func(conn net.Conn, _ *bufio.Reader, _ Request) {
		_ = WriteReply(conn, Reply{OK: true, Size: int64(len(png)), Note: "masked: a PIN entry"})
		_, _ = conn.Write(png)
	})
	var out, notes bytes.Buffer
	if err := Call(path, Request{Op: OpScreenshot}, nil, &out, &notes); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), png) || notes.String() != "masked: a PIN entry\n" {
		t.Fatalf("out %q notes %q", out.Bytes(), notes.String())
	}
	path = fakeShell(t, func(conn net.Conn, _ *bufio.Reader, _ Request) {
		_ = WriteReply(conn, Reply{OK: true, Size: 100})
		_, _ = conn.Write([]byte("short"))
	})
	if err := Call(path, Request{Op: OpPull, Path: "x"}, nil, io.Discard, nil); err == nil || !strings.Contains(err.Error(), "received 5 of 100 bytes") {
		t.Fatalf("a short file: %v", err)
	}
}

// A push sends its bytes only once the Shell lets them in; progress lines
// come before the result.
func TestCallBodyAndProgress(t *testing.T) {
	got := make(chan []byte, 1)
	path := fakeShell(t, func(conn net.Conn, r *bufio.Reader, req Request) {
		_ = WriteReply(conn, Reply{OK: true})
		body := make([]byte, req.Size)
		_, _ = io.ReadFull(r, body)
		got <- body
		_ = WriteReply(conn, Reply{OK: true, More: true, Text: " 50% Copying"})
		_ = WriteReply(conn, Reply{OK: true, Text: "installed into slot b"})
	})
	var out bytes.Buffer
	if err := Call(path, Request{Op: OpUpdate, Size: 6}, strings.NewReader("bundle"), &out, nil); err != nil {
		t.Fatal(err)
	}
	if string(<-got) != "bundle" || out.String() != " 50% Copying\ninstalled into slot b\n" {
		t.Fatalf("out %q", out.String())
	}
	path = fakeShell(t, func(conn net.Conn, _ *bufio.Reader, _ Request) {
		_ = WriteReply(conn, Reply{Error: "not the computer's to touch"})
	})
	if err := Call(path, Request{Op: OpPush, Path: "/etc/passwd", Size: 3}, strings.NewReader("abc"), io.Discard, nil); err == nil || err.Error() != "not the computer's to touch" {
		t.Fatalf("a refused push: %v", err)
	}
}

// A forward lasts until the Shell ends it, or until the computer's side
// (body) does, which the Shell sees as the connection's end.
func TestCallForward(t *testing.T) {
	ended := make(chan struct{})
	path := fakeShell(t, func(conn net.Conn, r *bufio.Reader, _ Request) {
		_ = WriteReply(conn, Reply{OK: true, Text: "forwarding"})
		_, _ = io.Copy(io.Discard, r)
		close(ended)
	})
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() { done <- Call(path, Request{Op: OpForward, Port: 8080}, pr, &out, nil) }()
	pw.Close()
	<-ended
	if err := <-done; err != nil || out.String() != "forwarding\n" {
		t.Fatalf("%v %q", err, out.String())
	}
}
