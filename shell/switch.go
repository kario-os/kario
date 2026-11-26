package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/switchrecv"
	"github.com/kario-os/kario/ui"
)

// Moving from an old phone (Settings › My device › Move from an old phone;
// switch.state, switch.start, switch.stop, switch.undo): the receiver of
// kario-switch/1 (package switchrecv, docs/36). Start listens on TCP 47800
// on the address of the Wi-Fi network the phone is on, whose name the code
// carries for the old phone to join, or on the personal hotspot's, turning
// the hotspot on when Wi-Fi is not connected or the user asked for it (and
// off again at the end); on no other interface, and a connection from
// outside that network is dropped. A session whose network goes (the
// Wi-Fi disconnected, on another network or with another address, the
// hotspot off) ends, saying why. Settings shows the session's pairing URL
// as a QR code on a
// secure surface. What arrives goes where the phone keeps it: contacts
// into the Shell's store, messages to phone-smsd, calls into telephonyd's
// log, calendar events as one .ics file in Files (there is no calendar
// app), photos and videos into Files. The session's record (switch.json)
// keeps the IDs of what it added, never content, so undo takes out exactly
// that. The keys are in memory only; the session ends with done, after 30
// minutes without a request or when the user stops it, and the listener
// and a hotspot it turned on go with it. The log gets counts. A move over
// Bluetooth (switch_bt.go) reads a paired phone instead and keeps the same
// record.

type switchShell struct {
	state string // "" (idle), ui.SwitchStarting, ui.SwitchWaiting; receiving comes from sess
	// gen counts starts: a stop while one is under way voids it.
	gen  int
	sess *switchrecv.Session
	srv  *http.Server
	// bt is a move over Bluetooth instead (switch_bt.go).
	bt    *btMove
	kinds []string
	code  string
	addr  string
	// network and pass: the hotspot the old phone joins ("" on Wi-Fi);
	// onHotspot: the receiver listens on it, ours: this session turned it
	// on (and turns it off). wifi: the Wi-Fi network it listens on
	// instead, with the phone's address there when it started.
	network, pass   string
	onHotspot, ours bool
	wifi            wifiLink
	// err: why the last start or undo failed, or why the last session
	// ended when its network went (lost, ui.SwitchLost*).
	err, lost string
	// rec is the session's record, last the import that can be undone.
	rec, last *switchRecord
	loaded    bool
	// cal is the session's calendar file in Files, events what it holds.
	cal    *fileItem
	events []string
	// anyPort: listen on a free port, not 47800 (tests).
	anyPort bool
	posted  atomic.Bool
}

// switchRecord is what an import added, by kind and by ID.
type switchRecord struct {
	At       int64          `json:"at"`
	Ended    string         `json:"ended,omitempty"`
	Device   string         `json:"device,omitempty"`
	Added    map[string]int `json:"added"`
	Failed   int            `json:"failed"`
	Contacts []string       `json:"contacts,omitempty"`
	SMS      []string       `json:"sms,omitempty"`
	Calls    []string       `json:"calls,omitempty"`
	Files    []string       `json:"files,omitempty"`
	// Calendar is the .ics file the calendar app imported.
	Calendar string `json:"calendar,omitempty"`
	Undone   bool   `json:"undone,omitempty"`
	// Notes: over Bluetooth, the kinds that did not all come and why
	// (ui.SwitchNote*).
	Notes map[string]string `json:"notes,omitempty"`
}

func (r *switchRecord) empty() bool {
	return len(r.Contacts)+len(r.SMS)+len(r.Calls)+len(r.Files) == 0
}

func (c *Core) switchPath() string { return filepath.Join(c.cfg.StateDir, "shell", "switch.json") }

// switchStaging is where files are staged as they arrive: inside Files'
// directory, so taking one in is a rename.
func (c *Core) switchStaging() string { return filepath.Join(c.files.dir, ".switch") }

func (c *Core) switchLast() *switchRecord {
	w := &c.sw
	if !w.loaded {
		w.loaded = true
		if raw, err := os.ReadFile(c.switchPath()); err == nil {
			var r switchRecord
			if json.Unmarshal(raw, &r) == nil && !r.empty() {
				w.last = &r
			}
		}
	}
	return w.last
}

func (c *Core) saveSwitch() {
	r := c.sw.last
	if r == nil {
		_ = os.Remove(c.switchPath())
		return
	}
	raw, _ := json.Marshal(r)
	if err := contacts.WriteFile(c.switchPath(), raw); err != nil {
		c.log.Printf("switch: %v", err)
	}
}

func (b *broker) dispatchSwitch(m ui.Msg, reply func(any, error)) {
	c := b.c
	switch m.Cap {
	case ui.CapSwitchStart:
		var args ui.SwitchStartArgs
		_ = json.Unmarshal(m.Args, &args)
		if args.Bluetooth != "" {
			c.switchStartBT(args.Bluetooth, args.Kinds, func(err error) { reply(c.switchState(), err) })
			return
		}
		c.switchStart(args.Kinds, args.Hotspot, func(err error) { reply(c.switchState(), err) })
	case ui.CapSwitchStop:
		c.switchStop(ui.SwitchEndedStopped)
		reply(c.switchState(), nil)
	case ui.CapSwitchUndo:
		c.switchUndo(func(err error) { reply(c.switchState(), err) })
	default:
		reply(c.switchState(), nil)
	}
}

func (c *Core) switchState() ui.SwitchState {
	w := &c.sw
	st := ui.SwitchState{State: firstNonEmpty(w.state, ui.SwitchIdle), Error: w.err, Lost: w.lost}
	if w.sess != nil {
		p := w.sess.Progress()
		if p.State == switchrecv.StateReceiving {
			st.State = ui.SwitchReceiving
		}
		st.Kinds, st.Code, st.Address, st.Network, st.Passphrase = w.sess.Accept(), w.code, w.addr, w.network, w.pass
		st.Wifi = w.wifi.ssid
		st.Device = deviceLabel(p.Device)
		for _, k := range p.Kinds {
			st.Progress = append(st.Progress, ui.SwitchProgress{Kind: k.Kind, Expected: k.Expected, Stored: k.Stored, Skipped: k.Skipped, Failed: k.Failed})
		}
	}
	if mv := w.bt; mv != nil {
		st.State, st.Progress = mv.snapshot()
		st.Kinds, st.Bluetooth, st.Device = mv.kinds, mv.name, mv.name
	}
	if r := c.switchLast(); r != nil && w.sess == nil && w.bt == nil {
		sum := &ui.SwitchSummary{At: r.At, Ended: r.Ended, Device: r.Device, Failed: r.Failed, Undone: r.Undone}
		for _, k := range ui.SwitchKinds {
			if n := r.Added[k]; n > 0 || r.Notes[k] != "" {
				sum.Added = append(sum.Added, ui.SwitchProgress{Kind: k, Stored: n, Note: r.Notes[k]})
			}
		}
		st.Last = sum
	}
	return st
}

func deviceLabel(d switchrecv.Device) string {
	name := strings.TrimSpace(d.Brand + " " + d.Model)
	if d.OS != "" {
		name = strings.TrimSpace(name + " · " + strings.TrimSpace(d.OS+" "+d.OSVersion))
	}
	return name
}

// switchKinds are the kinds asked for that the phone can take now: a
// phone without phone-smsd or telephonyd takes no messages or calls.
func (c *Core) switchKinds(asked []string) []string {
	var out []string
	for _, k := range ui.SwitchKinds {
		switch {
		case !slices.Contains(asked, k):
		case k == ui.SwitchSMS && !c.sms.up, k == ui.SwitchCallLog && c.tel == nil:
		default:
			out = append(out, k)
		}
	}
	return out
}

// switchStart brings the receiver up: on the Wi-Fi network the phone is
// on (connected, with an IPv4 address), or on the personal hotspot, turned
// on for it if need be; done runs once the code can be shown. hotspot asks
// for the hotspot even on Wi-Fi, and moves a session that is still
// waiting on Wi-Fi there: on networks that keep their devices apart the
// old phone cannot reach the Wi-Fi address.
func (c *Core) switchStart(asked []string, hotspot bool, done func(error)) {
	w := &c.sw
	if hotspot && w.sess != nil && !w.onHotspot && w.sess.Progress().State == switchrecv.StateWaiting {
		c.switchStop(ui.SwitchEndedStopped)
	}
	kinds := c.switchKinds(asked)
	switch {
	case w.state != "" || w.sess != nil || w.bt != nil:
		done(errors.New(tr.T("A move from an old phone is under way already")))
		return
	case len(kinds) == 0:
		done(errors.New(tr.T("Tick what to move first")))
		return
	case c.wifi == nil:
		done(errors.New(tr.T("This device has no Wi-Fi")))
		return
	}
	w.state, w.err, w.lost, w.kinds = ui.SwitchStarting, "", "", kinds
	w.gen++
	gen, wifi := w.gen, c.wifi
	fail := func(err error) {
		if w.gen == gen {
			w.state, w.err, w.wifi = "", err.Error(), wifiLink{}
			if w.ours {
				c.switchHotspotOff()
			}
		}
		done(err)
	}
	go func() {
		l, _ := wifi.link()
		c.Post(func() {
			if w.gen != gen {
				done(errors.New(ui.ErrCancelled))
				return
			}
			_, sim := wifi.(*simWifi)
			switch {
			case !hotspot && l.addr != "" && !c.hotspot.on:
				w.network, w.pass, w.onHotspot, w.ours, w.wifi = "", "", false, false, l
				host := l.addr
				if sim {
					host = "127.0.0.1"
				}
				c.switchListen(gen, host, done, fail)
			case c.hotspot.on:
				w.onHotspot, w.ours = true, false
				c.switchOnHotspot(gen, sim, done, fail)
			default:
				on := true
				c.setHotspot(ui.HotspotArgs{Enabled: &on}, func(err error) {
					if w.gen != gen {
						if err == nil {
							c.switchHotspotOff()
						}
						done(errors.New(ui.ErrCancelled))
						return
					}
					if err != nil {
						fail(err)
						return
					}
					w.onHotspot, w.ours = true, true
					if err := contacts.WriteFile(c.switchHotspotMark(), nil); err != nil {
						c.log.Printf("switch: %v", err)
					}
					c.switchOnHotspot(gen, sim, done, fail)
				})
			}
		})
	}()
}

func (c *Core) switchOnHotspot(gen int, sim bool, done, fail func(error)) {
	w := &c.sw
	w.network, w.pass = c.settings.HotspotSSID, c.settings.HotspotPass
	host := "127.0.0.1"
	if !sim {
		host = ipv4Of(c.hotspot.iface)
	}
	if host == "" {
		fail(errors.New(tr.T("The hotspot has no address yet; try again")))
		return
	}
	c.switchListen(gen, host, done, fail)
}

// switchHotspotMark is there while a session has the hotspot on that it
// turned on: a Shell that ends meanwhile leaves it, the next one turns the
// hotspot off (switchAtBoot).
func (c *Core) switchHotspotMark() string {
	return filepath.Join(c.cfg.StateDir, "shell", "switch-hotspot")
}

// switchAtBoot clears what a Shell that ended during a session left: the
// hotspot it turned on, the files half received. Before the hotspot comes
// up again.
func (c *Core) switchAtBoot() {
	_ = os.RemoveAll(c.switchStaging())
	if err := os.Remove(c.switchHotspotMark()); err == nil && c.settings.Hotspot {
		c.log.Printf("switch: the hotspot a move turned on goes off")
		c.settings.Hotspot = false
		c.saveSettings()
	}
}

// switchHotspotOff turns off a hotspot the session turned on.
func (c *Core) switchHotspotOff() {
	c.sw.ours = false
	_ = os.Remove(c.switchHotspotMark())
	if !c.settings.Hotspot {
		return
	}
	off := false
	c.setHotspot(ui.HotspotArgs{Enabled: &off}, func(err error) {
		if err != nil {
			c.log.Printf("switch: hotspot off: %v", err)
		}
	})
}

// switchListen opens the session on host and serves it there.
func (c *Core) switchListen(gen int, host string, done, fail func(error)) {
	w := &c.sw
	allow := subnetOf(net.ParseIP(host))
	if allow == nil {
		fail(fmt.Errorf(tr.T("No network on %s"), host))
		return
	}
	port := switchrecv.Port
	if w.anyPort {
		port = 0
	}
	ln, err := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		c.log.Printf("switch: %v", err)
		fail(errors.New(tr.T("Cannot receive on port %d: %v", port, err)))
		return
	}
	port = ln.Addr().(*net.TCPAddr).Port
	staging := c.switchStaging()
	_ = os.RemoveAll(staging)
	rcv := switchrecv.NewReceiver(c.log.Printf)
	rec := &switchRecord{At: c.now().Unix(), Added: map[string]int{}}
	sess, err := rcv.Open(switchrecv.Config{Name: tr.T("My %s", Brand), Host: host, Port: port, SSID: w.network, PSK: w.pass,
		Wifi: w.wifi.ssid, Accept: w.kinds, Sink: &switchSink{c: c, rec: rec}, Dir: staging, MaxFile: maxSwitchFile, OnChange: c.switchChanged})
	if err != nil {
		ln.Close()
		fail(err)
		return
	}
	srv := rcv.Server()
	go func() { _ = srv.Serve(switchListener{Listener: ln, allow: allow}) }()
	w.state, w.sess, w.srv, w.rec, w.cal, w.events = ui.SwitchWaiting, sess, srv, rec, nil, nil
	w.code, w.addr = sess.Pairing().URL(), net.JoinHostPort(host, strconv.Itoa(port))
	c.log.Printf("switch: receiving %s on %s (hotspot %v)", strings.Join(w.kinds, ","), w.addr, w.onHotspot)
	go c.switchWatch(sess, c.wifi, w.wifi)
	done(nil)
}

// maxSwitchFile bounds one photo or video from an old phone, as USB's
// videos are.
const maxSwitchFile = maxUSBVideo

// switchWatchEvery is how often a session looks at its network.
var switchWatchEvery = 2 * time.Second

// switchWatch ends the session's part on the Shell when it ends, and ends
// the session when the network it listens on goes: the hotspot switched
// off (or airplane mode), or the Wi-Fi it was on (on, an address: the
// session is on Wi-Fi) disconnected, on another network or with another
// address (switchLook).
func (c *Core) switchWatch(sess *switchrecv.Session, wifi wifiRadio, on wifiLink) {
	tick := time.NewTicker(switchWatchEvery)
	defer tick.Stop()
	down := 0
	for {
		select {
		case <-sess.Done():
			c.Post(func() { c.switchEnded(sess) })
			return
		case <-tick.C:
		}
		var now wifiLink
		gone := false
		if on.addr != "" {
			var err error
			now, err = wifi.link()
			gone, down = switchLook(on, now, err, down)
		}
		c.Post(func() {
			switch {
			case c.sw.sess != sess:
			case c.sw.onHotspot && !c.hotspot.on:
				c.switchLost(ui.SwitchLostHotspot, tr.T("Receiving stopped: the hotspot was turned off"))
			case gone:
				c.switchLost(ui.SwitchLostWifi, switchWifiGone(on, now))
			}
		})
	}
}

// switchLook is one look at the Wi-Fi a session is on: whether the session
// ends, and how many looks in a row found the Wi-Fi down (down: before
// this one). Down for one look may be a roam that comes back; on another
// network or with another address the session's address is gone at once.
func switchLook(on, now wifiLink, err error, down int) (bool, int) {
	switch {
	case err == nil && now == on:
		return false, 0
	case err == nil && now.addr != "":
		return true, down
	}
	return down+1 >= 2, down + 1
}

// switchWifiGone says what became of the Wi-Fi a session was on.
func switchWifiGone(was, now wifiLink) string {
	switch {
	case now.addr == "":
		return tr.T("Receiving stopped: Wi-Fi “%s” was disconnected", was.ssid)
	case now.ssid != was.ssid:
		return tr.T("Receiving stopped: the phone moved from Wi-Fi “%s” to “%s”", was.ssid, now.ssid)
	}
	return tr.T("Receiving stopped: the phone's address on Wi-Fi “%s” changed", was.ssid)
}

// switchLost ends the session whose network went, and says why; Settings
// offers to start again over the hotspot.
func (c *Core) switchLost(lost, why string) {
	c.log.Printf("switch: the %s went", lost)
	c.switchStop(ui.SwitchEndedNetwork)
	c.sw.err, c.sw.lost = why, lost
	c.showToast(why)
}

// switchChanged: the session moved on; the screens follow it (coalesced,
// from the receiver's goroutines).
func (c *Core) switchChanged() {
	if c.sw.posted.CompareAndSwap(false, true) {
		go c.Post(func() {
			c.sw.posted.Store(false)
			c.postFrame()
		})
	}
}

// switchStop ends what is under way: a start, or the session.
func (c *Core) switchStop(how string) {
	w := &c.sw
	w.gen++
	if w.bt != nil {
		c.switchEndedBT(w.bt, how)
		return
	}
	if w.sess == nil {
		if w.state == ui.SwitchStarting {
			w.state = ""
		}
		return
	}
	sess := w.sess
	sess.Close()
	c.switchEnded(sess, how)
}

// switchEnded closes the Shell's side of a session that ended: the
// listener, the hotspot if the session turned it on, the keys and code
// forgotten; what it added becomes the import that can be undone.
func (c *Core) switchEnded(sess *switchrecv.Session, how ...string) {
	w := &c.sw
	if w.sess != sess {
		return
	}
	p := sess.Progress()
	ended := map[string]string{switchrecv.StateDone: ui.SwitchEndedDone, switchrecv.StateIdle: ui.SwitchEndedIdle}[p.State]
	if len(how) > 0 {
		ended = how[0]
	}
	ended = firstNonEmpty(ended, ui.SwitchEndedStopped)
	_ = w.srv.Close()
	if w.ours {
		c.switchHotspotOff()
	}
	r := w.rec
	r.Ended, r.Device, r.Failed = ended, deviceLabel(p.Device), p.Failed
	for _, k := range p.Kinds {
		r.Added[k.Kind] = k.Stored
	}
	if w.cal != nil {
		// Without the calendar app the events stay a file in Files.
		if err := c.ImportCalendar(w.cal.ID); err != nil {
			c.log.Printf("switch: calendar: %v", err)
		} else {
			r.Calendar = w.cal.ID
		}
	}
	if !r.empty() {
		w.last = r
		c.saveSwitch()
		c.showToast(tr.T("Moving from the old phone has ended"))
	}
	c.log.Printf("switch: ended (%s): %v, %d failed", ended, r.Added, r.Failed)
	w.state, w.sess, w.srv, w.kinds, w.code, w.addr, w.network, w.pass = "", nil, nil, nil, "", "", "", ""
	w.onHotspot, w.ours, w.wifi, w.rec, w.cal, w.events = false, false, wifiLink{}, nil, nil, nil
}

// switchUndo takes out what the last import added: its contacts, files,
// messages and calls, by the IDs it kept. What could not be reached (a
// service down) stays in the record for another try.
func (c *Core) switchUndo(done func(error)) {
	w := &c.sw
	r := c.switchLast()
	switch {
	case w.sess != nil || w.state != "" || w.bt != nil:
		done(errors.New(tr.T("Stop the move from the old phone first")))
		return
	case r == nil || r.Undone:
		done(errors.New(tr.T("There is no import to undo")))
		return
	}
	if n, err := c.contactBook().DeleteIDs(r.Contacts); err != nil {
		c.log.Printf("switch: undo contacts: %v", err)
	} else {
		r.Contacts = nil
		if n > 0 {
			c.contactsChanged()
		}
	}
	if r.Calendar != "" {
		if err := c.UndoCalendarImport(r.Calendar); err != nil {
			c.log.Printf("switch: undo calendar: %v", err)
		}
		r.Calendar = ""
	}
	for _, id := range r.Files {
		c.files.remove(id)
	}
	r.Files = nil
	sms, calls := r.SMS, r.Calls
	go func() {
		var smsErr, telErr error
		if len(sms) > 0 {
			smsErr = c.smsRPC(svc.SMSDeleteImported, ui.SMSDeleteArgs{IDs: sms}, nil)
		}
		if len(calls) > 0 {
			telErr = c.telRPC(svc.TelLogDelete, map[string][]string{"ids": calls}, nil)
		}
		c.Post(func() {
			if smsErr == nil {
				r.SMS = nil
			}
			if telErr == nil {
				r.Calls = nil
			}
			err := errors.Join(smsErr, telErr)
			r.Undone = err == nil && len(r.Contacts) == 0
			c.saveSwitch()
			c.log.Printf("switch: undone (%v)", err)
			if err != nil {
				err = fmt.Errorf(tr.T("Not all of it was undone: %w"), err)
			}
			done(err)
		})
	}()
}

// switchListener drops connections from outside the network the receiver
// listens on (a packet for its address that came in another interface).
type switchListener struct {
	net.Listener
	allow *net.IPNet
}

func (l switchListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if a, ok := conn.RemoteAddr().(*net.TCPAddr); ok && l.allow.Contains(a.IP) {
			return conn, nil
		}
		conn.Close()
	}
}

// subnetOf is the network of the interface that has ip, nil when none has.
func subnetOf(ip net.IP) *net.IPNet {
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}
			}
		}
	}
	return nil
}

// switchSink takes what the receiver got, from its goroutine: the stores
// the loop owns are written on the loop, the services called from here.
type switchSink struct {
	c   *Core
	rec *switchRecord
}

var errSwitchGone = errors.New("the session has ended")

// onLoop runs f on the loop for the session's record; false when the
// session has ended or the loop did not get to it in time.
func (k *switchSink) onLoop(f func()) error {
	ran := make(chan bool, 1)
	k.c.Post(func() {
		if k.c.sw.rec != k.rec {
			ran <- false
			return
		}
		f()
		if !k.rec.empty() {
			k.c.sw.last = k.rec
			k.c.saveSwitch()
		}
		ran <- true
	})
	select {
	case ok := <-ran:
		if !ok {
			return errSwitchGone
		}
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("the system is busy")
	}
}

func (k *switchSink) Contacts(vcards []string) ([]switchrecv.Result, error) {
	out := make([]switchrecv.Result, len(vcards))
	var list []ui.Contact
	var of []int
	for i, v := range vcards {
		cs, _, err := contacts.Parse([]byte(v))
		switch {
		case err != nil:
			out[i] = switchrecv.Result{Outcome: switchrecv.Rejected, Reason: "not a vcard"}
		case len(cs) == 0:
			out[i] = switchrecv.Result{Outcome: switchrecv.Rejected, Reason: "no name or number"}
		}
		for _, x := range cs {
			list, of = append(list, x), append(of, i)
		}
	}
	var importErr error
	err := k.onLoop(func() {
		book := k.c.contactBook()
		ids, err := book.Import(list)
		if err != nil {
			importErr = err
			return
		}
		stored := map[int]bool{}
		for j, id := range ids {
			if id != "" {
				stored[of[j]] = true
				k.rec.Contacts = append(k.rec.Contacts, id)
			}
		}
		full := book.Len() >= contacts.MaxContacts
		for _, i := range of {
			switch {
			case stored[i]:
				out[i] = switchrecv.Result{}
			case full:
				out[i] = switchrecv.Result{Outcome: switchrecv.Rejected, Reason: "contacts full"}
			default:
				out[i] = switchrecv.Result{Outcome: switchrecv.Duplicate}
			}
		}
		if len(stored) > 0 {
			k.c.contactsChanged()
		}
	})
	return out, errors.Join(err, importErr)
}

// switchNumber is a number as telephonyd keeps one: digits, * and #, +
// first, 3 to 20 of them; "" for anything else.
func switchNumber(n string) string {
	n = strings.Map(func(r rune) rune {
		if strings.ContainsRune(" -().", r) {
			return -1
		}
		return r
	}, n)
	if len(n) < 3 || len(n) > 20 {
		return ""
	}
	for i, r := range n {
		if !(r >= '0' && r <= '9' || r == '*' || r == '#' || r == '+' && i == 0) {
			return ""
		}
	}
	return n
}

// switchCallType is a protocol call type as telephonyd's direction and
// result.
var switchCallType = map[string][2]string{"in": {"in", "answered"}, "out": {"out", "outgoing"}, "missed": {"in", "missed"}, "rejected": {"in", "rejected"}}

func (k *switchSink) CallLog(entries []switchrecv.CallEntry) ([]switchrecv.Result, error) {
	out := make([]switchrecv.Result, len(entries))
	var send []ui.TelLogEntry
	var of []int
	for i, e := range entries {
		n, t := switchNumber(e.Number), switchCallType[e.Type]
		if n == "" {
			out[i] = switchrecv.Result{Outcome: switchrecv.Rejected, Reason: "bad number"}
			continue
		}
		send, of = append(send, ui.TelLogEntry{Number: n, Dir: t[0], Result: t[1], At: e.At, Duration: e.Duration}), append(of, i)
	}
	if len(send) == 0 {
		return out, nil
	}
	var res struct {
		IDs []string `json:"ids"`
	}
	if err := k.c.telRPC(svc.TelLogImport, map[string]any{"entries": send}, &res); err != nil {
		return nil, err
	}
	if len(res.IDs) != len(send) {
		return nil, fmt.Errorf("telephonyd gave %d ids for %d calls", len(res.IDs), len(send))
	}
	err := k.onLoop(func() {
		for j, id := range res.IDs {
			if id == "" {
				out[of[j]] = switchrecv.Result{Outcome: switchrecv.Duplicate}
				continue
			}
			k.rec.Calls = append(k.rec.Calls, id)
		}
	})
	return out, err
}

func (k *switchSink) SMS(msgs []switchrecv.Message) ([]switchrecv.Result, error) {
	in := make([]ui.SMS, len(msgs))
	for i, m := range msgs {
		in[i] = ui.SMS{Peer: m.Address, Dir: m.Type, Text: m.Body, At: m.At / 1000, Read: m.Read}
	}
	var res svc.SMSImportRes
	if err := k.c.smsRPC(svc.SMSImport, svc.SMSImportArgs{Messages: in}, &res); err != nil {
		return nil, err
	}
	if len(res.IDs) != len(msgs) {
		return nil, fmt.Errorf("phone-smsd gave %d ids for %d messages", len(res.IDs), len(msgs))
	}
	out := make([]switchrecv.Result, len(msgs))
	err := k.onLoop(func() {
		for i, id := range res.IDs {
			if id == "" {
				out[i] = switchrecv.Result{Outcome: switchrecv.Duplicate}
				continue
			}
			k.rec.SMS = append(k.rec.SMS, id)
		}
	})
	return out, err
}

// Calendar keeps the session's events in one .ics file in Files, written
// anew with each batch.
func (k *switchSink) Calendar(events []string) ([]switchrecv.Result, error) {
	out := make([]switchrecv.Result, len(events))
	err := k.onLoop(func() {
		c, w := k.c, &k.c.sw
		all := append(slices.Clone(w.events), events...)
		raw := []byte(icsFile(all))
		var err error
		if w.cal == nil {
			var f *fileItem
			if f, err = c.files.add(c.files.freeName(tr.T("Calendar")+" "+c.now().Format("2006-01-02")+".ics"), raw, c.now()); err == nil {
				w.cal = f
				k.rec.Files = append(k.rec.Files, f.ID)
			}
		} else {
			err = c.files.replace(w.cal, raw)
		}
		if err != nil {
			for i := range out {
				out[i] = switchrecv.Result{Outcome: switchrecv.Rejected, Reason: "calendar file full"}
			}
			return
		}
		w.events = all
	})
	return out, err
}

// icsFile is a calendar of VEVENTs; their TZIDs are IANA zone names, which
// calendar apps know without a VTIMEZONE.
func icsFile(events []string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Kario//Switch//EN\r\n")
	for _, e := range events {
		b.WriteString(e + "\r\n")
	}
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

// File takes a photo or video into Files.
func (k *switchSink) File(f switchrecv.File) error {
	name, err := cleanFileName(f.Name)
	if err != nil {
		name = map[string]string{switchrecv.FileVideo: tr.T("Video")}[f.Kind]
		name = firstNonEmpty(name, tr.T("Photo"))
	}
	at := time.Unix(f.TakenAt, 0)
	if f.TakenAt <= 0 {
		at = k.c.now()
	}
	var addErr error
	err = k.onLoop(func() {
		c := k.c
		it, err := c.files.addPath(c.files.freeName(name), f.Path, f.Mime, at)
		if err != nil {
			addErr = err
			return
		}
		k.rec.Files = append(k.rec.Files, it.ID)
	})
	return errors.Join(err, addErr)
}
