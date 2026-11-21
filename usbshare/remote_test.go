package usbshare

import (
	"bytes"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dirLib is a library in a directory, as the Shell's stores are on disk:
// Open hands out files, so they can cross to the helper.
type dirLib struct{ dir string }

const musicName = "\u97f3\u4e50"

func (d *dirLib) Folders() []Folder {
	return []Folder{
		{ID: "photos", Name: "Photos"},
		{ID: "files", Name: "Files", Writable: true, MaxBytes: 64},
		{ID: "music", Name: musicName, Path: musicName, Writable: true, Nested: true, MaxBytes: 10},
	}
}

func (d *dirLib) path(folder, name string) string {
	return filepath.Join(d.dir, folder, filepath.FromSlash(name))
}

func (d *dirLib) ListDir(folder, dir string) ([]Item, bool) {
	ents, err := os.ReadDir(d.path(folder, dir))
	if err != nil {
		return nil, false
	}
	out := []Item{}
	for _, e := range ents {
		fi, _ := e.Info()
		out = append(out, Item{Name: e.Name(), Dir: e.IsDir(), Size: fi.Size(), ModTime: fi.ModTime()})
	}
	return out, true
}

func (d *dirLib) List(folder string) []Item {
	out, _ := d.ListDir(folder, "")
	return out
}

func (d *dirLib) MakeDir(folder, dir string) error { return os.MkdirAll(d.path(folder, dir), 0o700) }

func (d *dirLib) Open(folder, name string) (io.ReadSeekCloser, Item, error) {
	f, err := os.Open(d.path(folder, name))
	if err != nil {
		return nil, Item{}, err
	}
	fi, _ := f.Stat()
	return f, Item{Name: filepath.Base(name), Mime: "text/plain", Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

func (d *dirLib) Save(folder, name string, r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	p := d.path(folder, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	return name, os.WriteFile(p, b, 0o600)
}

func newDirLib(t *testing.T) *dirLib {
	d := &dirLib{dir: t.TempDir()}
	for _, f := range []string{"photos", "files", "music"} {
		os.MkdirAll(filepath.Join(d.dir, f), 0o700)
	}
	os.WriteFile(filepath.Join(d.dir, "photos", "beach.jpg"), []byte("jpeg"), 0o600)
	return d
}

func socketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "pair")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	return conn(fds[0]), conn(fds[1])
}

type twoParts struct {
	srv    *Server
	remote *Remote
	shell  *Helper
	logs   chan string
	added  chan string
}

// newSplit is sharing in two parts: the Shell's end serving lib, and the
// helper's Server over a Remote.
func newSplit(t *testing.T, lib Library) *twoParts {
	a, b := socketPair(t)
	s := &twoParts{logs: make(chan string, 64), added: make(chan string, 64)}
	s.shell = ServeHelper(a, HelperConfig{Lib: lib, Password: "12345678",
		Log:   func(l string) { s.logs <- l },
		Added: func(f, n string) { s.added <- f + "/" + n }})
	t.Cleanup(s.shell.Close)
	r, pass, err := Connect(b, Hello{Sandbox: []string{"seccomp", "fd-only"}, UID: 10042}, func() map[string]string {
		return map[string]string{"open": "EPERM"}
	})
	if err != nil {
		t.Fatal(err)
	}
	if pass != "12345678" {
		t.Fatalf("the helper got the password %q", pass)
	}
	s.remote = r
	s.srv = New(r, pass)
	s.srv.Log = r.Logf
	return s
}

func waitLine(t *testing.T, ch chan string, want string) {
	t.Helper()
	end := time.After(5 * time.Second)
	for {
		select {
		case l := <-ch:
			if strings.Contains(l, want) {
				return
			}
		case <-end:
			t.Fatalf("no %q", want)
		}
	}
}

func TestSharingAcrossProcesses(t *testing.T) {
	lib := newDirLib(t)
	s := newSplit(t, lib)
	if hi, ok := s.shell.Reported(); !ok || hi.UID != 10042 || strings.Join(hi.Sandbox, ",") != "seccomp,fd-only" {
		t.Fatalf("hello: %+v %v", hi, ok)
	}
	if got, err := s.shell.Probe(); err != nil || got["open"] != "EPERM" {
		t.Fatalf("probe: %v %v", got, err)
	}
	const pass = "12345678"
	if w := do(s.srv, "GET", "/", pass, nil, nil); !strings.Contains(w.Body.String(), "/"+"%E9%9F%B3%E4%B9%90/") {
		t.Fatalf("folders: %s", w.Body)
	}
	if w := do(s.srv, "GET", "/photos/beach.jpg", pass, nil, nil); w.Code != http.StatusOK || w.Body.String() != "jpeg" {
		t.Fatalf("download: %d %q", w.Code, w.Body)
	}
	waitLine(t, s.logs, "usb sharing: photos/beach.jpg copied to the computer")
	if w := do(s.srv, "PUT", "/files/a.txt", pass, strings.NewReader("hello"), nil); w.Code != http.StatusCreated {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	waitLine(t, s.added, "files/a.txt")
	waitLine(t, s.logs, "the computer added files/a.txt")
	if b, _ := os.ReadFile(filepath.Join(lib.dir, "files", "a.txt")); string(b) != "hello" {
		t.Fatalf("stored %q", b)
	}
	if w := do(s.srv, "PUT", "/photos/b.jpg", pass, strings.NewReader("x"), nil); w.Code != http.StatusForbidden {
		t.Fatalf("read-only: %d", w.Code)
	}
	const music = "/%E9%9F%B3%E4%B9%90/"
	if w := do(s.srv, "MKCOL", music+"Album", pass, nil, nil); w.Code != http.StatusCreated {
		t.Fatalf("MKCOL: %d %s", w.Code, w.Body)
	}
	if w := do(s.srv, "PUT", music+"Album/01.flac", pass, strings.NewReader("fLaC"), nil); w.Code != http.StatusCreated {
		t.Fatalf("PUT into the album: %d %s", w.Code, w.Body)
	}
	if w := do(s.srv, "PROPFIND", music+"Album/", pass, nil, map[string]string{"Depth": "1"}); !strings.Contains(w.Body.String(), "<D:href>"+music+"Album/01.flac</D:href>") {
		t.Fatalf("PROPFIND: %s", w.Body)
	}
	// Over the limit, with the length said or not: 413, and nothing kept.
	if w := do(s.srv, "PUT", music+"Album/big.flac", pass, strings.NewReader("01234567890"), nil); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the limit: %d", w.Code)
	}
	if w := do(s.srv, "PUT", music+"Album/big.flac", pass, io.LimitReader(zeros{}, 11), nil); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the limit, streamed: %d %s", w.Code, w.Body)
	}
	if w := do(s.srv, "PUT", music+"Album/ten.flac", pass, io.LimitReader(zeros{}, 10), nil); w.Code != http.StatusCreated {
		t.Fatalf("at the limit, streamed: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(lib.dir, "music", "Album", "big.flac")); err == nil {
		t.Fatal("a copy over the limit was kept")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("dir", "Live/03.flac")
	fw.Write([]byte("fLaC"))
	mw.Close()
	if w := do(s.srv, "POST", music, pass, &body, map[string]string{"Content-Type": mw.FormDataContentType()}); w.Code != http.StatusSeeOther {
		t.Fatalf("folder upload: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(lib.dir, "music", "Live", "03.flac")); err != nil {
		t.Fatalf("folder upload: %v", err)
	}
	if w := do(s.srv, "MKCOL", music+"a%01b", pass, nil, nil); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), tr.T("Not a name the phone takes for a file or folder")) {
		t.Fatalf("a control character in a folder name: %d %s", w.Code, w.Body)
	}
}

func TestTheShellRefusesAMisbehavingHelper(t *testing.T) {
	lib := newDirLib(t)
	a, b := socketPair(t)
	h := ServeHelper(a, HelperConfig{Lib: lib, Password: "12345678"})
	defer h.Close()
	if err := Misbehave(b, "files"); err != nil {
		t.Fatal(err)
	}
	var left []string
	filepath.Walk(lib.dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(lib.dir, p)
			left = append(left, rel)
		}
		return nil
	})
	sort.Strings(left)
	if strings.Join(left, ",") != "photos/beach.jpg" {
		t.Fatalf("files after the misbehaving helper: %v", left)
	}
}

// A library that hands out something other than a read-only file is not
// passed on.
type leakyLib struct{ *dirLib }

func (l leakyLib) Open(folder, name string) (io.ReadSeekCloser, Item, error) {
	f, err := os.OpenFile(l.path(folder, name), os.O_RDWR, 0)
	return f, Item{Name: name}, err
}

func TestOnlyReadOnlyFilesCross(t *testing.T) {
	s := newSplit(t, leakyLib{newDirLib(t)})
	if w := do(s.srv, "GET", "/photos/beach.jpg", "12345678", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("a writable file crossed: %d", w.Code)
	}
}

func TestTheHelperEndsWithTheShell(t *testing.T) {
	s := newSplit(t, newDirLib(t))
	s.shell.Close()
	select {
	case <-s.remote.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the helper's end stayed")
	}
	if s.remote.Folders() != nil {
		t.Fatal("folders with the Shell gone")
	}
}
