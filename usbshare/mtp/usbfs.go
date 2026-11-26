package mtp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/unix"
)

// USB is an MTP device this machine is the USB host of, through usbfs
// (/dev/bus/usb): on the emulator, dummy_hcd's host side reaching the
// phone's own gadget, which the selftest copies files with as a computer
// would. Nothing in the kernel claims a still image interface, so usbfs
// can.
type USB struct {
	f             *os.File
	iface         uint16
	ifaceName     uint8
	in, out, intr uint8
	packet        int
}

// usbfs's requests (linux/usbdevice_fs.h), laid out as the C structs.
type usbCtrl struct {
	RequestType, Request uint8
	Value, Index, Length uint16
	Timeout              uint32
	Data                 uintptr
}

type usbBulk struct {
	Ep, Len, Timeout uint32
	Data             uintptr
}

func ioc(dir, nr, size uintptr) uintptr { return dir<<30 | size<<16 | 'U'<<8 | nr }

var (
	usbdevfsControl = ioc(3, 0, unsafe.Sizeof(usbCtrl{}))
	usbdevfsBulk    = ioc(3, 2, unsafe.Sizeof(usbBulk{}))
	usbdevfsClaim   = ioc(2, 15, 4)
	usbdevfsRelease = ioc(2, 16, 4)
)

const usbTimeout = 20 * time.Second

func sysfsHex(dir, name string) (uint64, bool) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 16, 16)
	return v, err == nil
}

func sysfsInt(dir, name string) int {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	v, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return v
}

// FindUSB is the usbfs node of the configured device with that vendor and
// product ("" when there is none).
func FindUSB(vendor, product uint16) string {
	dirs, _ := filepath.Glob("/sys/bus/usb/devices/*")
	for _, d := range dirs {
		v, ok1 := sysfsHex(d, "idVendor")
		p, ok2 := sysfsHex(d, "idProduct")
		if !ok1 || !ok2 || uint16(v) != vendor || uint16(p) != product || sysfsInt(d, "bConfigurationValue") == 0 {
			continue
		}
		return fmt.Sprintf("/dev/bus/usb/%03d/%03d", sysfsInt(d, "busnum"), sysfsInt(d, "devnum"))
	}
	return ""
}

// OpenUSB waits for the device, finds its MTP interface (still image,
// 6/1/1) and claims it.
func OpenUSB(vendor, product uint16, wait time.Duration) (*USB, error) {
	end := time.Now().Add(wait)
	var node string
	for node = FindUSB(vendor, product); node == ""; node = FindUSB(vendor, product) {
		if time.Now().After(end) {
			return nil, fmt.Errorf("mtp: no USB device %04x:%04x", vendor, product)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var f *os.File
	var err error
	for {
		if f, err = os.OpenFile(node, os.O_RDWR, 0); err == nil || time.Now().After(end) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 4096)
	n, err := f.Read(raw)
	if err != nil {
		f.Close()
		return nil, err
	}
	u := &USB{f: f, packet: Packet}
	if err := u.parse(raw[:n]); err != nil {
		f.Close()
		return nil, err
	}
	iface := uint32(u.iface)
	if err := u.ioctl(usbdevfsClaim, unsafe.Pointer(&iface)); err != nil {
		f.Close()
		return nil, fmt.Errorf("mtp: claiming interface %d: %w", u.iface, err)
	}
	return u, nil
}

// parse finds the MTP interface and its endpoints in what usbfs reads
// first: the device descriptor and the configuration's.
func (u *USB) parse(b []byte) error {
	found := false
	for at := 0; at+2 <= len(b); {
		l, typ := int(b[at]), b[at+1]
		if l < 2 || at+l > len(b) {
			break
		}
		d := b[at : at+l]
		switch {
		case typ == 4 && l >= 9:
			if found {
				return u.ready()
			}
			if d[5] == 6 && d[6] == 1 && d[7] == 1 {
				found, u.iface, u.ifaceName = true, uint16(d[2]), d[8]
			}
		case typ == 5 && l >= 7 && found:
			addr, attr := d[2], d[3]&3
			switch {
			case attr == 2 && addr&0x80 != 0:
				u.in = addr
				u.packet = int(binary.LittleEndian.Uint16(d[4:]) & 0x7FF)
			case attr == 2:
				u.out = addr
			case attr == 3:
				u.intr = addr
			}
		}
		at += l
	}
	if !found {
		return errors.New("mtp: the device has no still image interface")
	}
	return u.ready()
}

func (u *USB) ready() error {
	if u.in == 0 || u.out == 0 {
		return errors.New("mtp: the MTP interface lacks bulk endpoints")
	}
	return nil
}

func (u *USB) ioctl(req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, u.f.Fd(), req, uintptr(arg))
		if errno == unix.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}

func (u *USB) bulk(ep uint8, p []byte, timeout time.Duration) (int, error) {
	buf := make([]byte, max(len(p), 1))
	if ep&0x80 == 0 {
		copy(buf, p)
	}
	t := usbBulk{Ep: uint32(ep), Len: uint32(len(p)), Timeout: uint32(timeout / time.Millisecond), Data: uintptr(unsafe.Pointer(&buf[0]))}
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, u.f.Fd(), usbdevfsBulk, uintptr(unsafe.Pointer(&t)))
	runtime.KeepAlive(buf)
	if errno != 0 {
		return 0, errno
	}
	if ep&0x80 != 0 {
		copy(p, buf[:r])
	}
	return int(r), nil
}

// usbPipe is one endpoint. A read shorter than a packet reads a whole
// packet and keeps the rest, as usbfs would overflow.
type usbPipe struct {
	u       *USB
	ep      uint8
	timeout time.Duration
	rest    []byte
}

func (p *usbPipe) Read(b []byte) (int, error) {
	if len(p.rest) > 0 {
		n := copy(b, p.rest)
		p.rest = p.rest[n:]
		return n, nil
	}
	pk := p.u.packet
	if len(b)%pk == 0 {
		return p.read(b)
	}
	tmp := make([]byte, (len(b)/pk+1)*pk)
	n, err := p.read(tmp)
	c := copy(b, tmp[:n])
	p.rest = tmp[c:n]
	return c, err
}

func (p *usbPipe) read(b []byte) (int, error) {
	for {
		n, err := p.u.bulk(p.ep, b, p.timeout)
		if errors.Is(err, unix.ETIMEDOUT) && p.timeout < usbTimeout {
			continue // the interrupt endpoint, between events
		}
		return n, err
	}
}

func (p *usbPipe) Write(b []byte) (int, error) { return p.u.bulk(p.ep, b, usbTimeout) }

// Control makes a control request on the device.
func (u *USB) Control(s Setup, data []byte) ([]byte, error) {
	if !s.in() {
		s.Length = uint16(len(data))
	}
	buf := make([]byte, max(int(s.Length), 1))
	copy(buf, data)
	c := usbCtrl{RequestType: s.RequestType, Request: s.Request, Value: s.Value, Index: s.Index, Length: s.Length,
		Timeout: uint32(usbTimeout / time.Millisecond), Data: uintptr(unsafe.Pointer(&buf[0]))}
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, u.f.Fd(), usbdevfsControl, uintptr(unsafe.Pointer(&c)))
	runtime.KeepAlive(buf)
	if errno == unix.EPIPE {
		return nil, ErrStall
	}
	if errno != 0 {
		return nil, errno
	}
	return buf[:r], nil
}

// Transport is the computer's side of the MTP interface.
func (u *USB) Transport() *HostTransport {
	t := &HostTransport{In: &usbPipe{u: u, ep: u.in, timeout: usbTimeout}, Out: &usbPipe{u: u, ep: u.out, timeout: usbTimeout},
		Ctl: u, Packet: u.packet, Close: u.Close}
	if u.intr != 0 {
		t.Intr = &usbPipe{u: u, ep: u.intr, timeout: time.Second}
	}
	return t
}

// Iface is the MTP interface's number.
func (u *USB) Iface() uint16 { return u.iface }

func (u *USB) Close() error {
	iface := uint32(u.iface)
	u.ioctl(usbdevfsRelease, unsafe.Pointer(&iface))
	return u.f.Close()
}

// String is a string descriptor in US English.
func (u *USB) String(index uint8) (string, error) {
	b, err := u.Control(Setup{RequestType: 0x80, Request: 6, Value: 3<<8 | uint16(index), Index: 0x0409, Length: 255}, nil)
	if err != nil {
		return "", err
	}
	if len(b) < 2 || int(b[0]) > len(b) {
		return "", errors.New("mtp: a short string descriptor")
	}
	units := make([]uint16, (int(b[0])-2)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2+2*i:])
	}
	return string(utf16.Decode(units)), nil
}

// InterfaceName is the MTP interface's string, which libmtp looks for.
func (u *USB) InterfaceName() (string, error) {
	if u.ifaceName == 0 {
		return "", errors.New("mtp: the interface has no name")
	}
	return u.String(u.ifaceName)
}

// CompatibleID reads the Microsoft OS descriptor as Windows does: the
// string at 0xEE ("MSFT100" and a vendor request code), then with that
// request the extended compatible ID of the first interface.
func (u *USB) CompatibleID() (string, error) {
	b, err := u.Control(Setup{RequestType: 0x80, Request: 6, Value: 3<<8 | 0xEE, Length: 0x12}, nil)
	if err != nil {
		return "", fmt.Errorf("the OS string descriptor: %w", err)
	}
	if len(b) < 18 {
		return "", errors.New("mtp: a short OS string descriptor")
	}
	sig := make([]uint16, 7)
	for i := range sig {
		sig[i] = binary.LittleEndian.Uint16(b[2+2*i:])
	}
	if string(utf16.Decode(sig)) != "MSFT100" {
		return "", fmt.Errorf("mtp: OS string %q", string(utf16.Decode(sig)))
	}
	vendor := b[16]
	hdr, err := u.Control(Setup{RequestType: 0xC0, Request: vendor, Index: 4, Length: 16}, nil)
	if err != nil || len(hdr) < 16 {
		return "", fmt.Errorf("the extended compatible ID's header: %v", err)
	}
	full, err := u.Control(Setup{RequestType: 0xC0, Request: vendor, Index: 4, Length: uint16(binary.LittleEndian.Uint32(hdr))}, nil)
	if err != nil {
		return "", fmt.Errorf("the extended compatible ID: %w", err)
	}
	for at := 16; at+24 <= len(full); at += 24 {
		if uint16(full[at]) == u.iface {
			return strings.TrimRight(string(full[at+2:at+10]), "\x00"), nil
		}
	}
	return "", errors.New("mtp: no compatible ID for the MTP interface")
}
