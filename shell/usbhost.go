package shell

import (
	"strings"
	"time"

	"github.com/kario-os/kario/hal/usbgadget"
	"github.com/kario-os/kario/hal/usbhost"
)

// The phone as a USB host on its USB-C port (27-usb-typec.md §2), on boards
// whose SKU says the port can be one ([usb] host). New devices on the
// port's buses wait unconfigured (authorized_default 0) until the Shell has
// read their descriptors: keyboards, mice, hubs and other devices go on at
// once (a keyboard plugged in while the phone is locked then types only
// into the lock screen, inputlock.go); anything with mass storage, a
// keyboard that is a stick too among them (the BadUSB trick), is asked
// about as storage, and only once the phone is unlocked; a device that
// would draw more from VBUS than the board gives stays off. The kernel's
// Type-C driver picks the port's role; the Shell only asks a powered hub
// or dock that leaves the phone its device for the host role
// (usbHostRole).

// Device states.
const (
	hostAllowed = "allowed"
	hostWaiting = "waiting" // storage, until the phone is unlocked
	hostAsking  = "asking"
	hostRefused = "refused" // the user said no: off until unplugged
	hostPower   = "power"   // over the VBUS budget: off while it is
)

// usbHostProbe: a computer resets the bus within a second of the plug;
// a partner that has not by then is a charger, a hub or a dock.
const usbHostProbe = 3 * time.Second

type usbHost struct {
	on     bool
	budget int
	buses  map[int]bool // the port's
	devs   map[string]*hostDevice
	prompt *confirmOverlay
	// The Type-C partner plugged in since, and whether it was asked for
	// the host role; role: the port's role last logged.
	partnerAt time.Time
	swapAsked bool
	role      string
}

type hostDevice struct {
	dev   usbhost.Device
	state string
}

// startUSBHost holds the port's buses: what is plugged in from now on waits
// for checkUSBHost. dummy_hcd's host is the emulator's computer, and a
// board's controller inside (a fingerprint sensor's) is not the port.
func (c *Core) startUSBHost() {
	h := &c.usb.host
	u := c.cfg.SKU.USB
	if !u.Host {
		return
	}
	h.on, h.budget = true, u.VBUSMA
	if h.budget == 0 {
		h.budget = 500
	}
	h.buses, h.devs = map[int]bool{}, map[string]*hostDevice{}
	for _, b := range usbhost.Buses() {
		if b.Dummy || u.Controller != "" && !strings.Contains(b.Path, u.Controller) {
			continue
		}
		h.buses[b.Num] = true
		if !b.Held {
			if err := usbhost.Hold(b.Num); err != nil {
				c.log.Printf("usb host: bus %d: %v", b.Num, err)
			}
		}
	}
	c.log.Printf("usb host: %d buses of the port held; devices draw up to %d mA from VBUS", len(h.buses), h.budget)
	c.checkUSBHost()
}

// checkUSBHost runs with the housekeeping: devices that came are decided
// on, those that went are forgotten, storage waiting for the unlock is
// asked about.
func (c *Core) checkUSBHost() {
	h := &c.usb.host
	if !h.on {
		return
	}
	c.usbHostRole()
	devs := usbhost.Devices(func(bus int) bool { return h.buses[bus] })
	seen := map[string]bool{}
	for _, d := range devs {
		seen[d.Name] = true
		hd := h.devs[d.Name]
		if hd == nil || hd.dev.VendorID != d.VendorID || hd.dev.ProductID != d.ProductID || hd.dev.Serial != d.Serial {
			if hd != nil {
				c.hostGone(hd)
			}
			hd = &hostDevice{dev: d}
			h.devs[d.Name] = hd
			c.hostArrived(hd, devs)
			continue
		}
		hd.dev = d
		switch hd.state {
		case hostPower:
			c.hostAllow(hd, devs, false)
		case hostWaiting:
			c.askStorage(hd)
		}
	}
	for name, hd := range h.devs {
		if !seen[name] {
			c.hostGone(hd)
			delete(h.devs, name)
		}
	}
}

// hostKind is what the log calls the device.
func hostKind(d usbhost.Device) string {
	switch {
	case d.Storage() && d.HID():
		return "storage that says it is a keyboard too"
	case d.Storage():
		return "storage"
	case d.Hub():
		return "hub"
	case d.Mouse():
		return "mouse"
	case d.Keyboard():
		return "keyboard"
	case d.HID():
		return "input device"
	}
	return "device"
}

func (c *Core) hostArrived(hd *hostDevice, devs []usbhost.Device) {
	d := hd.dev
	c.log.Printf("usb host: %s %s (%s, %04x:%04x, %d mA)", hostKind(d), d.Name, d.Label(), d.VendorID, d.ProductID, d.MaxPowerMA)
	if !d.Storage() {
		c.hostAllow(hd, devs, true)
		return
	}
	// Configured before the bus was held (plugged in at boot): off until
	// the user says.
	if d.Authorized {
		if err := usbhost.Authorize(d.Name, false); err != nil {
			c.log.Printf("usb host: %s: %v", d.Name, err)
		}
		hd.dev.Authorized = false
	}
	hd.state = hostWaiting
	if c.locked() {
		c.Wake("usb")
		c.showToast(tr.T("Unlock the phone to use the USB storage"))
	}
	c.askStorage(hd)
}

// hostAllow configures the device if the port can power it; tell: say
// so when it cannot.
func (c *Core) hostAllow(hd *hostDevice, devs []usbhost.Device, tell bool) {
	h := &c.usb.host
	d := hd.dev
	if !d.Authorized && usbhost.PowersVBUS() && !usbhost.Within(devs, d, h.budget) {
		if hd.state != hostPower || tell {
			c.log.Printf("usb host: %s needs %d mA; %d mA of %d are drawn: left off", d.Name, d.MaxPowerMA, usbhost.Draw(devs), h.budget)
			c.showToast(tr.T("“%s” needs %d mA; the phone gives %d mA. Plug it into a powered hub.", d.Label(), d.MaxPowerMA, h.budget))
		}
		hd.state = hostPower
		return
	}
	if !d.Authorized {
		if err := usbhost.Authorize(d.Name, true); err != nil {
			c.log.Printf("usb host: %s: %v", d.Name, err)
			return
		}
		hd.dev.Authorized = true
	}
	hd.state = hostAllowed
	c.log.Printf("usb host: %s allowed", d.Name)
}

// askStorage asks about a waiting storage device, once the phone is
// unlocked and nothing else is being asked; a device that is a keyboard too
// says so.
func (c *Core) askStorage(hd *hostDevice) {
	h := &c.usb.host
	if hd.state != hostWaiting || c.locked() || h.prompt != nil {
		return
	}
	d := hd.dev
	text := tr.T("Allow the USB storage “%s”?", d.Label())
	if d.HID() {
		text = tr.T("Allow the USB storage “%s”? It also says it is a keyboard or a mouse: allowed, it can type and click on this phone.", d.Label())
	}
	hd.state = hostAsking
	c.Wake("usb")
	h.prompt = &confirmOverlay{
		text: text, yes: tr.T("Allow"), no: tr.T("Don't allow"), trust: true,
		onYes: func() {
			h.prompt = nil
			if h.devs[d.Name] != hd {
				return
			}
			c.hostAllow(hd, usbhost.Devices(func(bus int) bool { return h.buses[bus] }), true)
			if hd.state == hostAllowed {
				c.showToast(tr.T("Allowed. The phone cannot open USB storage yet."))
			}
		},
		onNo: func() {
			h.prompt = nil
			if h.devs[d.Name] != hd {
				return
			}
			// The lock closed the question: it is asked again after the
			// unlock.
			if c.locked() {
				hd.state = hostWaiting
				return
			}
			hd.state = hostRefused
			c.log.Printf("usb host: %s not allowed by the user; left off until unplugged", d.Name)
		},
	}
	c.pushOverlay(h.prompt)
}

func (c *Core) hostGone(hd *hostDevice) {
	h := &c.usb.host
	if hd.state == hostAsking && h.prompt != nil {
		c.popOverlay(h.prompt)
		h.prompt = nil
	}
	c.log.Printf("usb host: %s unplugged (%s)", hd.dev.Name, hd.dev.Label())
}

// usbHostRole follows the Type-C port's role and logs it. A powered hub or
// dock is the power source, so the port starts as its device, and a dock
// that does not ask for a data role swap itself leaves the phone without
// the keyboard behind it: when nothing has reset the bus within
// usbHostProbe of the plug (a computer would have), the Shell asks the
// partner for the swap, once per plug, if it speaks PD.
func (c *Core) usbHostRole() {
	h := &c.usb.host
	ports := usbhost.Ports()
	if len(ports) == 0 {
		return
	}
	p := ports[0]
	role := usbhost.RoleNone
	if p.Partner {
		role = p.DataRole + " (" + p.PowerRole + ")"
	}
	if role != h.role {
		h.role = role
		c.log.Printf("usb-c: %s", role)
	}
	if !p.Partner {
		h.partnerAt, h.swapAsked = time.Time{}, false
		return
	}
	if h.partnerAt.IsZero() {
		h.partnerAt = time.Now()
	}
	if p.DataRole != usbhost.RoleDevice || !p.Swappable || !p.PartnerPD || h.swapAsked ||
		p.Accessory != "" && p.Accessory != "none" || time.Since(h.partnerAt) < usbHostProbe {
		return
	}
	if udcs := usbgadget.UDCs(); len(udcs) > 0 {
		switch usbgadget.State(udcs[0]) {
		case "default", "addressed", "configured", "suspended":
			return
		}
	}
	h.swapAsked = true
	if err := usbhost.SetDataRole(p.Name, usbhost.RoleHost); err != nil {
		c.log.Printf("usb-c: no computer answered in %s; the partner refused the host role: %v", usbHostProbe, err)
		return
	}
	c.log.Printf("usb-c: no computer answered in %s; asked the partner (a powered hub or dock) for the host role", usbHostProbe)
}
