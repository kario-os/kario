package displayd

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// The structures are the kernel's sizes (drm_mode.h).
func TestDRMLayout(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"drm_mode_modeinfo", unsafe.Sizeof(drmModeInfo{}), 68},
		{"drm_mode_card_res", unsafe.Sizeof(drmCardRes{}), 64},
		{"drm_mode_get_connector", unsafe.Sizeof(drmGetConnector{}), 80},
		{"drm_mode_get_encoder", unsafe.Sizeof(drmGetEncoder{}), 20},
		{"drm_mode_crtc", unsafe.Sizeof(drmCRTC{}), 104},
	} {
		if c.got != c.want {
			t.Errorf("%s: %d bytes, the kernel's has %d", c.name, c.got, c.want)
		}
	}
	if ioctlGetConnector != 0xc05064a7 || ioctlGetResources != 0xc04064a0 || ioctlGetCRTC != 0xc06864a1 || ioctlDropMaster != 0x641f {
		t.Errorf("ioctl numbers %#x %#x %#x %#x", ioctlGetConnector, ioctlGetResources, ioctlGetCRTC, ioctlDropMaster)
	}
}

func mode(w, h uint16, clock uint32, htotal, vtotal uint16, flags, typ uint32) drmModeInfo {
	return drmModeInfo{Clock: clock, HDisplay: w, HTotal: htotal, VDisplay: h, VTotal: vtotal, Flags: flags, Type: typ}
}

// Rates as the kernel rounds them; the panel's are its modes of the size
// it runs at, progressive.
func TestPanelRates(t *testing.T) {
	for _, c := range []struct {
		m    drmModeInfo
		want int
	}{
		{mode(1024, 768, 65000, 1344, 806, 0, 0), 60},
		{mode(1920, 1080, 297000, 2200, 1125, 0, 0), 120},
		{mode(1920, 1080, 74250, 2200, 1125, drmModeFlagInterlace, 0), 60},
		{mode(640, 480, 25175, 800, 525, 0, 0), 60},
		{mode(320, 240, 12588, 800, 263, drmModeFlagDblscan, 0), 30},
		{mode(1, 1, 1, 0, 0, 0, 0), 0},
	} {
		if got := modeRate(c.m); got != c.want {
			t.Errorf("%+v: %d Hz, want %d", c.m, got, c.want)
		}
	}
	// A 1080x2340 panel with 60, 90 and 120 Hz modes (the same timing at
	// three clocks), a 720p fallback and an interlaced mode.
	m60 := mode(1080, 2340, 164000, 1180, 2316, 0, drmModeTypePreferred)
	m90 := mode(1080, 2340, 246000, 1180, 2316, 0, 0)
	m120 := mode(1080, 2340, 328000, 1180, 2316, 0, 0)
	small := mode(720, 1560, 82000, 800, 1708, 0, 0)
	inter := mode(1080, 2340, 164000, 1180, 2316, drmModeFlagInterlace, 0)
	rates, hz := panelRates([]drmModeInfo{m120, small, m60, inter, m90}, nil)
	if !slices.Equal(rates, []int{60, 90, 120}) || hz != 60 {
		t.Fatalf("rates %v at %d Hz, want [60 90 120] at the preferred 60", rates, hz)
	}
	if _, hz := panelRates([]drmModeInfo{m120, m60, m90}, &m90); hz != 90 {
		t.Fatalf("the CRTC at 90: %d Hz", hz)
	}
	// The emulator's virtio-gpu: one mode.
	if rates, hz := panelRates([]drmModeInfo{mode(480, 640, 24000, 640, 625, 0, drmModeTypePreferred)}, nil); !slices.Equal(rates, []int{60}) || hz != 60 {
		t.Fatalf("one mode: %v at %d", rates, hz)
	}
	if connectorName(16, 1) != "DSI-1" || connectorName(15, 1) != "Virtual-1" || connectorName(99, 2) != "Unknown-2" {
		t.Fatal(connectorName(16, 1), connectorName(15, 1), connectorName(99, 2))
	}
}

// The hottest of the zones named caps the rate, until it is coolBy under
// the cap; zones of other types do not count.
func TestThermal(t *testing.T) {
	root := t.TempDir()
	zone := func(n int, typ, temp string) {
		d := filepath.Join(root, "thermal_zone"+string(rune('0'+n)))
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "type"), []byte(typ+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(d, "temp"), []byte(temp+"\n"), 0o644)
	}
	zone(0, "cpu-thermal", "80000")
	zone(1, "skin-therm", "41000")
	zone(2, "battery", "39500")
	th := newThermal(root, "skin-therm, battery", 45)
	if th.check() || th.hot {
		t.Fatal("hot at 41 °C")
	}
	zone(2, "battery", "45200")
	if !th.check() || !th.hot {
		t.Fatal("not hot at 45.2 °C")
	}
	zone(2, "battery", "43000")
	if th.check() || !th.hot {
		t.Fatal("cooled at 43 °C, within coolBy")
	}
	zone(2, "battery", "41900")
	if !th.check() || th.hot {
		t.Fatal("still hot at 41.9 °C")
	}
	if !newThermal(root, "", 45).sim {
		t.Fatal("no zones named: not the simulated one")
	}
}

// openPanel's specs.
func TestOpenPanel(t *testing.T) {
	p, err := openPanel("sim")
	if err != nil || !slices.Equal(p.Rates(), []int{60, 120}) || p.Hz() != 60 {
		t.Fatalf("sim: %v %v", p, err)
	}
	p, err = openPanel("sim:120,30,60,30")
	if err != nil || !slices.Equal(p.Rates(), []int{30, 60, 120}) {
		t.Fatalf("sim with rates: %v %v", p, err)
	}
	for _, bad := range []string{"sim:fast", "sim:0", "hdmi"} {
		if _, err := openPanel(bad); err == nil {
			t.Errorf("%q taken", bad)
		}
	}
	k, err := openPanel("kms:/nonexistent/card9")
	if err != nil || k.Name() != "" || len(k.Rates()) != 0 {
		t.Fatalf("kms before it is read: %v %v", k, err)
	}
	if err := k.Open(); err == nil {
		t.Fatal("opened a card that is not there")
	}
}

// The service end to end over its socket: a subscriber hears the rate go
// up with a key and down after the hold, the setting fixing it, the cap
// and the simulated heat holding it down.
func TestServiceEvents(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, svc.DisplaydSock)
	d := newDaemon(newSimPanel([]int{60, 120}), newThermal(dir, "", 45), false)
	hub, err := wire.ServeHub(sock, d.handle)
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.hub = hub
	d.mu.Unlock()
	go d.run()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	sub := wire.New(nc)
	_ = sub.Send(wire.RPC{ID: 1, Method: wire.Subscribe})
	events := make(chan svc.DisplayEvent, 64)
	go func() {
		for {
			var ev svc.DisplayEvent
			if sub.Recv(&ev) != nil {
				close(events)
				return
			}
			if ev.Event == svc.DisplayEventRate {
				events <- ev
			}
		}
	}()
	cl, err := wire.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	wait := func(hz int, reason string) svc.DisplayEvent {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.Hz == hz && ev.Want == hz && (reason == "" || ev.Reason == reason) {
					return ev
				}
			case <-deadline:
				var st svc.DisplayStatus
				_ = cl.Call(svc.DisplayState, nil, &st)
				t.Fatalf("no event of %d Hz (%s); state %+v", hz, reason, st)
			}
		}
	}
	var st svc.DisplayStatus
	time.Sleep(50 * time.Millisecond)
	if err := cl.Call(svc.DisplayState, nil, &st); err != nil || st.Hz != 60 || st.Panel != "sim" || !slices.Equal(st.Rates, []int{60, 120}) {
		t.Fatalf("state %+v %v", st, err)
	}
	if err := cl.Call(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceInput, Kind: svc.DisplayInteraction}, &st); err != nil {
		t.Fatal(err)
	}
	t1 := time.Now()
	wait(120, svc.DisplaySourceInput)
	wait(60, "idle")
	if since := time.Since(t1); since < Hold-50*time.Millisecond {
		t.Fatalf("down after %v, before the hold", since)
	}
	if err := cl.Call(svc.DisplaySetting, map[string]string{"setting": ui.RefreshHigh}, &st); err != nil {
		t.Fatal(err)
	}
	wait(120, "setting")
	if err := cl.Call(svc.DisplayCap, svc.DisplayCapArgs{Source: svc.DisplayCapBattery, Hz: 60}, &st); err != nil {
		t.Fatal(err)
	}
	wait(60, "cap:battery")
	_ = cl.Call(svc.DisplayCap, svc.DisplayCapArgs{Source: svc.DisplayCapBattery}, &st)
	wait(120, "setting")
	if err := cl.Call(svc.DisplaySimThermal, map[string]bool{"hot": true}, nil); err != nil {
		t.Fatal(err)
	}
	wait(60, "cap:thermal")
	_ = cl.Call(svc.DisplaySimThermal, map[string]bool{"hot": false}, nil)
	ev := wait(120, "setting")
	if ev.Switches < 5 {
		t.Fatalf("%d switches counted", ev.Switches)
	}
	if err := cl.Call(svc.DisplayCap, svc.DisplayCapArgs{Source: svc.DisplayCapThermal, Hz: 60}, nil); err == nil {
		t.Fatal("the Shell set the thermal cap")
	}
	if err := cl.Call(svc.DisplayHint, svc.DisplayHintArgs{Source: "x", Kind: "faster"}, nil); err == nil {
		t.Fatal("an unknown hint was taken")
	}
	raw, _ := json.Marshal(ev)
	var back svc.DisplayEvent
	if json.Unmarshal(raw, &back) != nil || back.Hz != 120 || back.Event != svc.DisplayEventRate {
		t.Fatalf("the event does not read back: %s", raw)
	}
}

// A phone's panel: the rate is the renderer's to switch; displayd's choice
// stands until the renderer says which mode it set.
func TestKMSWaitsForTheRenderer(t *testing.T) {
	k := &kmsPanel{name: "kms:DSI-1", rates: []int{60, 120}, hz: 60}
	d := newDaemon(k, newThermal(t.TempDir(), "", 45), false)
	d.policy.SetRates(k.Rates())
	d.mu.Lock()
	d.policy.Hint(svc.DisplayHintArgs{Source: svc.DisplaySourceInput, Kind: svc.DisplayInteraction}, time.Now())
	d.decide()
	st := d.state()
	d.mu.Unlock()
	if st.Want != 120 || st.Hz != 60 {
		t.Fatalf("before the renderer's answer: %+v", st)
	}
	raw, _ := json.Marshal(svc.DisplayPanelArgs{Applied: 120})
	if _, err := d.handle(svc.DisplayPanel, raw); err != nil {
		t.Fatal(err)
	}
	if st := d.state(); st.Hz != 120 {
		t.Fatalf("after: %+v", st)
	}
	raw, _ = json.Marshal(svc.DisplayPanelArgs{Applied: 75})
	_, _ = d.handle(svc.DisplayPanel, raw)
	if st := d.state(); st.Hz != 120 {
		t.Fatalf("a rate the panel has no mode of: %+v", st)
	}
}
