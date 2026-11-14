package shell

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kario-os/kario/hal/usbgadget"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/usbshare"
)

// USB to a computer, the way an iPhone connects (27-usb-typec.md): the
// phone is a USB network device from boot (hal/usbgadget), but its side of
// the link has no address until the user trusts the computer on the
// phone, so nothing on the phone is reachable over the cable before. A
// charger or a computer that has not configured the device is only power.
// Trusted, the computer gets an address over DHCP (systemd-networkd's
// server on the link) and reaches file sharing (usbshare) with a password
// the phone shows; unplugging ends it and forgets the trust. The sharing
// server runs in a sandboxed helper process (usbhelper.go), the library
// here; file transfer over MTP, the other choice, is usbmtp.go. In
// developer mode the same link reaches the debug shell (SSH), as Xcode's
// tunnel reaches a phone in Developer Mode.

const (
	usbAddr    = "10.71.0.1"
	usbPrefix  = 24
	usbNetFile = "10-kario-usb.network"
	// usbComputerNetFile: the emulator's computer end (usbUnmanaged).
	usbComputerNetFile = "10-kario-usb-computer.network"
)

type usbState struct {
	udc     string
	cfg     usbgadget.Config
	sim     bool   // no controller: the developer machine, driven by the selftest
	simOn   bool   // the simulated cable
	state   string // the controller's last state
	trusted bool
	prompt  *confirmOverlay // asking whether to trust it
	pass    string
	ln      *net.TCPListener // sharing's socket, kept while the helper restarts
	addr    string           // where sharing listens
	net     string           // the runtime network file written for the link
	added   int              // files the computer added
	// helper serves the computer (nil while it restarts); last is the
	// latest, ended or not; restarts counts its starts after crashes.
	helper, last *usbHelper
	restarts     int
	// File transfer (usbmtp.go): the MTP function while it is in use;
	// switchBy, the gadget coming back as an MTP device by then;
	// waitUnlock, a computer waiting for the phone to be unlocked.
	mtp        *usbMTP
	switchBy   time.Time
	waitUnlock bool
	// resetBy: a bus reset under way (usbReset), configured again by then.
	resetBy time.Time
}

// startUSB sets up the gadget where there is a device controller. On the
// emulator the controller is dummy_hcd's, whose cable would be plugged in
// from boot: there it stays unbound until the selftest plugs it.
func (c *Core) startUSB() {
	c.usb.cfg = usbgadget.Default(c.deviceSerial())
	udcs := usbgadget.UDCs()
	if len(udcs) == 0 {
		c.usb.sim = true
		return
	}
	c.usb.udc = udcs[0]
	c.usbFirewall(false)
	c.usbUnmanaged()
	if strings.HasPrefix(c.usb.udc, "dummy_udc") {
		// Unplugged at every start, whatever an earlier run left.
		_ = os.WriteFile(filepath.Join("/sys/kernel/config/usb_gadget", c.usb.cfg.Name, "UDC"), []byte("\n"), 0o644)
		c.log.Printf("usb: %s (the emulator's cable, unplugged until the selftest plugs it)", c.usb.udc)
		return
	}
	if _, err := usbgadget.Setup(c.usb.cfg); err != nil {
		c.log.Printf("usb: %v", err)
		return
	}
	c.log.Printf("usb: a network device on %s", c.usb.udc)
}

func (c *Core) deviceSerial() string {
	if b, err := os.ReadFile("/etc/machine-id"); err == nil && len(b) >= 12 {
		return strings.ToUpper(strings.TrimSpace(string(b))[:12])
	}
	return "KARIO0000000"
}

// PlugUSB plugs the simulated cable in or out (selftest): the emulator's
// gadget bound to dummy_hcd or not; on the developer machine, a state.
func (c *Core) PlugUSB(on bool) error {
	if c.usb.sim {
		c.usb.simOn = on
		c.checkUSB()
		return nil
	}
	if !on {
		g := filepath.Join("/sys/kernel/config/usb_gadget", c.usb.cfg.Name, "UDC")
		_ = os.WriteFile(g, []byte("\n"), 0o644)
		endComputer()
		c.checkUSB()
		return nil
	}
	if _, err := usbgadget.Setup(c.usb.cfg); err != nil {
		return err
	}
	go attachComputer(c.usb.cfg.Serial, computerGen())
	c.checkUSB()
	return nil
}

func (c *Core) usbState() string {
	if c.usb.sim {
		if c.usb.simOn {
			return "configured"
		}
		return "not attached"
	}
	if c.usb.udc == "" {
		return ""
	}
	// A gadget bound to no controller talks to no computer, whatever the
	// controller says (dummy_hcd keeps its last state).
	if b, err := os.ReadFile(filepath.Join("/sys/kernel/config/usb_gadget", c.usb.cfg.Name, "UDC")); err != nil || strings.TrimSpace(string(b)) == "" {
		return "not attached"
	}
	return usbgadget.State(c.usb.udc)
}

// checkUSB runs with the housekeeping: a computer that configured the
// device is asked about; one that went away ends sharing.
func (c *Core) checkUSB() {
	st := c.usbState()
	if c.usbSwitching(st) || c.usbReset(st) {
		return
	}
	c.usbUnlocked(st)
	if st == c.usb.state {
		return
	}
	prev := c.usb.state
	c.usb.state = st
	if prev != "" || st != "" {
		c.log.Printf("usb: %s", st)
	}
	switch st {
	case "configured":
		if !c.usb.trusted && c.usb.prompt == nil {
			c.askTrust()
		}
	case "suspended":
		// The computer sleeps: sharing stays for when it wakes.
	default:
		c.endUSB()
	}
}

// usbReset: a bus reset takes the device through default and addressed
// (Windows resets after an error; libmtp resets a phone it takes for
// Android when it lets go): the computer is still there and enumerates
// it again, so sharing and the trust stay, unless it is not configured
// again within usbSwitchWait.
func (c *Core) usbReset(st string) bool {
	if c.usb.resetBy.IsZero() {
		if !c.usb.trusted || c.usb.state != "configured" || st != "default" && st != "addressed" {
			return false
		}
		c.usb.resetBy = time.Now().Add(usbSwitchWait)
		c.log.Printf("usb: %s (a bus reset); sharing stays while the computer enumerates the phone again", st)
		return true
	}
	if st == "configured" {
		c.usb.resetBy = time.Time{}
		return true
	}
	if (st == "default" || st == "addressed") && time.Now().Before(c.usb.resetBy) {
		return true
	}
	c.usb.resetBy = time.Time{}
	return false
}

func (c *Core) askTrust() {
	if c.usbTrustWaits() {
		return
	}
	c.Wake("usb")
	c.usb.prompt = &confirmOverlay{
		text: tr.T("Trust this computer? It will be able to copy your photos, videos, music and files, and add to them. Unplugging the cable ends it."),
		yes:  tr.T("Trust"), no: tr.T("Don't trust"),
		onYes: func() {
			c.usb.prompt = nil
			if c.usbState() != "configured" {
				return
			}
			if err := c.shareUSB(); err != nil {
				c.log.Printf("usb sharing: %v", err)
				c.showToast(tr.T("File sharing did not start: %s", err.Error()))
			}
		},
		onNo: func() {
			c.usb.prompt = nil
			c.log.Printf("usb: the user did not trust the computer; charging only")
		},
	}
	c.pushOverlay(c.usb.prompt)
}

// shareUSB: the trust given, the link gets its address and sharing starts.
func (c *Core) shareUSB() error {
	if c.usbMode() == ui.USBModeMTP {
		return c.shareMTP()
	}
	c.usb.trusted = true
	c.usb.pass = usbshare.Password()
	addr := "127.0.0.1:0"
	if !c.usb.sim {
		c.usbFirewall(true)
		if err := c.usbNetwork(true); err != nil {
			return err
		}
		addr = fmt.Sprintf("%s:80", usbAddr)
	}
	ln, err := listenWhenUp(addr, 15*time.Second)
	if err != nil {
		c.usbNetwork(false)
		return err
	}
	c.usb.ln = ln.(*net.TCPListener)
	c.usb.addr = ln.Addr().String()
	c.usb.restarts = 0
	if err := c.startUSBHelper(); err != nil {
		ln.Close()
		c.usb.ln = nil
		c.usbNetwork(false)
		return err
	}
	host := usbAddr
	if c.usb.sim {
		host = c.usb.addr
	}
	c.log.Printf("usb sharing: on http://%s/", c.usb.addr)
	c.addNotif(c.sys, notifUSB(host, c.usb.pass))
	return nil
}

func notifUSB(host, pass string) ui.Notification {
	return ui.Notification{ID: "usb-sharing", Title: tr.T("Computer password %s", pass),
		Body: tr.T("Sharing files: on the computer open http://%s, or connect to it as a network drive", host)}
}

// listenWhenUp retries while the link's address is being configured.
func listenWhenUp(addr string, wait time.Duration) (net.Listener, error) {
	end := time.Now().Add(wait)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil || time.Now().After(end) {
			return ln, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// usbNetwork gives the link its address and DHCP server (a runtime
// systemd-networkd file, gone at the next boot) or takes them away.
func (c *Core) usbNetwork(on bool) error {
	dir := "/run/systemd/network"
	path := filepath.Join(dir, usbNetFile)
	ifname := usbgadget.Interface(c.usb.cfg)
	if on {
		conf := fmt.Sprintf("[Match]\nName=%s\n\n[Network]\nAddress=%s/%d\nDHCPServer=yes\nLinkLocalAddressing=no\nIPv6AcceptRA=no\n\n[DHCPServer]\nPoolOffset=10\nPoolSize=4\nEmitDNS=no\nEmitRouter=no\n", ifname, usbAddr, usbPrefix)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
			return err
		}
		c.usb.net = path
	} else {
		if c.usb.net == "" {
			return nil
		}
		_ = os.WriteFile(path, []byte(unmanagedConf("Name", ifname)), 0o644)
		c.usb.net = ""
		// networkd leaves an unmanaged link as it was: the trust ends with
		// its address.
		defer func() {
			_ = exec.Command("ip", "addr", "flush", "dev", ifname).Run()
			_ = exec.Command("ip", "link", "set", ifname, "down").Run()
		}()
	}
	if out, err := exec.Command("networkctl", "reload").CombinedOutput(); err != nil {
		return fmt.Errorf("networkctl reload: %v: %s", err, out)
	}
	_ = exec.Command("networkctl", "reconfigure", ifname).Run()
	return nil
}

// usbUnmanaged keeps networkd off both ends of the cable until they are
// used: the phone's until a computer is trusted, whatever catch-all
// .network the system has (QEMU's ip=dhcp makes systemd-network-generator
// write one for every interface), and on the emulator the computer's,
// which the selftest brings up in a namespace of its own. The device
// announces its connection only when it changes, and usbnet takes the
// announcement only while the interface is open: brought up by networkd
// first and then moved, the computer's end stays without carrier.
func (c *Core) usbUnmanaged() {
	dir := "/run/systemd/network"
	files := map[string]string{usbNetFile: unmanagedConf("Name", usbgadget.Interface(c.usb.cfg))}
	if strings.HasPrefix(c.usb.udc, "dummy_udc") {
		_, host := usbgadget.MACs(c.usb.cfg.Serial)
		files[usbComputerNetFile] = unmanagedConf("MACAddress", host)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.log.Printf("usb: %v", err)
		return
	}
	for name, conf := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(conf), 0o644); err != nil {
			c.log.Printf("usb: %v", err)
		}
	}
	_ = exec.Command("networkctl", "reload").Run()
}

func unmanagedConf(key, value string) string {
	return fmt.Sprintf("[Match]\n%s=%s\n\n[Link]\nUnmanaged=yes\n", key, value)
}

// endUSB: the cable is out (or only power): sharing stops, the trust goes.
func (c *Core) endUSB() { c.endSharing("the cable is out") }

func (c *Core) endSharing(why string) {
	if c.usb.prompt != nil {
		c.popOverlay(c.usb.prompt)
		c.usb.prompt = nil
	}
	if !c.usb.trusted {
		return
	}
	c.usb.trusted = false
	c.stopUSBHelper()
	c.endMTP()
	if c.usb.ln != nil {
		c.usb.ln.Close()
		c.usb.ln = nil
	}
	if err := c.usbNetwork(false); err != nil {
		c.log.Printf("usb: %v", err)
	}
	c.usbFirewall(false)
	out := c.notifs[:0]
	for _, n := range c.notifs {
		if n.app != c.sys || n.n.ID != "usb-sharing" {
			out = append(out, n)
		}
	}
	c.notifs = out
	c.log.Printf("usb sharing: ended (%s)", why)
	c.showToast(tr.T("Stopped sharing with the computer"))
}

// USBSharing is where sharing listens and its password ("" when off).
func (c *Core) USBSharing() (addr, pass string) {
	if !c.usb.trusted || c.usb.ln == nil {
		return "", ""
	}
	return c.usb.addr, c.usb.pass
}

// usbLibrary is what the computer sees: the folders people copy to and
// from (shown in the device's language), each the system's own store, so
// what the computer adds is indexed as if saved on the phone: photos and
// videos (the gallery's library), music (the music library, folders kept:
// an album folder stays an album) and the user's files. The media and file
// stores are the shell loop's; music's copies run outside it. The helper
// reaches it through usbshare.Helper, which checks what it asks first;
// Open returns files opened read-only, and Save reads its copy to the end.
type usbLibrary struct{ c *Core }

func (l usbLibrary) call(f func()) {
	done := make(chan struct{})
	l.c.Post(func() { f(); close(done) })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// Largest video the computer may add.
const maxUSBVideo = 4 << 30

// Folders are named in English whatever the phone's language, as Android's
// DCIM and Music are: a computer's scripts and sync tools find them by name,
// and a name that changed with the language would lose them.
func (l usbLibrary) Folders() []usbshare.Folder {
	var st syscall.Statfs_t
	_ = syscall.Statfs(l.c.cfg.StateDir, &st)
	f := func(id, name string, extra usbshare.Folder) usbshare.Folder {
		extra.ID, extra.Name, extra.Path, extra.Writable = id, name, name, true
		extra.Capacity, extra.Free = int64(st.Blocks)*st.Bsize, int64(st.Bavail)*st.Bsize
		return extra
	}
	return []usbshare.Folder{
		f("photos", "Photos", usbshare.Folder{MaxBytes: maxPhotoBytes, Accepts: []string{".jpg", ".jpeg", ".png"}}),
		f("videos", "Videos", usbshare.Folder{MaxBytes: maxUSBVideo, Accepts: []string{".webm"}}),
		f("music", "Music", usbshare.Folder{Nested: true, MaxBytes: maxMusicFile}),
		f("files", "Files", usbshare.Folder{MaxBytes: maxFileBytes}),
	}
}

func (l usbLibrary) List(folder string) []usbshare.Item {
	if folder == "music" {
		out, _ := l.ListDir(folder, "")
		return out
	}
	var out []usbshare.Item
	l.call(func() {
		switch folder {
		case "photos", "videos":
			for _, it := range l.c.media.items {
				if it.video() == (folder == "videos") {
					out = append(out, usbshare.Item{Name: mediaFileName(it), Size: it.Size, ModTime: time.Unix(it.At, 0)})
				}
			}
		case "files":
			for _, f := range l.c.files.Items {
				out = append(out, usbshare.Item{Name: f.Name, Mime: f.Mime, Size: f.Size, ModTime: time.Unix(f.At, 0)})
			}
		}
	})
	return out
}

// ListDir and MakeDir: the folders inside Music (usbshare.Dirs).
func (l usbLibrary) ListDir(folder, dir string) ([]usbshare.Item, bool) {
	if folder != "music" {
		return nil, false
	}
	ents, ok := l.c.music().usbList(dir)
	if !ok {
		return nil, false
	}
	out := []usbshare.Item{}
	for _, e := range ents {
		out = append(out, usbshare.Item{Name: e.name, Dir: e.dir, Size: e.size, ModTime: e.mod, Mime: mime.TypeByExtension(strings.ToLower(filepath.Ext(e.name)))})
	}
	return out, true
}

func (l usbLibrary) MakeDir(folder, dir string) error {
	if folder != "music" {
		return errors.New("read-only")
	}
	return l.c.music().usbMakeDir(dir)
}

// mediaFileName is a photo's or video's name with its extension, as a
// computer expects a file to be called.
func mediaFileName(it *mediaItem) string {
	ext := "." + it.ext()
	if strings.HasSuffix(strings.ToLower(it.Name), ext) {
		return it.Name
	}
	return it.Name + ext
}

func (l usbLibrary) Open(folder, name string) (io.ReadSeekCloser, usbshare.Item, error) {
	if folder == "music" {
		f, fi, err := l.c.music().usbOpen(name)
		if err != nil {
			return nil, usbshare.Item{}, err
		}
		return f, usbshare.Item{Name: fi.Name(), Size: fi.Size(), ModTime: fi.ModTime(), Mime: mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))}, nil
	}
	var path string
	var item usbshare.Item
	l.call(func() {
		switch folder {
		case "photos", "videos":
			for _, it := range l.c.media.items {
				if it.video() == (folder == "videos") && mediaFileName(it) == name {
					path, item = l.c.media.path(it), usbshare.Item{Name: mediaFileName(it), Size: it.Size, ModTime: time.Unix(it.At, 0)}
				}
			}
		case "files":
			for _, f := range l.c.files.Items {
				if f.Name == name {
					path, item = l.c.files.path(f), usbshare.Item{Name: f.Name, Mime: f.Mime, Size: f.Size, ModTime: time.Unix(f.At, 0)}
				}
			}
		}
	})
	if path == "" {
		return nil, item, os.ErrNotExist
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, item, err
	}
	return f, item, nil
}

// freeMediaName is a photo or video name not yet in the library ("name
// (2)" otherwise); called on the loop.
func (l usbLibrary) freeMediaName(stem string) string {
	taken := map[string]bool{}
	for _, it := range l.c.media.items {
		taken[it.Name] = true
	}
	got := stem
	for i := 2; taken[got]; i++ {
		got = fmt.Sprintf("%s (%d)", stem, i)
	}
	return got
}

func (l usbLibrary) Save(folder, name string, r io.Reader) (string, error) {
	switch folder {
	case "music":
		got, err := l.c.music().usbSave(name, r)
		if err == nil {
			l.c.Post(func() { l.c.log.Printf("usb sharing: music %s added", got) })
		}
		return got, err
	case "photos":
		return l.savePhoto(name, r)
	case "videos":
		return l.saveVideo(name, r)
	case "files":
	default:
		return "", errors.New("read-only")
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxFileBytes+1))
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxFileName {
		return "", fmt.Errorf("a file name of 1 to %d bytes", maxFileName)
	}
	var got string
	l.call(func() {
		taken := map[string]bool{}
		for _, f := range l.c.files.Items {
			taken[f.Name] = true
		}
		got = name
		ext := filepath.Ext(name)
		for i := 2; taken[got]; i++ {
			got = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
		}
		_, err = l.c.files.add(got, raw, l.c.now())
	})
	return got, err
}

// savePhoto adds a JPEG or PNG to the library, as the camera would.
func (l usbLibrary) savePhoto(name string, r io.Reader) (string, error) {
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		return "", errors.New(tr.T("Photos takes JPEG and PNG pictures"))
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxPhotoBytes+1))
	if err != nil {
		return "", err
	}
	stem := strings.TrimSpace(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))
	if stem == "" || strings.HasPrefix(stem, ".") {
		return "", errMusicName()
	}
	var got string
	l.call(func() {
		var it *mediaItem
		if it, err = l.c.media.add(raw, l.freeMediaName(stem), l.c.now()); err == nil {
			got = mediaFileName(it)
		}
	})
	return got, err
}

// saveVideo adds a WebM video: copied next to the library outside the
// loop, then put in place of a new, empty entry, and probed like a video
// saved on the phone.
func (l usbLibrary) saveVideo(name string, r io.Reader) (string, error) {
	if !playableVideo(name) {
		return "", errors.New(tr.T("Videos takes WebM videos"))
	}
	stem := strings.TrimSpace(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))
	if stem == "" || strings.HasPrefix(stem, ".") {
		return "", errMusicName()
	}
	var dir string
	l.call(func() { dir = l.c.media.dir })
	tmp, err := os.CreateTemp(dir, ".copy-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	empty, err := os.CreateTemp(dir, ".empty-*")
	if err != nil {
		return "", err
	}
	empty.Close()
	defer os.Remove(empty.Name())
	var it *mediaItem
	var dst string
	l.call(func() {
		if it, err = l.c.media.addVideo(empty.Name(), l.freeMediaName(stem), "webm", l.c.now()); err == nil {
			dst = l.c.media.path(it)
		}
	})
	if err != nil || it == nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	var got string
	l.call(func() {
		it.Size = n
		l.c.media.save()
		l.c.probeVideos()
		got = mediaFileName(it)
	})
	return got, nil
}
