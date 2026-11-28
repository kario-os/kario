package location

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// ModemManager's Location interface (org.freedesktop.ModemManager1.Modem.Location).
const (
	mmName     = "org.freedesktop.ModemManager1"
	mmRoot     = "/org/freedesktop/ModemManager1"
	mmLocation = mmName + ".Modem.Location"
	// MMModemLocationSource: the cell the modem is on (MCC, MNC, LAC, CI,
	// TAC), the NMEA trace of the modem's GPS, and its A-GPS over SUPL,
	// mobile station based or assisted.
	mmSource3GPP    = uint32(1 << 0)
	mmSourceNMEA    = uint32(1 << 2)
	mmSourceAGPSMSA = uint32(1 << 5)
	mmSourceAGPSMSB = uint32(1 << 6)
	// MM_MODEM_LOCATION_ASSISTANCE_DATA_TYPE_XTRA: predicted orbits in
	// Qualcomm's XTRA format.
	mmAssistXTRA  = uint32(1 << 0)
	mmCallTimeout = 10 * time.Second
	// mmReinject: assistance data the modem was given is given again
	// after this long even if unchanged (a modem reset may lose it).
	mmReinject = 24 * time.Hour
)

// MMBus is the system bus as the ModemManager receiver uses it (a fake
// ModemManager in tests).
type MMBus interface {
	Object(dest string, path dbus.ObjectPath) dbus.BusObject
	Close() error
}

func systemBus() (MMBus, error) { return dbus.ConnectSystemBus() }

// ModemManager reads the modem's GPS through ModemManager: Setup adds the
// GPS NMEA source to those the modem has on, GetLocation is read every
// Poll, and the sources are put back as they were at the end. Its NMEA is
// the latest sentence of each kind, so the same epoch comes again until
// the next one; the Decoder hands each out once.
//
// Before Setup the receiver is assisted where the modem can be (A-GNSS):
// given the SUPL server and A-GPS turned on (mobile station based where
// it can, else assisted), and given the predicted orbits Assistance
// fetches from the servers the modem names (XTRA).
type ModemManager struct {
	Poll time.Duration
	Now  func() time.Time
	// Dial is the system bus; nil: the real one.
	Dial func() (MMBus, error)
	// SUPL is the A-GNSS server, "host:port"; "" none.
	SUPL string
	// Assistance fetches assistance data from the modem's servers; nil:
	// none is given.
	Assistance func(ctx context.Context, servers []string) ([]byte, error)
	// Assisted hears, each time the receiver comes on, what it was given.
	Assisted func(Assist)

	mu       sync.Mutex
	injected struct {
		modem dbus.ObjectPath
		sum   [32]byte
		at    time.Time
	}
}

// Assist is what the modem's receiver was given as it came on: the SUPL
// server and the A-GPS mode ("msb", "msa" or "" without), the bytes of
// assistance data given now (Held: the same data was given before), and
// what failed, the receiver running on without it.
type Assist struct {
	Modem    string `json:"modem"`
	SUPL     string `json:"supl,omitempty"`
	AGPS     string `json:"agps,omitempty"`
	Injected int    `json:"injected,omitempty"`
	Held     bool   `json:"held,omitempty"`
	Err      string `json:"error,omitempty"`
}

func mmCall(ctx context.Context, obj dbus.BusObject, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(ctx, mmCallTimeout)
	defer cancel()
	return obj.CallWithContext(ctx, method, 0, args...)
}

// mmModem is the first modem with the Location interface, and its
// properties there.
func mmModem(ctx context.Context, bus MMBus) (dbus.ObjectPath, map[string]dbus.Variant, error) {
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := mmCall(ctx, bus.Object(mmName, mmRoot), "org.freedesktop.DBus.ObjectManager.GetManagedObjects").Store(&objs); err != nil {
		return "", nil, fmt.Errorf("modemmanager: %w", err)
	}
	return locationModem(objs)
}

func (m *ModemManager) dial() (MMBus, error) {
	if m.Dial != nil {
		return m.Dial()
	}
	return systemBus()
}

func (m *ModemManager) Run(ctx context.Context, fix func(Fix)) error {
	bus, err := m.dial()
	if err != nil {
		return fmt.Errorf("modemmanager: %w", err)
	}
	defer bus.Close()
	path, props, err := mmModem(ctx, bus)
	if err != nil {
		return err
	}
	caps, _ := props["Capabilities"].Value().(uint32)
	enabled, _ := props["Enabled"].Value().(uint32)
	signals, _ := props["SignalsLocation"].Value().(bool)
	if caps&mmSourceNMEA == 0 {
		return fmt.Errorf("modemmanager: %s has no GPS NMEA source (capabilities %#x)", path, caps)
	}
	modem := bus.Object(mmName, path)
	agps, as := m.assist(ctx, modem, path, props)
	want := enabled | mmSourceNMEA | agps
	err = mmCall(ctx, modem, mmLocation+".Setup", want, signals).Err
	if err != nil && agps != 0 && ctx.Err() == nil {
		as.Err = join(as.Err, "a-gps: "+err.Error())
		as.SUPL, as.AGPS = "", ""
		want = enabled | mmSourceNMEA
		err = mmCall(ctx, modem, mmLocation+".Setup", want, signals).Err
	}
	if err != nil {
		return fmt.Errorf("modemmanager: setup: %w", err)
	}
	if m.Assisted != nil {
		m.Assisted(as)
	}
	defer func() {
		if want != enabled {
			_ = mmCall(context.Background(), modem, mmLocation+".Setup", enabled, signals).Err
		}
	}()
	poll := m.Poll
	if poll <= 0 {
		poll = time.Second
	}
	_ = mmCall(ctx, modem, mmLocation+".SetGpsRefreshRate", uint32(max(1, poll/time.Second))).Err
	d := &Decoder{Now: m.Now}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		var loc map[uint32]dbus.Variant
		if err := mmCall(ctx, modem, mmLocation+".GetLocation").Store(&loc); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("modemmanager: %w", err)
		}
		for _, l := range nmeaLines(loc) {
			if f, ok := d.Feed(l); ok {
				fix(f)
			}
		}
	}
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// assist gives the modem what helps its receiver to a first fix: the SUPL
// server, and the assistance data, unless it has had the same lately. The
// sources to turn on with the GPS come back (A-GPS, if a SUPL server was
// taken).
func (m *ModemManager) assist(ctx context.Context, modem dbus.BusObject, path dbus.ObjectPath, props map[string]dbus.Variant) (uint32, Assist) {
	a := Assist{Modem: string(path)}
	caps, _ := props["Capabilities"].Value().(uint32)
	var agps uint32
	if m.SUPL != "" && caps&(mmSourceAGPSMSB|mmSourceAGPSMSA) != 0 {
		var err error
		if cur, _ := props["SuplServer"].Value().(string); cur != m.SUPL {
			err = mmCall(ctx, modem, mmLocation+".SetSuplServer", m.SUPL).Err
		}
		switch {
		case err != nil:
			a.Err = "supl: " + err.Error()
		case caps&mmSourceAGPSMSB != 0:
			agps, a.SUPL, a.AGPS = mmSourceAGPSMSB, m.SUPL, "msb"
		default:
			agps, a.SUPL, a.AGPS = mmSourceAGPSMSA, m.SUPL, "msa"
		}
	}
	types, _ := props["SupportedAssistanceData"].Value().(uint32)
	servers, _ := props["AssistanceDataServers"].Value().([]string)
	if m.Assistance == nil || types&mmAssistXTRA == 0 || len(servers) == 0 {
		return agps, a
	}
	data, err := m.Assistance(ctx, servers)
	if err != nil {
		a.Err = join(a.Err, "assistance data: "+err.Error())
		return agps, a
	}
	sum := sha256.Sum256(data)
	now := time.Now()
	m.mu.Lock()
	held := m.injected.modem == path && m.injected.sum == sum && now.Sub(m.injected.at) < mmReinject
	m.mu.Unlock()
	if held {
		a.Held = true
		return agps, a
	}
	if err := mmCall(ctx, modem, mmLocation+".InjectAssistanceData", data).Err; err != nil {
		a.Err = join(a.Err, "assistance data: "+err.Error())
		return agps, a
	}
	m.mu.Lock()
	m.injected.modem, m.injected.sum, m.injected.at = path, sum, now
	m.mu.Unlock()
	a.Injected = len(data)
	return agps, a
}

// ServingCell is the cell the modem is on, from ModemManager's Location
// interface: its 3GPP source, turned on for the read if it was off and
// left on (it only reports what the modem knows anyway, and turning it
// off could race the receiver's own Setup).
func ServingCell(ctx context.Context, dial func() (MMBus, error)) (Cell, error) {
	if dial == nil {
		dial = systemBus
	}
	bus, err := dial()
	if err != nil {
		return Cell{}, fmt.Errorf("modemmanager: %w", err)
	}
	defer bus.Close()
	path, props, err := mmModem(ctx, bus)
	if err != nil {
		return Cell{}, err
	}
	caps, _ := props["Capabilities"].Value().(uint32)
	enabled, _ := props["Enabled"].Value().(uint32)
	signals, _ := props["SignalsLocation"].Value().(bool)
	if caps&mmSource3GPP == 0 {
		return Cell{}, fmt.Errorf("modemmanager: %s does not report its cell", path)
	}
	modem := bus.Object(mmName, path)
	if enabled&mmSource3GPP == 0 {
		if err := mmCall(ctx, modem, mmLocation+".Setup", enabled|mmSource3GPP, signals).Err; err != nil {
			return Cell{}, fmt.Errorf("modemmanager: setup: %w", err)
		}
	}
	var loc map[uint32]dbus.Variant
	if err := mmCall(ctx, modem, mmLocation+".GetLocation").Store(&loc); err != nil {
		return Cell{}, fmt.Errorf("modemmanager: %w", err)
	}
	s, _ := loc[mmSource3GPP].Value().(string)
	c, ok := parseMMCell(s)
	if !ok {
		return Cell{}, fmt.Errorf("modemmanager: no cell (%q)", s)
	}
	return c, nil
}

// parseMMCell reads the 3GPP source's "MCC,MNC,LAC,CI,TAC" (the last three
// in hexadecimal). The radio is not given: a TAC is LTE's or NR's (NR's
// cell identities are longer than 28 bits), a LAC without one GSM's, or
// UMTS's when the cell identity is longer than 16 bits.
func parseMMCell(s string) (Cell, bool) {
	f := strings.Split(strings.TrimSpace(s), ",")
	if len(f) < 4 {
		return Cell{}, false
	}
	mcc, err1 := strconv.Atoi(f[0])
	mnc, err2 := strconv.Atoi(f[1])
	lac, err3 := strconv.ParseUint(f[2], 16, 32)
	ci, err4 := strconv.ParseUint(f[3], 16, 40)
	var tac uint64
	var err5 error
	if len(f) > 4 && f[4] != "" {
		tac, err5 = strconv.ParseUint(f[4], 16, 32)
	}
	if err := errors.Join(err1, err2, err3, err4, err5); err != nil || ci == 0 {
		return Cell{}, false
	}
	c := Cell{MCC: mcc, MNC: mnc, ID: int64(ci)}
	switch {
	case tac != 0 && ci > 1<<28-1:
		c.Radio, c.Area = "nr", int(tac)
	case tac != 0:
		c.Radio, c.Area = "lte", int(tac)
	case ci > 0xffff:
		c.Radio, c.Area = "wcdma", int(lac)
	default:
		c.Radio, c.Area = "gsm", int(lac)
	}
	return c, c.Area > 0
}

// locationModem is the first modem (by path) with the Location interface.
func locationModem(objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant) (dbus.ObjectPath, map[string]dbus.Variant, error) {
	var paths []string
	for p, ifs := range objs {
		if _, ok := ifs[mmLocation]; ok {
			paths = append(paths, string(p))
		}
	}
	if len(paths) == 0 {
		return "", nil, errors.New("modemmanager: no modem with a location interface")
	}
	sort.Strings(paths)
	p := dbus.ObjectPath(paths[0])
	return p, objs[p][mmLocation], nil
}

// nmeaLines are the sentences of GetLocation's GPS NMEA source.
func nmeaLines(loc map[uint32]dbus.Variant) []string {
	v, ok := loc[mmSourceNMEA]
	if !ok {
		return nil
	}
	s, _ := v.Value().(string)
	return strings.FieldsFunc(s, func(r rune) bool { return r == '\r' || r == '\n' })
}
