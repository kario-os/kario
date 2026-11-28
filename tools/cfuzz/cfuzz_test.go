// Package cfuzz fuzzes the C and C++ parsers from Go: their harnesses
// (sdk/c/fuzz_lines.c, telephonyd/test/json_lines.cpp and rpc_lines.cpp,
// jsrt/src/fuzz_ws.c, fuzz_ws_answer.c, fuzz_fetch_head.c and fuzz_utf8.c), built with the address and undefined behaviour sanitizers (make fuzz
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

// FuzzTelephonydRPC is what the Shell, phone-smsd and phone-audiod write
// on phone-telephonyd's socket: the requests, served by the real server
// over the simulated modem (telephonyd/test/rpc_lines.cpp), each line
// answered once and in its time.
func FuzzTelephonydRPC(f *testing.F) {
	f.Add(lines(`{"id":1,"method":"status"}`, `{"id":2,"method":"subscribe"}`, `{"id":3,"method":"radio.set","params":{"on":false}}`,
		`{"id":4,"method":"radio.set","params":{"on":true}}`, `{"id":5,"method":"data.set","params":{"on":true}}`,
		`{"id":6,"method":"roaming.set","params":{"on":true,"national":false}}`, `{"id":7,"method":"apn.list"}`,
		`{"id":8,"method":"apn.set","params":{"op":"save","apn":{"id":"cmnet","name":"CMNET","apn":"cmnet.test","types":["default"],"ip_type":"ipv4"}}}`,
		`{"id":9,"method":"apn.mms"}`, `{"id":10,"method":"mms.connect"}`, `{"id":11,"method":"mms.disconnect"}`))
	f.Add(lines(`{"id":1,"method":"dial","params":{"number":"13800000001"}}`, `{"id":2,"method":"calls"}`,
		`{"id":3,"method":"dtmf","params":{"call_id":"1","tones":"123#"}}`, `{"id":4,"method":"hold","params":{"call_id":"1","on":true}}`,
		`{"id":5,"method":"mute","params":{"call_id":"1","on":true}}`, `{"id":6,"method":"sim.ring","params":{"number":"13700000000"}}`,
		`{"id":7,"method":"answer","params":{"call_id":"2"}}`, `{"id":8,"method":"sim.hangup","params":{"number":"13700000000"}}`,
		`{"id":9,"method":"hangup","params":{"call_id":"1","blocked":true}}`, `{"id":10,"method":"log"}`))
	f.Add(lines(`{"id":1,"method":"sms.send","params":{"to":"10086","text":"caf\u00e9 \ud83d\ude00","id":"o1"}}`,
		`{"id":2,"method":"sim.sms","params":{"from":"10086","text":"held","held":true}}`, `{"id":3,"method":"sms.pending"}`,
		`{"id":4,"method":"sms.ack","params":{"keys":["m1@failed","nothing"]}}`, `{"id":5,"method":"sms.legacy"}`,
		`{"id":6,"method":"sms.legacy_drop"}`, `{"id":7,"method":"sim.sms","params":{"from":"10086","data":"0041","class":0}}`,
		`{"id":8,"method":"sim.cb","params":{"channel":4371,"serial":12288,"text":"Flood warning"}}`))
	f.Add(lines(`{"id":1,"method":"ussd.initiate","params":{"code":"*101#"}}`, `{"id":2,"method":"ussd.respond","params":{"text":"1"}}`,
		`{"id":3,"method":"ussd.cancel"}`, `{"id":4,"method":"sim.ussd","params":{"text":"Your plan renews tomorrow.","request":false}}`,
		`{"id":5,"method":"sim.network","params":{"sim_mcc":"334","net_mcc":"334","nitz_offset":-360,"nitz_dst":1,"spn":"x"}}`,
		`{"id":6,"method":"sim.roam","params":{"net_mcc":"262","net_mnc":"02","operator":"Vodafone.de"}}`,
		`{"id":7,"method":"sim.lock","params":{"pin":"1234"}}`, `{"id":8,"method":"sim.unlock","params":{"pin":"12"}}`,
		`{"id":9,"method":"sim.unlock","params":{"pin":"1234"}}`, `{"id":10,"method":"sim.pin_lock","params":{"pin":"1234","on":true}}`,
		`{"id":11,"method":"sim.change_pin","params":{"pin":"1234","new_pin":"2468"}}`, `{"id":12,"method":"phonebook.read"}`))
	f.Add(lines(`{"id":1,"method":"log.import","params":{"entries":[{"number":"13800000001","dir":"in","result":"answered","at":1700000000,"duration":30},{"number":"x"}]}}`,
		`{"id":2,"method":"log.delete","params":{"ids":["i1","nope",3]}}`, `{"id":3,"method":"sim.slow","params":{"dial_ms":3000,"answer_ms":5000}}`,
		`{"id":4,"method":"dial","params":{"number":"10086"}}`, `{"id":5,"method":"nope"}`, `not json`, `[1]`, ``,
		`{"id":"x","method":7,"params":[]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		bin := harness(t, "telephonyd-rpc")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin)
		var stderr bytes.Buffer
		cmd.Env = []string{"ASAN_OPTIONS=detect_leaks=1:max_allocation_size_mb=64", "TMPDIR=" + t.TempDir()}
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

// FuzzWebSocketAnswer is a WebSocket server's answer to the opening
// handshake: kario-jsrt's check of it (jsrt/src/wsframe.c) against the
// harness's own reading (fuzz_ws_answer.c); the first byte picks the
// protocols the client offered.
func FuzzWebSocketAnswer(f *testing.F) {
	const accept = "Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n"
	ok := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" + accept
	f.Add([]byte("\x00" + ok + "\r\n\x81\x02hi"))
	f.Add([]byte("\x01" + ok + "Sec-WebSocket-Protocol: chat\r\n\r\n"))
	f.Add([]byte("\x02" + "HTTP/1.1 101 Switching Protocols\r\nupgrade:WebSocket \r\nconnection: keep-alive, Upgrade\r\n" + accept + "sec-websocket-protocol:\tsuperchat\r\n\r\n"))
	f.Add([]byte("\x03" + ok + "Sec-WebSocket-Protocol: v3.kario\r\n\r\n"))
	f.Add([]byte("\x00" + ok + "Sec-WebSocket-Extensions: permessage-deflate\r\n\r\n"))
	f.Add([]byte("\x00HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"))
	f.Add([]byte("\x00HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		bin := harness(t, "ws-answer")
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

// FuzzFetchHead is a server's response headers as libcurl hands them to
// fetch, line by line: kario-jsrt's check of each line and its split of
// them into the app's headers (jsrt/src/fetchhead.c) against the
// harness's own reading (fuzz_fetch_head.c).
func FuzzFetchHead(f *testing.F) {
	f.Add([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nSet-Cookie: a=1\r\nset-cookie: b=2\r\n__proto__: p\r\nConstructor: c\r\n\r\n"))
	f.Add([]byte("HTTP/1.1 301 Moved Permanently\r\nLocation: /next\r\nSet-Cookie: a=1\r\n\r\nHTTP/1.1 200 OK\r\nX-A: 1\r\n\r\n"))
	f.Add([]byte("HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 200 OK\r\nA:  b \r\nA:\r\nA:\t c\r\nb: \xc3\xa9\xff\r\n\r\n"))
	f.Add([]byte("HTTP/2 200\r\nx-a: 1\r\nx-a: 2\r\ncontent-length: 2\r\n\r\n"))
	f.Add([]byte("HTTP/1.1 200 OK\r\n" + strings.Repeat("a:x\n", 2000) + "\n"))
	f.Add([]byte("HTTP/1.1 200 OK\r\n" + strings.Repeat("a:x\n", 2100) + "\n")) // past the harness's 8 KB
	f.Add([]byte("HTTP/1.1 200 OK\r\nX: a\x00b\r\nY: c\r\n\r\n"))
	f.Add([]byte("HTTP/1.1 200 OK\r\nA: 1\r\n fold\r\nno colon\r\n:empty\r\nb@d: x\r\nv: \x7f\r\nw: a\rb\r\n\r\n"))
	f.Add([]byte("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nX-Trailer: 1\r\n\r\n"))
	f.Add([]byte("HTTP/1.1 204 No Content\r\n\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		bin := harness(t, "fetch-head")
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
