package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/tools/prebuilt"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// TestMeasureHost is a host's start to its home screen, the opening of a
// mini-program (notes) to its first page, the first and once its runtime is
// up (goja keeps it, kario-jsrt starts a new one each time), and the
// memory of the host and its runtimes with the program open, each a
// median of runs, for host binaries started as the Shell starts apps:
//
//	KARIO_MEASURE_MINIAPP="before=/path/to/old/miniapp after=$PWD/build/bin/miniapp after+sandbox=$PWD/build/bin/miniapp" \
//	    go test -run MeasureHost -v ./apps/miniapp
//
// A binary runs beside kario-jsrt (make jsrt's); one whose name ends in
// +sandbox beside build/usr's phone-sandbox too (skipped when make build's
// prefix is missing or older than its sources).
func TestMeasureHost(t *testing.T) {
	spec := os.Getenv("KARIO_MEASURE_MINIAPP")
	if spec == "" {
		t.Skip("KARIO_MEASURE_MINIAPP is not set")
	}
	bin := jsrtBinary(t)
	type subject struct{ name, dir string }
	var subjects []subject
	for _, kv := range strings.Fields(spec) {
		name, path, _ := strings.Cut(kv, "=")
		dir := t.TempDir()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "phone-app-miniapp"), raw, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Symlink(bin, filepath.Join(dir, jsrt.Program))
		if strings.HasSuffix(name, "+sandbox") {
			_ = os.Symlink(filepath.Join(prebuilt.Prefix(t), "lib", "phone", "bin", "phone-sandbox"), filepath.Join(dir, "phone-sandbox"))
		}
		subjects = append(subjects, subject{name, dir})
	}
	const runs = 15
	for _, s := range subjects {
		var home, open, reopen []time.Duration
		var hostIdle, hostPss, kidsPss, hostRss, kidsRss []int
		for i := 0; i < runs; i++ {
			r := measureRun(t, filepath.Join(s.dir, "phone-app-miniapp"))
			home, open, reopen = append(home, r.home), append(open, r.open), append(reopen, r.reopen)
			hostIdle, hostPss, kidsPss = append(hostIdle, r.idlePss), append(hostPss, r.hostPss), append(kidsPss, r.kidsPss)
			hostRss, kidsRss = append(hostRss, r.hostRss), append(kidsRss, r.kidsRss)
		}
		t.Logf("%-15s home %5.1f ms  open %5.1f ms  reopen %5.1f ms  | idle host Pss %5d KB | open: host Pss %5d KB (Rss %5d), runtimes Pss %5d KB (Rss %5d), total Pss %5d KB",
			s.name, ms(median(home)), ms(median(open)), ms(median(reopen)), median(hostIdle),
			median(hostPss), median(hostRss), median(kidsPss), median(kidsRss), median(hostPss)+median(kidsPss))
	}
}

type measured struct {
	home, open, reopen                          time.Duration
	idlePss, hostPss, kidsPss, hostRss, kidsRss int
}

func measureRun(t *testing.T, exe string) measured {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := os.NewFile(uintptr(fds[0]), "shell"), os.NewFile(uintptr(fds[1]), "app")
	data := t.TempDir()
	cmd := exec.Command(exe)
	cmd.ExtraFiles = []*os.File{child}
	cmd.Env = []string{"PHONE_UI_FD=3", "PHONE_APP_ID=" + testHostID, "PHONE_DATA_DIR=" + data, "PHONE_LANG=en",
		"HOME=" + data, "PATH=" + os.Getenv("PATH"), "GOMEMLIMIT=144MiB"}
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child.Close()
	conn := wire.New(parent)
	defer func() {
		conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	msgs := make(chan ui.Msg, 256)
	go func() {
		for {
			var m ui.Msg
			if conn.Recv(&m) != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()
	next := func(match func(ui.Msg) bool) {
		timeout := time.After(10 * time.Second)
		for {
			select {
			case m, ok := <-msgs:
				if !ok {
					t.Fatal("the host ended")
				}
				if match(m) {
					return
				}
			case <-timeout:
				t.Fatal("timed out")
			}
		}
	}
	var r measured
	next(func(m ui.Msg) bool { return m.T == ui.MsgHello })
	_ = conn.Send(ui.Msg{T: ui.MsgStart})
	next(func(m ui.Msg) bool { return m.T == ui.MsgReset })
	r.home = time.Since(t0)
	time.Sleep(300 * time.Millisecond)
	r.idlePss, _ = memory(cmd.Process.Pid)
	var page string
	opened := func(m ui.Msg) bool {
		if m.T == ui.MsgPush && strings.HasPrefix(m.Screen.ID, "mp:notes:index:") {
			page = m.Screen.ID
			return true
		}
		return false
	}
	t1 := time.Now()
	_ = conn.Send(ui.Msg{T: ui.MsgAction, ScreenID: "home", Node: "p:notes", Action: ui.ActionOpen})
	next(opened)
	r.open = time.Since(t1)
	time.Sleep(300 * time.Millisecond)
	r.hostPss, r.hostRss = memory(cmd.Process.Pid)
	for _, pid := range children(cmd.Process.Pid) {
		p, rss := memory(pid)
		r.kidsPss, r.kidsRss = r.kidsPss+p, r.kidsRss+rss
	}
	_ = conn.Send(ui.Msg{T: ui.MsgAction, ScreenID: page, Action: "mp.exit"})
	next(func(m ui.Msg) bool { return m.T == ui.MsgPop })
	_ = conn.Send(ui.Msg{T: ui.MsgPopped, ScreenID: page})
	time.Sleep(100 * time.Millisecond)
	t2 := time.Now()
	_ = conn.Send(ui.Msg{T: ui.MsgAction, ScreenID: "home", Node: "p:notes", Action: ui.ActionOpen})
	next(opened)
	r.reopen = time.Since(t2)
	return r
}

// memory is a process's Pss and Rss in KB.
func memory(pid int) (pss, rss int) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		n, _ := strconv.Atoi(fields[1])
		switch fields[0] {
		case "Pss:":
			pss = n
		case "Rss:":
			rss = n
		}
	}
	return pss, rss
}

func children(pid int) []int {
	var out []int
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		n, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", n))
		if err != nil {
			continue
		}
		s := string(raw)
		if i := strings.LastIndexByte(s, ')'); i > 0 {
			if f := strings.Fields(s[i+1:]); len(f) > 1 && f[1] == strconv.Itoa(pid) {
				out = append(out, n)
			}
		}
	}
	return out
}

func median[T int | time.Duration](xs []T) T {
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
