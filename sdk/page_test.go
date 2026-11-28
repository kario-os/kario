package sdk

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/kario-os/kario/ui"
)

// An update of a page the Shell does not hold is not sent; one of a page
// on the stack is.
func TestUpdateOnlyHeldPages(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	shell := os.NewFile(uintptr(fds[1]), "shell")
	defer shell.Close()
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	a.Update(ui.Screen{ID: "closed"})
	a.Reset(ui.Screen{ID: "root"})
	a.Update(ui.Screen{ID: "closed"})
	a.Push(ui.Screen{ID: "open"})
	a.Update(ui.Screen{ID: "open", Title: "new"})
	var got []string
	sc := bufio.NewScanner(shell)
	for sc.Scan() {
		var m ui.Msg
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Screen == nil {
			continue
		}
		got = append(got, m.T+" "+m.Screen.ID)
		if m.T == ui.MsgUpdate {
			break
		}
	}
	want := []string{ui.MsgReset + " root", ui.MsgPush + " open", ui.MsgUpdate + " open"}
	if len(got) != len(want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sent %v, want %v", got, want)
		}
	}
}
