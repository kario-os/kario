package shell

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/kario-os/kario/ui"
)

// iwd (net.connman.iwd): the station device's networks, strongest first; a
// new protected network's passphrase goes into iwd's own store as the
// provisioning file it reads (/var/lib/iwd/<name>.psk) before the
// connection, so no agent is needed; forgetting removes it there too.

const (
	iwdName   = "net.connman.iwd"
	iwdDevice = iwdName + ".Device"
	iwdSta    = iwdName + ".Station"
	iwdNet    = iwdName + ".Network"
	iwdKnown  = iwdName + ".KnownNetwork"
	iwdAP     = iwdName + ".AccessPoint"
	iwdDir    = "/var/lib/iwd"
)

type iwdRadio struct {
	bus     *dbus.Conn
	mu      sync.Mutex
	scanned time.Time
	// testCA: the CA an enterprise network's server must chain to instead
	// of the system's, while the selftest's access point is up
	// (radio_hostapd.go); testNet: that access point's SSID, which hands
	// out no addresses.
	testCA, testNet string
}

// iwdObjs is one look at iwd's objects; each call takes its own.
type iwdObjs map[dbus.ObjectPath]map[string]map[string]dbus.Variant

func (r *iwdRadio) load() (iwdObjs, error) {
	var o iwdObjs
	if err := busCall(r.bus.Object(iwdName, "/"), "org.freedesktop.DBus.ObjectManager.GetManagedObjects").Store(&o); err != nil {
		return nil, fmt.Errorf("iwd: %w", err)
	}
	return o, nil
}

func (o iwdObjs) prop(path dbus.ObjectPath, iface, name string) any {
	if v, ok := o[path][iface][name]; ok {
		return v.Value()
	}
	return nil
}

// devices are the Wi-Fi devices by name; station leaves out the one made an
// access point.
func (o iwdObjs) devices(station bool) []dbus.ObjectPath {
	var devs []dbus.ObjectPath
	for p, ifs := range o {
		if _, ok := ifs[iwdDevice]; ok && (!station || o.prop(p, iwdDevice, "Mode") != "ap") {
			devs = append(devs, p)
		}
	}
	sort.Slice(devs, func(i, j int) bool {
		return fmt.Sprint(o.prop(devs[i], iwdDevice, "Name")) < fmt.Sprint(o.prop(devs[j], iwdDevice, "Name"))
	})
	return devs
}

func (o iwdObjs) device() dbus.ObjectPath {
	if d := o.devices(true); len(d) > 0 {
		return d[0]
	}
	return ""
}

func (o iwdObjs) network(dev dbus.ObjectPath, ssid string) dbus.ObjectPath {
	for p, ifs := range o {
		if _, ok := ifs[iwdNet]; ok && o.prop(p, iwdNet, "Name") == ssid && o.prop(p, iwdNet, "Device") == dev {
			return p
		}
	}
	return ""
}

func (r *iwdRadio) state() (ui.WifiState, error) {
	st := ui.WifiState{Backend: "iwd", State: "off", Networks: []ui.WifiNet{}}
	o, err := r.load()
	if err != nil {
		return st, err
	}
	dev := o.device()
	if dev == "" {
		return st, nil
	}
	st.Enabled = o.prop(dev, iwdDevice, "Powered") == true
	if _, ok := o[dev][iwdSta]; !st.Enabled || !ok {
		return st, nil
	}
	st.State, _ = o.prop(dev, iwdSta, "State").(string)
	if st.State == "roaming" {
		st.State = "connected"
	}
	sta := r.bus.Object(iwdName, dev)
	r.mu.Lock()
	scan := time.Since(r.scanned) > 10*time.Second
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
		return st, err
	}
	for _, n := range ordered {
		name, _ := o.prop(n.Path, iwdNet, "Name").(string)
		sec, _ := o.prop(n.Path, iwdNet, "Type").(string)
		_, known := o.prop(n.Path, iwdNet, "KnownNetwork").(dbus.ObjectPath)
		on, _ := o.prop(n.Path, iwdNet, "Connected").(bool)
		st.Networks = append(st.Networks, ui.WifiNet{SSID: name, Security: sec, Signal: barsOf(int(n.Signal) / 100), Known: known, Connected: on})
	}
	if st.State == "connected" {
		if name, _ := o.prop(dev, iwdDevice, "Name").(string); name != "" {
			st.Address = ipv4Of(name)
		}
	}
	return st, nil
}

func ipv4Of(ifname string) string {
	ifc, err := net.InterfaceByName(ifname)
	if err != nil {
		return ""
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return ""
}

func (r *iwdRadio) setEnabled(on bool) error {
	o, err := r.load()
	if err != nil {
		return err
	}
	dev := o.device()
	if dev == "" {
		return errors.New(tr.T("No Wi-Fi device"))
	}
	return busCall(r.bus.Object(iwdName, dev), "org.freedesktop.DBus.Properties.Set", iwdDevice, "Powered", dbus.MakeVariant(on)).Err
}

// iwdFile is the name iwd keeps a network under: the SSID itself when it is
// plain, else "=" and its bytes in hex.
var plainSSID = regexp.MustCompile(`^[A-Za-z0-9_ -]+$`)

func iwdFile(ssid, kind string) string {
	name := ssid
	if !plainSSID.MatchString(ssid) {
		name = "=" + hex.EncodeToString([]byte(ssid))
	}
	return filepath.Join(iwdDir, name+"."+kind)
}

// systemCAs: the certificates an enterprise network's server must chain to.
const systemCAs = "/etc/ssl/certs/ca-certificates.crt"

// eapConfig is iwd's provisioning file for an 802.1X network
// (iwd.network(5)): the server's certificate checked against the system's
// CAs and the domain, the user's name and password only inside the tunnel
// (outside it, anonymous at the user's realm).
func eapConfig(c ui.WifiArgs, ca string) string {
	m, inner := "PEAP", "MSCHAPV2"
	switch c.EAP {
	case "ttls":
		m, inner = "TTLS", "Tunneled-MSCHAPv2"
	case "ttls-pap":
		m, inner = "TTLS", "Tunneled-PAP"
	}
	outer := "anonymous"
	if _, realm, ok := strings.Cut(c.Identity, "@"); ok {
		outer += "@" + realm
	}
	return fmt.Sprintf("[Security]\nEAP-Method=%[1]s\nEAP-Identity=%[2]s\nEAP-%[1]s-CACert=%[3]s\nEAP-%[1]s-ServerDomainMask=%[4]s\n"+
		"EAP-%[1]s-Phase2-Method=%[5]s\nEAP-%[1]s-Phase2-Identity=%[6]s\nEAP-%[1]s-Phase2-Password=%[7]s\n",
		m, outer, ca, c.Domain, inner, c.Identity, c.Passphrase)
}

// provision writes a network's file for iwd and waits until iwd knows it
// (it reads the directory through inotify).
func (r *iwdRadio) provision(p dbus.ObjectPath, file, content string) error {
	if err := os.MkdirAll(iwdDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		return err
	}
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if o, err := r.load(); err == nil {
			if _, ok := o.prop(p, iwdNet, "KnownNetwork").(dbus.ObjectPath); ok {
				break
			}
		}
	}
	return nil
}

func (r *iwdRadio) connect(ssid string, cred ui.WifiArgs) error {
	pass := cred.Passphrase
	o, err := r.load()
	if err != nil {
		return err
	}
	p := o.network(o.device(), ssid)
	if p == "" {
		return errors.New(tr.T("Out of range"))
	}
	sec, _ := o.prop(p, iwdNet, "Type").(string)
	_, known := o.prop(p, iwdNet, "KnownNetwork").(dbus.ObjectPath)
	provisioned := ""
	switch {
	case sec == "8021x" && (!known || cred.Identity != ""):
		if err := checkEnterprise(cred); err != nil {
			return err
		}
		provisioned = iwdFile(ssid, "8021x")
		r.mu.Lock()
		ca, conf := firstNonEmpty(r.testCA, systemCAs), ""
		if ssid == r.testNet {
			conf = testNetIPv4
		}
		r.mu.Unlock()
		if err := r.provision(p, provisioned, eapConfig(cred, ca)+conf); err != nil {
			return err
		}
	case sec == "psk" && !known && pass == "":
		return errors.New(ui.ErrNeedPassphrase)
	case sec == "psk" && pass != "":
		if strings.ContainsAny(pass, "\n\r") || len(pass) < 8 || len(pass) > 63 {
			return errors.New(tr.T("The passphrase is 8 to 63 characters"))
		}
		provisioned = iwdFile(ssid, "psk")
		if err := r.provision(p, provisioned, "[Security]\nPassphrase="+pass+"\n"); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.bus.Object(iwdName, p).CallWithContext(ctx, iwdNet+".Connect", 0).Err; err != nil {
		var de dbus.Error
		if errors.As(err, &de) && de.Name == iwdName+".Failed" && provisioned != "" {
			_ = os.Remove(provisioned)
			if sec == "8021x" {
				return errors.New(tr.T("Connection failed: wrong user name or password, or the server's certificate is not for %s", cred.Domain))
			}
			return errors.New(tr.T("Connection failed: wrong passphrase"))
		}
		return fmt.Errorf(tr.T("Connection failed: %w"), err)
	}
	return nil
}

func (r *iwdRadio) forget(ssid string) error {
	o, err := r.load()
	if err != nil {
		return err
	}
	for p, ifs := range o {
		if _, ok := ifs[iwdKnown]; ok && o.prop(p, iwdKnown, "Name") == ssid {
			return busCall(r.bus.Object(iwdName, p), iwdKnown+".Forget").Err
		}
	}
	return nil
}

// startAP makes the last Wi-Fi device an access point (the selftest on the
// emulator's simulated radios).
func (r *iwdRadio) startAP(ssid, pass string) error {
	var devs []dbus.ObjectPath
	for end := time.Now().Add(10 * time.Second); len(devs) < 2; time.Sleep(200 * time.Millisecond) {
		o, err := r.load()
		if err != nil {
			return err
		}
		if devs = o.devices(false); len(devs) < 2 && time.Now().After(end) {
			return fmt.Errorf("%d Wi-Fi devices, need two", len(devs))
		}
	}
	ap := r.bus.Object(iwdName, devs[len(devs)-1])
	if err := busCall(ap, "org.freedesktop.DBus.Properties.Set", iwdDevice, "Mode", dbus.MakeVariant("ap")).Err; err != nil {
		return err
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		err := busCall(ap, iwdAP+".Start", ssid, pass).Err
		if err == nil || time.Now().After(end) {
			return err
		}
	}
}
