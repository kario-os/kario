package location

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakeMM is ModemManager with one modem, as the receiver sees it on the
// bus: its Location interface's properties, what GetLocation answers, the
// calls made (method and arguments) and the methods made to fail.
type fakeMM struct {
	mu       sync.Mutex
	props    map[string]dbus.Variant
	loc      map[uint32]dbus.Variant
	calls    []string
	fail     map[string]error
	injected []byte
}

const fakeModem = dbus.ObjectPath("/org/freedesktop/ModemManager1/Modem/0")

func newFakeMM(caps, enabled uint32) *fakeMM {
	trace := strings.Join([]string{refGGA, refGSA, refRMC}, "\r\n")
	return &fakeMM{props: map[string]dbus.Variant{
		"Capabilities":    dbus.MakeVariant(caps),
		"Enabled":         dbus.MakeVariant(enabled),
		"SignalsLocation": dbus.MakeVariant(false),
		"SuplServer":      dbus.MakeVariant(""),
	}, loc: map[uint32]dbus.Variant{mmSourceNMEA: dbus.MakeVariant(trace)}, fail: map[string]error{}}
}

func (f *fakeMM) dial() (MMBus, error) { return f, nil }
func (f *fakeMM) Close() error         { return nil }
func (f *fakeMM) Object(dest string, path dbus.ObjectPath) dbus.BusObject {
	return &fakeMMObj{f: f, dest: dest, path: path}
}

func (f *fakeMM) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

type fakeMMObj struct {
	f    *fakeMM
	dest string
	path dbus.ObjectPath
}

func (o *fakeMMObj) CallWithContext(ctx context.Context, method string, flags dbus.Flags, args ...any) *dbus.Call {
	f := o.f
	f.mu.Lock()
	defer f.mu.Unlock()
	name := method[strings.LastIndex(method, ".")+1:]
	if name == "InjectAssistanceData" {
		f.calls = append(f.calls, fmt.Sprintf("%s %d", name, len(args[0].([]byte))))
	} else {
		f.calls = append(f.calls, fmt.Sprint(name, args))
	}
	if err := f.fail[name]; err != nil {
		return &dbus.Call{Err: err}
	}
	switch name {
	case "GetManagedObjects":
		props := map[string]dbus.Variant{}
		for k, v := range f.props {
			props[k] = v
		}
		return &dbus.Call{Body: []any{map[dbus.ObjectPath]map[string]map[string]dbus.Variant{fakeModem: {mmLocation: props}}}}
	case "Setup":
		if src := args[0].(uint32); src&f.props["Capabilities"].Value().(uint32) != src {
			return &dbus.Call{Err: errors.New("unsupported location source")}
		}
		if f.fail["SetupAGPS"] != nil && args[0].(uint32)&(mmSourceAGPSMSA|mmSourceAGPSMSB) != 0 {
			return &dbus.Call{Err: f.fail["SetupAGPS"]}
		}
		f.props["Enabled"], f.props["SignalsLocation"] = dbus.MakeVariant(args[0].(uint32)), dbus.MakeVariant(args[1].(bool))
	case "SetSuplServer":
		f.props["SuplServer"] = dbus.MakeVariant(args[0].(string))
	case "InjectAssistanceData":
		f.injected = append([]byte{}, args[0].([]byte)...)
	case "GetLocation":
		return &dbus.Call{Body: []any{f.loc}}
	}
	return &dbus.Call{}
}

func (o *fakeMMObj) Call(method string, flags dbus.Flags, args ...any) *dbus.Call {
	return o.CallWithContext(context.Background(), method, flags, args...)
}
func (o *fakeMMObj) Go(method string, flags dbus.Flags, ch chan *dbus.Call, args ...any) *dbus.Call {
	return o.Call(method, flags, args...)
}
func (o *fakeMMObj) GoWithContext(ctx context.Context, method string, flags dbus.Flags, ch chan *dbus.Call, args ...any) *dbus.Call {
	return o.CallWithContext(ctx, method, flags, args...)
}
func (o *fakeMMObj) AddMatchSignal(string, string, ...dbus.MatchOption) *dbus.Call {
	return &dbus.Call{}
}
func (o *fakeMMObj) RemoveMatchSignal(string, string, ...dbus.MatchOption) *dbus.Call {
	return &dbus.Call{}
}
func (o *fakeMMObj) GetProperty(string) (dbus.Variant, error) {
	return dbus.Variant{}, errors.New("no")
}
func (o *fakeMMObj) StoreProperty(string, any) error { return errors.New("no") }
func (o *fakeMMObj) SetProperty(string, any) error   { return errors.New("no") }
func (o *fakeMMObj) Destination() string             { return o.dest }
func (o *fakeMMObj) Path() dbus.ObjectPath           { return o.path }

// runMM runs the receiver until its first fix and returns what it was
// told about assistance.
func runMM(t *testing.T, m *ModemManager) Assist {
	t.Helper()
	var got Assist
	m.Assisted = func(a Assist) { got = a }
	m.Poll = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fixed := false
	err := m.Run(ctx, func(Fix) {
		fixed = true
		cancel()
	})
	if err != nil || !fixed {
		t.Fatalf("run: %v, fix %v", err, fixed)
	}
	return got
}

func hasCall(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// A-GNSS: a modem that can use SUPL is given the product's server and has
// MS-based A-GPS turned on with its GPS; the predicted orbits are fetched
// from the servers it names and injected, once; at the end its sources
// are as they were.
func TestModemManagerAssist(t *testing.T) {
	fake := newFakeMM(mmSource3GPP|mmSourceNMEA|mmSourceAGPSMSA|mmSourceAGPSMSB, mmSource3GPP)
	fake.props["SupportedAssistanceData"] = dbus.MakeVariant(mmAssistXTRA)
	fake.props["AssistanceDataServers"] = dbus.MakeVariant([]string{"https://xtra1.example.net/xtra3grc.bin", "https://xtra2.example.net/xtra3grc.bin"})
	var asked []string
	m := &ModemManager{Dial: fake.dial, SUPL: "supl.example.net:7275", Assistance: func(_ context.Context, servers []string) ([]byte, error) {
		asked = servers
		return []byte("predicted orbits"), nil
	}}
	a := runMM(t, m)
	if a.SUPL != "supl.example.net:7275" || a.AGPS != "msb" || a.Injected != 16 || a.Held || a.Err != "" || len(asked) != 2 {
		t.Fatalf("%+v (asked %v)", a, asked)
	}
	calls := fake.log()
	want := []string{"GetManagedObjects[]", "SetSuplServer[supl.example.net:7275]", "InjectAssistanceData 16", fmt.Sprintf("Setup[%d false]", mmSource3GPP|mmSourceNMEA|mmSourceAGPSMSB)}
	for i, w := range want {
		if i >= len(calls) || calls[i] != w {
			t.Fatalf("call %d: want %q, got %q", i, w, calls)
		}
	}
	if last := calls[len(calls)-1]; last != fmt.Sprintf("Setup[%d false]", mmSource3GPP) || string(fake.injected) != "predicted orbits" {
		t.Fatalf("the sources were not put back: %q", calls)
	}
	fake.calls = nil
	a = runMM(t, m)
	if !a.Held || a.Injected != 0 || a.AGPS != "msb" || hasCall(fake.log(), "SetSuplServer") || hasCall(fake.log(), "InjectAssistanceData") {
		t.Fatalf("the second start gave it all again: %+v %q", a, fake.log())
	}
}

// A modem without A-GPS or assistance gets neither; whatever of it fails,
// the receiver runs on without it.
func TestModemManagerAssistFails(t *testing.T) {
	plain := newFakeMM(mmSourceNMEA, 0)
	a := runMM(t, &ModemManager{Dial: plain.dial, SUPL: "supl.example.net:7275", Assistance: func(context.Context, []string) ([]byte, error) {
		t.Fatal("assistance fetched for a modem that takes none")
		return nil, nil
	}})
	if a.SUPL != "" || a.AGPS != "" || a.Err != "" || hasCall(plain.log(), "SetSuplServer") {
		t.Fatalf("%+v %q", a, plain.log())
	}
	for _, c := range []struct {
		fail, want string
		agps       string
	}{
		{"SetSuplServer", "supl:", ""},
		{"SetupAGPS", "a-gps:", ""},
		{"InjectAssistanceData", "assistance data:", "msa"},
		{"fetch", "assistance data: offline", "msa"},
	} {
		fake := newFakeMM(mmSourceNMEA|mmSourceAGPSMSA, 0)
		fake.props["SupportedAssistanceData"] = dbus.MakeVariant(mmAssistXTRA)
		fake.props["AssistanceDataServers"] = dbus.MakeVariant([]string{"https://xtra.example.net/x.bin"})
		fake.fail[c.fail] = errors.New("refused")
		a := runMM(t, &ModemManager{Dial: fake.dial, SUPL: "supl.example.net:7275", Assistance: func(context.Context, []string) ([]byte, error) {
			if c.fail == "fetch" {
				return nil, errors.New("offline")
			}
			return []byte("x"), nil
		}})
		if !strings.Contains(a.Err, c.want) || a.AGPS != c.agps {
			t.Errorf("%s failing: %+v", c.fail, a)
		}
	}
}

// The serving cell: the 3GPP source, turned on for the read; its string
// read into the cell's identity and radio.
func TestServingCell(t *testing.T) {
	fake := newFakeMM(mmSource3GPP|mmSourceNMEA, mmSourceNMEA)
	fake.loc[mmSource3GPP] = dbus.MakeVariant("460,00,0,1A2B3C4,10E1")
	c, err := ServingCell(context.Background(), fake.dial)
	if err != nil || c != (Cell{Radio: "lte", MCC: 460, MNC: 0, Area: 0x10e1, ID: 0x1a2b3c4}) {
		t.Fatalf("%+v %v", c, err)
	}
	if !hasCall(fake.log(), fmt.Sprintf("Setup[%d false]", mmSource3GPP|mmSourceNMEA)) {
		t.Fatalf("the 3GPP source was not turned on: %q", fake.log())
	}
	if _, err := ServingCell(context.Background(), newFakeMM(mmSourceNMEA, 0).dial); err == nil {
		t.Fatal("a cell from a modem that reports none")
	}
	for s, want := range map[string]Cell{
		"460,01,2F5B,C3A5,0":        {Radio: "gsm", MCC: 460, MNC: 1, Area: 0x2f5b, ID: 0xc3a5},
		"214,07,2F5B,C3A51":         {Radio: "wcdma", MCC: 214, MNC: 7, Area: 0x2f5b, ID: 0xc3a51},
		"001,01,0,123456789,ABCDEF": {Radio: "nr", MCC: 1, MNC: 1, Area: 0xabcdef, ID: 0x123456789},
	} {
		if c, ok := parseMMCell(s); !ok || c != want {
			t.Errorf("%q: %+v", s, c)
		}
	}
	for _, s := range []string{"", "460,00", "460,00,0,0,10E1", "460,00,0,1A2B,0", "x,00,1,2,3"} {
		if c, ok := parseMMCell(s); ok {
			t.Errorf("%q: %+v", s, c)
		}
	}
}

// The assistance data is fetched once a day, from the first server that
// answers; the copy kept is given for a week while none does, and the
// servers are not tried again within the hour.
func TestAssistCache(t *testing.T) {
	var hits int
	body := "predicted orbits"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/big" {
			w.Write(make([]byte, assistMax+1))
			return
		}
		io.WriteString(w, body)
	}))
	defer srv.Close()
	ck := newClock()
	ck.t = time.Now()
	dir := t.TempDir()
	a := &AssistCache{Dir: dir, Now: ck.now}
	servers := []string{"ftp://xtra.example.net/x.bin", srv.URL + "/big", srv.URL + "/x.bin"}
	got, err := a.Fetch(context.Background(), servers)
	if err != nil || string(got) != body || hits != 2 {
		t.Fatalf("%q %v, %d hits", got, err, hits)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "assist-*.bin"))
	if st, err := os.Stat(files[0]); len(files) != 1 || err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", files, err)
	}
	ck.step(time.Hour)
	if got, _ := a.Fetch(context.Background(), servers); string(got) != body || hits != 2 {
		t.Fatalf("fetched again within the day: %d hits", hits)
	}
	ck.step(assistRefresh)
	body = "newer orbits"
	if got, _ := a.Fetch(context.Background(), servers); string(got) != body || hits != 4 {
		t.Fatalf("a day later: %q, %d hits", got, hits)
	}
	srv.Close()
	ck.step(assistRefresh + time.Minute)
	if got, err := a.Fetch(context.Background(), servers); err != nil || string(got) != body {
		t.Fatalf("servers down, the copy kept: %q %v", got, err)
	}
	ck.step(assistKeep)
	if _, err := a.Fetch(context.Background(), servers); err == nil {
		t.Fatal("a copy over a week old given")
	}
	ck.step(time.Minute)
	if _, err := a.Fetch(context.Background(), servers); err == nil || !strings.Contains(err.Error(), "less than an hour") {
		t.Fatalf("the servers tried again within the hour: %v", err)
	}
}
