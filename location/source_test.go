package location

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// The simulated receiver is off until run, gives no fix for TTFF epochs,
// then fixes where it was put, and is off again once its context ends.
func TestSimRun(t *testing.T) {
	s := NewSim(nil)
	s.Interval, s.TTFF = 10*time.Millisecond, 2
	s.Set(31.2304, 121.4737, 0, 0)
	if s.On() {
		t.Fatal("on before run")
	}
	ctx, cancel := context.WithCancel(context.Background())
	fixes := make(chan Fix, 64)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, func(f Fix) { fixes <- f }) }()
	select {
	case f := <-fixes:
		if !near(f.Lat, 31.2304, 1e-6) || !near(f.Lon, 121.4737, 1e-6) {
			t.Fatalf("%+v", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no fix")
	}
	if !s.On() || s.Starts() != 1 {
		t.Fatalf("on %v starts %d", s.On(), s.Starts())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.On() {
		t.Fatal("still on")
	}
}

// The emulator's path: the simulated receiver writes NMEA into a named
// pipe; the Stream reading it parses real sentences. Epochs written while
// nothing reads are dropped, and the reader's end does not end the writer.
func TestFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gnss.nmea")
	sim := NewSim(nil)
	sim.Interval, sim.TTFF = 10*time.Millisecond, 1
	sim.Set(22.5431, 114.0579, 0, 0)
	feedCtx, stopFeed := context.WithCancel(context.Background())
	defer stopFeed()
	fed := make(chan error, 1)
	go func() { fed <- sim.Feed(feedCtx, path) }()
	for i := 0; ; i++ {
		if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeNamedPipe != 0 {
			break
		}
		if i > 500 {
			t.Fatal("no pipe")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if sim.On() {
		t.Fatal("the receiver came on with nothing reading")
	}
	for round := 0; round < 2; round++ {
		src := &Stream{Path: path, FIFO: true}
		ctx, cancel := context.WithCancel(context.Background())
		fixes := make(chan Fix, 64)
		done := make(chan error, 1)
		go func() { done <- src.Run(ctx, func(f Fix) { fixes <- f }) }()
		select {
		case f := <-fixes:
			if !near(f.Lat, 22.5431, 1e-6) || !near(f.Lon, 114.0579, 1e-6) || f.Satellites != 8 {
				t.Fatalf("round %d: %+v", round, f)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: no fix", round)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the stream did not stop")
		}
	}
	stopFeed()
	if err := <-fed; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "plain"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Stream{Path: filepath.Join(filepath.Dir(path), "plain"), FIFO: true}).Run(context.Background(), func(Fix) {}); err == nil || !strings.Contains(err.Error(), "not a named pipe") {
		t.Fatalf("a regular file taken for the pipe: %v", err)
	}
}

// A device that ends (unplugged) or is not there stops the stream with the
// reason; an overlong line is skipped and what follows still parses.
func TestStreamEnds(t *testing.T) {
	dir := t.TempDir()
	if err := (&Stream{Path: filepath.Join(dir, "gnss0")}).Run(context.Background(), func(Fix) {}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing device: %v", err)
	}
	sim := NewSim(nil)
	body := strings.Repeat("x", 10000) + "\n" + strings.Join(sim.Epoch(time.Now(), true), "\r\n") + "\r\n"
	path := filepath.Join(dir, "capture.nmea")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []Fix
	err := (&Stream{Path: path}).Run(context.Background(), func(f Fix) { got = append(got, f) })
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(got) != 1 {
		t.Fatalf("%v, %d fixes", err, len(got))
	}
}

func TestModemManagerLocation(t *testing.T) {
	objs := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		"/org/freedesktop/ModemManager1/Modem/3": {mmLocation: {"Capabilities": dbus.MakeVariant(uint32(7))}},
		"/org/freedesktop/ModemManager1/Modem/1": {mmLocation: {"Capabilities": dbus.MakeVariant(uint32(5))}},
		"/org/freedesktop/ModemManager1/Modem/0": {mmName + ".Modem": {}},
	}
	p, props, err := locationModem(objs)
	if err != nil || p != "/org/freedesktop/ModemManager1/Modem/1" || props["Capabilities"].Value() != uint32(5) {
		t.Fatalf("%s %v %v", p, props, err)
	}
	if _, _, err := locationModem(nil); err == nil {
		t.Fatal("no modem, no error")
	}
	trace := strings.Join([]string{refGGA, refGSA, refRMC}, "\r\n")
	lines := nmeaLines(map[uint32]dbus.Variant{1: dbus.MakeVariant(map[string]any{}), mmSourceNMEA: dbus.MakeVariant(trace)})
	if len(lines) != 3 || lines[2] != refRMC {
		t.Fatalf("%q", lines)
	}
	d := &Decoder{}
	if got := feed(d, append(lines, lines...)...); len(got) != 1 {
		t.Fatalf("the same trace read twice made %d fixes", len(got))
	}
}

func TestNew(t *testing.T) {
	for _, v := range []string{"sim", "nmea:/dev/gnss0", "fifo:/run/phone/gnss.nmea", "modemmanager"} {
		s, _ := ParseSpec(v)
		if New(s, nil) == nil {
			t.Errorf("%s: no source", v)
		}
	}
	if New(Spec{Kind: "none"}, nil) != nil {
		t.Error("a source for no receiver")
	}
}
