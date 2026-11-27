package shell

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/kario-os/kario/hal/usbgadget"
	"github.com/kario-os/kario/netfw"
)

// usbFirewall: the USB link reaches nothing until the computer is trusted,
// then file sharing (and DHCP for its address), and in developer mode the
// debug shell (SSH) too. The rules are the firewall's (netfw.Link), so a
// service that listens on every interface is still closed to the cable.
// When developer mode goes off the debug shell takes no new connection,
// but the ones open still come in (netfw.Link's Drain): the Shell ends the
// developer link's requests, and the session's end and the computer's
// answer to it have to get through, or the computer's ssh waits for TCP
// to give up (some 15 minutes). Port 22 closes for them too once nothing
// came in on them for the drain's idle time, or at its limit; nothing
// resets them.
func (c *Core) usbFirewall(open bool) {
	l := netfw.Link{Name: usbgadget.Interface(c.usb.cfg)}
	if open {
		l.UDP = []uint16{67}
		l.TCP = []uint16{80}
		switch {
		case c.settings.DeveloperMode:
			l.TCP = append(l.TCP, 22)
		case slices.Contains(c.usb.link.TCP, 22) || len(c.usb.link.Drain) > 0:
			l.Drain = []uint16{22}
		}
	}
	c.applyLink(l)
}

const (
	// usbDrainIdle, usbDrainMax: the debug shell's open connections finish
	// until none sent anything for usbDrainIdle, at most for usbDrainMax
	// (drainLimits); usbDrainTick: how often the drain's count is read.
	usbDrainIdle = 3 * time.Second
	usbDrainMax  = 30 * time.Second
	usbDrainTick = 500 * time.Millisecond
)

// linkFirewall is the firewall's part the cable uses (netfw.Firewall).
type linkFirewall interface {
	ApplyLink(netfw.Link) error
	LinkDrained(name string) (uint64, error)
}

// linkFW is where the link's rules go: nil without nftables and for the
// developer machine's simulated cable (c.usb.fw: the tests' firewall).
func (c *Core) linkFW() linkFirewall {
	switch {
	case c.usb.fw != nil:
		return c.usb.fw
	case c.traffic == nil || c.usb.sim:
		return nil
	}
	return c.traffic.fw
}

// drainLimits are the drain's idle time and limit: c.usb.drainIdle and
// drainMax where set (a setting could), else the defaults.
func (c *Core) drainLimits() (idle, limit time.Duration) {
	return cmp.Or(c.usb.drainIdle, usbDrainIdle), cmp.Or(c.usb.drainMax, usbDrainMax)
}

// applyLink puts the link's rules in force; a kernel that refuses the
// drain's conntrack match gets the port closed at once, as it was before
// the drain.
func (c *Core) applyLink(l netfw.Link) {
	if len(l.Drain) > 0 && len(c.usb.link.Drain) == 0 {
		now := time.Now()
		c.usb.drain = linkDrain{gen: c.usb.drain.gen + 1, since: now, moved: now}
		c.drainLater(c.usb.drain.gen)
	}
	c.usb.link = l
	fw := c.linkFW()
	if fw == nil {
		return
	}
	err := fw.ApplyLink(l)
	if err != nil && len(l.Drain) > 0 {
		c.log.Printf("usb: firewall: %v; tcp %v closed at once", err, l.Drain)
		l.Drain = nil
		c.usb.link = l
		err = fw.ApplyLink(l)
	}
	if err != nil {
		c.log.Printf("usb: firewall: %v", err)
		return
	}
	if len(l.Drain) > 0 {
		c.log.Printf("usb: the link reaches udp %v, tcp %v; tcp %v only for the connections open", l.UDP, l.TCP, l.Drain)
		return
	}
	c.log.Printf("usb: the link reaches udp %v, tcp %v", l.UDP, l.TCP)
}

func (c *Core) drainLater(gen int) {
	time.AfterFunc(usbDrainTick, func() { c.Post(func() { c.drainUSB(gen) }) })
}

// drainUSB closes the draining ports once their connections are done.
func (c *Core) drainUSB(gen int) {
	if gen != c.usb.drain.gen || len(c.usb.link.Drain) == 0 {
		return
	}
	var n uint64
	var err error
	if fw := c.linkFW(); fw != nil {
		n, err = fw.LinkDrained(c.usb.link.Name)
	}
	idle, limit := c.drainLimits()
	why := c.usb.drain.done(time.Now(), n, idle, limit)
	if err != nil {
		why = fmt.Sprintf("could not be followed (%v)", err)
	}
	if why == "" {
		c.drainLater(gen)
		return
	}
	l := c.usb.link
	c.log.Printf("usb: the connections open on tcp %v %s; closed to them too", l.Drain, why)
	l.Drain = nil
	c.applyLink(l)
}

// linkDrain follows the connections a closing port lets finish: when it
// began, the drain rule's count and when that last moved.
type linkDrain struct {
	gen          int
	since, moved time.Time
	packets      uint64
}

// done says why the drain is over, "" while it is not: nothing came in
// for idle, or limit has passed since it began.
func (d *linkDrain) done(now time.Time, packets uint64, idle, limit time.Duration) string {
	if packets != d.packets {
		d.packets, d.moved = packets, now
	}
	switch {
	case now.Sub(d.moved) >= idle:
		return fmt.Sprintf("were quiet for %s", idle)
	case now.Sub(d.since) >= limit:
		return fmt.Sprintf("were still busy after %s", limit)
	}
	return ""
}

// usbDeveloperMode applies a change of developer mode to a trusted link at
// once: the debug shell closes when developer mode goes off, not at the
// next unplugging.
func (c *Core) usbDeveloperMode() {
	if c.usb.trusted && c.usb.mtp == nil {
		c.usbFirewall(true)
	}
}
