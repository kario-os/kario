package shell

import (
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestLowMemory(t *testing.T) {
	psi := "some avg10=23.50 avg60=4.00 avg300=1.00 total=123\nfull avg10=1.00 avg60=0.00 avg300=0.00 total=9\n"
	if v := parsePSI(psi); v != 23.5 {
		t.Fatalf("parsePSI = %v", v)
	}
	if v := parsePSI("full avg10=1.00\n"); v != -1 {
		t.Fatalf("parsePSI without a some line = %v", v)
	}
	for _, c := range []struct {
		m   memState
		low bool
	}{
		{memState{totalKB: 1000, availKB: 500, psi: -1}, false},
		{memState{totalKB: 1000, availKB: 99, psi: -1}, true},
		{memState{totalKB: 1000, availKB: 500, psi: 25}, true},
		{memState{psi: -1}, false},
	} {
		if c.m.low() != c.low {
			t.Errorf("%+v: low = %v", c.m, !c.low)
		}
	}
}

func TestLowMemoryVictimOrder(t *testing.T) {
	mem := map[string]int64{"bg-small": 10, "bg-big": 50, "old-small": 5, "old-big": 90, "recent1": 500, "recent2": 400, "fg": 900}
	saved := appMemoryOf
	appMemoryOf = func(_ *Core, a *App) int64 { return mem[a.M.ID] }
	defer func() { appMemoryOf = saved }()
	app := func(id string) *App { return &App{M: &Manifest{ID: id}, running: true, proc: &os.Process{}} }
	apps := map[string]*App{}
	c := &Core{}
	for _, id := range []string{"bg-small", "bg-big", "old-small", "old-big", "recent1", "recent2", "fg"} {
		apps[id] = app(id)
		c.apps = append(c.apps, apps[id])
	}
	c.fg = apps["fg"]
	c.mru = []*App{apps["fg"], apps["recent1"], apps["recent2"], apps["old-big"], apps["old-small"]}
	var order []string
	for {
		v := c.lowMemVictim()
		if v == nil {
			break
		}
		order = append(order, v.M.ID)
		v.ending = true
	}
	want := []string{"bg-big", "bg-small", "old-big", "old-small", "recent1", "recent2"}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Fatalf("order %v, want %v", order, want)
	}
}

func TestLowMemoryEndsBackgroundPagesFirst(t *testing.T) {
	app := func(id string, system bool) *App {
		return &App{M: &Manifest{ID: id}, running: true, system: system, proc: &os.Process{}}
	}
	browser, news, calc, clock := app("browser", true), app("news", false), app("calc", false), app("clock", false)
	c := &Core{log: log.New(io.Discard, "", 0), webprocs: map[string]*webProc{}, webviews: map[string]*webView{}}
	c.apps = []*App{browser, news, calc, clock}
	c.fg = clock
	c.mru = []*App{clock, browser, calc, news}
	for _, a := range []*App{browser, news, clock} {
		cmd := exec.Command("sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET, 0)
		if err != nil {
			t.Fatal(err)
		}
		syscall.Close(fds[1])
		fc, err := net.FileConn(os.NewFile(uintptr(fds[0]), "ctl"))
		if err != nil {
			t.Fatal(err)
		}
		p := &webProc{app: a, cmd: cmd, ctl: fc.(*net.UnixConn)}
		c.webprocs[a.M.ID] = p
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	}
	saved := appMemoryOf
	appMemoryOf = func(*Core, *App) int64 { return 1 }
	defer func() { appMemoryOf = saved }()
	var ended []string
	for i := 0; i < 6; i++ {
		before := map[string]bool{}
		for id := range c.webprocs {
			before[id] = true
		}
		c.SimulateLowMemory(1)
		c.checkMemory()
		for id := range before {
			if c.webprocs[id] == nil {
				ended = append(ended, "pages:"+id)
			}
		}
		for _, a := range c.apps {
			if a.ending && !slices.Contains(ended, a.M.ID) {
				ended = append(ended, a.M.ID)
			}
		}
	}
	want := "pages:news pages:browser news calc"
	if got := strings.Join(ended, " "); got != want {
		t.Fatalf("ended %q, want %q", got, want)
	}
	if c.webprocs["clock"] == nil || c.LowMemoryPageStops() != 2 {
		t.Fatalf("the foreground app's pages were ended, or %d page stops", c.LowMemoryPageStops())
	}
}
