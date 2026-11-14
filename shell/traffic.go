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
// permissions › Network). The kernel counters are harvested (read and
// zeroed) into totals kept on disk, so a shell restart loses nothing and a
// reboot loses at most what came after the last harvest.

type traffic struct {
	fw     *netfw.Firewall
	path   string
	totals map[string]netfw.Usage
}

func (c *Core) setupTraffic() {
	if !c.Sandboxed() {
		return
	}
	fw, err := netfw.Open()
	if err != nil {
		c.log.Printf("traffic: off (%v)", err)
		return
	}
	t := &traffic{fw: fw, path: filepath.Join(c.cfg.StateDir, "shell", "traffic.json"), totals: map[string]netfw.Usage{}}
	if raw, err := os.ReadFile(t.path); err == nil {
		_ = json.Unmarshal(raw, &t.totals)
	}
	c.traffic = t
	c.harvestTraffic()
	if err := c.applyNetPolicy(); err != nil {
		c.log.Printf("traffic: off (%v)", err)
		c.traffic = nil
		return
	}
	c.log.Printf("traffic: per-app counters and Wi-Fi only in nftables table inet %s", netfw.Table)
}

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
	var ps []netfw.Policy
	for id, uid := range c.appUIDs() {
		// A host app's web views ("<app>/web") follow its network setting
		// and count with it.
		host := strings.TrimSuffix(id, "/web")
		if c.appByID(host) == nil {
			continue
		}
		ps = append(ps, netfw.Policy{UID: uid, WifiOnly: c.netState(host) == "wifi"})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].UID < ps[j].UID })
	return c.traffic.fw.Apply(ps)
}

func (c *Core) harvestTraffic() {
	t := c.traffic
	if t == nil {
		return
	}
	got, err := t.fw.Harvest()
	if err != nil {
		c.log.Printf("traffic: %v", err)
		return
	}
	if len(got) == 0 {
		return
	}
	byUID := map[uint32]string{}
	for id, uid := range c.appUIDs() {
		byUID[uid] = id
	}
	for uid, u := range got {
		id, ok := byUID[uid]
		id = strings.TrimSuffix(id, "/web")
		if !ok || u.Total() == 0 {
			continue
		}
		sum := t.totals[id]
		sum.Add(u)
		t.totals[id] = sum
	}
	raw, _ := json.Marshal(t.totals)
	_ = os.WriteFile(t.path, raw, 0o600)
}

func (c *Core) forgetTraffic(id string) {
	if c.traffic == nil {
		return
	}
	delete(c.traffic.totals, id)
	raw, _ := json.Marshal(c.traffic.totals)
	_ = os.WriteFile(c.traffic.path, raw, 0o600)
}

// trafficView is what Settings › Data usage shows; nil when there is no meter
// (no root: a developer machine).
func (c *Core) trafficView() []ui.TrafficEntry {
	if c.traffic == nil {
		return nil
	}
	c.harvestTraffic()
	out := []ui.TrafficEntry{}
	for id, u := range c.traffic.totals {
		name := id
		if a := c.appByID(id); a != nil {
			name = a.M.Name
		}
		out = append(out, ui.TrafficEntry{App: id, Name: name, WifiRx: u.WifiRx, WifiTx: u.WifiTx, MobileRx: u.MobileRx, MobileTx: u.MobileTx})
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].WifiRx+out[i].WifiTx+out[i].MobileRx+out[i].MobileTx, out[j].WifiRx+out[j].WifiTx+out[j].MobileRx+out[j].MobileTx
		if ti != tj {
			return ti > tj
		}
		return out[i].App < out[j].App
	})
	return out
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
