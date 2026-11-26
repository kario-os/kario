package shell

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The emulator's device on the personal hotspot, for the selftest
// (hotspot-client), or on the Wi-Fi network the phone is on (wifi-client,
// the old phone of the move over a shared Wi-Fi): a radio iwd leaves
// alone (the third mac80211_hwsim one,
// which hostapd's enterprise access point takes later) moves into a network
// namespace of its own, where wpa_supplicant (BSD-3-Clause, in the
// emulator's image only) joins the hotspot and busybox's udhcpc takes an
// address from iwd's DHCP server. A namespace of its own: in the phone's,
// the kernel would deliver the device's packets to the phone's addresses
// locally, past the radio and the NAT.

const wpaSupplicantBin = "/usr/sbin/wpa_supplicant"

// hotspotTestDir holds the device's files: wpa_supplicant's configuration
// and log, udhcpc's script and the lease it took.
const hotspotTestDir = "/run/phone-hotspot-test"

// udhcpcScript configures the lease in the device's namespace and writes it
// down; never the phone's resolver (the default script hands the DNS
// servers to resolvectl, which would apply them to the phone).
const udhcpcScript = `#!/bin/sh
exec >>"$0.log" 2>&1
echo "$1 ip=$ip mask=$mask router=$router dns=$dns"
case "$1" in
deconfig)
	ip -4 addr flush dev "$interface"
	ip link set dev "$interface" up
	;;
bound|renew)
	ip -4 addr flush dev "$interface"
	ip addr add dev "$interface" local "$ip/$mask"
	for r in $router; do ip route add default via "$r" dev "$interface"; break; done
	echo "$ip $router" > "$0.lease.tmp"
	echo "$dns" >> "$0.lease.tmp"
	mv "$0.lease.tmp" "$0.lease"
	;;
esac
exit 0
`

type hotspotClient struct {
	name, iface, dir string
	ssid             string // the network it joins
	reqs             chan func()
	quit, done       chan struct{}
}

// hotspotLease is what iwd's DHCP server gave the device.
type hotspotLease struct {
	addr, router string
	dns          []string
}

// canHotspotClient: wpa_supplicant and udhcpc are installed and a radio is
// free for the device.
func (r *iwdRadio) canHotspotClient() bool {
	if _, err := os.Stat(wpaSupplicantBin); err != nil {
		return false
	}
	if _, err := exec.LookPath("udhcpc"); err != nil {
		return false
	}
	_, err := r.freeRadio()
	return err == nil
}

// startHotspotClient moves the free radio into the device's namespace and
// starts wpa_supplicant there for ssid with pass; joined tells when it is
// associated.
func (r *iwdRadio) startHotspotClient(name, ssid, pass string) (*hotspotClient, error) {
	iface, err := r.freeRadio()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "phy80211", "index"))
	if err != nil {
		return nil, err
	}
	phy, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
	if err != nil {
		return nil, err
	}
	_ = os.RemoveAll(hotspotTestDir)
	if err := os.MkdirAll(hotspotTestDir, 0o700); err != nil {
		return nil, err
	}
	psk, err := wpaPSK(ssid, pass)
	if err != nil {
		return nil, err
	}
	h := &hotspotClient{name: name, iface: iface, dir: hotspotTestDir, ssid: ssid, reqs: make(chan func()), quit: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan error, 1)
	go ownThread(func() { h.run(uint32(phy), ready) })
	if err := <-ready; err != nil {
		return nil, err
	}
	conf := fmt.Sprintf("ctrl_interface=%s\nnetwork={\n\tssid=%x\n\tpsk=%x\n\tkey_mgmt=WPA-PSK\n\tproto=RSN\n\tpairwise=CCMP\n}\n", h.path("ctrl"), ssid, psk)
	if err := os.WriteFile(h.path("wpa.conf"), []byte(conf), 0o600); err != nil {
		h.end()
		return nil, err
	}
	if _, err := h.start(h.path("wpa.log"), wpaSupplicantBin, "-D", "nl80211", "-i", iface, "-c", h.path("wpa.conf")); err != nil {
		h.end()
		return nil, err
	}
	return h, nil
}

func (h *hotspotClient) path(name string) string { return filepath.Join(h.dir, name) }

// run is the device, on a thread of its own (ownThread): its namespace is
// the thread's, which ends with run; the processes started on it get
// SIGKILL then (Pdeathsig), and the radio goes back to the phone's namespace.
func (h *hotspotClient) run(phy uint32, ready chan<- error) {
	defer close(h.done)
	if err := syscall.Unshare(syscall.CLONE_NEWNET); err != nil {
		ready <- err
		return
	}
	tid := syscall.Gettid()
	moved := make(chan error, 1)
	go func() { moved <- moveWiphy(phy, tid) }()
	if err := <-moved; err != nil {
		ready <- fmt.Errorf("moving phy%d: %w", phy, err)
		return
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if _, err := net.InterfaceByName(h.iface); err == nil {
			break
		}
		if time.Now().After(end) {
			ready <- fmt.Errorf("%s did not come into the namespace", h.iface)
			return
		}
	}
	ready <- nil
	for {
		select {
		case f := <-h.reqs:
			f()
		case <-h.quit:
			return
		}
	}
}

// start runs a program in the device's namespace, its output into log;
// exited gets how it ended.
func (h *hotspotClient) start(log string, prog string, args ...string) (exited <-chan error, err error) {
	out, err := os.Create(log)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(prog, args...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	errc := make(chan error, 1)
	select {
	case h.reqs <- func() { errc <- cmd.Start() }:
	case <-h.done:
		out.Close()
		return nil, errors.New("the device's namespace is gone")
	}
	if err := <-errc; err != nil {
		out.Close()
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		out.Close()
	}()
	return done, nil
}

func (h *hotspotClient) logText(name string) string {
	raw, _ := os.ReadFile(h.path(name))
	return string(raw)
}

// wpaState is wpa_supplicant's state (COMPLETED once associated), asked
// on its control socket, which a filesystem path reaches from any
// namespace; its log is no use while running (stdio buffers it).
func (h *hotspotClient) wpaState() string {
	local := h.path("wpa-client")
	_ = os.Remove(local)
	conn, err := net.DialUnix("unixgram", &net.UnixAddr{Name: local, Net: "unixgram"}, &net.UnixAddr{Name: filepath.Join(h.path("ctrl"), h.iface), Net: "unixgram"})
	if err != nil {
		return ""
	}
	defer func() {
		conn.Close()
		_ = os.Remove(local)
	}()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("STATUS")); err != nil {
		return ""
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		if v, ok := strings.CutPrefix(line, "wpa_state="); ok {
			return v
		}
	}
	return ""
}

func (h *hotspotClient) joined() bool { return h.wpaState() == "COMPLETED" }

// dhcp starts udhcpc, which asks with the device's name as its host name
// (what the hotspot's device list shows); lease tells when it has an
// address.
func (h *hotspotClient) dhcp() error {
	script := h.path("udhcpc.sh")
	if err := os.WriteFile(script, []byte(udhcpcScript), 0o700); err != nil {
		return err
	}
	_, err := h.start(h.path("udhcpc.log"), "udhcpc", "-i", h.iface, "-f", "-q", "-n", "-t", "10", "-T", "2", "-s", script, "-x", "hostname:"+h.name)
	return err
}

func (h *hotspotClient) lease() (hotspotLease, bool) {
	return parseLease(h.logText("udhcpc.sh.lease"))
}

// parseLease reads what udhcpcScript writes: "<address> <routers>" (a
// lease may come without a router) and the DNS servers on the next line.
func parseLease(s string) (hotspotLease, bool) {
	lines := strings.Split(s, "\n")
	f := strings.Fields(lines[0])
	if len(f) < 1 || net.ParseIP(f[0]) == nil {
		return hotspotLease{}, false
	}
	l := hotspotLease{addr: f[0]}
	if len(f) > 1 {
		l.router = f[1]
	}
	if len(lines) > 1 {
		l.dns = strings.Fields(lines[1])
	}
	return l, true
}

// ping sends one echo request from the device; nil when the answer came.
func (h *hotspotClient) ping(addr string) error {
	exited, err := h.start(h.path("ping.log"), "ping", "-c", "1", "-W", "2", addr)
	if err != nil {
		return err
	}
	select {
	case err = <-exited:
	case <-time.After(5 * time.Second):
		err = errors.New("ping did not end")
	}
	if err != nil {
		return fmt.Errorf("no answer from %s: %v: %s", addr, err, strings.TrimSpace(h.logText("ping.log")))
	}
	return nil
}

// dial connects from the device's namespace: the socket is made on its
// thread and, once made, works from any.
func (h *hotspotClient) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	res := make(chan result, 1)
	select {
	case h.reqs <- func() {
		var d net.Dialer
		conn, err := d.DialContext(ctx, network, addr)
		res <- result{conn, err}
	}:
	case <-h.done:
		return nil, errors.New("the device's namespace is gone")
	}
	r := <-res
	return r.conn, r.err
}

// diag is what the device's side of the link looks like, for a failure.
func (h *hotspotClient) diag() string {
	var b strings.Builder
	for i, args := range [][]string{{"-4", "addr"}, {"route"}} {
		log := h.path(fmt.Sprintf("diag%d.log", i))
		exited, err := h.start(log, "ip", args...)
		if err == nil {
			select {
			case err = <-exited:
			case <-time.After(3 * time.Second):
				err = errors.New("did not end")
			}
		}
		fmt.Fprintf(&b, "%s ip %s (%v):\n%s", h.name, strings.Join(args, " "), err, h.logText(filepath.Base(log)))
	}
	return b.String()
}

// end stops the device: its thread ends, the kernel kills its processes,
// the namespace goes and the radio comes back.
func (h *hotspotClient) end() {
	select {
	case <-h.quit:
	default:
		close(h.quit)
	}
	<-h.done
}
