package usbshare

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memLib struct {
	files map[string]map[string][]byte
}

func (m *memLib) Folders() []Folder {
	return []Folder{{ID: "photos", Name: "Photos"}, {ID: "files", Name: "Files", Writable: true}}
}

func (m *memLib) List(folder string) []Item {
	var out []Item
	for name, b := range m.files[folder] {
		out = append(out, Item{Name: name, Size: int64(len(b)), ModTime: time.Unix(1, 0)})
	}
	return out
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func (m *memLib) Open(folder, name string) (io.ReadSeekCloser, Item, error) {
	b, ok := m.files[folder][name]
	if !ok {
		return nil, Item{}, errors.New("no such file")
	}
	return nopCloser{bytes.NewReader(b)}, Item{Name: name, Mime: "image/jpeg", Size: int64(len(b)), ModTime: time.Unix(1, 0)}, nil
}

func (m *memLib) Save(folder, name string, r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if m.files[folder] == nil {
		m.files[folder] = map[string][]byte{}
	}
	m.files[folder][name] = b
	return name, nil
}

func newServer() (*Server, *memLib) {
	lib := &memLib{files: map[string]map[string][]byte{"photos": {"\u6d77\u8fb9.jpg": []byte("jpeg")}}}
	return New(lib, "12345678"), lib
}

func do(s *Server, method, path, pass string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	if pass != "" {
		r.SetBasicAuth("kario", pass)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestThePasswordGuardsEverything(t *testing.T) {
	s, _ := newServer()
	if w := do(s, "GET", "/", "", nil, nil); w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no password: %d", w.Code)
	}
	for i := 0; i < maxFailures; i++ {
		do(s, "GET", "/", "00000000", nil, nil)
	}
	if !s.Locked() {
		t.Fatal("not locked after many wrong passwords")
	}
	if w := do(s, "GET", "/", "12345678", nil, nil); w.Code != http.StatusForbidden {
		t.Fatalf("locked, right password: %d", w.Code)
	}
}

func TestBrowsingAndCopying(t *testing.T) {
	s, _ := newServer()
	w := do(s, "GET", "/", "12345678", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `href="/photos/"`) {
		t.Fatalf("index: %d %s", w.Code, w.Body)
	}
	w = do(s, "GET", "/photos/", "12345678", nil, nil)
	if !strings.Contains(w.Body.String(), "/photos/%E6%B5%B7%E8%BE%B9.jpg") || strings.Contains(w.Body.String(), "<form") {
		t.Fatalf("photos: %s", w.Body)
	}
	w = do(s, "GET", "/photos/%E6%B5%B7%E8%BE%B9.jpg?download", "12345678", nil, nil)
	if w.Code != 200 || w.Body.String() != "jpeg" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := do(s, "GET", "/photos/../../etc/passwd", "12345678", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("outside the library: %d", w.Code)
	}
}

func TestWebDAV(t *testing.T) {
	s, lib := newServer()
	if w := do(s, "OPTIONS", "/", "12345678", nil, nil); w.Header().Get("DAV") != "1" {
		t.Fatalf("OPTIONS: %v", w.Header())
	}
	w := do(s, "PROPFIND", "/photos/", "12345678", nil, map[string]string{"Depth": "1"})
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), "<D:href>/photos/%E6%B5%B7%E8%BE%B9.jpg</D:href>") ||
		!strings.Contains(w.Body.String(), "<D:getcontentlength>4</D:getcontentlength>") {
		t.Fatalf("PROPFIND: %d %s", w.Code, w.Body)
	}
	if w := do(s, "PUT", "/photos/x.jpg", "12345678", strings.NewReader("x"), nil); w.Code != http.StatusForbidden {
		t.Fatalf("PUT into a read-only folder: %d", w.Code)
	}
	if w := do(s, "PUT", "/files/notes.txt", "12345678", strings.NewReader("hello"), nil); w.Code != http.StatusCreated || string(lib.files["files"]["notes.txt"]) != "hello" {
		t.Fatalf("PUT: %d", w.Code)
	}
}

func TestUploadFromTheBrowser(t *testing.T) {
	s, lib := newServer()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "C:\\Users\\me\\report.pdf")
	fw.Write([]byte("%PDF"))
	mw.Close()
	w := do(s, "POST", "/files/", "12345678", &body, map[string]string{"Content-Type": mw.FormDataContentType()})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if _, ok := lib.files["files"]["report.pdf"]; !ok || len(lib.files["files"]) != 1 {
		t.Fatalf("files: %v", lib.files["files"])
	}
}

func TestPasswords(t *testing.T) {
	a, b := Password(), Password()
	if len(a) != 8 || a == b {
		t.Fatalf("passwords %q %q", a, b)
	}
}

// tree is a library with a nested, writable "music" folder under a
// display name, as the shell's is.
type tree struct {
	memLib
	dirs  map[string]bool
	limit int64
}

func (t *tree) Folders() []Folder {
	return append(t.memLib.Folders(), Folder{ID: "music", Name: "\u97f3\u4e50", Path: "\u97f3\u4e50", Writable: true, Nested: true, MaxBytes: t.limit})
}

func (t *tree) ListDir(folder, dir string) ([]Item, bool) {
	if folder != "music" || !t.dirs[dir] {
		return nil, false
	}
	var out []Item
	for d := range t.dirs {
		if strings.HasPrefix(d, dir+"/") && !strings.Contains(d[len(dir)+1:], "/") {
			out = append(out, Item{Name: d[len(dir)+1:], Dir: true})
		}
	}
	for name, b := range t.files["music"] {
		if strings.HasPrefix(name, dir+"/") && !strings.Contains(name[len(dir)+1:], "/") {
			out = append(out, Item{Name: name[len(dir)+1:], Size: int64(len(b))})
		}
	}
	return out, true
}

func (t *tree) List(folder string) []Item {
	if folder != "music" {
		return t.memLib.List(folder)
	}
	var out []Item
	for d := range t.dirs {
		if !strings.Contains(d, "/") {
			out = append(out, Item{Name: d, Dir: true})
		}
	}
	return out
}

func (t *tree) MakeDir(folder, dir string) error {
	t.dirs[dir] = true
	return nil
}

func (t *tree) Save(folder, name string, r io.Reader) (string, error) {
	if i := strings.LastIndex(name, "/"); i > 0 {
		t.dirs[name[:i]] = true
	}
	return t.memLib.Save(folder, name, r)
}

func TestNestedFolders(t *testing.T) {
	lib := &tree{memLib: memLib{files: map[string]map[string][]byte{}}, dirs: map[string]bool{}, limit: 10}
	s := New(lib, "12345678")
	const music = "/%E9%9F%B3%E4%B9%90/"
	if w := do(s, "GET", "/", "12345678", nil, nil); !strings.Contains(w.Body.String(), `href="`+music+`"`) {
		t.Fatalf("the folder's URL is its shown name: %s", w.Body)
	}
	if w := do(s, "MKCOL", music+"Album", "12345678", nil, nil); w.Code != http.StatusCreated || !lib.dirs["Album"] {
		t.Fatalf("MKCOL: %d", w.Code)
	}
	if w := do(s, "PUT", music+"Album/01.flac", "12345678", strings.NewReader("fLaC"), nil); w.Code != http.StatusCreated || string(lib.files["music"]["Album/01.flac"]) != "fLaC" {
		t.Fatalf("PUT into an album: %d", w.Code)
	}
	if w := do(s, "PUT", "/music/Album/02.flac", "12345678", strings.NewReader("fLaC"), nil); w.Code != http.StatusCreated {
		t.Fatalf("the folder's ID works too: %d", w.Code)
	}
	w := do(s, "PROPFIND", music+"Album/", "12345678", nil, map[string]string{"Depth": "1"})
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), "<D:href>"+music+"Album/01.flac</D:href>") {
		t.Fatalf("PROPFIND of the album: %d %s", w.Code, w.Body)
	}
	w = do(s, "PROPFIND", music, "12345678", nil, map[string]string{"Depth": "1"})
	if !strings.Contains(w.Body.String(), "<D:href>"+music+"Album/</D:href><D:propstat><D:prop><D:displayname>Album</D:displayname><D:resourcetype><D:collection>") {
		t.Fatalf("PROPFIND of music: %s", w.Body)
	}
	w = do(s, "GET", music+"Album/", "12345678", nil, nil)
	if !strings.Contains(w.Body.String(), music+"Album/01.flac") || !strings.Contains(w.Body.String(), "webkitdirectory") {
		t.Fatalf("album page: %s", w.Body)
	}
	if w := do(s, "PUT", music+"Album/big.flac", "12345678", strings.NewReader("01234567890"), nil); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the folder's limit: %d", w.Code)
	}
	if w := do(s, "PUT", "/files/a/b.txt", "12345678", strings.NewReader("x"), nil); w.Code != http.StatusNotFound {
		t.Fatalf("folders inside a flat folder: %d", w.Code)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("dir", "Live/03.flac")
	fw.Write([]byte("fLaC"))
	fw, _ = mw.CreateFormFile("dir", "../../evil.flac")
	fw.Write([]byte("x"))
	mw.Close()
	if w := do(s, "POST", music, "12345678", &body, map[string]string{"Content-Type": mw.FormDataContentType()}); w.Code != http.StatusSeeOther {
		t.Fatalf("folder upload: %d %s", w.Code, w.Body)
	}
	if _, ok := lib.files["music"]["Live/03.flac"]; !ok {
		t.Fatalf("a folder from the browser keeps its name: %v", lib.files["music"])
	}
	if _, ok := lib.files["music"]["evil.flac"]; !ok {
		t.Fatalf("a path out of the folder keeps only its name: %v", lib.files["music"])
	}
}
