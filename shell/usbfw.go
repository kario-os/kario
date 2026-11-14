package shell

import (
	"github.com/kario-os/kario/hal/usbgadget"
	"github.com/kario-os/kario/netfw"
)

// usbFirewall: the USB link reaches nothing until the computer is trusted,
// then file sharing (and DHCP for its address), and in developer mode the
// debug shell (SSH) too. The rules are the firewall's (netfw.Link), so a
// service that listens on every interface is still closed to the cable.
func (c *Core) usbFirewall(open bool) {
	if c.traffic == nil || c.usb.sim {
		return
	}
	l := netfw.Link{Name: usbgadget.Interface(c.usb.cfg)}
	if open {
		l.UDP = []uint16{67}
		l.TCP = []uint16{80}
		if c.settings.DeveloperMode {
			l.TCP = append(l.TCP, 22)
		}
	}
	if err := c.traffic.fw.ApplyLink(l); err != nil {
		c.log.Printf("usb: firewall: %v", err)
		return
	}
	c.log.Printf("usb: the link reaches udp %v, tcp %v", l.UDP, l.TCP)
}
