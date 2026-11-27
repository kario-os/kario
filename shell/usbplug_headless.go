package shell

import (
	"fmt"
	"strings"
	"time"
)

// USBPlugEnv, when set, says whoever runs the selftest plugs a USB keyboard
// or mouse into the phone's port when the usb-plug step asks: QEMU's harness
// does it through QMP (yocto/qemu-selftest.sh, qemu-buttons.py).
const USBPlugEnv = "PHONE_SELFTEST_USB_PLUG"

// usbPlugKinds: what the step plugs and what the Shell has to make of it.
var usbPlugKinds = map[string]string{"usb-kbd": "keyboard", "usb-mouse": "mouse"}

// usbPlugged counts the devices of a kind the Shell holds in a state.
func (r *Runner) usbPlugged(kind, state string) int {
	n := 0
	for _, hd := range r.C.usb.host.devs {
		if hostKind(hd.dev) == kind && (state == "" || hd.state == state) {
			n++
		}
	}
	return n
}

// usb-plug usb-kbd|usb-mouse on|off [ms]: a USB keyboard or mouse plugged
// into the port or pulled when asked on the output (PHONE-SELFTEST-PLUG;
// ask_headless.go); done once the Shell has allowed one more of that kind,
// or holds one fewer.
func (r *Runner) usbPlug(arg string) error {
	rest, ms := lastNum(strings.TrimSpace(arg), 20000)
	f := strings.Fields(rest)
	if len(f) != 2 || usbPlugKinds[f[0]] == "" || (f[1] != "on" && f[1] != "off") {
		return fmt.Errorf("usb-plug usb-kbd|usb-mouse on|off [ms]")
	}
	kind, on := usbPlugKinds[f[0]], f[1] == "on"
	r.C.checkUSBHost()
	allowed, held := r.usbPlugged(kind, hostAllowed), r.usbPlugged(kind, "")
	r.plugs++
	again := r.ask("plug", r.plugs, fmt.Sprintf("PHONE-SELFTEST-PLUG: %d %s %s", r.plugs, f[0], f[1]))
	if _, ok := r.waitFor(func(string) bool {
		r.C.checkUSBHost()
		if on && r.usbPlugged(kind, hostAllowed) > allowed || !on && r.usbPlugged(kind, "") < held {
			return true
		}
		again()
		return false
	}, ms); !ok {
		var seen []string
		for name, hd := range r.C.usb.host.devs {
			seen = append(seen, fmt.Sprintf("%s %s %s", name, hostKind(hd.dev), hd.state))
		}
		return fmt.Errorf("usb-plug %s %s: the Shell holds %s", f[0], f[1], strings.Join(seen, ", "))
	}
	r.pump(40 * time.Millisecond)
	return nil
}
