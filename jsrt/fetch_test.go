package jsrt

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// fetchCAEnv is the CA bundle kario-jsrt's fetch trusts in place of the
// system's (net.c); the Shell never sets it.
const fetchCAEnv = "KARIO_JSRT_CA_FILE"

func TestFetch(t *testing.T)     { testFetch(t, jsrtEngine(t)) }
func TestNodeFetch(t *testing.T) { testFetch(t, nodeEngine(t)) }

func fetchServer() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		fmt.Fprint(w, "hi")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if v := r.Header.Get("X-App"); v != "" {
			w.Header().Set("X-Test", v)
		}
		fmt.Fprintf(w, "%s|%s|%s", r.Method, r.Header.Get("Content-Type"), raw)
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"n":3,"s":"x"}`)
	})
	mux.HandleFunc("/size", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s|%s|%d", r.Method, r.Header.Get("Content-Type"), len(raw))
	})
	mux.HandleFunc("/form", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f, fh, err := r.FormFile("f")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		raw, _ := io.ReadAll(f)
		fmt.Fprintf(w, "%v|%q|%q|%s:%s:%s", strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=----KarioFormBoundary"),
			r.FormValue("a"), r.FormValue("q"), fh.Filename, fh.Header.Get("Content-Type"), raw)
	})
	mux.HandleFunc("/missing", http.NotFound)
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < 9; i++ {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
		fmt.Fprint(w, "late")
	})
	return mux
}

func testFetch(t *testing.T, start engine) {
	plain := httptest.NewServer(fetchServer())
	defer plain.Close()
	mux := http.NewServeMux()
	mux.Handle("/", fetchServer())
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/hello", http.StatusFound)
	})
	mux.HandleFunc("/downgrade", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/hello", http.StatusFound)
	})
	secure := httptest.NewTLSServer(mux)
	defer secure.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := appCopy(t, "fetch")
	run := func(s *shell, plan map[string]any, want string) {
		t.Helper()
		raw, _ := json.Marshal(plan)
		s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "fetch", Value: string(raw)})
		s.screenText(want)
	}

	t.Setenv(fetchCAEnv, ca)
	t.Setenv("NODE_EXTRA_CA_CERTS", ca)
	s := start(t, t.TempDir(), "en", filepath.Join(dir, "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	for _, c := range []struct {
		plan map[string]any
		want string
	}{
		{map[string]any{"url": plain.URL + "/hello"}, "ok 200 true false yes hi"},
		{map[string]any{"url": secure.URL + "/hello"}, "ok 200 true false yes hi"},
		{map[string]any{"url": secure.URL + "/json", "read": "json"}, `ok 200 true false - {"n":3,"s":"x"}`},
		{map[string]any{"url": secure.URL + "/missing"}, "ok 404 false false -"},
		{map[string]any{"url": secure.URL + "/echo", "init": map[string]any{"method": "post", "body": "a=1", "headers": map[string]any{"X-App": "v"}}},
			"ok 200 true false v POST|text/plain;charset=UTF-8|a=1"},
		{map[string]any{"url": secure.URL + "/echo", "init": map[string]any{"method": "PUT", "bytes": "raw", "headers": map[string]any{"Content-Type": "application/octet-stream"}}},
			"ok 200 true false - PUT|application/octet-stream|raw"},
		{map[string]any{"url": secure.URL + "/echo", "init": map[string]any{"method": "HEAD"}}, "ok 200 true false -"},
		{map[string]any{"url": secure.URL + "/redirect"}, "ok 200 true true yes hi"},
		{map[string]any{"url": secure.URL + "/redirect", "init": map[string]any{"method": "POST", "body": "x"}}, "ok 200 true true yes hi"},
		{map[string]any{"url": secure.URL + "/redirect", "init": map[string]any{"method": "PUT", "body": "x"}}, "ok 302 false false -"},
		{map[string]any{"url": secure.URL + "/big", "read": "bytes"}, "err TypeError: fetch: the response is over 8 MB"},
		{map[string]any{"url": secure.URL + "/slow", "init": map[string]any{"timeout": 300}}, "err TypeError: fetch:"},
		{map[string]any{"url": secure.URL + "/downgrade"}, "err TypeError: fetch: a redirect away from https://"},
		{map[string]any{"url": "http://example.com/"}, "err TypeError: fetch: http:// only to the device's own loopback"},
		{map[string]any{"url": "http://127.0.0.1.invalid/"}, "err TypeError: fetch: http:// only to the device's own loopback"},
		{map[string]any{"url": "http://127.example.invalid:8080/"}, "err TypeError: fetch: http:// only to the device's own loopback"},
		{map[string]any{"url": "http://127.0.0.010/"}, "err TypeError: fetch: http:// only to the device's own loopback"},
		{map[string]any{"url": "ftp://127.0.0.1/"}, "err TypeError: fetch: only https:// URLs"},
		{map[string]any{"url": "https://user:pw@example.com/"}, "err TypeError: fetch: a URL with a user name or password"},
		{map[string]any{"url": secure.URL + "/echo", "init": map[string]any{"headers": map[string]any{"Host": "x"}}}, "err TypeError: fetch: a header the network sets itself"},
		{map[string]any{"url": secure.URL + "/echo", "init": map[string]any{"body": "x"}}, "err TypeError: fetch: a GET or HEAD request has no body"},
		{map[string]any{"url": secure.URL + "/echo", "init": map[string]any{"method": "TRACE"}}, "err TypeError: fetch: method is one of"},
		{map[string]any{"url": secure.URL + "/echo", "init": map[string]any{"timeout": 0}}, "err TypeError: fetch: timeout is from 1 to 120000 ms"},
	} {
		run(s, c.plan, c.want)
	}

	// Without the CA the test server's certificate is not trusted.
	t.Setenv(fetchCAEnv, "")
	t.Setenv("NODE_EXTRA_CA_CERTS", "")
	s = start(t, t.TempDir(), "en", filepath.Join(dir, "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	run(s, map[string]any{"url": secure.URL + "/hello"}, "err TypeError: fetch:")

	// Without "net" in the manifest there is no fetch at all.
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id": "com.example.test", "name": "Fetch test", "version": "1.0", "runtime": "js", "main": "main.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s = start(t, t.TempDir(), "en", filepath.Join(dir, "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	run(s, map[string]any{"url": plain.URL + "/hello"}, `err TypeError: fetch: the app's manifest does not ask for the "net" permission`)
}

func TestFetchWeb(t *testing.T)     { testFetchWeb(t, jsrtEngine(t)) }
func TestNodeFetchWeb(t *testing.T) { testFetchWeb(t, nodeEngine(t)) }

// The web's Blob, File, FormData, Headers, Request and Response (one
// implementation on both engines, sdk/ts/node/web.js): what each does
// without the network, the same once taken before first use (on
// kario-jsrt, stand-ins until then); a FormData body goes as
// multipart/form-data, a Blob with its type, a Request with its method,
// headers and body (fetch's options over it); fetch's answer is a
// Response whose headers are also properties by name, as before.
func testFetchWeb(t *testing.T, start engine) {
	srv := httptest.NewTLSServer(fetchServer())
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fetchCAEnv, ca)
	t.Setenv("NODE_EXTRA_CA_CERTS", ca)
	s := start(t, t.TempDir(), "en", filepath.Join(appCopy(t, "fetch"), "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	for _, c := range []struct {
		plan map[string]any
		want string
	}{
		{map[string]any{"web": "classes"}, strings.Join([]string{
			`headers 1, 2 true content-type,set-cookie,set-cookie,x-a 2 null v TypeError: Headers.set: "bad name" is not a header name TypeError: Headers.set: a header value with a line break or NUL`,
			"blob 5 text/plain h\u00e9!? 3 !? true TypeError: Blob: the parts are a list of strings, ArrayBuffers, typed arrays and Blobs",
			"file a.txt 3 text/plain 5 true",
			"form 1+2 true y blob true a,a,f,s TypeError: FormData: a file name goes with a Blob or File",
			"form2 2 null",
			"request POST https://example.com/x text/plain;charset=UTF-8 hi hi same-origin false TypeError: Request: a GET or HEAD request has no body TypeError: Request: method is one of GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS",
			"response 201 true Made text/plain;charset=UTF-8 3 ok ok default true 2",
			`json 202 application/json {"n":1} https://e/ RangeError: Response: status is from 200 to 599 TypeError: Response: a 204 response has no body null`,
			"stream 2 true the body was read as a stream",
			"early true true tagged t TypeError",
		}, " / ")},
		{map[string]any{"web": "form", "url": srv.URL + "/form"}, `200 - true|"1\r\n2"|"say \"hi\""|x.txt:text/plain:xyz`},
		{map[string]any{"web": "blob", "url": srv.URL + "/size"}, "200 - POST|image/png|3"},
		{map[string]any{"web": "request", "url": srv.URL + "/echo"}, "200 v PUT|text/plain;charset=UTF-8|x"},
		{map[string]any{"web": "override", "url": srv.URL + "/echo"}, "200 w POST||x"},
		{map[string]any{"web": "response", "url": srv.URL + "/hello"}, `response true true yes yes true function basic "" 2 true hi ` +
			"TypeError: Headers: the headers of a response from the network are read-only " +
			"TypeError: Response.clone: a response from the network is not cloned (text() and the like can be read again)"},
	} {
		raw, _ := json.Marshal(c.plan)
		s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "fetch", Value: string(raw)})
		s.screenText(c.want)
	}
}
