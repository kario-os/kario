package usbgadget

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/hal"
)

// The blob FunctionFS takes: the v2 header with the counts it says, the
// descriptors of each speed, then the Microsoft OS descriptor.
func TestMTPDescriptors(t *testing.T) {
	b := MTPDescriptors()
	u32 := func(off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
	if u32(0) != ffsDescriptorsMagicV2 || int(u32(4)) != len(b) || u32(8) != ffsHasFS|ffsHasHS|ffsHasSS|ffsHasMSOS {
		t.Fatalf("header % x", b[:12])
	}
	counts := []uint32{u32(12), u32(16), u32(20)}
	at := 28
	for speed, n := range counts {
		var eps []uint16
		for k := uint32(0); k < n; k++ {
			l, typ := int(b[at]), b[at+1]
			switch typ {
			case 4:
				if !bytes.Equal(b[at+4:at+9], []byte{3, 6, 1, 1, 1}) {
					t.Errorf("speed %d interface % x", speed, b[at:at+l])
				}
			case 5:
				eps = append(eps, uint16(b[at+2])<<8|binary.LittleEndian.Uint16(b[at+4:]))
			}
			at += l
		}
		want := map[int][]uint16{0: {0x8100 | 64, 0x0200 | 64, 0x8300 | 28}, 1: {0x8100 | 512, 0x0200 | 512, 0x8300 | 28}, 2: {0x8100 | 1024, 0x0200 | 1024, 0x8300 | 28}}[speed]
		for k := range want {
			if k >= len(eps) || eps[k] != want[k] {
				t.Errorf("speed %d endpoints %x, want %x", speed, eps, want)
				break
			}
		}
	}
	if u32(24) != 1 {
		t.Errorf("OS descriptors: %d", u32(24))
	}
	osd := b[at:]
	if len(osd) != 35 || binary.LittleEndian.Uint32(osd[1:]) != 35 || binary.LittleEndian.Uint16(osd[5:]) != 0x0100 ||
		binary.LittleEndian.Uint16(osd[7:]) != 4 || osd[9] != 1 || osd[11] != 0 || osd[12] != 1 || string(osd[13:16]) != "MTP" {
		t.Errorf("OS descriptor % x", osd)
	}
	s := MTPStrings()
	if binary.LittleEndian.Uint32(s) != ffsStringsMagic || int(binary.LittleEndian.Uint32(s[4:])) != len(s) || !bytes.HasSuffix(s, []byte("\x09\x04MTP\x00")) {
		t.Errorf("strings % x", s)
	}
}

// The function's files as a mounted FunctionFS has them, here plain
// files: ep0 takes the descriptors, then the strings, and the endpoints
// open in the order the helper takes them, each the way round it carries
// data.
func TestOpenMTP(t *testing.T) {
	root := t.TempDir()
	ffs := filepath.Join(root, "usb-ffs")
	old := FFSDir
	hal.SysRoot, FFSDir = root, ffs
	t.Cleanup(func() { hal.SysRoot, FFSDir = "/sys", old })
	os.MkdirAll(ffs, 0o755)
	for _, ep := range []string{"ep0", "ep1", "ep2", "ep3"} {
		os.WriteFile(filepath.Join(ffs, ep), nil, 0o644)
	}
	c := Default("KARIO0001")
	m, err := OpenMTP(c)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(root, "kernel", "config", "usb_gadget", "kario", "functions", mtpFunction)); err != nil || !st.IsDir() {
		t.Errorf("the configfs function: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(ffs, "ep0")); !bytes.Equal(b, append(MTPDescriptors(), MTPStrings()...)) {
		t.Errorf("ep0 got % x", b)
	}
	var names []string
	for _, f := range m.Files() {
		names = append(names, filepath.Base(f.Name()))
	}
	if strings.Join(names, " ") != "ep0 ep1 ep2 ep3" {
		t.Errorf("files %v", names)
	}
	if _, err := m.In.Write([]byte("x")); err != nil {
		t.Errorf("bulk-in: %v", err)
	}
	if _, err := m.Intr.Write([]byte("x")); err != nil {
		t.Errorf("interrupt: %v", err)
	}
	if _, err := m.Out.Write([]byte("x")); err == nil {
		t.Error("bulk-out is open for writing")
	}
	m.Close()
	os.Remove(filepath.Join(ffs, "ep3"))
	if _, err := OpenMTP(c); err == nil {
		t.Error("opened without its interrupt endpoint")
	}
	if err := errNoFFS("mount functionfs", unix.ENODEV); !strings.Contains(err.Error(), "CONFIG_USB_CONFIGFS_F_FS") || !errors.Is(err, unix.ENODEV) {
		t.Errorf("no FunctionFS: %v", err)
	}
	if err := errNoFFS("mount functionfs", unix.EPERM); strings.Contains(err.Error(), "CONFIG_") {
		t.Errorf("not root: %v", err)
	}
}

func TestSwitchingToMTPAndBack(t *testing.T) {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	os.MkdirAll(filepath.Join(root, "class", "udc", "dummy_udc.0"), 0o755)
	c := Default("KARIO0001")
	if _, err := Setup(c); err != nil {
		t.Fatal(err)
	}
	g := filepath.Join(root, "kernel", "config", "usb_gadget", "kario")
	os.MkdirAll(filepath.Join(g, "functions", mtpFunction), 0o755)
	read := func(f string) string {
		b, _ := os.ReadFile(filepath.Join(g, f))
		return strings.TrimSpace(string(b))
	}
	linked := func(fn string) bool {
		_, err := os.Lstat(filepath.Join(g, "configs", "c.1", fn))
		return err == nil
	}
	if err := UseMTP(c, true); err != nil {
		t.Fatal(err)
	}
	if !linked(mtpFunction) || linked(ncmFunction) || read("idProduct") != "0x0105" || read("os_desc/use") != "1" ||
		read("os_desc/qw_sign") != "MSFT100" || read("UDC") != "dummy_udc.0" {
		t.Errorf("MTP: mtp %v ncm %v product %s os_desc %s UDC %s", linked(mtpFunction), linked(ncmFunction), read("idProduct"), read("os_desc/use"), read("UDC"))
	}
	if target, err := os.Readlink(filepath.Join(g, "os_desc", "c.1")); err != nil || !strings.HasSuffix(target, "configs/c.1") {
		t.Errorf("os_desc link %q, %v", target, err)
	}
	if err := UseNCM(c, false); err != nil {
		t.Fatal(err)
	}
	if linked(mtpFunction) || !linked(ncmFunction) || read("idProduct") != "0x0104" || read("os_desc/use") != "0" || read("UDC") != "" {
		t.Errorf("back to NCM: mtp %v ncm %v product %s os_desc %s UDC %q", linked(mtpFunction), linked(ncmFunction), read("idProduct"), read("os_desc/use"), read("UDC"))
	}
	// A Shell that ended while on MTP: Setup takes the function out again.
	UseMTP(c, false)
	if _, err := Setup(c); err != nil {
		t.Fatal(err)
	}
	if linked(mtpFunction) || !linked(ncmFunction) || read("idProduct") != "0x0104" || read("UDC") != "dummy_udc.0" {
		t.Errorf("Setup after MTP: mtp %v ncm %v product %s UDC %q", linked(mtpFunction), linked(ncmFunction), read("idProduct"), read("UDC"))
	}
	if PacketSize("high-speed") != 512 || PacketSize("full-speed") != 64 || PacketSize("") != 512 {
		t.Error("packet sizes")
	}
}
