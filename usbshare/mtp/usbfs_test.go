package mtp

import (
	"testing"
	"unsafe"
)

func TestUsbfsRequests(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 && (usbdevfsBulk != 0xC0185502 || usbdevfsControl != 0xC0185500) {
		t.Errorf("USBDEVFS_BULK %#x, USBDEVFS_CONTROL %#x", usbdevfsBulk, usbdevfsControl)
	}
	if usbdevfsClaim != 0x8004550F || usbdevfsRelease != 0x80045510 {
		t.Errorf("USBDEVFS_CLAIMINTERFACE %#x, RELEASEINTERFACE %#x", usbdevfsClaim, usbdevfsRelease)
	}
}

// What usbfs reads first: the device descriptor, then the configuration
// with an interface of another class before the MTP one.
func TestUsbfsFindsTheMTPInterface(t *testing.T) {
	raw := []byte{18, 1, 0, 2, 0, 0, 0, 64, 0x6b, 0x1d, 0x05, 0x01, 0, 1, 1, 2, 3, 1}
	raw = append(raw, 9, 2, 46, 0, 2, 1, 4, 0x80, 250)
	raw = append(raw, 9, 4, 0, 0, 1, 0xFF, 0, 0, 0, 7, 5, 0x84, 2, 0, 2, 0)
	raw = append(raw, 9, 4, 1, 0, 3, 6, 1, 1, 5,
		7, 5, 0x81, 2, 0, 2, 0, 7, 5, 0x02, 2, 0, 2, 0, 7, 5, 0x83, 3, 28, 0, 6)
	u := &USB{packet: Packet}
	if err := u.parse(raw); err != nil {
		t.Fatal(err)
	}
	if u.iface != 1 || u.ifaceName != 5 || u.in != 0x81 || u.out != 0x02 || u.intr != 0x83 || u.packet != 512 {
		t.Errorf("parsed %+v", u)
	}
}
