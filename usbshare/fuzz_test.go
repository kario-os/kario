package usbshare

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

// nameLib is a library in memory that keeps a note of every name it gets
// that is not one inside its folder: what the Shell's end must never pass
// on, whatever the helper asks.
type nameLib struct {
	mu    sync.Mutex
	files map[string][]byte
	dirs  map[string]bool
	bad   []string
}

func newNameLib() *nameLib {
	return &nameLib{files: map[string][]byte{"photos/beach.jpg": []byte("jpeg"), "music/Album/01.flac": []byte("fLaC")},
		dirs: map[string]bool{"music/Album": true}}
}

func (l *nameLib) Folders() []Folder {
	return []Folder{
		{ID: "photos", Name: "Photos", Thumbs: true},
		{ID: "files", Name: "Files", Writable: true, MaxBytes: 64},
		{ID: "music", Name: musicName, Path: musicName, Writable: true, Nested: true, MaxBytes: 32},
	}
}

func (l *nameLib) check(folder, name string) {
	ok := name != ""
	for _, p := range strings.Split(strings.ReplaceAll(name, `\`, "/"), "/") {
		ok = ok && p != "" && p != "." && p != ".." && !strings.ContainsFunc(p, unicode.IsControl)
	}
	if folder != "music" && strings.ContainsAny(name, `/\`) {
		ok = false
	}
	if !ok {
		l.mu.Lock()
		l.bad = append(l.bad, folder+": "+name)
		l.mu.Unlock()
	}
}

func (l *nameLib) list(prefix string) []Item {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Item
	for k, b := range l.files {
		if rest, ok := strings.CutPrefix(k, prefix); ok && !strings.Contains(rest, "/") {
			out = append(out, Item{Name: rest, Size: int64(len(b)), ModTime: time.Unix(1, 0)})
		}
	}
	for k := range l.dirs {
		if rest, ok := strings.CutPrefix(k, prefix); ok && !strings.Contains(rest, "/") {
			out = append(out, Item{Name: rest, Dir: true, ModTime: time.Unix(1, 0)})
		}
	}
	return out
}

func (l *nameLib) List(folder string) []Item { return l.list(folder + "/") }

func (l *nameLib) ListDir(folder, dir string) ([]Item, bool) {
	l.check(folder, dir)
	l.mu.Lock()
	ok := l.dirs[folder+"/"+dir]
	l.mu.Unlock()
	if !ok {
		return nil, false
	}
	return l.list(folder + "/" + dir + "/"), true
}

func (l *nameLib) MakeDir(folder, dir string) error {
	l.check(folder, dir)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dirs[folder+"/"+dir] = true
	return nil
}

type memFile struct{ *bytes.Reader }

func (memFile) Close() error { return nil }

func (l *nameLib) Open(folder, name string) (io.ReadSeekCloser, Item, error) {
	l.check(folder, name)
	l.mu.Lock()
	b, ok := l.files[folder+"/"+name]
	l.mu.Unlock()
	if !ok {
		return nil, Item{}, os.ErrNotExist
	}
	return memFile{bytes.NewReader(b)}, Item{Name: path.Base(name), Size: int64(len(b)), ModTime: time.Unix(1, 0)}, nil
}

func (l *nameLib) Thumb(folder, name string) (Thumb, error) {
	l.check(folder, name)
	return Thumb{JPEG: []byte("\xff\xd8"), W: 1, H: 1}, nil
}

func (l *nameLib) Save(folder, name string, r io.Reader) (string, error) {
	l.check(folder, name)
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files[folder+"/"+name] = b
	return name, nil
}

func httpSeeds() []string {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("dir", "Live/03.flac")
	fw.Write([]byte("fLaC"))
	fw, _ = mw.CreateFormFile("file", `C:\Users\x\a.txt`)
	fw.Write([]byte("hello"))
	mw.Close()
	music := "/%E9%9F%B3%E4%B9%90/"
	return []string{
		"GET / HTTP/1.1\r\nHost: phone\r\n\r\n",
		"GET /photos/beach.jpg?download HTTP/1.1\r\nHost: phone\r\nRange: bytes=1-2,0-\r\n\r\n",
		"PROPFIND " + music + "Album/ HTTP/1.1\r\nHost: phone\r\nDepth: 1\r\n\r\n",
		"PUT /files/a.txt HTTP/1.1\r\nHost: phone\r\nContent-Length: 5\r\n\r\nhello",
		"PUT " + music + "Album/02.flac HTTP/1.1\r\nHost: phone\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nfLaC\r\n0\r\n\r\n",
		"MKCOL " + music + "Live HTTP/1.1\r\nHost: phone\r\n\r\n",
		fmt.Sprintf("POST %s HTTP/1.1\r\nHost: phone\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n%s", music, mw.FormDataContentType(), body.Len(), body.Bytes()),
		"OPTIONS /files/ HTTP/1.1\r\nHost: phone\r\nAuthorization: Basic a2FyaW86MDAwMDAwMDA=\r\n\r\n",
	}
}

// FuzzServer is a computer's request to the sharing page and the WebDAV
// drive (with the password, unless it brings its own).
func FuzzServer(f *testing.F) {
	for _, s := range httpSeeds() {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return
		}
		if r.Header.Get("Authorization") == "" {
			r.SetBasicAuth("kario", "12345678")
		}
		s := New(newNameLib(), "12345678")
		s.ServeHTTP(httptest.NewRecorder(), r)
	})
}

func helperSeeds() []string {
	line := func(id int, method string, params any) string {
		raw, _ := json.Marshal(params)
		b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": json.RawMessage(raw)})
		return string(b) + "\n"
	}
	hello := line(1, msgHello, Hello{Sandbox: []string{"seccomp"}, UID: 10042})
	return []string{
		hello + line(2, msgFolders, nil) + line(3, msgList, args{Folder: "files"}) + line(4, msgOpen, args{Folder: "photos", Name: "beach.jpg"}),
		hello + line(2, msgSave, args{Folder: "files", Name: "a.txt"}) + line(3, msgEnd, args{ID: 1, Size: 5, Complete: true}),
		hello + line(2, msgListDir, args{Folder: "music", Name: "Album"}) + line(3, msgMakeDir, args{Folder: "music", Name: `Live\..\..\x`}) +
			line(0, msgLog, args{Line: "usb sharing: \x1b[2Jhi"}),
		line(1, msgOpen, args{Folder: "files", Name: "../../shell/settings.json"}) + hello + hello,
		hello + line(2, msgThumb, args{Folder: "photos", Name: "beach.jpg"}) + line(3, msgThumb, args{Folder: "photos", Name: `a\..\..\x`}) +
			line(4, msgThumb, args{Folder: "files", Name: "a.txt"}),
	}
}

// FuzzHelper is a compromised phone-usbshared: whatever it sends the
// Shell's end, the library gets only names inside their folders, and the
// end lets go of everything once the helper is gone.
func FuzzHelper(f *testing.F) {
	for _, s := range helperSeeds() {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		a, b := socketPair(t)
		lib := newNameLib()
		h := ServeHelper(a, HelperConfig{Lib: lib, Password: "12345678", Lang: func() string { return "en" }})
		go io.Copy(io.Discard, b)
		b.Write(data)
		b.CloseWrite()
		select {
		case <-h.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("the Shell's end outlived the helper's messages")
		}
		b.Close()
		for end := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
			h.p.mu.Lock()
			busy := h.p.busy
			h.p.mu.Unlock()
			if busy == 0 {
				break
			}
			if time.Now().After(end) {
				t.Fatalf("%d requests still answered with the helper gone", busy)
			}
		}
		lib.mu.Lock()
		defer lib.mu.Unlock()
		if len(lib.bad) > 0 {
			t.Fatalf("the library got names outside their folders: %q", lib.bad)
		}
	})
}

// FuzzCheckName: a name the Shell takes stays inside its folder.
func FuzzCheckName(f *testing.F) {
	for _, s := range []string{"a.txt", "Album/01.flac", `..\evil.txt`, "a/../../b", "a\x01b", "\u97f3\u4e50/\u4e13\u8f91"} {
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, name string, nested bool) {
		if rel := relName(name); rel != "" && (strings.HasPrefix(path.Clean(rel), "../") || path.IsAbs(rel)) {
			t.Fatalf("relName(%q) = %q leaves its folder", name, rel)
		}
		got, err := checkName(Folder{ID: "f", Nested: nested}, name)
		if err != nil {
			return
		}
		clean := path.Clean("/" + strings.ReplaceAll(got, `\`, "/"))
		if clean == "/" || strings.Count(clean, "/") != strings.Count(strings.ReplaceAll(got, `\`, "/"), "/")+1 {
			t.Fatalf("%q is taken and is %q once cleaned", got, clean)
		}
		if !nested && strings.ContainsAny(got, `/\`) {
			t.Fatalf("%q is taken in a flat folder", got)
		}
	})
}
