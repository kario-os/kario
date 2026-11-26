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

// Calls made with Go reach the Shell in the order they were made, as two
// presses of a switch must: the second is the one that stays.
func TestGoKeepsTheOrder(t *testing.T) {
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
	const n = 50
	for i := range n {
		a.Go("test.set", map[string]int{"n": i}, nil, nil)
	}
	sc := bufio.NewScanner(shell)
	for i := 0; i < n && sc.Scan(); {
		var m ui.Msg
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.T != ui.MsgReq {
			continue
		}
		var args map[string]int
		_ = json.Unmarshal(m.Args, &args)
		if args["n"] != i {
			t.Fatalf("request %d arrived as the %dth", args["n"], i)
		}
		i++
	}
}
