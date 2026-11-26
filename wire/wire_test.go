package wire

import (
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"syscall"
	"testing"
)

func TestFilesKeepOrderAcrossReadAhead(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, b := New(os.NewFile(uintptr(fds[0]), "a")), New(os.NewFile(uintptr(fds[1]), "b"))
	defer a.Close()
	defer b.Close()
	tmp := t.TempDir()
	var sent []*os.File
	for _, name := range []string{"first", "second"} {
		f, err := os.Create(tmp + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(name)
		f.Seek(0, 0)
		sent = append(sent, f)
	}
	type msg struct {
		Name string `json:"name"`
		FDs  int    `json:"fds"`
	}
	// Both messages (and their files) are sent before the receiver reads, so
	// its scanner reads ahead past the first message.
	for i, name := range []string{"first", "second"} {
		if err := a.SendFiles(msg{Name: name, FDs: 1}, sent[i]); err != nil {
			t.Fatal(err)
		}
		sent[i].Close()
	}
	if err := a.Send(msg{Name: "none"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first", "second", "none"} {
		var m msg
		if err := b.Recv(&m); err != nil {
			t.Fatal(err)
		}
		files := b.TakeFiles(m.FDs)
		if m.Name != want || len(files) != m.FDs {
			t.Fatalf("got %q with %d files, want %q", m.Name, len(files), want)
		}
		if m.FDs == 1 {
			got, _ := io.ReadAll(files[0])
			files[0].Close()
			if string(got) != want {
				t.Fatalf("message %q carried the file of %q", want, got)
			}
		}
	}
}

func openFDs(t *testing.T) int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip(err)
	}
	return len(ents)
}

func TestRefusedFilesAreClosed(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, b := New(os.NewFile(uintptr(fds[0]), "app")), New(os.NewFile(uintptr(fds[1]), "shell"))
	defer a.Close()
	defer b.Close()
	b.RefuseFiles()
	f, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	before := openFDs(t)
	for i := 0; i < 50; i++ {
		if err := a.SendFiles(map[string]int{"n": i}, f, f, f); err != nil {
			t.Fatal(err)
		}
		var m map[string]int
		if err := b.Recv(&m); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	if got := b.TakeFiles(10); len(got) != 0 {
		t.Fatalf("refusing connection kept %d files", len(got))
	}
	if after := openFDs(t); after > before {
		t.Fatalf("%d descriptors leaked into the refusing side", after-before)
	}
}

// More files in a message than one read takes: the refusing side closes
// what arrives, the kernel drops the rest, and nothing is logged.
func TestRefusedFilesPastOneRead(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, b := New(os.NewFile(uintptr(fds[0]), "app")), New(os.NewFile(uintptr(fds[1]), "shell"))
	defer a.Close()
	defer b.Close()
	b.RefuseFiles()
	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)
	f, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rights := make([]int, 2*maxFDsInMsg)
	for i := range rights {
		rights[i] = int(f.Fd())
	}
	before := openFDs(t)
	for i := 0; i < 50; i++ {
		if _, _, err := a.uc.WriteMsgUnix([]byte(`{"n":1}`+"\n"), syscall.UnixRights(rights...), nil); err != nil {
			t.Fatal(err)
		}
		var m map[string]int
		if err := b.Recv(&m); err != nil {
			t.Fatal(err)
		}
	}
	if after := openFDs(t); after > before {
		t.Fatalf("%d descriptors leaked into the refusing side", after-before)
	}
	if logged.Len() > 0 {
		t.Fatalf("refused files were logged:\n%s", logged.String())
	}
}

// After CloseWrite the peer reads the end, though a copy of the socket is
// still open elsewhere, and what it sends afterwards still arrives.
func TestCloseWrite(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	shell, app := New(os.NewFile(uintptr(fds[0]), "shell")), New(os.NewFile(uintptr(fds[1]), "app"))
	defer shell.Close()
	defer app.Close()
	stored, err := shell.File()
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Close()
	if err := shell.Send(map[string]string{"t": "last"}); err != nil {
		t.Fatal(err)
	}
	if err := shell.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := app.Recv(&m); err != nil || m["t"] != "last" {
		t.Fatalf("got %v, %v", m, err)
	}
	if err := app.Recv(&m); !errors.Is(err, io.EOF) {
		t.Fatalf("after CloseWrite the peer reads %v, want EOF", err)
	}
	if err := app.Send(map[string]string{"t": "state"}); err != nil {
		t.Fatal(err)
	}
	if err := shell.Recv(&m); err != nil || m["t"] != "state" {
		t.Fatalf("the peer's message after CloseWrite: %v, %v", m, err)
	}
	if err := shell.Send(map[string]string{"t": "more"}); err == nil {
		t.Fatal("a send after CloseWrite went through")
	}
}
