package shell

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"github.com/kario-os/kario/netfw"
	"github.com/kario-os/kario/ui"
)

// The personal hotspot (Settings › Personal hotspot; hotspot.state,
// hotspot.set): the Wi-Fi radio becomes a WPA2 access point (iwd's AP
// mode), whose DHCP server is iwd's own, and what its clients send leaves
// through the interface of the default route (mobile data on a phone),
// masqueraded as the phone (netfw.ApplyShare), with IPv4 forwarding on for
// just those two interfaces. One radio: the Wi-Fi station is off while the
// hotspot is on and comes back as the user had it. The switch, the name,
// the passphrase, the security, hiding, the band and the device limit are
// settings and hold across restarts; the passphrase goes to Settings,
// which shows it, and to iwd as its key, never into the log. Airplane mode
// turns the hotspot off. Settings lists the devices on it (hotspot_dhcp.go
// names them), what they used and their daily limit (hotspot_usage.go).
// iwd 3.12's access point is WPA2-Personal only (no SAE, so no WPA3 or its
// transition mode) and always shows its name; it takes 5 GHz from its
// profile's Channel (36 and up) and has no device limit, which the Shell
// keeps itself, sending one device too many away as iwd sends a station
// away. A developer machine gets a simulated hotspot beside its simulated
// Wi-Fi, which shares nothing and can do all of it.

// hotspotConfig is what the access point starts with; dns: the servers its
// DHCP hands out.
type hotspotConfig struct {
	ssid, pass string
	security   string
	hidden     bool
	band       string
	maxClients int
	dns        []string
}

// sameAP: the access point a and b describe is the same (the device limit
// and the DNS servers change nothing of it).
func (a hotspotConfig) sameAP(b hotspotConfig) bool {
	return a.ssid == b.ssid && a.pass == b.pass && a.security == b.security && a.hidden == b.hidden && a.band == b.band
}

// hotspotCaps is what an access point can do: the security it offers, its
// hiding its name, the bands the radio and the country allow it.
type hotspotCaps struct {
	securities []string
	hide       bool
	bands      []string
}

// hotspotRadio is a Wi-Fi radio that can be the hotspot's access point.
type hotspotRadio interface {
	wifiRadio
	// startHotspot turns the station off and the access point on, or
	// restarts one already up. iface is its network interface and subnet
	// the clients' ("" and nil for the simulated one).
	startHotspot(cfg hotspotConfig) (iface string, subnet *net.IPNet, err error)
	// stopHotspot ends the access point and brings the station back,
	// powered as wifiOn says; without an access point it does nothing.
	stopHotspot(wifiOn bool) error
	hotspotDevices() ([]ui.HotspotDevice, error)
	// hotspotCounters: the bytes the access point's interface received
	// and sent.
	hotspotCounters() (rx, tx uint64, err error)
	hotspotCaps() hotspotCaps
	// hotspotLimit sets the device limit of the access point up (0: none).
	hotspotLimit(n int)
}

// sharer is the firewall's part the hotspot uses (netfw.Firewall).
type sharer interface {
	ApplyShare(name, in, out string, src *net.IPNet) error
}

// hotspotShare names the hotspot's sharing in the firewall.
const hotspotShare = "hotspot"

// hotspotMark is the text status bar's sign of the hotspot on.
const hotspotMark = "📡"

// Where the kernel's routes and forwarding switches are, and where
// systemd-resolved lists the DNS servers it uses.
var (
	routesFile    = "/proc/net/route"
	forwardingDir = "/proc/sys/net/ipv4/conf"
	resolvFiles   = []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"}
)

// hotspotWant is what the settings ask of the radio. resume: the Shell
// starts with the hotspot on (the station's switch from before holds).
type hotspotWant struct {
	on, resume bool
	cfg        hotspotConfig
	wifiOn     bool
}

// hotspotAP is how the radio and the firewall are (hotspotShell.mu).
type hotspotAP struct {
	up              bool
	cfg             hotspotConfig
	iface, upstream string
	subnet          *net.IPNet
	// shared: the firewall and forwarding are set for iface and upstream;
	// fwd: the interfaces whose forwarding the hotspot turned on, with the
	// value each had before.
	shared bool
	fwd    map[string]string
}

type hotspotShell struct {
	// The loop's: the access point is up (the status bar), on iface,
	// sharing upstream; watching: a goroutine follows the default route.
	on              bool
	iface, upstream string
	watching        bool
	// want is what the settings asked for last; whichever apply holds mu
	// brings the radio and the firewall to it, off the loop, with a
	// firewall connection of its own (fw).
	want atomic.Pointer[hotspotWant]
	mu   sync.Mutex
	ap   hotspotAP
	fw   sharer
	// The loop's, as the radio last said: what it can do, the devices on
	// it; and the usage counted (hotspot_usage.go).
	caps    hotspotCaps
	devices []ui.HotspotDevice
	usage   hotspotUsage
	// The selftest's (hotspot_headless.go): the emulator's device on the
	// hotspot, and the forwarding switches the last hotspot-share saw.
	client  *hotspotClient
	seenFwd map[string]string
}

func (c *Core) hotspotRadio() hotspotRadio {
	hs, _ := c.wifi.(hotspotRadio)
	return hs
}

// hotspotAtBoot opens the hotspot's firewall connection, clears what an
// earlier Shell may have left there and in the radio, and brings the
// hotspot up again if it was on.
func (c *Core) hotspotAtBoot() {
	if c.traffic != nil {
		if fw, err := netfw.Open(); err == nil {
			c.hotspot.fw = fw
		}
	}
	hs := c.hotspotRadio()
	if hs == nil {
		return
	}
	c.loadHotspotUsage()
	go func() {
		caps := hs.hotspotCaps()
		c.Post(func() { c.hotspot.caps = caps })
	}()
	c.switchAtBoot()
	s := &c.settings
	if s.Hotspot && s.Airplane {
		s.Hotspot = false
		c.saveSettings()
	}
	if s.Hotspot {
		c.hotspotDefaults()
		c.syncHotspot(true, nil)
		return
	}
	fw, wifiOn := c.hotspot.fw, !s.HotspotWifiOff
	go func() {
		c.hotspot.mu.Lock()
		defer c.hotspot.mu.Unlock()
		if fw != nil {
			_ = fw.ApplyShare(hotspotShare, "", "", nil)
		}
		_ = hs.stopHotspot(wifiOn)
	}()
}

// hotspotDefaults makes up the name and the passphrase the first time.
func (c *Core) hotspotDefaults() {
	s := &c.settings
	if s.HotspotSSID != "" && s.HotspotPass != "" {
		return
	}
	if s.HotspotSSID == "" {
		s.HotspotSSID = defaultHotspotSSID(Brand)
	}
	if s.HotspotPass == "" {
		s.HotspotPass = randomPassphrase()
	}
	c.saveSettings()
}

// defaultHotspotSSID is the brand and four random hex digits, as phones
// name theirs ("Kario-3F7A").
func defaultHotspotSSID(brand string) string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	suffix := fmt.Sprintf("-%04X", binary.BigEndian.Uint16(b[:]))
	name := strings.ReplaceAll(strings.TrimSpace(brand), " ", "-")
	for len(name)+len(suffix) > 32 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	return name + suffix
}

// passAlphabet leaves out the characters read one for another (0 O 1 l I).
const passAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randomPassphrase() string {
	b := make([]byte, 12)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(passAlphabet))))
		b[i] = passAlphabet[n.Int64()]
	}
	return string(b)
}

// checkHotspotSSID: 1 to 32 bytes (802.11), no control characters, and no
// "/" (iwd names the access point's profile after it).
func checkHotspotSSID(ssid string) error {
	switch {
	case strings.TrimSpace(ssid) == "":
		return errors.New(tr.T("Enter a network name"))
	case len(ssid) > 32:
		return errors.New(tr.T("The network name is at most 32 bytes (a Chinese character takes 3)"))
	case strings.ContainsFunc(ssid, func(r rune) bool { return r == '/' || unicode.IsControl(r) || r == utf8.RuneError }):
		return errors.New(tr.T("The network name cannot contain “/” or line breaks"))
	}
	return nil
}

// checkHotspotPass: WPA2's passphrase, 8 to 63 printable ASCII characters.
func checkHotspotPass(pass string) error {
	if strings.ContainsFunc(pass, func(r rune) bool { return r < ' ' || r > '~' }) {
		return errors.New(tr.T("The password takes letters, digits and symbols only"))
	}
	if len(pass) < 8 || len(pass) > 63 {
		return errors.New(tr.T("The passphrase is 8 to 63 characters"))
	}
	return nil
}

// wpaPSK is the key WPA2 derives from a passphrase and the SSID (IEEE
// 802.11i, PBKDF2-SHA1): what iwd and a client are given instead of it.
func wpaPSK(ssid, pass string) ([]byte, error) {
	return pbkdf2.Key(sha1.New, pass, []byte(ssid), 4096, 32)
}

// hotspotSettings is the access point the settings describe.
func (c *Core) hotspotSettings() hotspotConfig {
	s := c.settings
	return hotspotConfig{ssid: s.HotspotSSID, pass: s.HotspotPass, security: firstNonEmpty(s.HotspotSecurity, ui.HotspotWPA2),
		hidden: s.HotspotHidden, band: firstNonEmpty(s.HotspotBand, ui.HotspotBand24), maxClients: s.HotspotMax}
}

func (c *Core) hotspotState() ui.HotspotState {
	c.hotspotDefaults()
	s, h, cfg := c.settings, &c.hotspot, c.hotspotSettings()
	st := ui.HotspotState{Enabled: s.Hotspot, Started: h.on, SSID: s.HotspotSSID, Passphrase: s.HotspotPass, Upstream: h.upstream,
		Security: cfg.security, Hidden: cfg.hidden, Band: cfg.band, MaxClients: cfg.maxClients,
		Securities: firstNonNil(h.caps.securities, []string{ui.HotspotWPA2}), CanHide: h.caps.hide, Bands: firstNonNil(h.caps.bands, []string{ui.HotspotBand24}),
		Devices: []ui.HotspotDevice{}, Usage: c.hotspotUsageView(), DailyLimit: s.HotspotDayLimit}
	if c.hotspotRadio() != nil {
		st.Backend = radioName(c.wifi)
	}
	if h.on {
		st.Devices = append(st.Devices, h.devices...)
	}
	st.Clients = len(st.Devices)
	return st
}

func firstNonNil(a, b []string) []string {
	if a != nil {
		return a
	}
	return b
}

// answerHotspot replies with the state, what the access point can do and,
// while it is up, the devices on it and their usage, which the radio is
// asked for off the loop.
func (c *Core) answerHotspot(reply func(any, error)) {
	hs := c.hotspotRadio()
	if hs == nil {
		reply(c.hotspotState(), nil)
		return
	}
	started := c.hotspot.on
	go func() {
		caps := hs.hotspotCaps()
		var devs []ui.HotspotDevice
		var rx, tx uint64
		cerr := errors.New("off")
		if started {
			devs, _ = hs.hotspotDevices()
			rx, tx, cerr = hs.hotspotCounters()
		}
		c.Post(func() {
			h := &c.hotspot
			h.caps = caps
			if started && h.on {
				h.devices = devs
				if cerr == nil {
					c.hotspotCount(rx, tx, false)
				}
			}
			reply(c.hotspotState(), nil)
		})
	}()
}

func (b *broker) dispatchHotspot(m ui.Msg, reply func(any, error)) {
	c := b.c
	if m.Cap == ui.CapHotspotState {
		c.answerHotspot(reply)
		return
	}
	var args ui.HotspotArgs
	if err := json.Unmarshal(m.Args, &args); err != nil || args == (ui.HotspotArgs{}) {
		reply(nil, errors.New("hotspot.set takes enabled, ssid, passphrase, security, hidden, band, max_clients or daily_limit"))
		return
	}
	c.setHotspot(args, func(err error) {
		if err != nil {
			reply(nil, err)
			return
		}
		c.answerHotspot(reply)
	})
}

// setHotspot is hotspot.set: the settings change at once, the radio and
// the firewall follow off the loop; done runs on the loop after.
func (c *Core) setHotspot(args ui.HotspotArgs, done func(error)) {
	c.hotspotDefaults()
	s := &c.settings
	if args.SSID != "" {
		if err := checkHotspotSSID(args.SSID); err != nil {
			done(err)
			return
		}
	}
	if args.Passphrase != "" {
		if err := checkHotspotPass(args.Passphrase); err != nil {
			done(err)
			return
		}
	}
	caps := c.hotspot.caps
	secs, bands := firstNonNil(caps.securities, []string{ui.HotspotWPA2}), firstNonNil(caps.bands, []string{ui.HotspotBand24})
	switch {
	case args.Security != "" && !slices.Contains(secs, args.Security):
		done(errors.New(tr.T("This phone's hotspot cannot use that security: it offers %s", strings.ToUpper(strings.Join(secs, ", ")))))
		return
	case args.Hidden != nil && *args.Hidden && !caps.hide:
		done(errors.New(tr.T("This phone's hotspot cannot hide its name")))
		return
	case args.Band != "" && !slices.Contains(bands, args.Band):
		done(errors.New(tr.T("The hotspot cannot use %s GHz here: the radio or the country does not allow it", args.Band)))
		return
	case args.MaxClients != nil && (*args.MaxClients < 0 || *args.MaxClients > ui.HotspotMaxClients):
		done(errors.New(tr.T("The device limit is 1 to %d, or none", ui.HotspotMaxClients)))
		return
	}
	on := s.Hotspot
	if args.Enabled != nil {
		on = *args.Enabled
	}
	switch {
	case on && c.hotspotRadio() == nil:
		done(errors.New(tr.T("This device has no Wi-Fi")))
		return
	case on && s.Airplane:
		done(errors.New(tr.T("Turn airplane mode off first")))
		return
	}
	if args.SSID != "" && args.SSID != s.HotspotSSID {
		s.HotspotSSID = args.SSID
		c.log.Printf("hotspot: network name %q", args.SSID)
	}
	if args.Passphrase != "" && args.Passphrase != s.HotspotPass {
		s.HotspotPass = args.Passphrase
		c.log.Printf("hotspot: new passphrase")
	}
	if args.Security != "" {
		s.HotspotSecurity = args.Security
	}
	if args.Hidden != nil {
		s.HotspotHidden = *args.Hidden
	}
	if args.Band != "" {
		s.HotspotBand = args.Band
	}
	if args.MaxClients != nil {
		s.HotspotMax = *args.MaxClients
	}
	if args.DailyLimit != nil && *args.DailyLimit != s.HotspotDayLimit {
		s.HotspotDayLimit = *args.DailyLimit
		c.log.Printf("hotspot: daily limit %d bytes (0: none)", s.HotspotDayLimit)
	}
	if args.Security != "" || args.Hidden != nil || args.Band != "" || args.MaxClients != nil {
		cfg := c.hotspotSettings()
		c.log.Printf("hotspot: %s, hidden %v, %s GHz, devices %d (0: no limit)", cfg.security, cfg.hidden, cfg.band, cfg.maxClients)
	}
	if on != s.Hotspot {
		s.Hotspot = on
		c.log.Printf("hotspot %v", on)
	}
	c.saveSettings()
	c.syncHotspot(false, done)
}

// wifiOverHotspot is wifi.set turning Wi-Fi on while the hotspot is on:
// one radio, so the hotspot goes off and brings the station back on.
func (c *Core) wifiOverHotspot(reply func(any, error)) {
	c.settings.HotspotWifiOff = false
	off := false
	c.setHotspot(ui.HotspotArgs{Enabled: &off}, func(err error) { reply(map[string]bool{"ok": true}, err) })
}

// hotspotOffFor turns the switch off for airplane mode; the returned
// function (nil when it was off) takes the radio down, off the loop,
// before airplane mode looks at the station.
func (c *Core) hotspotOffFor() func() {
	if !c.settings.Hotspot {
		return nil
	}
	c.settings.Hotspot = false
	c.saveSettings()
	c.log.Printf("hotspot false (airplane mode)")
	hs, fw := c.hotspotRadio(), c.hotspot.fw
	c.storeHotspotWant(false)
	return func() { _ = c.applyHotspot(hs, fw) }
}

func (c *Core) storeHotspotWant(resume bool) *hotspotWant {
	s := c.settings
	w := &hotspotWant{on: s.Hotspot, resume: resume, cfg: c.hotspotSettings(), wifiOn: !s.HotspotWifiOff}
	c.hotspot.want.Store(w)
	return w
}

// syncHotspot hands the settings to the radio, off the loop; done (if any)
// gets the result on the loop.
func (c *Core) syncHotspot(resume bool, done func(error)) {
	c.storeHotspotWant(resume)
	hs, fw := c.hotspotRadio(), c.hotspot.fw
	if hs == nil {
		if done != nil {
			done(nil)
		}
		return
	}
	go func() {
		err := c.applyHotspot(hs, fw)
		if done != nil {
			c.Post(func() { done(err) })
		}
	}()
}

// applyHotspot brings the radio and the firewall to what was asked last
// and tells the loop how they are; from any goroutine.
func (c *Core) applyHotspot(hs hotspotRadio, fw sharer) error {
	h := &c.hotspot
	h.mu.Lock()
	defer h.mu.Unlock()
	wp := h.want.Load()
	if wp == nil || hs == nil {
		return nil
	}
	w, ap := *wp, &h.ap
	var err error
	record, wifiWasOn := false, false
	// The counters before the access point went off or was made again,
	// and after it came on, for the usage.
	var before, after counterSample
	wasUp := ap.up
	switch {
	case !w.on && ap.up:
		c.unshareHotspot(fw)
		before = sampleCounters(hs)
		err = hs.stopHotspot(w.wifiOn)
		*ap = hotspotAP{}
		c.log.Printf("hotspot: off (Wi-Fi %v)", w.wifiOn)
	case w.on && (!ap.up || !w.cfg.sameAP(ap.cfg)):
		if !ap.up && !w.resume {
			st, e := hs.state()
			record, wifiWasOn = true, e == nil && st.Enabled
		}
		c.unshareHotspot(fw)
		if ap.up {
			before = sampleCounters(hs)
		}
		var iface string
		var subnet *net.IPNet
		cfg := w.cfg
		cfg.dns = upstreamDNS()
		iface, subnet, err = hs.startHotspot(cfg)
		if err != nil {
			*ap = hotspotAP{}
			back := w.wifiOn
			if record {
				back = wifiWasOn
			}
			_ = hs.stopHotspot(back)
			break
		}
		*ap = hotspotAP{up: true, cfg: w.cfg, iface: iface, subnet: subnet}
		c.log.Printf("hotspot: on as %q on %s (clients %v, %s, %s GHz)", w.cfg.ssid, firstNonEmpty(iface, "the simulated radio"), subnet, w.cfg.security, w.cfg.band)
		after = sampleCounters(hs)
		c.shareHotspot(fw)
	case w.on && ap.up:
		if w.cfg.maxClients != ap.cfg.maxClients {
			hs.hotspotLimit(w.cfg.maxClients)
			ap.cfg.maxClients = w.cfg.maxClients
		}
		c.shareHotspot(fw)
	}
	if err != nil {
		c.log.Printf("hotspot: %v", err)
	}
	on, iface, upstream := ap.up, ap.iface, ap.upstream
	failed := err != nil && w.on
	c.Post(func() {
		if before.ok {
			c.hotspotCount(before.rx, before.tx, !on)
		}
		switch {
		case on && !wasUp:
			c.hotspotSession(after.rx, after.tx, after.ok)
		case on && after.ok:
			c.hotspotRebase(after.rx, after.tx)
		case !on:
			h.devices = nil
		}
		changed := h.on != on || h.iface != iface
		h.on, h.iface, h.upstream = on, iface, upstream
		if record {
			c.settings.HotspotWifiOff = !wifiWasOn
			c.saveSettings()
		}
		if failed && h.want.Load() == wp {
			c.settings.Hotspot = false
			c.saveSettings()
			c.storeHotspotWant(false)
		}
		if changed {
			c.vpnFirewall()
		}
		c.watchHotspot()
	})
	return err
}

// shareHotspot points the NAT and forwarding at the default route's
// interface when it changed (mu); without one the clients reach only the
// phone.
func (c *Core) shareHotspot(fw sharer) {
	ap := &c.hotspot.ap
	if ap.iface == "" || fw == nil {
		return
	}
	raw, _ := os.ReadFile(routesFile)
	up, _ := defaultRoute(string(raw), ap.iface)
	if ap.shared && up == ap.upstream {
		return
	}
	c.unshareHotspot(fw)
	if up == "" || ap.subnet == nil {
		c.log.Printf("hotspot: nothing to share (default route %q, clients %v)", up, ap.subnet)
		return
	}
	if err := fw.ApplyShare(hotspotShare, ap.iface, up, ap.subnet); err != nil {
		c.log.Printf("hotspot: firewall: %v", err)
		return
	}
	ap.upstream, ap.shared, ap.fwd = up, true, map[string]string{}
	for _, i := range []string{ap.iface, up} {
		old, err := setForwarding(i, "1")
		if err != nil {
			c.log.Printf("hotspot: forwarding on %s: %v", i, err)
			continue
		}
		ap.fwd[i] = old
	}
	c.log.Printf("hotspot: %s shares %s with %v", ap.iface, up, ap.subnet)
}

// unshareHotspot lifts the NAT and puts forwarding back as it was (mu).
func (c *Core) unshareHotspot(fw sharer) {
	ap := &c.hotspot.ap
	if fw != nil && ap.shared {
		if err := fw.ApplyShare(hotspotShare, "", "", nil); err != nil {
			c.log.Printf("hotspot: firewall: %v", err)
		}
	}
	for i, old := range ap.fwd {
		if _, err := setForwarding(i, old); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.log.Printf("hotspot: forwarding on %s: %v", i, err)
		}
	}
	ap.upstream, ap.shared, ap.fwd = "", false, nil
}

// hotspotWatch is how often the hotspot looks for a new default route (a
// data link that came up, or a new one) and at its counters.
const hotspotWatch = 5 * time.Second

// watchHotspot follows the default route and the usage while the access
// point is up.
func (c *Core) watchHotspot() {
	h := &c.hotspot
	hs := c.hotspotRadio()
	if !h.on || h.watching || hs == nil {
		return
	}
	h.watching = true
	go func() {
		for {
			time.Sleep(hotspotWatch)
			s := sampleCounters(hs)
			stop := make(chan bool, 1)
			c.Post(func() {
				if !h.on {
					h.watching = false
					stop <- true
					return
				}
				if s.ok {
					c.hotspotCount(s.rx, s.tx, false)
				}
				if h.iface != "" {
					c.syncHotspot(false, nil)
				}
				stop <- false
			})
			if <-stop {
				return
			}
		}
	}()
}

// setForwarding sets an interface's IPv4 forwarding ("0" or "1") and
// returns what it was.
func setForwarding(iface, v string) (string, error) {
	p := filepath.Join(forwardingDir, iface, "forwarding")
	old, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(old)), os.WriteFile(p, []byte(v), 0o644)
}

func forwarding(iface string) string {
	raw, _ := os.ReadFile(filepath.Join(forwardingDir, iface, "forwarding"))
	return strings.TrimSpace(string(raw))
}

// defaultRoute is the interface and gateway of the IPv4 default route
// with the lowest metric in routes (/proc/net/route), leaving out the
// interface skip (the hotspot's own).
func defaultRoute(routes, skip string) (string, net.IP) {
	best, metric := "", 0
	var gw net.IP
	for _, line := range strings.Split(routes, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" || f[0] == skip {
			continue
		}
		flags, err1 := strconv.ParseUint(f[3], 16, 16)
		m, err2 := strconv.Atoi(f[6])
		g, err3 := strconv.ParseUint(f[2], 16, 32)
		if err1 != nil || err2 != nil || err3 != nil || flags&1 == 0 {
			continue
		}
		if best == "" || m < metric {
			best, metric = f[0], m
			// The kernel prints the address as it lies in memory.
			gw = binary.NativeEndian.AppendUint32(nil, uint32(g))
		}
	}
	return best, gw
}

// upstreamDNS is the IPv4 DNS servers the phone uses, which the hotspot's
// DHCP hands out (systemd-resolved's own list, else resolv.conf's).
func upstreamDNS() []string {
	for _, f := range resolvFiles {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if dns := resolvers(string(raw)); len(dns) > 0 {
			return dns
		}
	}
	return nil
}

// resolvers is resolv.conf's IPv4 name servers, without the local ones (a
// stub resolver the clients cannot reach).
func resolvers(conf string) []string {
	var out []string
	for _, line := range strings.Split(conf, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		if ip := net.ParseIP(f[1]).To4(); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
			out = append(out, ip.String())
		}
	}
	return out
}

func ipv4Net(ifname string) *net.IPNet {
	ifc, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return &net.IPNet{IP: n.IP.Mask(n.Mask).To4(), Mask: n.Mask}
		}
	}
	return nil
}

// The simulated hotspot: devices join it from the selftest
// (hotspot-client) with the passphrase, as a real access point would let
// them: one that knows no WPA3 not onto a WPA3-only one, one without
// 5 GHz not onto 5 GHz, none past the device limit (refused at the
// association, as hostapd's max_num_sta does); a scan shows it unless it
// is hidden. Its counters move as the selftest says (hotspot-traffic).

type simAP struct {
	cfg     hotspotConfig
	clients []ui.HotspotDevice
	rx, tx  uint64
	joined  int
}

// simDevice is what a simulated device can do.
type simDevice struct{ wpa2Only, only24 bool }

func (s *simWifi) startHotspot(cfg hotspotConfig) (string, *net.IPNet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = false
	for _, n := range s.nets {
		n.Connected = false
	}
	s.ap = &simAP{cfg: cfg}
	return "", nil, nil
}

func (s *simWifi) stopHotspot(wifiOn bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ap != nil {
		s.ap, s.enabled = nil, wifiOn
	}
	return nil
}

func (s *simWifi) hotspotDevices() ([]ui.HotspotDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ap == nil {
		return nil, nil
	}
	return slices.Clone(s.ap.clients), nil
}

func (s *simWifi) hotspotCounters() (uint64, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ap == nil {
		return 0, 0, errors.New("the hotspot is off")
	}
	return s.ap.rx, s.ap.tx, nil
}

func (s *simWifi) hotspotCaps() hotspotCaps {
	return hotspotCaps{securities: []string{ui.HotspotWPA2, ui.HotspotWPA2WPA3, ui.HotspotWPA3}, hide: true, bands: []string{ui.HotspotBand24, ui.HotspotBand5}}
}

func (s *simWifi) hotspotLimit(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ap != nil {
		s.ap.cfg.maxClients = n
	}
}

// joinHotspot: a device named name joins with pass (selftest).
func (s *simWifi) joinHotspot(name, pass string) error { return s.joinAs(name, pass, simDevice{}) }

func (s *simWifi) joinAs(name, pass string, d simDevice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ap := s.ap
	switch {
	case ap == nil:
		return errors.New("the hotspot is off")
	case d.only24 && ap.cfg.band == ui.HotspotBand5:
		return errors.New("the hotspot is on 5 GHz, which the device does not have")
	case d.wpa2Only && ap.cfg.security == ui.HotspotWPA3:
		return errors.New("the hotspot is WPA3 only, which the device does not know")
	case pass != ap.cfg.pass:
		return errors.New("wrong passphrase")
	case ap.cfg.maxClients > 0 && len(ap.clients) >= ap.cfg.maxClients:
		return fmt.Errorf("refused: the hotspot has %d devices, its limit", len(ap.clients))
	}
	ap.joined++
	ap.clients = append(ap.clients, ui.HotspotDevice{MAC: fmt.Sprintf("02:4b:41:52:48:%02x", ap.joined), Name: name,
		Address: fmt.Sprintf("192.168.43.%d", 1+ap.joined), Since: time.Now().Unix()})
	return nil
}

// leaveHotspot: the device named name leaves ("": all of them).
func (s *simWifi) leaveHotspot(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ap != nil {
		s.ap.clients = slices.DeleteFunc(s.ap.clients, func(d ui.HotspotDevice) bool { return name == "" || d.Name == name })
	}
}

// hotspotSeen: a device nearby that scans sees the hotspot.
func (s *simWifi) hotspotSeen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ap != nil && !s.ap.cfg.hidden
}

// hotspotTraffic: the devices download down and upload up bytes.
func (s *simWifi) hotspotTraffic(down, up uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ap == nil || len(s.ap.clients) == 0 {
		return errors.New("no device on the hotspot")
	}
	s.ap.tx += down
	s.ap.rx += up
	return nil
}

// The hotspot on iwd: the phone's Wi-Fi device in AP mode. Its profile
// (iwd.ap(5)) carries the key and the DNS servers, which Start cannot;
// iwd reads it at the start, and it goes at once, so the passphrase is
// stored nowhere but in the settings.

const (
	// iwdAPDir is where iwd reads access point profiles.
	iwdAPDir  = iwdDir + "/ap"
	iwdAPDiag = iwdName + ".AccessPointDiagnostic"
)

// apProfile gives the clients the phone as their router: iwd's DHCP
// server (iwd 3.12, ell 0.83) sends a router only when the profile names
// a Gateway, whatever iwd.ap(5) says, and without one a client gets an
// address but no way out.
// channel (0: iwd's own, 6) is the [General] Channel: 36 and up is 5 GHz.
func apProfile(psk []byte, ap *net.IPNet, dns []string, channel int) string {
	p := "[Security]\nPreSharedKey=" + hex.EncodeToString(psk) + "\n"
	if channel != 0 {
		p = "[General]\nChannel=" + strconv.Itoa(channel) + "\n\n" + p
	}
	if ap == nil {
		return p
	}
	p += "\n[IPv4]\nAddress=" + ap.IP.String() + "\nGateway=" + ap.IP.String() + "\nNetmask=" + net.IP(ap.Mask).String() + "\n"
	if len(dns) > 0 {
		p += "DNSList=" + strings.Join(dns, ",") + "\n"
	}
	return p
}

// apAddress is the phone's address on the hotspot: .1 of the first /24,
// from Android's 192.168.43.0 on, that no network the phone is on overlaps.
func apAddress(used []*net.IPNet) *net.IPNet {
	mask := net.CIDRMask(24, 32)
	for _, b := range [][2]byte{{192, 168}, {172, 20}} {
		for i := 43; i < 64; i++ {
			n := &net.IPNet{IP: net.IPv4(b[0], b[1], byte(i), 0).To4(), Mask: mask}
			if !slices.ContainsFunc(used, func(u *net.IPNet) bool { return u.Contains(n.IP) || n.Contains(u.IP) }) {
				return &net.IPNet{IP: net.IPv4(b[0], b[1], byte(i), 1).To4(), Mask: mask}
			}
		}
	}
	return nil
}

// localNets are the IPv4 networks on the phone's interfaces other than iface.
func localNets(iface string) []*net.IPNet {
	ifcs, _ := net.Interfaces()
	var out []*net.IPNet
	for _, ifc := range ifcs {
		if ifc.Name == iface {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				out = append(out, &net.IPNet{IP: n.IP.Mask(n.Mask).To4(), Mask: n.Mask})
			}
		}
	}
	return out
}

func (r *iwdRadio) startHotspot(cfg hotspotConfig) (string, *net.IPNet, error) {
	if cfg.security != ui.HotspotWPA2 || cfg.hidden {
		return "", nil, errors.New(tr.T("This phone's hotspot (iwd) offers WPA2 only and cannot hide its name"))
	}
	o, err := r.load()
	if err != nil {
		return "", nil, err
	}
	dev := o.phone()
	if dev == "" {
		return "", nil, errors.New(tr.T("No Wi-Fi device"))
	}
	name, _ := o.prop(dev, iwdDevice, "Name").(string)
	channel := 0
	if cfg.band == ui.HotspotBand5 {
		if freqs, err := ifaceFreqs(name); err == nil {
			channel = apChannel5(freqs)
		}
		if channel == 0 {
			return "", nil, errors.New(tr.T("The hotspot cannot use %s GHz here: the radio or the country does not allow it", cfg.band))
		}
	}
	r.endAPWatch()
	obj := r.bus.Object(iwdName, dev)
	set := func(prop string, v any) error {
		return busCall(obj, "org.freedesktop.DBus.Properties.Set", iwdDevice, prop, dbus.MakeVariant(v)).Err
	}
	if o.prop(dev, iwdDevice, "Mode") == "ap" && o.prop(dev, iwdAP, "Started") == true {
		_ = busCall(obj, iwdAP+".Stop").Err
	}
	if o.prop(dev, iwdDevice, "Powered") != true {
		if err := set("Powered", true); err != nil {
			return "", nil, err
		}
	}
	if err := set("Mode", "ap"); err != nil {
		return "", nil, err
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if o, err = r.load(); err == nil && o[dev][iwdAP] != nil {
			break
		}
		if time.Now().After(end) {
			_ = set("Mode", "station")
			return "", nil, fmt.Errorf(tr.T("The hotspot did not start: %w"), errors.New("no access point interface"))
		}
	}
	psk, err := wpaPSK(cfg.ssid, cfg.pass)
	if err != nil {
		return "", nil, err
	}
	ap := apAddress(localNets(name))
	if ap == nil && r.logf != nil {
		r.logf("hotspot: every private subnet is in use; the clients get no router")
	}
	profile := filepath.Join(iwdAPDir, cfg.ssid+".ap")
	err = os.MkdirAll(iwdAPDir, 0o700)
	if err == nil {
		err = os.WriteFile(profile, []byte(apProfile(psk, ap, cfg.dns, channel)), 0o600)
	}
	if err == nil {
		err = busCall(obj, iwdAP+".StartProfile", cfg.ssid).Err
	}
	_ = os.Remove(profile)
	if err != nil {
		if r.logf != nil {
			r.logf("hotspot: no profile (%v); the clients get no router or DNS servers", err)
		}
		// Start cannot name a channel: 5 GHz needs the profile.
		err2 := errors.New("5 GHz needs the profile")
		if channel == 0 {
			err2 = busCall(obj, iwdAP+".Start", cfg.ssid, cfg.pass).Err
		}
		if err2 != nil {
			_ = set("Mode", "station")
			return "", nil, fmt.Errorf(tr.T("The hotspot did not start: %w"), errors.Join(err, err2))
		}
	}
	r.startAPWatch(name, cfg.maxClients)
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if n := ipv4Net(name); n != nil {
			return name, n, nil
		}
	}
	return name, nil, nil
}

func (r *iwdRadio) stopHotspot(wifiOn bool) error {
	r.endAPWatch()
	o, err := r.load()
	if err != nil {
		return err
	}
	dev := o.phone()
	if dev == "" || o.prop(dev, iwdDevice, "Mode") != "ap" {
		return nil
	}
	obj := r.bus.Object(iwdName, dev)
	if o.prop(dev, iwdAP, "Started") == true {
		_ = busCall(obj, iwdAP+".Stop").Err
	}
	if err := busCall(obj, "org.freedesktop.DBus.Properties.Set", iwdDevice, "Mode", dbus.MakeVariant("station")).Err; err != nil {
		return err
	}
	return busCall(obj, "org.freedesktop.DBus.Properties.Set", iwdDevice, "Powered", dbus.MakeVariant(wifiOn)).Err
}

// hotspotDevices are the stations the kernel has on the access point (or,
// failing that, iwd's diagnostics), named from their DHCP requests.
func (r *iwdRadio) hotspotDevices() ([]ui.HotspotDevice, error) {
	o, err := r.load()
	if err != nil {
		return nil, err
	}
	dev := o.phone()
	name, _ := o.prop(dev, iwdDevice, "Name").(string)
	sts, err := apStations(name)
	if err != nil {
		var diag []map[string]dbus.Variant
		if derr := busCall(r.bus.Object(iwdName, dev), iwdAPDiag+".GetDiagnostics").Store(&diag); derr != nil {
			return nil, errors.Join(err, derr)
		}
		sts = nil
		for _, d := range diag {
			mac, _ := d["Address"].Value().(string)
			var secs uint32
			if v, ok := d["ConnectedTime"]; ok {
				secs, _ = v.Value().(uint32)
			}
			sts = append(sts, apStation{mac: strings.ToLower(mac), connected: time.Duration(secs) * time.Second})
		}
	}
	r.mu.Lock()
	w := r.apw
	r.mu.Unlock()
	now := time.Now()
	out := []ui.HotspotDevice{}
	for _, st := range sts {
		d := ui.HotspotDevice{MAC: st.mac, Since: now.Add(-st.connected).Unix()}
		if w != nil {
			l := w.leases.get(st.mac)
			d.Name, d.Address = l.name, l.addr
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since < out[j].Since })
	return out, nil
}

func (r *iwdRadio) hotspotCounters() (uint64, uint64, error) {
	r.mu.Lock()
	w := r.apw
	r.mu.Unlock()
	if w == nil {
		return 0, 0, errors.New("the hotspot is off")
	}
	return ifaceCounters(w.iface)
}

// ifaceCounters are an interface's bytes received and sent.
func ifaceCounters(iface string) (uint64, uint64, error) {
	var out [2]uint64
	for i, name := range []string{"rx_bytes", "tx_bytes"} {
		raw, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "statistics", name))
		if err != nil {
			return 0, 0, err
		}
		if out[i], err = strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64); err != nil {
			return 0, 0, err
		}
	}
	return out[0], out[1], nil
}

// hotspotCaps: iwd's access point is WPA2 only and never hidden; the
// bands are what the phone's radio and the regulatory domain allow.
func (r *iwdRadio) hotspotCaps() hotspotCaps {
	caps := hotspotCaps{securities: []string{ui.HotspotWPA2}, bands: []string{ui.HotspotBand24}}
	o, err := r.load()
	if err != nil {
		return caps
	}
	name, _ := o.prop(o.phone(), iwdDevice, "Name").(string)
	if freqs, err := ifaceFreqs(name); err == nil {
		if b := apBands(freqs); len(b) > 0 {
			caps.bands = b
		}
	}
	return caps
}

// ifaceFreqs are the channels of the radio under a Wi-Fi interface.
func ifaceFreqs(iface string) ([]wifiFreq, error) {
	raw, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "phy80211", "index"))
	if err != nil {
		return nil, err
	}
	phy, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
	if err != nil {
		return nil, err
	}
	return wiphyFreqs(uint32(phy))
}

func (r *iwdRadio) hotspotLimit(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.apw != nil {
		r.apw.limit.Store(int32(n))
	}
}

// iwdAPWatch is the Shell's own watch on iwd's access point: the device
// limit (a station that associates past it is sent away at once, on
// nl80211's event, or at the next look without events) and the devices'
// DHCP (their names).
type iwdAPWatch struct {
	iface  string
	limit  atomic.Int32
	leases dhcpLeases
	stop   chan struct{}
	done   sync.WaitGroup
}

// apFresh is how recently a station must have associated to be the one
// sent away: the devices already on stay when the limit is lowered.
const apFresh = 3 * time.Second

func (r *iwdRadio) startAPWatch(iface string, limit int) {
	logf := r.logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	w := &iwdAPWatch{iface: iface, stop: make(chan struct{})}
	w.limit.Store(int32(limit))
	r.mu.Lock()
	r.apw = w
	r.mu.Unlock()
	w.done.Add(2)
	go func() {
		defer w.done.Done()
		if err := snoopDHCP(iface, &w.leases, w.stop); err != nil {
			logf("hotspot: no device names: %v", err)
		}
	}()
	go func() {
		defer w.done.Done()
		ifc, err := net.InterfaceByName(iface)
		if err == nil {
			err = newStations(ifc.Index, w.stop, func(string) { w.enforce(logf) })
		}
		if err == nil {
			return
		}
		logf("hotspot: no station events (%v); the device limit is looked at every second", err)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				w.enforce(logf)
			}
		}
	}()
}

func (r *iwdRadio) endAPWatch() {
	r.mu.Lock()
	w := r.apw
	r.apw = nil
	r.mu.Unlock()
	if w != nil {
		close(w.stop)
		w.done.Wait()
	}
}

func (w *iwdAPWatch) enforce(logf func(string, ...any)) {
	n := int(w.limit.Load())
	if n <= 0 {
		return
	}
	sts, err := apStations(w.iface)
	if err != nil {
		return
	}
	for _, mac := range overLimit(sts, n, apFresh) {
		if err := delStation(w.iface, mac, reasonAPFull); err != nil {
			logf("hotspot: sending %s away: %v", mac, err)
			continue
		}
		logf("hotspot: sent %s away: %d devices allowed", mac, n)
	}
}

// overLimit are the stations to send away so that no more than limit
// stay: the newest, of those that associated within fresh.
func overLimit(sts []apStation, limit int, fresh time.Duration) []string {
	if limit <= 0 || len(sts) <= limit {
		return nil
	}
	sorted := slices.Clone(sts)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].connected < sorted[j].connected })
	var out []string
	for _, st := range sorted {
		if len(sts)-len(out) <= limit || st.connected > fresh {
			break
		}
		out = append(out, st.mac)
	}
	return out
}
