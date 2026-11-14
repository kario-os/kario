package shell

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/hal/usbgadget"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/usbshare/mtp"
)

// File transfer over MTP (27-usb-typec.md §3.3), the other way a trusted
// computer gets the shared folders: Settings › USB chooses it or network
// sharing (the default). The trust is the same; once given, the gadget is
// enumerated again with an MTP function (hal/usbgadget) in place of the
// network one, and the computer shows the phone as a device with four
// storages, Photos, Videos, Music and Files. The same sandboxed helper
// speaks MTP (usbshare/mtp) over the same library and checks
// (usbLibrary, usbshare.Helper); it gets the function's files, which the
// Shell opened as root, and opens nothing. There is no password: MTP has
// none, so under a lock with a PIN the trust is asked only once the phone
// is unlocked, as Android shows MTP storage only unlocked. On the
// developer machine the cable is simulated (mtp.NewSim) and the selftest
// is the computer; on the emulator the computer is dummy_hcd's host side
// over usbfs.

// usbSwitchWait is how long the gadget may take to come back as an MTP
// device before the cable counts as pulled.
const usbSwitchWait = 15 * time.Second

type usbMTP struct {
	ffs *usbgadget.MTP
	// sim: the simulated cable's device ends and the computer's.
	sim  mtp.SimFiles
	host *mtp.HostTransport
	// pc is the selftest's computer, once it has asked.
	pc *mtp.Initiator
}

func (m *usbMTP) files() []*os.File {
	if m.ffs != nil {
		return m.ffs.Files()
	}
	return m.sim.List()
}

// usbMode is what a trusted computer gets: ui.USBModeMTP or
// ui.USBModeNetwork.
func (c *Core) usbMode() string {
	if c.settings.USBMode == ui.USBModeMTP {
		return ui.USBModeMTP
	}
	return ui.USBModeNetwork
}

// setUSBMode is settings.set usb_mode: 1 file transfer (MTP), 0 network
// sharing. It applies from the next time a computer is trusted.
func (c *Core) setUSBMode(v float64) error {
	switch v {
	case 0:
		c.settings.USBMode = ""
	case 1:
		c.settings.USBMode = ui.USBModeMTP
	default:
		return errors.New("usb_mode is 0 (network sharing) or 1 (file transfer)")
	}
	c.log.Printf("usb: a trusted computer gets %s", c.usbMode())
	return nil
}

// usbView is settings.get's "usb": the mode and whether a computer is
// being shared with now.
func (c *Core) usbView() map[string]any {
	return map[string]any{"mode": c.usbMode(), "sharing": c.usb.trusted}
}

// usbTrustWaits: under a PIN lock MTP is not offered; the question waits
// for the unlock (checkUSB asks it then).
func (c *Core) usbTrustWaits() bool {
	if c.usbMode() != ui.USBModeMTP || !c.secureLocked() {
		return false
	}
	if !c.usb.waitUnlock {
		c.usb.waitUnlock = true
		c.Wake("usb")
		c.log.Printf("usb: a computer; file transfer is asked about once the phone is unlocked")
		c.showToast(tr.T("Unlock the phone to transfer files with this computer"))
	}
	return true
}

// usbUnlocked asks about the computer that waited for the unlock.
func (c *Core) usbUnlocked(st string) {
	if !c.usb.waitUnlock {
		return
	}
	if st != "configured" {
		c.usb.waitUnlock = false
		return
	}
	if !c.secureLocked() && !c.usb.trusted && c.usb.prompt == nil {
		c.usb.waitUnlock = false
		c.askTrust()
	}
}

// usbSwitching: the gadget is being enumerated again as an MTP device;
// the states on the way are not the cable coming out.
func (c *Core) usbSwitching(st string) bool {
	if c.usb.switchBy.IsZero() {
		return false
	}
	if st == "configured" || time.Now().After(c.usb.switchBy) {
		c.usb.switchBy = time.Time{}
		return false
	}
	return true
}

// shareMTP: the trust given with file transfer chosen.
func (c *Core) shareMTP() error {
	c.usb.trusted, c.usb.pass, c.usb.restarts = true, "", 0
	m := &usbMTP{}
	if c.usb.sim {
		dev, host, err := mtp.NewSim(mtp.Packet)
		if err != nil {
			c.usb.trusted = false
			return err
		}
		m.sim, m.host = dev, host
	} else {
		f, err := usbgadget.OpenMTP(c.usb.cfg)
		if err != nil {
			c.usb.trusted = false
			return err
		}
		m.ffs = f
		c.usb.switchBy = time.Now().Add(usbSwitchWait)
		if err := usbgadget.UseMTP(c.usb.cfg, true); err != nil {
			c.usb.mtp = m
			c.endMTP()
			c.usb.trusted = false
			return err
		}
	}
	c.usb.mtp = m
	if err := c.startUSBHelper(); err != nil {
		c.endMTP()
		c.usb.trusted = false
		return err
	}
	c.log.Printf("usb sharing: file transfer (MTP) on")
	c.addNotif(c.sys, ui.Notification{ID: "usb-sharing", Title: tr.T("File transfer on"),
		Body: tr.T("The computer shows the phone as a device with Photos, Videos, Music and Files (MTP). Unplugging the cable ends it.")})
	return nil
}

// endMTP gives the gadget its network function back (bound again where
// the cable is in, or always is: not the emulator's unplugged one) and
// closes the function.
func (c *Core) endMTP() {
	m := c.usb.mtp
	if m == nil {
		return
	}
	c.usb.mtp, c.usb.switchBy = nil, time.Time{}
	if m.pc != nil {
		m.pc.Close()
	}
	if m.host != nil {
		m.host.Close()
		m.sim.Close()
	}
	if m.ffs == nil {
		return
	}
	b, _ := os.ReadFile("/sys/kernel/config/usb_gadget/" + c.usb.cfg.Name + "/UDC")
	bind := strings.TrimSpace(string(b)) != "" || !strings.HasPrefix(c.usb.udc, "dummy_udc")
	if err := usbgadget.UseNCM(c.usb.cfg, bind); err != nil {
		c.log.Printf("usb: back to the network function: %v", err)
	}
	m.ffs.Close()
}

// usbHelperFiles are the helper's descriptors besides its end of the
// pair (fd 4): what becomes fd 3, then fds 5 and on. Network sharing
// gives the listening socket; MTP the function's ep0, then bulk in, bulk
// out and interrupt. They are copies, the caller closes them.
func (c *Core) usbHelperFiles() ([]*os.File, error) {
	if c.usb.mtp == nil {
		lf, err := c.usb.ln.File()
		if err != nil {
			return nil, err
		}
		return []*os.File{lf}, nil
	}
	var out []*os.File
	for _, f := range c.usb.mtp.files() {
		fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			closeFiles(out)
			return nil, err
		}
		out = append(out, os.NewFile(uintptr(fd), f.Name()))
	}
	return out, nil
}

// usbHelperEnv tells the helper to speak MTP, over what, and the phone's
// name.
func (c *Core) usbHelperEnv() []string {
	m := c.usb.mtp
	if m == nil {
		return nil
	}
	transport, packet := mtp.TransportFFS, usbgadget.PacketSize(usbgadget.Speed(c.usb.udc))
	if m.ffs == nil {
		transport, packet = mtp.TransportSim, mtp.Packet
	}
	dev, _ := json.Marshal(mtp.DeviceInfo{Manufacturer: c.usb.cfg.Manufacturer, Model: c.usb.cfg.Product,
		Version: shownVersion(), Serial: c.usb.cfg.Serial})
	return []string{mtp.EnvMode + "=" + mtp.ModeMTP, mtp.EnvTransport + "=" + transport,
		mtp.EnvPacket + "=" + strconv.Itoa(packet), mtp.EnvDevice + "=" + string(dev)}
}

// --- Selftest ---

// mtpComputer is the computer's side, with a session open: the simulated
// cable's other end, or on the emulator the phone's gadget over usbfs.
// It must not run on the shell's loop.
func (c *Core) mtpComputer(wait time.Duration) (*mtp.Initiator, error) {
	m := c.usb.mtp
	if m == nil {
		return nil, errors.New("no file transfer")
	}
	if m.pc != nil {
		return m.pc, nil
	}
	var pc *mtp.Initiator
	if m.host != nil {
		pc = mtp.NewInitiator(m.host)
	} else {
		u, err := mtp.OpenUSB(c.usb.cfg.VendorID, usbgadget.MTPProductID, wait)
		if err != nil {
			return nil, err
		}
		pc = mtp.NewInitiator(u.Transport())
		pc.Iface = u.Iface()
	}
	if err := pc.OpenSession(); err != nil {
		if m.host == nil {
			pc.Close()
		}
		return nil, err
	}
	m.pc = pc
	return pc, nil
}

// usbMTPCmd is the selftest's usb-mtp; paths start with a storage
// ("/Music/Album/x.lrc"):
//
//	storages <name>...      the computer sees these storages, in order
//	ls <path> <text>        a name in that folder contains text
//	props <path>            Windows' reading of a folder: the properties
//	                        offered and their descriptions, then every
//	                        object's in one property list each
//	get <path> <text>       the file contains text
//	partial <path> <offset> <text>   so many bytes at offset are text
//	                        (GetPartialObject64)
//	put <path> <text>       the computer copies text there, making the
//	                        folders on the way (Music keeps folders)
//	refused delete|rename|put <path> [<text>]   the phone refuses it and
//	                        what was there stays as it was
//	event <name>            the phone sent that event (StorageInfoChanged...)
//	windows                 (the emulator) the descriptors Windows and libmtp
//	                        tell MTP by: the OS descriptor's compatible ID
//	                        and the interface's name, both "MTP"
//	libmtp detect|files <text>   (the emulator) libmtp's mtp-detect or
//	                        mtp-files, as Linux file managers reach the
//	                        phone, prints text
//	libmtp get <path> <text>   (the emulator) mtp-getfile copies the file,
//	                        which contains text
//	kernel                  the kernel has what file transfer needs; runs
//	                        before require-usb, which would skip the case
//	gone                    the computer no longer sees an MTP device
func (r *Runner) usbMTPCmd(arg string) error {
	f := strings.Fields(arg)
	if len(f) == 0 {
		return errors.New("usb-mtp storages|ls|props|get|partial|put|refused|event|windows|libmtp|kernel|gone ...")
	}
	c := r.C
	if f[0] == "gone" {
		return r.mtpGone()
	}
	if f[0] == "kernel" {
		return c.mtpKernel()
	}
	if c.usb.mtp == nil {
		return errors.New("no file transfer: is usb_mode 1 and the computer trusted?")
	}
	wait := r.scaled(20 * time.Second)
	if f[0] == "windows" || f[0] == "libmtp" {
		if c.usb.sim {
			return fmt.Errorf("usb-mtp %s needs a USB host side (the emulator)", f[0])
		}
		if f[0] == "libmtp" {
			return r.offLoop(func() error {
				said, err := c.mtpLibmtp(f[1:], wait, r.scaled(60*time.Second))
				if err == nil {
					fmt.Fprintf(r.Out, "  libmtp: %s\n", said)
				}
				return err
			})
		}
		return r.offLoop(func() error { return mtpDescriptors(c.usb.cfg.VendorID, wait) })
	}
	return r.offLoop(func() error {
		pc, err := c.mtpComputer(wait)
		if err != nil {
			return err
		}
		err = mtpStep(pc, f)
		if mtp.Code(err) == mtp.RespSessionNotOpen {
			// A helper started again after a crash: the computer opens a
			// new session, as Windows does.
			if err = pc.OpenSession(); err == nil {
				err = mtpStep(pc, f)
			}
		}
		return err
	})
}

func mtpStep(pc *mtp.Initiator, f []string) error {
	need := func(n int) error {
		if len(f) < n {
			return fmt.Errorf("usb-mtp %s: too few arguments", f[0])
		}
		return nil
	}
	text := func(from int) string { return strings.Join(f[from:], " ") }
	switch f[0] {
	case "storages":
		ids, err := pc.StorageIDs()
		if err != nil {
			return err
		}
		var names []string
		for _, id := range ids {
			si, err := pc.StorageInfo(id)
			if err != nil {
				return err
			}
			names = append(names, si.Description)
		}
		if strings.Join(names, " ") != text(1) {
			return fmt.Errorf("storages %v, want %s", names, text(1))
		}
	case "ls":
		if err := need(3); err != nil {
			return err
		}
		at, err := pc.Find(f[1])
		if err != nil {
			return err
		}
		names, err := pc.Names(at)
		if err != nil {
			return err
		}
		for _, n := range names {
			if strings.Contains(n, text(2)) {
				return nil
			}
		}
		return fmt.Errorf("%s has %q, none with %q", f[1], names, text(2))
	case "props":
		if err := need(2); err != nil {
			return err
		}
		return mtpProps(pc, f[1])
	case "get", "partial":
		if err := need(3); err != nil {
			return err
		}
		at, err := pc.Find(f[1])
		if err != nil {
			return err
		}
		if f[0] == "partial" {
			if err := need(4); err != nil {
				return err
			}
			off, err := strconv.ParseInt(f[2], 10, 64)
			if err != nil {
				return err
			}
			b, err := pc.Partial(at.Handle, off, uint32(len(text(3))), true)
			if err != nil {
				return err
			}
			if string(b) != text(3) {
				return fmt.Errorf("%d bytes of %s at %d: %q", len(text(3)), f[1], off, b)
			}
			return nil
		}
		var b bytes.Buffer
		if err := pc.Get(at.Handle, &b); err != nil {
			return err
		}
		if !strings.Contains(b.String(), text(2)) {
			return fmt.Errorf("%s has no %q: %.300s", f[1], text(2), b.String())
		}
	case "put":
		if err := need(3); err != nil {
			return err
		}
		return mtpPut(pc, f[1], []byte(text(2)))
	case "refused":
		if err := need(3); err != nil {
			return err
		}
		return mtpRefused(pc, f[1], f[2], text(3))
	case "event":
		if err := need(2); err != nil {
			return err
		}
		code := map[string]uint16{"ObjectAdded": mtp.EventObjectAdded, "ObjectRemoved": mtp.EventObjectRemoved,
			"ObjectInfoChanged": mtp.EventObjectInfoChanged, "StorageInfoChanged": mtp.EventStorageInfoChanged}[f[1]]
		if code == 0 {
			return fmt.Errorf("no event %q", f[1])
		}
		_, err := pc.WaitEvent(code, 10*time.Second)
		return err
	default:
		return fmt.Errorf("usb-mtp %s?", f[0])
	}
	return nil
}

// mtpPut copies data to p, making the folders on the way that are not
// there yet.
func mtpPut(pc *mtp.Initiator, p string, data []byte) error {
	dir, name := path.Split(strings.TrimSuffix(p, "/"))
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	at, err := pc.Find("/" + parts[0])
	if err != nil {
		return err
	}
	for i, d := range parts[1:] {
		next, err := pc.Find("/" + strings.Join(parts[:i+2], "/"))
		if err != nil {
			h, err := pc.MakeDir(at.Storage, at.Handle, d)
			if err != nil {
				return fmt.Errorf("making %s: %w", d, err)
			}
			next = mtp.Found{Storage: at.Storage, Handle: h}
		}
		at = next
	}
	_, err = pc.Put(at.Storage, at.Handle, name, data)
	return err
}

func mtpRefused(pc *mtp.Initiator, what, p, arg string) error {
	if what == "put" {
		err := mtpPut(pc, p, []byte(arg))
		if err == nil || mtp.Code(err) == 0 {
			return fmt.Errorf("copying %s was not refused: %v", p, err)
		}
		if _, ferr := pc.Find(p); ferr == nil {
			return fmt.Errorf("%s is there after %v", p, err)
		}
		return nil
	}
	at, err := pc.Find(p)
	if err != nil {
		return err
	}
	switch what {
	case "delete":
		err = pc.Delete(at.Handle)
	case "rename":
		err = pc.SetName(at.Handle, arg)
	default:
		return fmt.Errorf("refused %s?", what)
	}
	if mtp.Code(err) != mtp.RespObjectWriteProtected {
		return fmt.Errorf("%s %s: %v, want Object_WriteProtected", what, p, err)
	}
	again, err := pc.Find(p)
	if err != nil || again.Handle != at.Handle {
		return fmt.Errorf("%s is not there as it was after the %s: %v", p, what, err)
	}
	return nil
}

// mtpProps reads a folder as Windows Explorer does and checks what every
// object says of itself.
func mtpProps(pc *mtp.Initiator, p string) error {
	codes, err := pc.PropsSupported(mtp.FormatUndefined)
	if err != nil {
		return err
	}
	for _, code := range codes {
		if _, err := pc.PropDesc(code, mtp.FormatUndefined); err != nil {
			return fmt.Errorf("property 0x%04x: %w", code, err)
		}
	}
	at, err := pc.Find(p)
	if err != nil {
		return err
	}
	hs, err := pc.Handles(at.Storage, 0, at.Handle)
	if err != nil {
		return err
	}
	if len(hs) == 0 {
		return fmt.Errorf("%s is empty", p)
	}
	for _, h := range hs {
		list, err := pc.PropList(h, 0, 0xFFFFFFFF, 0, 0)
		if err != nil {
			return err
		}
		got := map[uint16]any{}
		for _, e := range list {
			got[e.Code] = e.Value
		}
		name, _ := got[mtp.PropObjectFileName].(string)
		uid, _ := got[mtp.PropPersistentUID].([]byte)
		if name == "" || got[mtp.PropStorageID] != uint64(at.Storage) || len(uid) != 16 || got[mtp.PropName] != name {
			return fmt.Errorf("object %d in %s: %v", h, p, got)
		}
		if parent := got[mtp.PropParentObject]; at.Handle != 0xFFFFFFFF && parent != uint64(at.Handle) || at.Handle == 0xFFFFFFFF && parent != uint64(0) {
			return fmt.Errorf("%s's parent is %v", name, parent)
		}
	}
	return nil
}

// mtpDescriptors reads the gadget's descriptors as Windows does (the
// Microsoft OS descriptor) and as libmtp does (the interface's name).
func mtpDescriptors(vendor uint16, wait time.Duration) error {
	u, err := mtp.OpenUSB(vendor, usbgadget.MTPProductID, wait)
	if err != nil {
		return err
	}
	defer u.Close()
	id, err := u.CompatibleID()
	if err != nil {
		return err
	}
	name, err := u.InterfaceName()
	if err != nil {
		return err
	}
	if id != "MTP" || name != "MTP" {
		return fmt.Errorf("compatible ID %q, interface %q", id, name)
	}
	return nil
}

// mtpKernel: FunctionFS where there is a device controller, and on the
// emulator (QEMU's virt board) dummy_hcd, both ends of its cable
// (phone-usb.cfg, phone-usb-sim.cfg). Without dummy_hcd the emulator has
// no controller and require-usb would skip the USB cases unseen.
// FunctionFS is read from the kernel's configuration: its file system
// registers only with the first FunctionFS function.
func (c *Core) mtpKernel() error {
	board, _ := os.ReadFile("/sys/firmware/devicetree/base/compatible")
	var missing []string
	if bytes.Contains(board, []byte("linux,dummy-virt")) && !strings.HasPrefix(c.usb.udc, "dummy_udc") {
		missing = append(missing, "CONFIG_USB_DUMMY_HCD (the emulator has no dummy_udc)")
	}
	if !c.usb.sim {
		switch has, known := kernelOption("CONFIG_USB_CONFIGFS_F_FS"); {
		case !known:
			c.log.Printf("usb: no /proc/config.gz; FunctionFS is checked when file transfer starts")
		case !has:
			missing = append(missing, "CONFIG_USB_CONFIGFS_F_FS")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the kernel lacks %s", strings.Join(missing, ", "))
	}
	if c.usb.sim {
		c.log.Printf("usb: no device controller; file transfer goes over the simulated cable")
	} else {
		c.log.Printf("usb: FunctionFS and the controller %s for file transfer", c.usb.udc)
	}
	return nil
}

// kernelOption: the running kernel's configuration sets option, built in
// or as a module; known is false without /proc/config.gz.
func kernelOption(option string) (has, known bool) {
	f, err := os.Open("/proc/config.gz")
	if err != nil {
		return false, false
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return false, false
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		return false, false
	}
	return bytes.Contains(b, []byte("\n"+option+"=y")) || bytes.Contains(b, []byte("\n"+option+"=m")), true
}

// mtpLibmtp runs libmtp's own tools at the phone, the library Linux file
// managers (gvfs, KIO) reach MTP devices with; libmtp-bin is in the
// emulator's image only. The selftest's computer ends its session and
// lets go of the interface first, as one program claims it at a time;
// its next step opens another. It returns the line the tool said text in
// (get: the bytes it copied).
func (c *Core) mtpLibmtp(f []string, wait, run time.Duration) (string, error) {
	m := c.usb.mtp
	if m == nil || len(f) < 2 {
		return "", errors.New("usb-mtp libmtp detect|files <text> | get <path> <text>, with file transfer on")
	}
	var tool, want, local string
	var args []string
	switch f[0] {
	case "detect", "files":
		tool, want = "mtp-"+f[0], strings.Join(f[1:], " ")
	case "get":
		if len(f) < 3 {
			return "", errors.New("usb-mtp libmtp get <path> <text>")
		}
		pc, err := c.mtpComputer(wait)
		if err != nil {
			return "", err
		}
		at, err := pc.Find(f[1])
		if err != nil {
			return "", err
		}
		tmp, err := os.CreateTemp("", "mtp-getfile-*")
		if err != nil {
			return "", err
		}
		local = tmp.Name()
		tmp.Close()
		defer os.Remove(local)
		tool, want, args = "mtp-getfile", strings.Join(f[2:], " "), []string{strconv.FormatUint(uint64(at.Handle), 10), local}
	default:
		return "", fmt.Errorf("usb-mtp libmtp %s?", f[0])
	}
	bin, err := exec.LookPath(tool)
	if err != nil {
		return "", fmt.Errorf("libmtp's %s (libmtp-bin, in the emulator's image): %w", tool, err)
	}
	if m.pc != nil {
		m.pc.CloseSession()
		m.pc.Close()
		m.pc = nil
	}
	// libmtp resets a device it takes for Android (DeviceInfo names
	// android.com) when it lets go of it: the last tool's reset may still
	// be under way.
	for end := time.Now().Add(wait); mtp.FindUSB(c.usb.cfg.VendorID, usbgadget.MTPProductID) == ""; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(end) {
			return "", errors.New("the computer does not see the phone as an MTP device")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), run)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	got := string(out)
	if local != "" {
		b, _ := os.ReadFile(local)
		got = string(b)
	}
	if !strings.Contains(got, want) {
		return "", fmt.Errorf("%s (%v): no %q; it printed %.800s", tool, err, want, out)
	}
	if local != "" {
		return fmt.Sprintf("%s copied %d bytes: %q", tool, len(got), got), nil
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, want) {
			return tool + ": " + strings.TrimSpace(l), nil
		}
	}
	return tool, nil
}

func (r *Runner) mtpGone() error {
	c := r.C
	if c.usb.mtp != nil {
		return errors.New("file transfer is still on")
	}
	if c.usb.sim {
		return nil
	}
	return r.offLoop(func() error {
		end := time.Now().Add(r.scaled(5 * time.Second))
		for mtp.FindUSB(c.usb.cfg.VendorID, usbgadget.MTPProductID) != "" {
			if time.Now().After(end) {
				return errors.New("the computer still sees the phone as an MTP device")
			}
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	})
}
