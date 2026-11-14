package wire

import (
	"io"
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
