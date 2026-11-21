package media

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestSurface(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "frames.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		var req SubscribeReq
		line, _ := bufio.NewReader(nc).ReadBytes('\n')
		_ = json.Unmarshal(line, &req)
		surf, err := NewSurface(req.W, req.H)
		if err != nil || ServeSurface(nc.(*net.UnixConn), surf) != nil {
			return
		}
		defer surf.Close()
		f := NewFrame(req.W, req.H)
		for seq := uint32(1); seq <= 3; seq++ {
			for i := range f.Pix {
				f.Pix[i] = byte(seq)
			}
			f.Seq = seq
			surf.Publish(f)
			if Notify(nc, seq) != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(time.Second)
	}()
	got := make(chan uint32, 8)
	sub, err := SubscribeSurface(sock, "cam", 32, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	view, err := OpenSurface(sub.File)
	if err != nil {
		t.Fatal(err)
	}
	if view.W != 32 || view.H != 24 {
		t.Fatalf("surface %dx%d", view.W, view.H)
	}
	go func() {
		for {
			if seq, n := sub.Seq(); n >= 3 {
				got <- seq
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	select {
	case seq := <-got:
		f := view.Latest()
		if seq != 3 || f == nil || f.Seq != 3 || f.Pix[0] != 3 || f.Pix[len(f.Pix)-1] != 3 {
			t.Fatalf("frame %d: %+v", seq, f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frames")
	}
}
