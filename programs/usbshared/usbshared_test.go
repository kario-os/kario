package usbshared

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/usbshare"
	"github.com/kario-os/kario/usbshare/mtp"
)

// The helper runs as a child process, as the Shell starts it: fd 3 the
// listener, fd 4 its end of the socket pair.
func TestMain(m *testing.M) {
	if os.Getenv("USBSHARED_TEST_CHILD") != "" {
		Main()
		return
	}
	os.Exit(m.Run())
}

// memLib is the Shell's library for the test: files read from memory
// through a pipe would not do (the Shell hands out files), so Open writes
// a temporary file.
type memLib struct {
	dir   string
	mu    sync.Mutex
	files map[string][]byte
}

func (m *memLib) Folders() []usbshare.Folder {
	return []usbshare.Folder{{ID: "files", Name: "Files", Writable: true, MaxBytes: 16}}
}

func (m *memLib) List(string) []usbshare.Item {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []usbshare.Item
	for n, b := range m.files {
		out = append(out, usbshare.Item{Name: n, Size: int64(len(b))})
	}
	return out
}

func (m *memLib) Open(_, name string) (io.ReadSeekCloser, usbshare.Item, error) {
	m.mu.Lock()
	b, ok := m.files[name]
	m.mu.Unlock()
	if !ok {
		return nil, usbshare.Item{}, os.ErrNotExist
	}
	f, err := os.CreateTemp(m.dir, "open-*")
	if err != nil {
		return nil, usbshare.Item{}, err
	}
	f.Write(b)
	f.Close()
	ro, err := os.Open(f.Name())
	return ro, usbshare.Item{Name: name, Mime: "text/plain", Size: int64(len(b))}, err
}

func (m *memLib) Save(_, name string, r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.files[name] = b
	m.mu.Unlock()
	return name, nil
}

func TestTheHelperProcess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	lf, _ := ln.(*net.TCPListener).File()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	mine, theirs := os.NewFile(uintptr(fds[0]), "shell"), os.NewFile(uintptr(fds[1]), "helper")
	nc, _ := net.FileConn(mine)
	mine.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "USBSHARED_TEST_CHILD=1")
	cmd.ExtraFiles = []*os.File{lf, theirs}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lf.Close()
	theirs.Close()
	lib := &memLib{dir: t.TempDir(), files: map[string][]byte{"a.txt": []byte("from the phone")}}
	hello := make(chan usbshare.Hello, 1)
	h := usbshare.ServeHelper(nc.(*net.UnixConn), usbshare.HelperConfig{Lib: lib, Password: "12345678", Hello: func(hi usbshare.Hello) { hello <- hi }})
	select {
	case hi := <-hello:
		if !strings.Contains(strings.Join(hi.Sandbox, ","), "fd-only") || hi.UID != os.Getuid() {
			t.Errorf("hello: %+v", hi)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no hello\n%s", stderr.String())
	}
	got, err := h.Probe()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"open", "create", "socket-inet", "socket-unix", "exec", "ptrace"} {
		if got[k] != "EPERM" && got[k] != "EACCES" {
			t.Errorf("the helper could %s: %q", k, got[k])
		}
	}
	base := "http://" + ln.Addr().String()
	req, _ := http.NewRequest("GET", base+"/files/a.txt", nil)
	req.SetBasicAuth("kario", "12345678")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "from the phone" {
		t.Errorf("download through the helper: %d %q", resp.StatusCode, body)
	}
	for _, c := range []struct {
		body string
		want int
	}{{"from the computer", http.StatusRequestEntityTooLarge}, {"small", http.StatusCreated}} {
		req, _ = http.NewRequest("PUT", base+"/files/b.txt", io.MultiReader(strings.NewReader(c.body)))
		req.SetBasicAuth("kario", "12345678")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("PUT %q: %d, want %d", c.body, resp.StatusCode, c.want)
		}
	}
	lib.mu.Lock()
	if string(lib.files["b.txt"]) != "small" {
		t.Errorf("stored %q", lib.files["b.txt"])
	}
	lib.mu.Unlock()
	h.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Errorf("the helper ended with %v\n%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the helper outlived the Shell's end")
	}
}

// phone-usbshared for MTP, on a simulated cable as on the developer
// machine: fd 3 the control endpoint, 4 the Shell, 5 to 7 the endpoints.
func TestTheHelperOverMTP(t *testing.T) {
	dev, host, err := mtp.NewSim(mtp.Packet)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	mine, theirs := os.NewFile(uintptr(fds[0]), "shell"), os.NewFile(uintptr(fds[1]), "helper")
	nc, _ := net.FileConn(mine)
	mine.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "USBSHARED_TEST_CHILD=1", mtp.EnvMode+"="+mtp.ModeMTP, mtp.EnvTransport+"="+mtp.TransportSim,
		mtp.EnvDevice+`={"Manufacturer":"Kario","Model":"Kario phone","Serial":"KARIO0000001"}`)
	cmd.ExtraFiles = []*os.File{dev.EP0, theirs, dev.In, dev.Out, dev.Intr}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	dev.Close()
	theirs.Close()
	lib := &memLib{dir: t.TempDir(), files: map[string][]byte{"a.txt": []byte("from the phone")}}
	hello := make(chan usbshare.Hello, 1)
	h := usbshare.ServeHelper(nc.(*net.UnixConn), usbshare.HelperConfig{Lib: lib, Hello: func(hi usbshare.Hello) { hello <- hi }})
	select {
	case hi := <-hello:
		if !strings.Contains(strings.Join(hi.Sandbox, ","), "fd-only") {
			t.Errorf("hello: %+v", hi)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no hello\n%s", stderr.String())
	}
	got, err := h.Probe()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"open", "create", "socket-inet", "socket-unix", "exec", "ptrace"} {
		if got[k] != "EPERM" && got[k] != "EACCES" {
			t.Errorf("the helper could %s: %q", k, got[k])
		}
	}
	pc := mtp.NewInitiator(host)
	di, err := pc.DeviceInfo()
	if err != nil || di.Model != "Kario phone" || di.Serial != "KARIO0000001" {
		t.Fatalf("device info %+v, %v\n%s", di, err, stderr.String())
	}
	if err := pc.OpenSession(); err != nil {
		t.Fatal(err)
	}
	a, err := pc.Find("/Files/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	if err := pc.Get(a.Handle, &body); err != nil || body.String() != "from the phone" {
		t.Errorf("download through the helper: %q, %v", body.String(), err)
	}
	if _, err := pc.Put(a.Storage, 0xFFFFFFFF, "b.txt", []byte("small")); err != nil {
		t.Errorf("upload: %v", err)
	}
	_, err = pc.Put(a.Storage, 0xFFFFFFFF, "c.txt", []byte("over sixteen bytes"))
	if mtp.Code(err) != mtp.RespObjectTooLarge {
		t.Errorf("over the folder's limit: %v", err)
	}
	if err := pc.Delete(a.Handle); mtp.Code(err) != mtp.RespObjectWriteProtected {
		t.Errorf("delete: %v", err)
	}
	lib.mu.Lock()
	if string(lib.files["b.txt"]) != "small" || lib.files["c.txt"] != nil || lib.files["a.txt"] == nil {
		t.Errorf("stored %q", lib.files)
	}
	lib.mu.Unlock()
	h.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Errorf("the helper ended with %v\n%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the helper outlived the Shell's end")
	}
}
