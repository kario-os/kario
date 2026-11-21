package shell

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/kario-os/kario/location"
)

// Assisted and network location, as the product sets them up (profile
// "location"). A modem's receiver is given the product's SUPL server and
// the predicted orbits its servers offer (location.ModemManager). While
// the receiver gives no fix, a network location service places the phone
// by the Wi-Fi access points it hears and the cell it is on: only while
// location is on and an app allowed it needs a fix, never by a hidden
// network or one whose name opts out (location.Observation.Usable), with
// the answers kept on the phone and the service asked sparingly
// (location.Locator). The user can turn it off in Settings › Location.

// LocationProfile is the product's location services.
type LocationProfile struct {
	// SUPLServer is the A-GNSS server ("host:port") a modem's receiver is
	// given when it can use one; "" none.
	SUPLServer string `json:"supl_server,omitempty"`
	// NoAssistanceData: the predicted orbits a modem names servers for are
	// not fetched.
	NoAssistanceData bool `json:"no_assistance_data,omitempty"`
	// Network is the network location service; without one, a simulated
	// phone (no SKU, or a simulated modem) has the fake one, a phone none.
	Network *NetworkLocation `json:"network,omitempty"`
}

// NetworkLocation names the service: "http", one that speaks the
// Geolocation API's JSON at URL, its API key read from KeyFile or from the
// environment variable KeyEnv (the owner's own, never in the image);
// "fake" (location.FakeProvider); or "none". MaxPerHour bounds the
// requests (the service's quota).
type NetworkLocation struct {
	Provider   string `json:"provider"`
	URL        string `json:"url,omitempty"`
	KeyFile    string `json:"key_file,omitempty"`
	KeyEnv     string `json:"key_env,omitempty"`
	MaxPerHour int    `json:"max_per_hour,omitempty"`
}

const (
	// locNetAfter: the receiver had this long without a fix before the
	// network is asked; locNetEvery spaces the lookups while it has none.
	locNetAfter   = 5 * time.Second
	locNetEvery   = 30 * time.Second
	locNetTimeout = 20 * time.Second
	// locScanEvery: the Wi-Fi station is asked for a new scan at most this
	// often for the network location.
	locScanEvery = 30 * time.Second
)

type locNet struct {
	loc  *location.Locator
	kind string
	host string
	// cells reads the cell the phone is on; nil: none.
	cells func(context.Context) ([]location.Cell, error)
	busy  bool
	next  time.Time
	err   string
}

func (c *Core) locProfile() LocationProfile {
	if c.profile == nil {
		return LocationProfile{}
	}
	return c.profile.Location
}

// simulatedPhone: the developer machine, or an image whose modem is
// simulated (the emulator).
func (c *Core) simulatedPhone() bool { return c.cfg.SKU.ID == "" || c.cfg.SKU.Modem == "sim" }

// locNetInit sets up the network location service the product names.
func (c *Core) locNetInit() {
	n := c.locProfile().Network
	if n == nil {
		n = &NetworkLocation{Provider: "none"}
		if c.simulatedPhone() {
			n.Provider = "fake"
		}
	}
	var p location.Provider
	switch n.Provider {
	case "none", "":
		return
	case "fake":
		p = &location.FakeProvider{Where: c.locFakeWhere}
		c.loc.net.host = "fake"
	case "http":
		u, err := url.Parse(n.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
			c.log.Printf("location: the network location service %q is not an https URL; none", n.URL)
			return
		}
		key := ""
		if n.KeyEnv != "" {
			key = os.Getenv(n.KeyEnv)
		}
		if n.KeyFile != "" {
			raw, err := os.ReadFile(n.KeyFile)
			if err != nil {
				c.log.Printf("location: the network location service's key: %v; none", err)
				return
			}
			key = strings.TrimSpace(string(raw))
		}
		p = &location.HTTPProvider{URL: n.URL, Key: key}
		c.loc.net.host = u.Host
	default:
		c.log.Printf("location: no network location provider %q", n.Provider)
		return
	}
	c.loc.net.kind = n.Provider
	c.loc.net.loc = &location.Locator{Provider: p, Path: filepath.Join(c.cfg.StateDir, "shell", "netloc.json"), MaxPerHour: n.MaxPerHour}
	switch {
	case c.cfg.SKU.Modem == "modemmanager":
		c.loc.net.cells = func(ctx context.Context) ([]location.Cell, error) {
			cell, err := location.ServingCell(ctx, nil)
			if err != nil {
				return nil, err
			}
			return []location.Cell{cell}, nil
		}
	case c.simulatedPhone():
		c.loc.net.cells = func(context.Context) ([]location.Cell, error) { return []location.Cell{simCell}, nil }
	}
	c.log.Printf("location: network location by %s (%s)", n.Provider, c.loc.net.host)
}

// simCell is the simulated network's cell.
var simCell = location.Cell{Radio: "lte", MCC: 460, MNC: 0, Area: 0x10e1, ID: 0x1a2b3c4, SignalDBm: -85}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// locFakeWhere is where the fake service places the phone: where the
// simulated receiver is. It runs off the loop.
func (c *Core) locFakeWhere() (float64, float64) {
	if sim := c.loc.simNow.Load(); sim != nil {
		return sim.Position()
	}
	return location.SimLat, location.SimLon
}

// locNetTick asks the network service where the phone is, while someone
// allowed needs the location and the receiver has given no fix for
// locNetAfter.
func (c *Core) locNetTick() {
	n := &c.loc.net
	if n.loc == nil || n.busy || c.settings.LocationNetworkOff || !c.locNeeded() {
		return
	}
	now := time.Now()
	since := c.loc.needSince
	if c.loc.gnssAt.After(since) {
		since = c.loc.gnssAt
	}
	if now.Sub(since) < locNetAfter || now.Before(n.next) {
		return
	}
	n.busy, n.next = true, now.Add(locNetEvery)
	wifi, cells, loc := c.wifi, n.cells, n.loc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), locNetTimeout)
		defer cancel()
		f, err := loc.Locate(ctx, observe(ctx, wifi, cells))
		c.Post(func() { c.locNetDone(f, err) })
	}()
}

func (c *Core) locNetDone(f location.Fix, err error) {
	n := &c.loc.net
	n.busy = false
	switch {
	case errors.Is(err, location.ErrRateLimited):
		return
	case err != nil:
		if err.Error() != n.err {
			c.log.Printf("location: network location: %v", err)
		}
		n.err = err.Error()
		return
	}
	n.err = ""
	if c.settings.LocationNetworkOff || !c.locNeeded() || time.Since(c.loc.gnssAt) < locNetAfter {
		return
	}
	c.log.Printf("location: placed by the network (±%.0f m)", f.AccuracyM)
	c.locHandOut(f)
}

// locSetNetwork is the user's switch for the network location; off, the
// answers kept on the phone go too.
func (c *Core) locSetNetwork(on bool) error {
	if c.loc.net.loc == nil {
		return errors.New(tr.T("This phone has no network location service"))
	}
	if on == !c.settings.LocationNetworkOff {
		return nil
	}
	c.settings.LocationNetworkOff = !on
	c.saveSettings()
	if !on {
		c.loc.net.loc.Forget()
	}
	c.log.Printf("network location turned %s by the user", map[bool]string{true: "on", false: "off"}[on])
	return nil
}

func (c *Core) locNetView() map[string]any {
	n := c.loc.net
	if n.loc == nil {
		return map[string]any{"provider": ""}
	}
	st := n.loc.Stats()
	return map[string]any{"provider": n.kind, "host": n.host, "on": !c.settings.LocationNetworkOff, "error": n.err,
		"requests": st.Requests, "cached": st.Cached}
}

// locAssist sets up the modem's A-GNSS: the product's SUPL server, the
// predicted orbits fetched into the state directory.
func (c *Core) locAssist(mm *location.ModemManager) {
	p := c.locProfile()
	if s := p.SUPLServer; s != "" {
		host, port, err := net.SplitHostPort(s)
		if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n <= 0 || n > 65535 {
			c.log.Printf("location: SUPL server %q is not host:port; none", s)
		} else {
			mm.SUPL = s
		}
	}
	if !p.NoAssistanceData {
		mm.Assistance = (&location.AssistCache{Dir: filepath.Join(c.cfg.StateDir, "shell", "gnss")}).Fetch
	}
	mm.Assisted = func(a location.Assist) {
		c.Post(func() {
			c.loc.assist = &a
			c.log.Printf("location: receiver assisted: SUPL %q (%s), %d bytes of assistance data (held %v) %s", a.SUPL, a.AGPS, a.Injected, a.Held, a.Err)
		})
	}
}

// wifiScanner is a Wi-Fi radio that can list the access points it hears.
type wifiScanner interface {
	accessPoints() ([]location.AccessPoint, error)
}

// observe is what the phone hears: the Wi-Fi radio's access points, the
// cell.
func observe(ctx context.Context, w wifiRadio, cells func(context.Context) ([]location.Cell, error)) location.Observation {
	var o location.Observation
	if s, ok := w.(wifiScanner); ok {
		o.WiFi, _ = s.accessPoints()
	}
	if cells != nil {
		o.Cells, _ = cells(ctx)
	}
	return o
}

// accessPoints are the access points of the networks iwd sees, each with
// its network's signal; a hidden network's are marked so. A scan is asked
// for now and then, for the next look.
func (r *iwdRadio) accessPoints() ([]location.AccessPoint, error) {
	o, err := r.load()
	if err != nil {
		return nil, err
	}
	dev := o.device()
	if dev == "" || o.prop(dev, iwdDevice, "Powered") != true {
		return nil, nil
	}
	sta := r.bus.Object(iwdName, dev)
	r.mu.Lock()
	scan := time.Since(r.scanned) > locScanEvery
	if scan {
		r.scanned = time.Now()
	}
	r.mu.Unlock()
	if scanning, _ := o.prop(dev, iwdSta, "Scanning").(bool); scan && !scanning {
		_ = busCall(sta, iwdSta+".Scan").Err
	}
	var ordered []struct {
		Path   dbus.ObjectPath
		Signal int16
	}
	if err := busCall(sta, iwdSta+".GetOrderedNetworks").Store(&ordered); err != nil {
		return nil, err
	}
	var out []location.AccessPoint
	for _, n := range ordered {
		name, _ := o.prop(n.Path, iwdNet, "Name").(string)
		hidden := false
		if k, ok := o.prop(n.Path, iwdNet, "KnownNetwork").(dbus.ObjectPath); ok {
			hidden, _ = o.prop(k, iwdKnown, "Hidden").(bool)
		}
		bss, _ := o.prop(n.Path, iwdNet, "ExtendedServiceSet").([]dbus.ObjectPath)
		for _, b := range bss {
			if addr, _ := o.prop(b, iwdName+".BasicServiceSet", "Address").(string); addr != "" {
				out = append(out, location.AccessPoint{BSSID: addr, SSID: name, SignalDBm: int(n.Signal) / 100, Hidden: hidden})
			}
		}
	}
	return out, nil
}

// accessPoints of the simulated radio: one each, its address made from the
// network's name.
func (s *simWifi) accessPoints() ([]location.AccessPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return nil, nil
	}
	var out []location.AccessPoint
	for _, n := range s.nets {
		h := fnv.New64a()
		h.Write([]byte(n.SSID))
		v := h.Sum64()
		out = append(out, location.AccessPoint{BSSID: fmt.Sprintf("00:%02x:%02x:%02x:%02x:%02x", byte(v>>32), byte(v>>24), byte(v>>16), byte(v>>8), byte(v)),
			SSID: n.SSID, SignalDBm: -90 + 12*n.Signal})
	}
	return out, nil
}
