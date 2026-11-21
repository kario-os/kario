package shell

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/netfw"
	"github.com/kario-os/kario/ui"
)

// Per-app traffic and "Wi-Fi only" (Settings › Data usage, Settings › App
// permissions › Network), and the same rule for an app whose mobile data
// is blocked (background data, its own limit; netpolicy.go). The kernel
// counters are harvested (read and zeroed) into the days data usage keeps
// (datausage.go), so a shell restart loses nothing and a reboot loses at
// most what came after the last harvest.

type traffic struct {
	fw *netfw.Firewall
}

func (c *Core) setupTraffic() {
	c.dataLedger()
	if !c.Sandboxed() {
		return
	}
	fw, err := netfw.Open()
	if err != nil {
		c.log.Printf("traffic: off (%v); %s", err, trafficOff)
		return
	}
	c.traffic = &traffic{fw: fw}
	c.harvestTraffic()
	if err := c.applyNetPolicy(); err != nil {
		c.log.Printf("traffic: off (%v); %s", err, trafficOff)
		c.traffic = nil
		return
	}
	c.log.Printf("traffic: per-app counters, Wi-Fi only and the apps' mobile data blocks in nftables table inet %s", netfw.Table)
}

// trafficOff is what a phone without the per-app firewall goes without.
const trafficOff = "data usage by network only; Wi-Fi only, background data and the apps' own limits are not enforced on their connections"

func (c *Core) appUIDs() map[string]uint32 {
	uids := map[string]uint32{}
	if raw, err := os.ReadFile(filepath.Join(c.cfg.StateDir, "shell", "uids.json")); err == nil {
		_ = json.Unmarshal(raw, &uids)
	}
	return uids
}

// netState is the network permission: granted, wifi (Wi-Fi only) or denied.
func (c *Core) netState(id string) string {
	return firstNonEmpty(c.broker.perms[id][ui.PermNet], "granted")
}

func (c *Core) applyNetPolicy() error {
	if c.traffic == nil {
		return nil
	}
	if err := c.applyAppRules(); err != nil {
		return err
	}
	return c.applyVPNRules()
}

// applyAppRules replaces the per-app rules alone; the VPN's chains stay.
func (c *Core) applyAppRules() error {
	if c.traffic == nil {
		return nil
	}
	return c.traffic.fw.Apply(c.netPolicies(), c.vpnTunnels()...)
}

// netPolicies are the firewall's rules for the apps' uids, by uid.
func (c *Core) netPolicies() []netfw.Policy {
	var ps []netfw.Policy
	for id, uid := range c.appUIDs() {
		// A host app's web views ("<app>/web") follow its network setting
		// and its blocks, and count with it.
		a := c.appByID(strings.TrimSuffix(id, "/web"))
		if a == nil {
			continue
		}
		ps = append(ps, netfw.Policy{UID: uid, WifiOnly: c.mobileBlock(a) != ""})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].UID < ps[j].UID })
	return ps
}

// harvestApps reads and zeroes the per-app counters: what each app (its web
// views with it) used since the last harvest; nil without the firewall.
func (c *Core) harvestApps() map[string]netfw.Usage {
	t := c.traffic
	if t == nil {
		return nil
	}
	got, err := t.fw.Harvest()
	if err != nil {
		c.log.Printf("traffic: %v", err)
		return nil
	}
	byUID := map[uint32]string{}
	for id, uid := range c.appUIDs() {
		byUID[uid] = id
	}
	out := map[string]netfw.Usage{}
	for uid, u := range got {
		id, ok := byUID[uid]
		if !ok || u.Total() == 0 {
			continue
		}
		id = strings.TrimSuffix(id, "/web")
		sum := out[id]
		sum.Add(u)
		out[id] = sum
	}
	return out
}

// harvestTraffic counts what the counters hold now and writes it down
// (before an app is removed, at shutdown), once setupTraffic read the days.
func (c *Core) harvestTraffic() {
	if !c.radios.data.loaded {
		return
	}
	c.countData()
	c.saveData(true)
}

// sendAs sends three 100-byte datagrams to TEST-NET-1 (RFC 5737) the way
// the app would: a socket is stamped with the creating thread's fsuid,
// which is what the firewall matches. Selftest only.
func (c *Core) sendAs(id string) error {
	uid, err := c.appUID(id)
	if err != nil {
		return err
	}
	if err := c.applyNetPolicy(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		// The thread is not unlocked if fsuid cannot be put back: it then
		// ends with this goroutine instead of running other code.
		runtime.LockOSThread()
		prev, _ := unix.SetfsuidRetUid(int(uid))
		conn, err := net.Dial("udp4", "192.0.2.1:9")
		if back, _ := unix.SetfsuidRetUid(prev); back != int(uid) {
			done <- os.ErrPermission
			return
		}
		runtime.UnlockOSThread()
		if err == nil {
			defer conn.Close()
			for i := 0; i < 3 && err == nil; i++ {
				_, err = conn.Write(make([]byte, 100))
			}
		}
		done <- err
	}()
	return <-done
}
