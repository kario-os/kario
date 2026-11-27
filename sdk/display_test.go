package sdk

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// HintDisplay asks display.hint with the hint and hands done the rate the
// system answers, or its refusal.
func TestHintDisplay(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	shell := wire.New(os.NewFile(uintptr(fds[1]), "shell"))
	defer shell.Close()
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	go a.Run()
	type result struct {
		res ui.DisplayHintRes
		err error
	}
	got := make(chan result, 2)
	a.HintDisplay(ui.DisplayHintArgs{AnimatingMS: 1000}, func(r ui.DisplayHintRes, err error) { got <- result{r, err} })
	a.HintDisplay(ui.DisplayHintArgs{FPS: 24}, func(r ui.DisplayHintRes, err error) { got <- result{r, err} })
	for i := 0; i < 2; i++ {
		var m ui.Msg
		for m.T != ui.MsgReq {
			if err := shell.Recv(&m); err != nil {
				t.Fatal(err)
			}
		}
		var args ui.DisplayHintArgs
		if err := json.Unmarshal(m.Args, &args); err != nil || m.Cap != ui.CapDisplayHint {
			t.Fatalf("request %s %s", m.Cap, m.Args)
		}
		if i == 0 {
			if args.AnimatingMS != 1000 {
				t.Fatalf("first hint %+v", args)
			}
			data, _ := json.Marshal(ui.DisplayHintRes{Hz: 120, Rates: []int{60, 120}})
			_ = shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, OK: true, Data: data})
		} else {
			if args.FPS != 24 {
				t.Fatalf("second hint %+v", args)
			}
			_ = shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, Err: ui.ErrDisplayHintRate})
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case r := <-got:
			if r.err == nil && r.res.Hz != 120 || r.err != nil && !strings.HasSuffix(r.err.Error(), ui.ErrDisplayHintRate) {
				t.Fatalf("result %+v", r)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no result")
		}
	}
}
