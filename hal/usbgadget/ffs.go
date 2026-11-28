package usbgadget

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/hal"
)

// File transfer over MTP (27-usb-typec.md §3.3): once the user trusts a
// computer with file transfer chosen, the gadget is enumerated again with
// an MTP function in place of the network one. The function is FunctionFS
// (ffs.mtp): the Shell, as root, mounts it, writes its descriptors and
// strings to ep0 and opens the endpoints; the sandboxed helper that speaks
// MTP gets the open files and nothing else. Windows learns that the
// interface is MTP from the Microsoft OS descriptor (configfs os_desc and
// the function's extended compatible ID "MTP"); libmtp also from the
// interface's name, "MTP".

const (
	mtpInstance = "mtp"
	mtpFunction = "ffs." + mtpInstance
	ncmFunction = "ncm.usb0"
	// MTPProductID is the Linux Foundation's FunctionFS gadget ID: a
	// product of its own, as Windows keeps what it learnt (the OS
	// descriptor, the driver) per product.
	MTPProductID = 0x0105
	// msVendorCode is the request Windows reads the extended compatible
	// ID with, as the OS string descriptor gives it.
	msVendorCode = "0x01"
)

// FFSDir is where the MTP function's FunctionFS is mounted.
var FFSDir = "/dev/usb-ffs/" + mtpInstance

// FunctionFS's blobs (linux/usb/functionfs.h).
const (
	ffsDescriptorsMagicV2 = 3
	ffsStringsMagic       = 2
	ffsHasFS              = 1
	ffsHasHS              = 2
	ffsHasSS              = 4
	ffsHasMSOS            = 8
)

// Endpoint addresses in the descriptors; FunctionFS names the files ep1,
// ep2, ep3 in this order.
const (
	epBulkIn  = 0x81
	epBulkOut = 0x02
	epIntr    = 0x83
	// intrPacket fits an MTP event with three parameters (24 bytes).
	intrPacket = 28
)

// MTPDescriptors is the function's descriptor blob: one interface of the
// still image class (6/1/1, PTP), bulk in and out and an interrupt
// endpoint, at full, high and super speed, and the extended compatible
// ID "MTP" for Windows.
func MTPDescriptors() []byte {
	iface := []byte{9, 4, 0, 0, 3, 6, 1, 1, 1}
	ep := func(addr, attr byte, maxPacket uint16, interval byte) []byte {
		return []byte{7, 5, addr, attr, byte(maxPacket), byte(maxPacket >> 8), interval}
	}
	comp := func(perInterval uint16) []byte {
		return []byte{6, 0x30, 0, 0, byte(perInterval), byte(perInterval >> 8)}
	}
	speed := func(bulk uint16, ss bool) []byte {
		out := append([]byte(nil), iface...)
		for _, e := range []struct {
			addr, attr byte
			max        uint16
			interval   byte
		}{{epBulkIn, 2, bulk, 0}, {epBulkOut, 2, bulk, 0}, {epIntr, 3, intrPacket, 6}} {
			out = append(out, ep(e.addr, e.attr, e.max, e.interval)...)
			if ss {
				per := uint16(0)
				if e.attr == 3 {
					per = intrPacket
				}
				out = append(out, comp(per)...)
			}
		}
		return out
	}
	fs, hs, ss := speed(64, false), speed(512, false), speed(1024, true)
	// The Microsoft OS descriptor: a header (interface, dwLength,
	// bcdVersion 1.00, wIndex 4: extended compatible ID, bCount 1) and the
	// function section for interface 0.
	compat := make([]byte, 24)
	compat[1] = 1
	copy(compat[2:], "MTP")
	osHdr := []byte{0}
	osHdr = binary.LittleEndian.AppendUint32(osHdr, uint32(11+len(compat)))
	osHdr = binary.LittleEndian.AppendUint16(osHdr, 0x0100)
	osHdr = binary.LittleEndian.AppendUint16(osHdr, 4)
	osHdr = append(osHdr, 1, 0)
	body := append(append(append(append(fs, hs...), ss...), osHdr...), compat...)
	var b []byte
	for _, v := range []uint32{ffsDescriptorsMagicV2, 0, ffsHasFS | ffsHasHS | ffsHasSS | ffsHasMSOS, 4, 4, 7, 1} {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	b = append(b, body...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)))
	return b
}

// MTPStrings is the function's strings: the interface's name in US
// English, which libmtp looks for.
func MTPStrings() []byte {
	var b []byte
	for _, v := range []uint32{ffsStringsMagic, 0, 1, 1} {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	b = binary.LittleEndian.AppendUint16(b, 0x0409)
	b = append(b, "MTP\x00"...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)))
	return b
}

// MTP is the function's open files, ep0 first; the Shell keeps them open
// while sharing (FunctionFS undoes the function when ep0 is last closed)
// and hands the helper the same files.
type MTP struct {
	EP0, In, Out, Intr *os.File
}

// Files are ep0, bulk in, bulk out and interrupt, the order the helper
// takes them in.
func (m *MTP) Files() []*os.File { return []*os.File{m.EP0, m.In, m.Out, m.Intr} }

func (m *MTP) Close() {
	for _, f := range m.Files() {
		if f != nil {
			f.Close()
		}
	}
}

// errNoFFS: configfs knows no "ffs" function (ENOENT) and the kernel no
// functionfs file system (ENODEV) when it was built without FunctionFS.
func errNoFFS(what string, err error) error {
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENODEV) {
		return fmt.Errorf("%s: %w (the kernel needs CONFIG_USB_CONFIGFS_F_FS)", what, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// OpenMTP makes the configfs function, mounts its FunctionFS (once),
// writes its descriptors and opens its endpoints.
func OpenMTP(c Config) (*MTP, error) {
	if err := os.MkdirAll(filepath.Join(configfs(), c.Name, "functions", mtpFunction), 0o755); err != nil {
		return nil, errNoFFS("the "+mtpFunction+" function", err)
	}
	if _, err := os.Stat(filepath.Join(FFSDir, "ep0")); err != nil {
		if err := os.MkdirAll(FFSDir, 0o755); err != nil {
			return nil, err
		}
		if err := unix.Mount(mtpInstance, FFSDir, "functionfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
			return nil, errNoFFS("mount functionfs", err)
		}
	}
	m := &MTP{}
	var err error
	if m.EP0, err = os.OpenFile(filepath.Join(FFSDir, "ep0"), os.O_RDWR, 0); err != nil {
		return nil, err
	}
	for _, blob := range [][]byte{MTPDescriptors(), MTPStrings()} {
		if _, err := m.EP0.Write(blob); err != nil {
			m.Close()
			return nil, fmt.Errorf("functionfs descriptors: %w", err)
		}
	}
	for _, e := range []struct {
		f    **os.File
		name string
		flag int
	}{{&m.In, "ep1", os.O_WRONLY}, {&m.Out, "ep2", os.O_RDONLY}, {&m.Intr, "ep3", os.O_WRONLY}} {
		if *e.f, err = os.OpenFile(filepath.Join(FFSDir, e.name), e.flag, 0); err != nil {
			m.Close()
			return nil, err
		}
	}
	return m, nil
}

// UseMTP gives the gadget the MTP function in place of the network one
// (its function must be open: OpenMTP) and binds it when bind; the
// computer sees the phone go and come back as an MTP device.
func UseMTP(c Config, bind bool) error {
	return use(c, mtpFunction, ncmFunction, MTPProductID, "MTP", "1", bind)
}

// UseNCM gives the gadget its network function back, as at boot.
func UseNCM(c Config, bind bool) error {
	return use(c, ncmFunction, mtpFunction, c.ProductID, "NCM", "0", bind)
}

func use(c Config, fn, other string, product uint16, label, osDesc string, bind bool) error {
	g := filepath.Join(configfs(), c.Name)
	udc := filepath.Join(g, "UDC")
	if b, err := os.ReadFile(udc); err == nil && strings.TrimSpace(string(b)) != "" {
		if err := os.WriteFile(udc, []byte("\n"), 0o644); err != nil {
			return fmt.Errorf("unbind: %w", err)
		}
	}
	cfg := filepath.Join(g, "configs", "c.1")
	if err := os.Remove(filepath.Join(cfg, other)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(cfg, fn)); err != nil {
		if err := os.Symlink(filepath.Join(g, "functions", fn), filepath.Join(cfg, fn)); err != nil {
			return err
		}
	}
	for _, s := range []struct{ path, value string }{
		{"idProduct", fmt.Sprintf("0x%04x", product)},
		{"configs/c.1/strings/0x409/configuration", label},
		{"os_desc/use", osDesc},
		{"os_desc/b_vendor_code", msVendorCode},
		{"os_desc/qw_sign", "MSFT100"},
	} {
		p := filepath.Join(g, s.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(s.value), 0o644); err != nil {
			return fmt.Errorf("%s: %w", s.path, err)
		}
	}
	if link := filepath.Join(g, "os_desc", "c.1"); osDesc == "1" {
		if _, err := os.Lstat(link); err != nil {
			if err := os.Symlink(cfg, link); err != nil {
				return err
			}
		}
	}
	if !bind {
		return nil
	}
	udcs := UDCs()
	if len(udcs) == 0 {
		return ErrNoUDC
	}
	if err := os.WriteFile(udc, []byte(udcs[0]), 0o644); err != nil {
		return fmt.Errorf("bind %s: %w", udcs[0], err)
	}
	return nil
}

// Speed is the controller's speed with a computer: "high-speed",
// "full-speed", "super-speed"... ("" when not known).
func Speed(udc string) string {
	b, _ := os.ReadFile(filepath.Join(hal.SysRoot, "class", "udc", udc, "current_speed"))
	return strings.TrimSpace(string(b))
}

// PacketSize is a bulk endpoint's packet size at that speed.
func PacketSize(speed string) int {
	switch {
	case strings.HasPrefix(speed, "full"):
		return 64
	case strings.HasPrefix(speed, "super"):
		return 1024
	}
	return 512
}
