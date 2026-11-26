package shell

import (
	"crypto/sha256"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/obex"
	"github.com/kario-os/kario/switchrecv"
	"github.com/kario-os/kario/ui"
)

// Moving from an old phone over Bluetooth (Settings › My device › Move
// from an old phone › Over Bluetooth; switch.start with Bluetooth), with
// nothing installed on the old phone: the Shell reads a paired phone's
// phone book and call histories (PBAP, which Android phones and iPhones
// serve) and its text messages (MAP, which Android phones serve) with
// package obex's clients, and gives what it reads to the sink
// kario-switch/1 uses (switch.go): the same stores, the same check
// against what Kario has, the same record and undo. The phone's own card
// is left out; PBAP has no call durations (0) and counts a rejected call
// as received. The old phone asks its user once per service: meanwhile
// the move is waiting. The log gets counts.

const (
	// btConsentWait is how long the old phone's user has to allow a
	// service, btAnswerWait how long the phone may take to answer after.
	btConsentWait = 90 * time.Second
	btAnswerWait  = 30 * time.Second
	// btPage is how many entries one request reads, one batch the sink
	// takes.
	btPage = 100
)

// btMove is a move over Bluetooth under way; its pull runs on a goroutine
// of its own (btMoveRun).
type btMove struct {
	addr, name string
	kinds      []string
	changed    func()

	mu      sync.Mutex
	phase   string // ui.SwitchStarting, ui.SwitchWaiting or ui.SwitchReceiving
	prog    map[string]*ui.SwitchProgress
	link    io.Closer // the link open now
	stopped bool
	// seen: what this move gave the sink (true) or left out (false), by
	// kind and hash, so a page a phone sends again is not given twice.
	seen map[string]map[[32]byte]bool
}

func newBTMove(addr, name string, kinds []string, changed func()) *btMove {
	mv := &btMove{addr: addr, name: name, kinds: kinds, changed: changed, phase: ui.SwitchStarting,
		prog: map[string]*ui.SwitchProgress{}, seen: map[string]map[[32]byte]bool{}}
	for _, k := range kinds {
		mv.prog[k] = &ui.SwitchProgress{Kind: k}
		mv.seen[k] = map[[32]byte]bool{}
	}
	return mv
}

func (mv *btMove) wants(k string) bool { return slices.Contains(mv.kinds, k) }

func (mv *btMove) update(f func()) {
	mv.mu.Lock()
	f()
	mv.mu.Unlock()
	mv.changed()
}

func (mv *btMove) setPhase(p string) { mv.update(func() { mv.phase = p }) }

// note says what became of kinds that did not all come; the first word
// stays.
func (mv *btMove) note(note string, kinds ...string) {
	mv.update(func() {
		for _, k := range kinds {
			if p := mv.prog[k]; p != nil && p.Note == "" {
				p.Note = note
			}
		}
	})
}

func (mv *btMove) isStopped() bool {
	mv.mu.Lock()
	defer mv.mu.Unlock()
	return mv.stopped
}

// snapshot is the move's state as switch.state gives it.
func (mv *btMove) snapshot() (string, []ui.SwitchProgress) {
	mv.mu.Lock()
	defer mv.mu.Unlock()
	var out []ui.SwitchProgress
	for _, k := range ui.SwitchBluetoothKinds {
		if p := mv.prog[k]; p != nil {
			out = append(out, *p)
		}
	}
	return mv.phase, out
}

// dial opens a link to the old phone's service, which stop closes.
func (mv *btMove) dial(blue btRadio, uuid string) (io.ReadWriteCloser, int, error) {
	link, mtu, err := blue.obexDial(mv.addr, uuid)
	if err != nil {
		return nil, 0, err
	}
	mv.mu.Lock()
	defer mv.mu.Unlock()
	if mv.stopped {
		go link.Close()
		return nil, 0, errSwitchGone
	}
	mv.link = link
	return link, mtu, nil
}

func (mv *btMove) hangUp(link io.Closer) {
	mv.mu.Lock()
	if mv.link == link {
		mv.link = nil
	}
	mv.mu.Unlock()
	_ = link.Close()
}

// stop ends the pull: its link closes (off the caller's goroutine: BlueZ
// takes its time to disconnect).
func (mv *btMove) stop() {
	mv.mu.Lock()
	mv.stopped = true
	l := mv.link
	mv.link = nil
	mv.mu.Unlock()
	if l != nil {
		go l.Close()
	}
}

// peerVersion is the profile version of the service a link reached, from
// its service record (0: unknown).
func peerVersion(link io.ReadWriter) uint16 {
	if v, ok := link.(interface{ peerVersion() uint16 }); ok {
		return v.peerVersion()
	}
	return 0
}

// btNote is what an error opening or reading a service means for the
// kinds it was to give.
func btNote(err error) string {
	var re obex.ResponseError
	switch {
	case errors.Is(err, errNotOffered):
		return ui.SwitchNoteNotOffered
	case errors.Is(err, obex.ErrNoAnswer):
		return ui.SwitchNoteRefused
	case errors.As(err, &re) && (re == obex.Forbidden || re == obex.Unauthorized || re == obex.NotAcceptable || re == obex.ServiceUnavailable):
		return ui.SwitchNoteRefused
	case errors.As(err, &re) && (re == obex.NotFound || re == obex.NotImplemented || re == obex.BadRequest):
		return ui.SwitchNoteNotOffered
	}
	return ui.SwitchNotePartial
}

// absent: the phone answered that it has no such object.
func absent(err error) bool {
	var re obex.ResponseError
	return errors.As(err, &re) && (re == obex.NotFound || re == obex.NotImplemented || re == obex.BadRequest || re == obex.Forbidden)
}

// btGive hands a batch to the sink, leaving out what this move gave it
// already, and counts what came of each. A sink whose store is away
// (phone-smsd or telephonyd restarting) gets the batch again a few times,
// then it counts as failed.
func btGive[T any](mv *btMove, kind string, items []T, key func(T) string, send func([]T) ([]switchrecv.Result, error)) error {
	var fresh []T
	var hashes [][32]byte
	skipped := 0
	mv.mu.Lock()
	for _, it := range items {
		h := sha256.Sum256([]byte(key(it)))
		if given, ok := mv.seen[kind][h]; ok {
			if given {
				skipped++
			}
			continue
		}
		mv.seen[kind][h] = true
		fresh, hashes = append(fresh, it), append(hashes, h)
	}
	mv.mu.Unlock()
	var res []switchrecv.Result
	var err error
	for try := 0; len(fresh) > 0; try++ {
		if res, err = send(fresh); err == nil && len(res) == len(fresh) || errors.Is(err, errSwitchGone) || try == 2 || mv.isStopped() {
			break
		}
		time.Sleep(time.Second)
	}
	if errors.Is(err, errSwitchGone) {
		return err
	}
	mv.update(func() {
		p := mv.prog[kind]
		p.Skipped += skipped
		if err != nil || len(res) != len(fresh) {
			p.Failed += len(fresh)
			for _, h := range hashes {
				delete(mv.seen[kind], h)
			}
			return
		}
		for _, r := range res {
			switch r.Outcome {
			case switchrecv.Stored:
				p.Stored++
			case switchrecv.Duplicate:
				p.Skipped++
			default:
				p.Failed++
			}
		}
	})
	return nil
}

// leaveOut marks an item this move does not give (the phone's own card).
func (mv *btMove) leaveOut(kind, key string) {
	mv.mu.Lock()
	mv.seen[kind][sha256.Sum256([]byte(key))] = false
	mv.mu.Unlock()
}

func (mv *btMove) fail(kind string, n int) {
	if n > 0 {
		mv.update(func() { mv.prog[kind].Failed += n })
	}
}

func (mv *btMove) expect(kind string, n int) {
	if n > 0 {
		mv.update(func() { mv.prog[kind].Expected += n })
	}
}

func same(s string) string { return s }

// switchStartBT starts a move from the paired phone at addr; done runs once
// it is under way (or could not start).
func (c *Core) switchStartBT(addr string, asked []string, done func(error)) {
	w := &c.sw
	var kinds []string
	for _, k := range c.switchKinds(asked) {
		if slices.Contains(ui.SwitchBluetoothKinds, k) {
			kinds = append(kinds, k)
		}
	}
	blue := c.bt
	switch {
	case w.state != "" || w.sess != nil || w.bt != nil:
		done(errors.New(tr.T("A move from an old phone is under way already")))
		return
	case len(kinds) == 0:
		done(errors.New(tr.T("Tick contacts, the call log or messages first")))
		return
	case blue == nil:
		done(errors.New(tr.T("This device has no Bluetooth")))
		return
	}
	w.state, w.err, w.lost = ui.SwitchStarting, "", ""
	w.gen++
	gen := w.gen
	go func() {
		st, err := blue.state()
		name := ""
		for _, d := range st.Devices {
			if d.Address == addr && d.Paired {
				name = d.Name
			}
		}
		c.Post(func() {
			if w.gen != gen {
				done(errors.New(ui.ErrCancelled))
				return
			}
			w.state = ""
			switch {
			case err != nil:
				w.err = err.Error()
				done(err)
				return
			case !st.Enabled:
				err = errors.New(tr.T("Bluetooth is off"))
			case name == "":
				err = errors.New(tr.T("Pair with the device first"))
			}
			if err != nil {
				w.err = err.Error()
				done(err)
				return
			}
			mv := newBTMove(addr, name, kinds, c.switchChanged)
			rec := &switchRecord{At: c.now().Unix(), Added: map[string]int{}}
			w.bt, w.rec = mv, rec
			c.log.Printf("switch: reading %s from %s over Bluetooth", strings.Join(kinds, ","), addr)
			go c.btMoveRun(mv, blue, &switchSink{c: c, rec: rec})
			done(nil)
		})
	}()
}

// btMoveRun reads the old phone, one service after the other.
func (c *Core) btMoveRun(mv *btMove, blue btRadio, sink *switchSink) {
	if mv.wants(ui.SwitchContacts) || mv.wants(ui.SwitchCallLog) {
		if err := c.btPullBook(mv, blue, sink); err != nil {
			c.log.Printf("switch: bluetooth phone book: %v", err)
		}
	}
	if mv.wants(ui.SwitchSMS) && !mv.isStopped() {
		if err := c.btPullMessages(mv, blue, sink); err != nil {
			c.log.Printf("switch: bluetooth messages: %v", err)
		}
	}
	c.Post(func() { c.switchEndedBT(mv, ui.SwitchEndedDone) })
}

// btPullBook reads the phone book and the call histories.
func (c *Core) btPullBook(mv *btMove, blue btRadio, sink *switchSink) error {
	var kinds []string
	for _, k := range []string{ui.SwitchContacts, ui.SwitchCallLog} {
		if mv.wants(k) {
			kinds = append(kinds, k)
		}
	}
	link, mtu, err := mv.dial(blue, uuidPBAP)
	if err != nil {
		mv.note(btNote(err), kinds...)
		return err
	}
	defer mv.hangUp(link)
	mv.setPhase(ui.SwitchWaiting)
	pb, err := obex.DialPhoneBook(link, mtu, peerVersion(link), btConsentWait, btAnswerWait)
	if err != nil {
		mv.note(btNote(err), kinds...)
		return err
	}
	mv.setPhase(ui.SwitchReceiving)
	if mv.wants(ui.SwitchContacts) {
		if err := c.btPullContacts(mv, pb, sink); err != nil {
			mv.note(btNote(err), kinds...)
			return err
		}
	}
	if mv.wants(ui.SwitchCallLog) {
		if err := c.btPullCalls(mv, pb, sink); err != nil {
			mv.note(btNote(err), ui.SwitchCallLog)
			return err
		}
	}
	return pb.Close()
}

func (c *Core) btPullContacts(mv *btMove, pb *obex.PhoneBook, sink *switchSink) error {
	const object = "telecom/pb.vcf"
	n, err := pb.Size(object)
	if err != nil {
		return err
	}
	owner, err := pb.OwnerFirst()
	if err != nil {
		return err
	}
	if owner {
		mv.expect(ui.SwitchContacts, n-1)
	} else {
		mv.expect(ui.SwitchContacts, n)
	}
	first := true
	return pb.PullAll(object, n, btPage, func(cards []string) error {
		if first && owner && len(cards) > 0 {
			mv.leaveOut(ui.SwitchContacts, cards[0])
			cards = cards[1:]
		}
		first = false
		return btGive(mv, ui.SwitchContacts, cards, same, sink.Contacts)
	})
}

// btCallType is a call history's kind as kario-switch/1's call type.
var btCallType = map[string]string{"RECEIVED": "in", "DIALED": "out", "MISSED": "missed"}

func (c *Core) btPullCalls(mv *btMove, pb *obex.PhoneBook, sink *switchSink) error {
	sizes := map[string]int{}
	var folders []string
	for _, f := range []string{"ich", "och", "mch"} {
		n, err := pb.Size("telecom/" + f + ".vcf")
		switch {
		case absent(err):
			continue
		case err != nil:
			return err
		}
		sizes[f] = n
		folders = append(folders, f)
	}
	switch {
	case len(folders) == 0:
		// A phone with the combined history only.
		n, err := pb.Size("telecom/cch.vcf")
		if absent(err) {
			mv.note(ui.SwitchNoteNotOffered, ui.SwitchCallLog)
			return nil
		}
		if err != nil {
			return err
		}
		sizes["cch"], folders = n, []string{"cch"}
	case len(folders) < 3:
		mv.note(ui.SwitchNotePartial, ui.SwitchCallLog)
	}
	for _, f := range folders {
		mv.expect(ui.SwitchCallLog, sizes[f])
	}
	for _, f := range folders {
		err := pb.PullAll("telecom/"+f+".vcf", sizes[f], btPage, func(cards []string) error {
			var entries []switchrecv.CallEntry
			bad := 0
			for _, card := range cards {
				call, err := obex.ParseCall(card, f, c.zone())
				if err != nil {
					bad++
					continue
				}
				entries = append(entries, switchrecv.CallEntry{Number: call.Number, Type: btCallType[call.Kind], At: call.At.Unix()})
			}
			mv.fail(ui.SwitchCallLog, bad)
			return btGive(mv, ui.SwitchCallLog, entries, func(e switchrecv.CallEntry) string {
				return e.Number + "|" + e.Type + "|" + time.Unix(e.At, 0).UTC().String()
			}, sink.CallLog)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// btPullMessages reads the inbox and the sent messages: SMS, and an MMS's
// text.
func (c *Core) btPullMessages(mv *btMove, blue btRadio, sink *switchSink) error {
	link, mtu, err := mv.dial(blue, uuidMAS)
	if err != nil {
		mv.note(btNote(err), ui.SwitchSMS)
		return err
	}
	defer mv.hangUp(link)
	mv.setPhase(ui.SwitchWaiting)
	ms, err := obex.DialMessages(link, mtu, btConsentWait, btAnswerWait)
	if err != nil {
		mv.note(btNote(err), ui.SwitchSMS)
		return err
	}
	mv.setPhase(ui.SwitchReceiving)
	sizes := map[string]int{}
	var folders []string
	for _, f := range []string{"inbox", "sent"} {
		n, err := ms.Size(f)
		switch {
		case absent(err):
			continue
		case err != nil:
			mv.note(btNote(err), ui.SwitchSMS)
			return err
		}
		sizes[f] = n
		folders = append(folders, f)
		mv.expect(ui.SwitchSMS, n)
	}
	switch {
	case len(folders) == 0:
		mv.note(ui.SwitchNoteNotOffered, ui.SwitchSMS)
	case len(folders) < 2:
		mv.note(ui.SwitchNotePartial, ui.SwitchSMS)
	}
	for _, f := range folders {
		err := ms.ListAll(f, sizes[f], btPage, c.zone(), func(es []obex.Entry) error {
			var msgs []switchrecv.Message
			bad := 0
			for _, e := range es {
				if !strings.HasPrefix(e.Type, "SMS") && e.Type != "MMS" {
					continue
				}
				b, err := ms.Message(e.Handle)
				var re obex.ResponseError
				switch {
				case errors.As(err, &re), errors.Is(err, obex.ErrNotBMessage), errors.Is(err, obex.ErrNative):
					bad++
					continue
				case err != nil:
					return err
				}
				m, ok := btMessage(f, e, b)
				if !ok {
					bad++
					continue
				}
				msgs = append(msgs, m)
			}
			mv.fail(ui.SwitchSMS, bad)
			return btGive(mv, ui.SwitchSMS, msgs, func(m switchrecv.Message) string {
				return m.Address + "|" + m.Type + "|" + time.UnixMilli(m.At).UTC().String() + "|" + m.Body
			}, sink.SMS)
		})
		if err != nil {
			mv.note(btNote(err), ui.SwitchSMS)
			return err
		}
	}
	return ms.Close()
}

// btMessage is a listed message as kario-switch/1's, checked as its
// receiver checks one: the other party's number (from the listing, else
// the bMessage's vCards; the first of several recipients), the text, the
// time; false when one is missing or too long.
func btMessage(folder string, e obex.Entry, b obex.BMessage) (switchrecv.Message, bool) {
	addr, cards, dir, read := e.Sender, b.From, "in", e.Read
	if folder == "sent" {
		addr, cards, dir, read = e.Recipient, b.To, "out", true
	}
	addr = firstAddress(addr)
	if addr == "" && len(cards) > 0 {
		addr = firstAddress(cards[0])
	}
	body := b.Text
	ok := addr != "" && len(addr) <= 64 && !strings.ContainsFunc(addr, func(r rune) bool { return r < 0x20 || r == 0x7f }) &&
		strings.TrimSpace(body) != "" && len(body) <= 64<<10 && utf8.ValidString(body) && !e.At.IsZero()
	return switchrecv.Message{Address: addr, Body: body, Type: dir, At: e.At.UnixMilli(), Read: read}, ok
}

func firstAddress(s string) string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' })
	if len(f) == 0 {
		return ""
	}
	return strings.TrimSpace(f[0])
}

// switchEndedBT closes the Shell's side of a move over Bluetooth: what it
// added becomes the import that can be undone; when nothing came, the
// page says why.
func (c *Core) switchEndedBT(mv *btMove, how string) {
	w := &c.sw
	if w.bt != mv {
		return
	}
	mv.stop()
	_, prog := mv.snapshot()
	r := w.rec
	r.Ended, r.Device = how, mv.name
	notes := map[string]string{}
	for _, p := range prog {
		r.Added[p.Kind] = p.Stored
		r.Failed += p.Failed
		if p.Note != "" {
			notes[p.Kind] = p.Note
		}
	}
	if len(notes) > 0 {
		r.Notes = notes
	}
	if !r.empty() {
		w.last = r
		c.saveSwitch()
		c.showToast(tr.T("Moving from the old phone has ended"))
	} else if how == ui.SwitchEndedDone {
		w.err = btNothing(mv.name, prog)
	}
	c.log.Printf("switch: bluetooth move ended (%s): %v, %d failed, notes %v", how, r.Added, r.Failed, notes)
	w.bt, w.rec = nil, nil
}

// btNothing says why nothing came from the phone.
func btNothing(name string, prog []ui.SwitchProgress) string {
	refused, offered, already := false, false, false
	for _, p := range prog {
		switch p.Note {
		case ui.SwitchNoteRefused:
			refused = true
		case ui.SwitchNoteNotOffered:
		default:
			offered = true
		}
		already = already || p.Skipped > 0
	}
	switch {
	case already:
		return tr.T("Nothing new came from “%s”: Kario has all of it already", name)
	case refused:
		return tr.T("“%s” did not allow it. Start again and allow it on that phone.", name)
	case !offered:
		return tr.T("“%s” does not give these over Bluetooth", name)
	}
	return tr.T("Nothing came from “%s”", name)
}
