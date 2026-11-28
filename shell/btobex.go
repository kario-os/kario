package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/obex"
	"github.com/kario-os/kario/obex/btphone"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Bluetooth file transfer and phone book access, in the shell's own OBEX
// (package obex) over the sockets BlueZ hands over, so no obexd runs.
// Receiving: a paired device's file is offered to the user, one file at a
// time, and goes into Files. Sending (bt.send): the user picks a file from
// Files in a system list, the app that asked never sees it; a device that
// asks for a code gets the one the user types (btcode.go). Kario answers
// and does not challenge it back: the code comes from the device, whose
// proof of it would show nothing; a device that insists (mutual
// authentication) refuses the answer, which reads as a wrong code. A paired car
// kit reads the contacts and the call log (PBAP) once the user allowed
// that device, which the user can take back in Settings. Kario reads an
// old phone's phone book and messages when moving from it (switch_bt.go).
// The selftest's other device (the second virtual controller on the
// emulator, a simulated one on a developer machine) takes the files sent
// to it, asking for a code first if given one (bt-peer-code), and can
// push files and read the phone book; made an old phone (bt-oldphone), it
// serves a simulated phone's (package obex/btphone).

const (
	uuidOPP  = "00001105-0000-1000-8000-00805f9b34fb"
	uuidPBAP = "0000112f-0000-1000-8000-00805f9b34fb"
	uuidPCE  = "0000112e-0000-1000-8000-00805f9b34fb"
	uuidMAS  = "00001132-0000-1000-8000-00805f9b34fb"
)

// errNotOffered: the device has no such service.
var errNotOffered = errors.New("bluetooth: the device does not offer this service")

// offerTimeout: an incoming file or phone book request nobody answers is
// refused.
const offerTimeout = 60 * time.Second

// sendWait bounds each answer of a device a file is sent to.
const sendWait = 90 * time.Second

// obexState is what the selftest's other device received and how its last
// push or pull went.
type obexState struct {
	mu   sync.Mutex
	got  map[string][]byte
	last *peerResult
	// phone: the old phone the device is (bt-oldphone); its user's
	// answers come through allow.
	phone *btphone.Phone
	allow chan bool
	// code: what the device asks for (OBEX authentication) before it
	// takes a file; "" none.
	code string
}

// setCode makes the device ask for code before it takes a file ("": not).
func (s *obexState) setCode(code string) {
	s.mu.Lock()
	s.code = code
	s.mu.Unlock()
}

// oldPhoneAsk is how long the old phone's simulated user waits for the
// selftest's answer (bt-oldphone-allow); none is no.
const oldPhoneAsk = 60 * time.Second

// asks makes the selftest answer for the old phone's user.
func (s *obexState) asks(ph *btphone.Phone) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allow == nil {
		s.allow = make(chan bool, 4)
	}
	allow := s.allow
	ph.Ask = func(string) bool {
		select {
		case ok := <-allow:
			return ok
		case <-time.After(oldPhoneAsk):
			return false
		}
	}
}

// setPhone makes the device the old phone ph.
func (s *obexState) setPhone(ph *btphone.Phone) {
	s.asks(ph)
	s.mu.Lock()
	s.phone = ph
	s.mu.Unlock()
}

// answer is the old phone's user answering its next question.
func (s *obexState) answer(yes bool) error {
	s.mu.Lock()
	allow := s.allow
	s.mu.Unlock()
	if allow == nil {
		return errors.New("no old phone: bt-oldphone first")
	}
	select {
	case allow <- yes:
		return nil
	default:
		return errors.New("the old phone has answers waiting already")
	}
}

type peerResult struct {
	err  error
	book string // the phone book it read
}

// obexServe answers a paired device's session on the phone's side:
// Object Push into Files, or the phone book. A device that challenges
// the phone (OBEX authentication, some car kits) gets the code the user
// types (btcode.go).
func (c *Core) obexServe(uuid, addr, name string, link io.ReadWriteCloser, mtu int) {
	defer link.Close()
	from := peerName(name, addr)
	s := &obex.Server{MaxObject: maxFileBytes, Auth: c.btAuth(from, nil)}
	switch uuid {
	case uuidOPP:
		s.Inbox = &btInbox{c: c, from: from}
	case uuidPBAP:
		s.Phonebook = &btBook{c: c, addr: addr, name: from}
	default:
		return
	}
	if err := s.Serve(link, mtu); err != nil {
		c.log.Printf("bluetooth: %s session with %s: %v", uuid[4:8], addr, err)
	}
}

// ask puts a yes-or-no question to the user from a session's goroutine;
// no answer in time is no.
func (c *Core) ask(text, yes, no string) bool {
	answer := make(chan bool, 1)
	var o *confirmOverlay
	c.Post(func() {
		o = &confirmOverlay{text: text, yes: yes, no: no, onYes: func() { answer <- true }, onNo: func() { answer <- false }}
		c.pushOverlay(o)
	})
	select {
	case ok := <-answer:
		return ok
	case <-time.After(offerTimeout):
		c.Post(func() { c.popOverlay(o) })
		return false
	}
}

type btInbox struct {
	c    *Core
	from string
}

func (in *btInbox) Offer(name, mime string, size int64) bool {
	what := tr.T("size unknown")
	if size >= 0 {
		what = sizeLabel(size)
	}
	return in.c.ask(tr.T("“%s” is sending you a file over Bluetooth:\n%s · %s\nAccept it into Files?", in.from, name, what), tr.T("Accept"), tr.T("Refuse"))
}

func (in *btInbox) Received(name, mime string, data []byte) error {
	c := in.c
	clean, err := cleanFileName(name)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	c.Post(func() {
		f, err := c.files.add(clean, data, c.now())
		if err == nil {
			c.log.Printf("bluetooth: received file %s (%d bytes) from %s", f.ID, f.Size, in.from)
			c.showToast(tr.T("Received “%s” into Files", f.Name))
			switch {
			case playableVideo(f.Name):
				c.offerVideo(f)
			case contacts.IsVCard(f.Name, mime):
				// A business card another phone sent: its contacts.
				c.offerVCard(f, in.from, func(ui.ContactsFile, error) {})
			}
		}
		done <- err
	})
	return <-done
}

// btBook is the phone book a car kit reads: the contacts and the call log
// as they were when it connected.
type btBook struct {
	c          *Core
	addr, name string
	own        string
	contacts   []ui.Contact
	log        []ui.TelLogEntry
}

func (b *btBook) Allowed() bool {
	c := b.c
	type known struct{ allowed, asked bool }
	k := make(chan known, 1)
	c.Post(func() {
		b.contacts = c.contacts()
		if c.tel != nil {
			b.own = c.tel.Own
		}
		v, ok := c.settings.BTPhonebook[b.addr]
		k <- known{v, ok}
	})
	got := <-k
	if !got.asked {
		got.allowed = c.ask(tr.T("“%s” asks for your contacts and call log over Bluetooth (a car kit shows them and calls by name).\nAllow it for this device?", b.name), tr.T("Allow"), tr.T("Deny"))
		c.Post(func() {
			if c.settings.BTPhonebook == nil {
				c.settings.BTPhonebook = map[string]bool{}
			}
			c.settings.BTPhonebook[b.addr] = got.allowed
			c.saveSettings()
		})
	}
	if got.allowed && c.telRPC(svc.TelLog, nil, &b.log) != nil {
		b.log = nil
	}
	c.log.Printf("bluetooth: phone book for %s: allowed %v", b.addr, got.allowed)
	return got.allowed
}

func (b *btBook) Owner() obex.Card { return obex.Card{Number: b.own} }

var pbapType = map[string]string{ui.NumberMobile: "CELL", ui.NumberHome: "HOME", ui.NumberWork: "WORK", ui.NumberOther: "VOICE"}

func (b *btBook) Contacts() []obex.Card {
	var out []obex.Card
	for _, x := range b.contacts {
		card := obex.Card{Name: x.Name, Number: x.Number}
		for _, n := range ui.NumbersOf(x) {
			card.Numbers = append(card.Numbers, obex.Tel{Number: n.Number, Type: pbapType[n.Label]})
		}
		out = append(out, card)
	}
	return out
}

func (b *btBook) Calls(kind string) []obex.Card {
	names := map[string]string{}
	for _, x := range b.contacts {
		for _, n := range ui.NumbersOf(x) {
			names[ui.NumberKey(n.Number)] = x.Name
		}
	}
	var out []obex.Card
	for _, e := range b.log {
		call := map[bool]string{true: "DIALED", false: "RECEIVED"}[e.Dir == "out"]
		if e.Dir == "in" && e.Result != "answered" {
			call = "MISSED"
		}
		folder := map[string]string{"DIALED": "och", "RECEIVED": "ich", "MISSED": "mch"}[call]
		if kind != "cch" && kind != folder {
			continue
		}
		out = append(out, obex.Card{Name: names[ui.NumberKey(e.Number)], Number: e.Number, At: time.Unix(e.At, 0).In(b.c.zone()), Call: call})
	}
	return out
}

// btSend is bt.send: the user picks one of their files, which goes to the
// paired device.
func (c *Core) btSend(a *App, m ui.Msg, reply func(any, error)) {
	var args ui.BTArgs
	_ = json.Unmarshal(m.Args, &args)
	blue := c.bt
	if blue == nil {
		reply(nil, errors.New(tr.T("This device has no Bluetooth")))
		return
	}
	go func() {
		st, err := blue.state()
		var dev *ui.BTDevice
		for i := range st.Devices {
			if st.Devices[i].Address == args.Address && st.Devices[i].Paired {
				dev = &st.Devices[i]
			}
		}
		c.Post(func() {
			switch {
			case err != nil:
				reply(nil, err)
			case dev == nil:
				reply(nil, errors.New(tr.T("Pair with the device first")))
			default:
				c.pickToSend(a, blue, *dev, reply)
			}
		})
	}()
}

func (c *Core) pickToSend(a *App, blue btRadio, dev ui.BTDevice, reply func(any, error)) {
	to := peerName(dev.Name, dev.Address)
	var es []menuEntry
	for i := len(c.files.Items) - 1; i >= 0; i-- {
		f := c.files.Items[i]
		if !f.inMemory() {
			continue
		}
		es = append(es, menuEntry{group: tr.T("Files"), label: f.Name + " · " + sizeLabel(f.Size), find: f.Name, run: func() {
			data, err := os.ReadFile(c.files.path(f))
			if err != nil {
				reply(nil, err)
				return
			}
			c.showToast(tr.T("Sending “%s” to “%s”…", f.Name, to))
			go func() {
				var none, late bool
				err := obexPut(blue, dev.Address, func(ch obex.Challenge, ended <-chan struct{}) string {
					code, out := c.askCode(to, ch.Realm, ended)
					select {
					case <-ended:
					default:
						none, late = code == "", out
					}
					return code
				}, f.Name, f.Mime, data)
				c.Post(func() {
					var re obex.ResponseError
					switch {
					case err != nil && late:
						err = errors.New(tr.T("No code typed in time: “%s” was not sent", f.Name))
					case err != nil && none:
						err = errors.New(ui.ErrCancelled)
					case errors.Is(err, obex.ErrBadPassword):
						err = errors.New(tr.T("“%s” did not take the code", to))
					case errors.As(err, &re) && (re == obex.Forbidden || re == obex.NotAcceptable):
						err = errors.New(tr.T("“%s” declined the file", to))
					case errors.As(err, &re) && re == obex.EntityTooLarge:
						err = errors.New(tr.T("“%s” has no room for the file", to))
					case err != nil:
						err = fmt.Errorf(tr.T("Sending failed: %w"), err)
					}
					if err != nil {
						reply(nil, err)
						return
					}
					c.log.Printf("bluetooth: %s sent file %s to %s", a.M.ID, f.ID, dev.Address)
					c.showToast(tr.T("Sent “%s” to “%s”", f.Name, to))
					reply(map[string]string{"sent": f.Name}, nil)
				})
			}()
		}})
	}
	if len(es) == 0 {
		reply(nil, errors.New(tr.T("No files to send: Files is empty")))
		return
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Send over Bluetooth · to “%s”", to), entries: es,
		onCancel: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// obexPut sends one object to the device's Object Push inbox; the other
// side's user has sendWait to accept it. ask (nil: none) gives the code
// for a challenge, "" for none; ended closes if the device ends the link
// meanwhile, which obexPut then gives as its error.
func obexPut(blue btRadio, addr string, ask func(ch obex.Challenge, ended <-chan struct{}) string, name, mime string, data []byte) error {
	link, mtu, err := blue.obexDial(addr, uuidOPP)
	if err != nil {
		return err
	}
	defer link.Close()
	var rw io.ReadWriter = link
	var w *watchedLink
	if dl, ok := link.(deadlineLink); ok {
		w = newWatchedLink(dl)
		rw = w
	}
	var auth obex.Auth
	if ask != nil {
		auth.Ask = func(ch obex.Challenge) (string, string, bool) {
			var ended <-chan struct{}
			if w != nil {
				ended = w.watch()
			}
			code := ask(ch, ended)
			return "", code, code != ""
		}
	}
	cl, err := obex.DialWith(rw, mtu, obex.Connect{Wait: sendWait, Timeout: sendWait, Auth: auth})
	if err == nil {
		if err = cl.Put(name, mime, data); err == nil {
			err = cl.Close()
		}
	}
	if w != nil && err != nil {
		select {
		case <-w.ended:
			return w.end
		default:
		}
	}
	return err
}

// deadlineLink is a link whose reads can be bounded, as the OBEX client
// does with each answer.
type deadlineLink interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
}

// watchedLink is read in the background while the user types the
// device's code (watch), when the OBEX client reads nothing, so that the
// device ending the link is seen at once. What that read brings is the
// client's next Read; only the client calls watch and Read.
type watchedLink struct {
	deadlineLink
	// ended closes when a watch saw the link end; end is why.
	ended chan struct{}
	end   error
	// done closes when the watch's read is over (nil: none runs); got
	// and err are what it read, not given to Read yet.
	done chan struct{}
	got  []byte
	err  error
}

func newWatchedLink(l deadlineLink) *watchedLink {
	return &watchedLink{deadlineLink: l, ended: make(chan struct{})}
}

// watch reads the link until something comes or it ends. The deadline
// the client's last read set goes: typing the code may take longer, and
// the client sets one again before its next read.
func (l *watchedLink) watch() <-chan struct{} {
	if l.done != nil || len(l.got) > 0 || l.err != nil || l.end != nil {
		return l.ended
	}
	_ = l.deadlineLink.SetReadDeadline(time.Time{})
	done := make(chan struct{})
	l.done = done
	go func() {
		b := make([]byte, 0x10000)
		n, err := l.deadlineLink.Read(b)
		l.got, l.err = b[:n], err
		if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			l.end = err
			close(l.ended)
		}
		close(done)
	}()
	return l.ended
}

func (l *watchedLink) Read(p []byte) (int, error) {
	if l.done != nil {
		<-l.done
		l.done = nil
	}
	switch {
	case len(l.got) > 0:
		n := copy(p, l.got)
		l.got = l.got[n:]
		return n, nil
	case l.err != nil:
		err := l.err
		l.err = nil
		return 0, err
	}
	return l.deadlineLink.Read(p)
}

// The selftest's other device.

// peerInbox takes whatever is sent to the other device.
type peerInbox struct{ s *obexState }

func (p peerInbox) Offer(string, string, int64) bool { return true }

func (p peerInbox) Received(name, _ string, data []byte) error {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	if p.s.got == nil {
		p.s.got = map[string][]byte{}
	}
	p.s.got[name] = append([]byte{}, data...)
	return nil
}

func (s *obexState) serve(uuid string, link io.ReadWriteCloser, mtu int) {
	defer link.Close()
	srv := &obex.Server{}
	s.mu.Lock()
	if s.phone != nil {
		srv = s.phone.Server()
	}
	if s.code != "" && uuid == uuidOPP {
		srv.Auth = obex.Auth{Password: s.code}
	}
	s.mu.Unlock()
	srv.Inbox = peerInbox{s}
	_ = srv.Serve(link, mtu)
}

// obexPeer is a backend whose other device the selftest drives.
type obexPeer interface {
	peerLink(uuid string) (io.ReadWriteCloser, int, error)
}

// peerRun has the other device send a file to the phone or (pull) read the
// phone book and the missed calls; it finishes in the background
// (peerDone).
func (c *Core) peerRun(pull bool, name, text string) error {
	p, ok := c.bt.(obexPeer)
	if !ok {
		return errors.New("no Bluetooth")
	}
	s := &c.radios.obex
	s.mu.Lock()
	s.last = nil
	s.mu.Unlock()
	go func() {
		res := &peerResult{}
		res.book, res.err = peerSession(p, pull, name, text)
		if res.err != nil {
			c.log.Printf("bluetooth: the selftest's device: %v", res.err)
		}
		s.mu.Lock()
		s.last = res
		s.mu.Unlock()
	}()
	return nil
}

// peerSession is one push or pull of the other device's. Its link is
// closed when it returns, so the next one can start at once.
func peerSession(p obexPeer, pull bool, name, text string) (book string, err error) {
	uuid := map[bool]string{false: uuidOPP, true: uuidPBAP}[pull]
	link, mtu, err := p.peerLink(uuid)
	if err != nil {
		return "", fmt.Errorf("connecting: %w", err)
	}
	defer func() {
		if cerr := link.Close(); err == nil {
			err = cerr
		}
	}()
	target := map[bool][]byte{true: obex.PBAPTarget}[pull]
	cl, err := obex.Dial(link, mtu, target)
	if err != nil {
		return "", err
	}
	if pull {
		var pb, missed []byte
		pb, _, err = cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil)
		if err == nil {
			missed, _, err = cl.Get("x-bt/phonebook", "telecom/mch.vcf", nil)
		}
		book = string(pb) + string(missed)
	} else {
		mime := "text/plain"
		if contacts.IsVCard(name, "") {
			mime = "text/x-vcard"
		}
		err = cl.Put(name, mime, []byte(text))
	}
	if err != nil {
		return book, err
	}
	return book, cl.Close()
}

// peerDone is the other device's last push or pull, nil while it runs.
func (c *Core) peerDone() *peerResult {
	s := &c.radios.obex
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// peerGot is a file the other device received, nil without one.
func (c *Core) peerGot(name string) []byte {
	s := &c.radios.obex
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got[name]
}
