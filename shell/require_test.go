package shell

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// require-previous goes on from a case of its own part only: the
// background jobs case (the second of its part) goes on from the weather
// case, which the chroot on Nura skipped (no location receiver the script
// can move), and it went on from the last case of the part before, failing
// at its first step. With only cases skipped for the environment before it
// in its part it is skipped too, saying which and why; cases skipped by
// --only still do not count.
func TestRequirePreviousStaysInItsPart(t *testing.T) {
	if !alone(t) {
		return
	}
	script := "== one\nsleep 10\n#part next\n== two\nrequire-env KARIO_TEST_NEVER_SET\nsleep 10\n" +
		"== three\nrequire-previous\nsleep 10\n== four\nsleep 10\n== five\nrequire-previous\nsleep 10\n"
	var out strings.Builder
	r := &Runner{C: snapCore(t), Out: &out}
	r.Run(script)
	if got, want := caseLine(r.Cases), "one PASS, two SKIP, three SKIP, four PASS, five PASS"; got != want {
		t.Fatalf("cases %s, want %s\n%s", got, want, out.String())
	}
	if m := r.Cases[2].Message; m != previousFailed+": two (needs env KARIO_TEST_NEVER_SET)" {
		t.Errorf("three skipped for %q", m)
	}
	r.Only = regexp.MustCompile("three")
	r.Run(script)
	if got := caseLine(r.Cases); !strings.Contains(got, "three PASS") {
		t.Errorf("--only three: %s\n%s", got, out.String())
	}
}

// The kernel features case holds the image to its own kernel's: another
// system's kernel (the chroot kit runs Nura's, which has no Landlock) is
// reported, with what it lacks, and the case skipped; the image's own
// still fails for what it lacks.
func TestRequireOwnKernel(t *testing.T) {
	if !alone(t) {
		return
	}
	mods := t.TempDir()
	_ = os.MkdirAll(filepath.Join(mods, "6.18.0-yocto"), 0o755)
	if f := foreignKernel(mods, "6.18.0-yocto"); f != "" {
		t.Errorf("the image's kernel: %q", f)
	}
	f := foreignKernel(mods, "7.1.0-rc1-sdm845")
	if !strings.Contains(f, filepath.Join(mods, "7.1.0-rc1-sdm845")) {
		t.Errorf("another system's kernel: %q", f)
	}
	c := snapCore(t)
	c.kernel.Release, c.kernel.Foreign = "7.1.0-rc1-sdm845", f
	c.kernel.Missing = map[string]string{"landlock": "no Landlock (Linux 5.13)"}
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	r.Run("== kernel\nrequire-own-kernel\nkernel missing none\n")
	if got := caseLine(r.Cases); got != "kernel SKIP" {
		t.Fatalf("another system's kernel: %s\n%s", got, out.String())
	}
	if m := r.Cases[0].Message; !strings.Contains(m, "7.1.0-rc1-sdm845") || !strings.Contains(m, "it lacks landlock (no Landlock (Linux 5.13))") {
		t.Errorf("skipped for %q", m)
	}
	if rep := c.CompatReport(r.Cases, true); rep.Sandbox.Foreign != f {
		t.Errorf("the report's sandbox: %+v", rep.Sandbox)
	}
	c.kernel.Foreign = ""
	r.Run("== kernel\nrequire-own-kernel\nkernel missing none\n")
	if got := caseLine(r.Cases); got != "kernel FAIL" {
		t.Fatalf("the image's own kernel: %s\n%s", got, out.String())
	}
}

// With the firewall, a mobile data block holds on the links that count as
// mobile: the chroot's probe packets left by Nura's Wi-Fi (the data usage
// counted the store's 384 bytes as Wi-Fi), so a block let them through, as
// it should. The cases sending them say so instead of failing.
func TestRequireMobileRoute(t *testing.T) {
	c := snapCore(t)
	if off := c.mobileRouteOff(); off != "" {
		t.Errorf("without the firewall (the Shell's policy is checked): %q", off)
	}
	old := probeRoute
	t.Cleanup(func() { probeRoute = old })
	c.traffic = &traffic{}
	t.Cleanup(func() { c.traffic = nil })
	for dev, want := range map[string]string{"eth0": "", "rmnet_data0": "", "wlan0": "leave by wlan0, which counts as Wi-Fi"} {
		probeRoute = func() (string, error) { return dev, nil }
		if off := c.mobileRouteOff(); want == "" && off != "" || want != "" && !strings.Contains(off, want) {
			t.Errorf("by %s: %q, want %q", dev, off, want)
		}
	}
	probeRoute = func() (string, error) { return "", errors.New("network is unreachable") }
	if off := c.mobileRouteOff(); !strings.Contains(off, "no route") {
		t.Errorf("no route: %q", off)
	}
}

// The VPN's device cases send packets outside the tunnel (an app it leaves
// out; one the kill switch refuses): on the 6T, with no route for them,
// the kernel refused both at once (network unreachable) and the cases
// failed. Without a route they are skipped, saying why; elsewhere the
// policies are checked and no route is needed.
func TestRequireDirectRoute(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	old, kind, loaded := directRoute, c.vpn.kind, c.radios.data.loaded
	t.Cleanup(func() { directRoute, c.vpn.kind, c.traffic, c.radios.data.loaded = old, kind, nil, loaded })
	// The stand-in firewall has no counters for data usage to read.
	c.radios.data.loaded = false
	asked := 0
	directRoute = func() (string, error) { asked++; return "", errors.New("netlink receive: network is unreachable") }
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	c.vpn.kind = "sim"
	r.Run("== direct\nrequire-direct-route\n")
	if got := caseLine(r.Cases); got != "direct PASS" || asked != 0 {
		t.Fatalf("the simulated VPN: %s, the route asked %d times\n%s", got, asked, out.String())
	}
	c.vpn.kind, c.traffic = "networkd", &traffic{}
	r.Run("== direct\nrequire-direct-route\nvpn-route com.phone.browser direct 100\n")
	if got, m := caseLine(r.Cases), r.Cases[0].Message; got != "direct SKIP" || !strings.Contains(m, "needs direct-route: no route for the packets to 192.0.2.1") || !strings.Contains(m, "network is unreachable") {
		t.Fatalf("no route outside the tunnel: %s %q\n%s", got, m, out.String())
	}
	directRoute = func() (string, error) { return "qmapmux0.0", nil }
	r.Run("== direct\nrequire-direct-route\n")
	if got := caseLine(r.Cases); got != "direct PASS" {
		t.Fatalf("by the mobile data link: %s\n%s", got, out.String())
	}
}

// The VPN case runs with either backend and says why where there is none
// (the chroot: no systemd-networkd on Nura's bus).
func TestRequireVPNKinds(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	kind := c.vpn.kind
	t.Cleanup(func() { c.vpn.kind, c.vpn.none = kind, "" })
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	c.vpn.kind = "sim"
	r.Run("== both\nrequire-vpn sim networkd\n== device\nrequire-vpn networkd\n")
	if got := caseLine(r.Cases); got != "both PASS, device SKIP" {
		t.Fatalf("sim: %s\n%s", got, out.String())
	}
	if m := r.Cases[0].Message; m != "" {
		t.Errorf("a case that passed has the message %q (the report's cases said \"needs previous \")", m)
	}
	c.vpn.kind, c.vpn.none = "", "no systemd-networkd on the system bus"
	r.Run("== both\nrequire-vpn sim networkd\n")
	if m := r.Cases[0].Message; r.Cases[0].Status != "SKIP" || !strings.Contains(m, "none (no systemd-networkd on the system bus)") {
		t.Errorf("no VPN: %s %q", r.Cases[0].Status, m)
	}
}

// wifi-ap puts the access point up on the simulated Wi-Fi or a second
// radio of iwd's; without Wi-Fi the case says so (the chroot: the Shell runs
// none there, Nura's NetworkManager has the radio).
func TestRequireWifiAP(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	w := c.wifi
	t.Cleanup(func() { c.wifi = w })
	c.wifi = newSimWifi()
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	r.Run("== ap\nrequire-wifi-ap\n")
	if got := caseLine(r.Cases); got != "ap PASS" {
		t.Fatalf("the simulated Wi-Fi: %s\n%s", got, out.String())
	}
	c.wifi = nil
	r.Run("== ap\nrequire-wifi-ap\nwifi-ap Kario-Test kario-5678\n")
	if got, m := caseLine(r.Cases), r.Cases[0].Message; got != "ap SKIP" || !strings.Contains(m, "no Wi-Fi") {
		t.Fatalf("no Wi-Fi: %s %q\n%s", got, m, out.String())
	}
}

// device-wipe asks the running systemd about the image's
// phone-wipe.service. In a root that is not the one systemd runs in,
// systemctl prints nothing and exits 0, and the step said only `"" <nil>`;
// now it says what systemctl said, and require-systemd skips the case there.
func TestDeviceWipeSaysWhatSystemdSaid(t *testing.T) {
	if !alone(t) {
		return
	}
	script := filepath.Join(t.TempDir(), "data-wipe")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldScript, oldCtl := wipeScript, systemctl
	t.Cleanup(func() { wipeScript, systemctl = oldScript, oldCtl })
	wipeScript = script
	answer := func(out, said string, err error) {
		systemctl = func(...string) ([]byte, []byte, error) { return []byte(out), []byte(said), err }
	}
	chroot := "Running in chroot, ignoring command 'show'"
	for _, c := range []struct {
		out, said string
		err       error
		want      string
	}{
		{"", chroot, nil, "systemctl answered nothing: " + chroot},
		{"", "", nil, "systemctl answered nothing: it printed nothing"},
		{"", "System has not been booted with systemd as init system (PID 1). Can't operate.", errors.New("exit status 1"), "systemctl: exit status 1 System has not been booted"},
		{"Before=\nUnitFileState=\n", "", nil, "systemd has no such unit"},
		{"Before=sysinit.target\nUnitFileState=disabled\n", "", nil, "phone-wipe.service is disabled, not enabled"},
		{"Before=basic.target\nUnitFileState=enabled\n", "", nil, "does not come before sysinit.target (Before=basic.target)"},
	} {
		answer(c.out, c.said, c.err)
		if err := deviceWipe(io.Discard); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("systemctl %q %q %v: %v, want %q", c.out, c.said, c.err, err, c.want)
		}
	}
	answer("Before=sysinit.target shutdown.target\nUnitFileState=enabled\n", "", nil)
	if err := deviceWipe(io.Discard); err == nil || !strings.Contains(err.Error(), "data-wipe: exit status 3") {
		t.Errorf("past the unit: %v, want the script's failure", err)
	}
	var out strings.Builder
	r := &Runner{C: snapCore(t), Out: &out}
	answer("", chroot, nil)
	r.Run("== wipe\nrequire-systemd\ndevice-wipe\n")
	if got, m := caseLine(r.Cases), r.Cases[0].Message; got != "wipe SKIP" || !strings.Contains(m, chroot) {
		t.Fatalf("another root's systemd: %s %q\n%s", got, m, out.String())
	}
	answer("Version=258\n", "", nil)
	r.Run("== wipe\nrequire-systemd\n")
	if got := caseLine(r.Cases); got != "wipe PASS" {
		t.Fatalf("this system's systemd: %s\n%s", got, out.String())
	}
}

// A capability only the skipped cases call (the location and the apps'
// settings cases, which need a location receiver the script can move) is
// counted skipped, with why, not never called; one whose case ran and did
// not call it still is never called.
func TestCompatReportCountsSkippedCapabilities(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	r.Run("== skipped\ncovers widget.set\nrequire-env KARIO_TEST_NEVER_SET\n== ran\ncovers location.stop\nsleep 10\n")
	if !slices.Equal(r.Cases[0].Covers, []string{"widget.set"}) || r.Cases[1].Covers != nil {
		t.Fatalf("covers %v, %v", r.Cases[0].Covers, r.Cases[1].Covers)
	}
	rep := c.CompatReport(r.Cases, true)
	if why := rep.Skipped["widget.set"]; !strings.Contains(why, "skipped: needs env KARIO_TEST_NEVER_SET") {
		t.Errorf("widget.set skipped for %q", why)
	}
	if slices.Contains(rep.Uncovered, "widget.set") || !slices.Contains(rep.Uncovered, "location.stop") {
		t.Errorf("never called: %v", rep.Uncovered)
	}
	if rep.Result != "FAIL" {
		t.Errorf("result %s with capabilities never called", rep.Result)
	}
}
