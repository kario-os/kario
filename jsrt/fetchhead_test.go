package jsrt

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

func TestFetchResponseHeaders(t *testing.T) { testFetchResponseHeaders(t, jsrtEngine(t), false) }

// Node.js 22's fetch (undici 6) joins a folded line to the one before, as
// RFC 9112 §5.2 tells a user agent to, where Node.js 24's (undici 7) refuses it;
// once joined, the runtime cannot tell it from a value with a space.
func TestNodeFetchResponseHeaders(t *testing.T) {
	start := nodeEngine(t)
	node, _ := nodeRunner(t)
	out, err := exec.Command(node, "-p", "process.versions.undici").Output()
	if err != nil {
		t.Fatal(err)
	}
	major, err := strconv.Atoi(strings.Split(strings.TrimSpace(string(out)), ".")[0])
	if err != nil {
		t.Fatalf("undici version %q: %v", out, err)
	}
	testFetchResponseHeaders(t, start, major < 7)
}

// rawServer answers every request on a plain socket with the header lines
// heads has for its path, byte for byte (what net/http would not send),
// and a body of "hi"; on /echo the body is the request's headers named
// __proto__ or constructor, sorted (Node.js's fetch never sends the
// first, whatever the runtime hands it).
func rawServer(t *testing.T, heads map[string]string) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				first, err := r.ReadString('\n')
				if err != nil || len(strings.Fields(first)) < 2 {
					return
				}
				var echo []string
				for {
					line, err := r.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
					name, value, _ := strings.Cut(strings.TrimRight(line, "\r\n"), ":")
					if name = strings.ToLower(name); name == "__proto__" || name == "constructor" {
						echo = append(echo, name+"="+strings.TrimSpace(value))
					}
				}
				path, body := strings.Fields(first)[1], "hi"
				if path == "/echo" {
					slices.Sort(echo)
					body = strings.Join(echo, ";")
				}
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\n%sContent-Length: %d\r\nConnection: close\r\n\r\n%s", heads[path], len(body), body)
			}()
		}
	}()
	return "http://" + l.Addr().String()
}

// heads runs the test app's heads plan (main.js) on a fresh app: what it
// shows, once it shows want.
func heads(t *testing.T, start engine, url string, names []string, sent map[string]string, want string) string {
	s := start(t, t.TempDir(), "en", filepath.Join(appCopy(t, "fetch"), "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	raw, _ := json.Marshal(map[string]any{"url": url, "heads": names, "sent": sent})
	s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "fetch", Value: string(raw)})
	return s.screenText(want)
}

// The response's headers reach the app by every name, an object's own
// included, repeated ones joined in their order, and a request's Headers
// go by such names too; a response whose headers have bytes no header may have is
// refused on both engines (Node.js 24's parser refuses each of these), a NUL
// with the words libcurl itself has for it since 7.84; foldJoined is an
// engine that takes a folded line. Both engines take up to 64 KB of headers
// (Node.js's own limit is 16 KB) and refuse more in the same words.
func testFetchResponseHeaders(t *testing.T, start engine, foldJoined bool) {
	fold := "err TypeError: fetch: "
	if foldJoined {
		fold = `heads ["1 folded"] hi gap`
	}
	url := rawServer(t, map[string]string{
		"/big":     "X-Big: " + strings.Repeat("b", 20<<10) + "\r\n",
		"/near":    "X-Big: " + strings.Repeat("b", 60<<10) + "\r\n",
		"/over":    "X-Big: " + strings.Repeat("b", 64<<10) + "\r\n",
		"/proto":   "__proto__: p\r\nConstructor: c\r\ntoString: t\r\nX-Test: yes\r\n",
		"/dup":     "X-A: 1\r\nx-a: 2\r\nSet-Cookie: a=1\r\nSet-Cookie: b=2\r\nX-B: \t3\r\n" + strings.Repeat("Many: x\r\n", 2000),
		"/nul":     "X-A: 1\r\nX-Test: a\x00b\r\nX-C: 3\r\n",
		"/nulname": "X-A: 1\r\nX\x00B: b\r\n",
		"/ctl":     "X-Test: a\x01b\r\n",
		"/del":     "X-Test: a\x7fb\r\n",
		"/notoken": "X B: 1\r\n",
		"/empty":   ": v\r\n",
		"/nocolon": "Garbage\r\n",
		"/fold":    "X-A: 1\r\n folded\r\n",
		"/cr":      "X-A: a\rb\r\n",
	})
	for _, c := range []struct {
		path  string
		heads []string
		sent  map[string]string
		want  string
	}{
		{"/proto", []string{"__proto__", "constructor", "tostring", "x-test"}, nil, `heads ["p","c","t","yes"] hi gap`},
		{"/dup", []string{"x-a", "set-cookie", "x-b", "many"}, nil, fmt.Sprintf(`heads ["1, 2","a=1, b=2","3","#%d"] hi gap`, 2000*3-2)},
		{"/echo", []string{"x"}, map[string]string{"constructor": "c"}, `heads [null] constructor=c gap`},
		{"/nul", []string{"x-a"}, nil, "err TypeError: fetch: Nul byte in header"},
		{"/nulname", []string{"x-a"}, nil, "err TypeError: fetch: Nul byte in header"},
		{"/ctl", []string{"x-test"}, nil, "err TypeError: fetch: "},
		{"/del", []string{"x-test"}, nil, "err TypeError: fetch: "},
		{"/notoken", []string{"x-b"}, nil, "err TypeError: fetch: "},
		{"/empty", []string{"x"}, nil, "err TypeError: fetch: "},
		{"/nocolon", []string{"x"}, nil, "err TypeError: fetch: "},
		{"/fold", []string{"x-a"}, nil, fold},
		{"/cr", []string{"x-a"}, nil, "err TypeError: fetch: "},
		{"/big", []string{"x-big"}, nil, fmt.Sprintf(`heads ["#%d"] hi gap`, 20<<10)},
		{"/near", []string{"x-big"}, nil, fmt.Sprintf(`heads ["#%d"] hi gap`, 60<<10)},
		{"/over", []string{"x-big"}, nil, "err TypeError: fetch: the response's headers are over 64 KB"},
	} {
		t.Run(strings.TrimPrefix(c.path, "/"), func(t *testing.T) {
			heads(t, start, url+c.path, c.heads, c.sent, c.want)
		})
	}
}

// Header lines of one name up to the 64 KB of them a response may have
// (16,350 with bare line feeds, which libcurl takes and Node.js's parser
// does not, nor more than 16 KB of headers) are joined without keeping the
// loop: the old join copied the value so far for every line, 134 ms of the
// loop on the development machine, where they now take a few.
func TestFetchManyHeaderLines(t *testing.T) {
	url := rawServer(t, map[string]string{"/many": strings.Repeat("a:x\n", 16350)})
	got := heads(t, jsrtEngine(t), url+"/many", []string{"a"}, nil, fmt.Sprintf(`heads ["#%d"] hi gap`, 16350*3-2))
	m := regexp.MustCompile(` gap (\d+)`).FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("no gap in %q", got)
	}
	gap, _ := strconv.Atoi(m[1])
	if gap > 50 {
		t.Errorf("the loop went %d ms without its timer while 16,350 header lines of one name came in", gap)
	}
	t.Logf("the loop's longest gap: %d ms", gap)
}
