package shell

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

// Selftests run at once on one machine (make pixel-selftest's form
// factors, make selftest's parts): the script's fetch server takes a free
// port in each, and fill gives the app its address.
func TestFetchServerOfTwoSelftestsAtOnce(t *testing.T) {
	var arg string
	for _, l := range strings.Split(SelftestScript, "\n") {
		if a, ok := strings.CutPrefix(l, "fetch-server "); ok {
			arg = a
		}
	}
	if arg == "" {
		t.Fatal("the script starts no fetch server")
	}
	var urls []string
	for range 2 {
		r := &Runner{C: snapCore(t), Out: io.Discard}
		if err := r.step("fetch-server", arg); err != nil {
			t.Fatalf("fetch-server %s: %v", arg, err)
		}
		u := r.fillText("{fetch}")
		res, err := http.Post(u+"/register", "application/json", strings.NewReader(`{"app":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if err := r.step("fetch-got", `POST /register "app":"x" 1000`); err != nil {
			t.Error(err)
		}
		urls = append(urls, u)
	}
	if !strings.HasPrefix(urls[0], "http://127.0.0.1:") || urls[0] == urls[1] {
		t.Errorf("the servers' URLs %v", urls)
	}
}

// What examples/netcheck checks against the server: a stream in two
// halves a second apart, a request held until the client goes away (which
// it notes), cookies set and sent back, a WebSocket echo.
func TestFetchServerPaths(t *testing.T) {
	if !alone(t) {
		return
	}
	r := &Runner{C: snapCore(t), Out: io.Discard}
	if err := r.step("fetch-server", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	base := r.fillText("{fetch}")

	res, err := http.Get(base + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	var first, last time.Time
	buf := make([]byte, 64<<10)
	for {
		k, err := res.Body.Read(buf)
		if k > 0 {
			if first.IsZero() {
				first = time.Now()
			}
			last, n = time.Now(), n+k
		}
		if err != nil {
			break
		}
	}
	res.Body.Close()
	if n != 256<<10 || last.Sub(first) < 900*time.Millisecond {
		t.Errorf("stream: %d bytes, the last %v after the first", n, last.Sub(first))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/hold", nil)
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Error("hold: answered")
	}
	if err := r.step("fetch-got", "GET /hold gone 2000"); err != nil {
		t.Error(err)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	get := func(path string) string {
		res, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return string(raw)
	}
	if got := get("/cookie/get"); got != "" {
		t.Errorf("cookies before: %q", got)
	}
	get("/cookie/set")
	if got := get("/cookie/get"); got != "keep=1; sid=netcheck" {
		t.Errorf("cookies sent back: %q", got)
	}

	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", key)
	br := bufio.NewReader(conn)
	hs, err := http.ReadResponse(br, nil)
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if err != nil || hs.StatusCode != http.StatusSwitchingProtocols || hs.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatalf("handshake: %v %+v", err, hs)
	}
	masked := func(op byte, p string) []byte {
		mask := []byte{1, 2, 3, 4}
		b := append([]byte{0x80 | op, 0x80 | byte(len(p))}, mask...)
		for i := range len(p) {
			b = append(b, p[i]^mask[i&3])
		}
		return b
	}
	for _, f := range [][]byte{masked(1, "hello kario"), masked(9, "hi"), masked(8, "\x03\xe8")} {
		if _, err := conn.Write(f); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for range 3 {
		var h [2]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			t.Fatalf("after %q: %v", got, err)
		}
		p := make([]byte, h[1]&0x7f)
		if _, err := io.ReadFull(br, p); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d %q", h[0]&0x0f, p))
	}
	if want := []string{`1 "hello kario"`, `10 "hi"`, `8 "\x03\xe8"`}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("WebSocket frames %q, want %q", got, want)
	}
	if err := r.step("fetch-got", "WS /ws hello kario 1000"); err != nil {
		t.Error(err)
	}
}
