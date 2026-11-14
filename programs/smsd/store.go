package smsd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kario-os/kario/ui"
)

// The store is a snapshot and an append-only log of the changes since: one
// JSON object a line, each synced before the change counts (a received
// message before telephonyd is told it may let it go). A line a crash cut
// short is dropped when the log is read; compaction writes a new snapshot
// and empties the log. A change carries its sequence number, so a crash
// between the two does not apply it twice. Files are 0600 in a 0700
// directory. No database: the volumes are a phone's.

const (
	maxMessages  = 10000
	maxAlerts    = 500
	maxSeen      = 2000 // received ids remembered, for a message telephonyd reports again
	maxNotify    = 200  // new messages and alerts the shell has not shown
	compactEvery = 256  // log lines
	// A repeat of an alert (same channel and serial number) within this
	// time is the same alert.
	alertRepeat = 24 * time.Hour
)

type change struct {
	Seq    int64        `json:"seq"`
	Op     string       `json:"op"` // add | state | read | delete | block | unblock | alert | notified
	Msgs   []*ui.SMS    `json:"msgs,omitempty"`
	IDs    []string     `json:"ids,omitempty"`
	State  string       `json:"state,omitempty"`
	At     int64        `json:"at,omitempty"` // a message sent again: when
	Thread string       `json:"thread,omitempty"`
	Alert  *ui.SMSAlert `json:"alert,omitempty"`
	// Seen: the received ids this change records (an alert's key too);
	// Notify: the new messages and alerts it gives the shell to show.
	Seen   []string `json:"seen,omitempty"`
	Notify []string `json:"notify,omitempty"`
}

type snapshot struct {
	Seq     int64         `json:"seq"`
	Msgs    []*ui.SMS     `json:"messages"`
	Alerts  []ui.SMSAlert `json:"alerts"`
	Blocked []string      `json:"blocked"`
	Seen    []string      `json:"seen"`
	Notify  []string      `json:"notify"`
}

type store struct {
	dir     string
	msgs    []*ui.SMS // oldest first
	byID    map[string]*ui.SMS
	alerts  []ui.SMSAlert
	blocked map[string]bool // by thread
	seen    []string
	seenSet map[string]bool
	notify  []string // what the shell has not shown yet, oldest first
	seq     int64
	log     *os.File
	logged  int
	now     func() time.Time
	nextID  int
}

func (s *store) snapPath() string { return filepath.Join(s.dir, "snapshot.json") }
func (s *store) logPath() string  { return filepath.Join(s.dir, "log") }

func openStore(dir string) (*store, error) {
	s := &store{dir: dir, byID: map[string]*ui.SMS{}, blocked: map[string]bool{}, seenSet: map[string]bool{}, now: time.Now}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if raw, err := os.ReadFile(s.snapPath()); err == nil {
		var snap snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return nil, fmt.Errorf("%s: %w", s.snapPath(), err)
		}
		s.seq = snap.Seq
		s.apply(change{Op: "add", Msgs: snap.Msgs, Seen: snap.Seen, Notify: snap.Notify})
		for _, a := range snap.Alerts {
			s.apply(change{Op: "alert", Alert: &a})
		}
		for _, b := range snap.Blocked {
			s.blocked[b] = true
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(s.logPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	s.log = f
	if err := s.replay(); err != nil {
		f.Close()
		return nil, err
	}
	if s.logged >= compactEvery {
		if err := s.compact(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// replay applies the log after the snapshot, and cuts off a last line a
// crash left unfinished.
func (s *store) replay() error {
	r := bufio.NewReader(s.log)
	var good int64
	for {
		line, err := r.ReadBytes('\n')
		var c change
		if err == io.EOF || (err == nil && json.Unmarshal(bytes.TrimSpace(line), &c) != nil) {
			break
		}
		if err != nil {
			return err
		}
		good += int64(len(line))
		s.logged++
		if c.Seq <= s.seq {
			continue
		}
		s.seq = c.Seq
		s.apply(c)
	}
	if err := s.log.Truncate(good); err != nil {
		return err
	}
	_, err := s.log.Seek(good, io.SeekStart)
	return err
}

func (s *store) close() error { return s.log.Close() }

// commit makes a change durable, then applies it.
func (s *store) commit(c change) error {
	c.Seq = s.seq + 1
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err := s.log.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := s.log.Sync(); err != nil {
		return err
	}
	s.seq = c.Seq
	s.apply(c)
	s.logged++
	if s.logged >= compactEvery {
		return s.compact()
	}
	return nil
}

func (s *store) compact() error {
	snap := snapshot{Seq: s.seq, Msgs: s.msgs, Alerts: s.alerts, Seen: s.seen, Notify: s.notify, Blocked: []string{}}
	for b := range s.blocked {
		snap.Blocked = append(snap.Blocked, b)
	}
	sort.Strings(snap.Blocked)
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	tmp := s.snapPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, s.snapPath())
	}
	if err != nil {
		return err
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	if err := s.log.Truncate(0); err != nil {
		return err
	}
	if _, err := s.log.Seek(0, io.SeekStart); err != nil {
		return err
	}
	s.logged = 0
	return s.log.Sync()
}

// apply changes the state in memory; applying a change twice is harmless.
func (s *store) apply(c change) {
	for _, id := range c.Seen {
		s.remember(id)
	}
	for _, id := range c.Notify {
		if !slices.Contains(s.notify, id) {
			s.notify = append(s.notify, id)
		}
	}
	if n := len(s.notify) - maxNotify; n > 0 {
		s.notify = append([]string(nil), s.notify[n:]...)
	}
	if c.Op == "read" || c.Op == "delete" || c.Op == "notified" {
		s.notify = slices.DeleteFunc(s.notify, func(id string) bool { return slices.Contains(c.IDs, id) })
	}
	switch c.Op {
	case "add":
		for _, m := range c.Msgs {
			if s.byID[m.ID] != nil {
				continue
			}
			s.msgs = append(s.msgs, m)
			s.byID[m.ID] = m
		}
		if n := len(s.msgs) - maxMessages; n > 0 {
			for _, m := range s.msgs[:n] {
				delete(s.byID, m.ID)
			}
			s.msgs = append([]*ui.SMS(nil), s.msgs[n:]...)
		}
	case "state":
		for _, id := range c.IDs {
			if m := s.byID[id]; m != nil {
				m.State = c.State
				if c.At != 0 {
					m.At = c.At
				}
			}
		}
	case "read":
		for _, id := range c.IDs {
			if m := s.byID[id]; m != nil {
				m.Read = true
			}
		}
	case "delete":
		gone := map[string]bool{}
		for _, id := range c.IDs {
			if s.byID[id] != nil {
				gone[id] = true
				delete(s.byID, id)
			}
		}
		if len(gone) > 0 {
			keep := s.msgs[:0]
			for _, m := range s.msgs {
				if !gone[m.ID] {
					keep = append(keep, m)
				}
			}
			s.msgs = keep
		}
	case "block":
		s.blocked[c.Thread] = true
	case "unblock":
		delete(s.blocked, c.Thread)
	case "alert":
		for _, a := range s.alerts {
			if a.ID == c.Alert.ID {
				return
			}
		}
		s.alerts = append(s.alerts, *c.Alert)
		if n := len(s.alerts) - maxAlerts; n > 0 {
			s.alerts = append([]ui.SMSAlert(nil), s.alerts[n:]...)
		}
	}
}

func (s *store) remember(id string) {
	if s.seenSet[id] {
		return
	}
	s.seen = append(s.seen, id)
	s.seenSet[id] = true
	if n := len(s.seen) - maxSeen; n > 0 {
		for _, old := range s.seen[:n] {
			delete(s.seenSet, old)
		}
		s.seen = append([]string(nil), s.seen[n:]...)
	}
}

// thread is how smsd compares numbers: spaces, dashes, dots and brackets
// dropped, and a Chinese number's country code (+86, 0086), as the
// contacts store writes numbers without it. A sender with letters (a
// company's name) is compared case-insensitively.
func thread(number string) string {
	number = strings.TrimSpace(number)
	var b strings.Builder
	for _, r := range number {
		switch {
		case r >= '0' && r <= '9', r == '+' && b.Len() == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return strings.ToLower(number)
		}
	}
	n := b.String()
	for _, cc := range []string{"+86", "0086"} {
		if rest, ok := strings.CutPrefix(n, cc); ok && len(rest) >= 5 {
			return rest
		}
	}
	return n
}

// receive stores a received message unless it already did; nil when it
// did. A blocked sender's goes to the blocked folder.
func (s *store) receive(m ui.SMS) (*ui.SMS, error) {
	if s.seenSet[m.ID] || s.byID[m.ID] != nil {
		return nil, nil
	}
	m.Dir, m.Thread, m.Read, m.State, m.Name = "in", thread(m.Peer), false, "", ""
	if s.blocked[m.Thread] {
		m.Folder, m.Read = ui.SMSFolderBlocked, true
	}
	m.Code = findCode(m.Text)
	c := change{Op: "add", Msgs: []*ui.SMS{&m}, Seen: []string{m.ID}}
	if m.Folder == "" {
		c.Notify = []string{m.ID}
	}
	if err := s.commit(c); err != nil {
		return nil, err
	}
	return s.byID[m.ID], nil
}

func (s *store) addOutgoing(to, text string) (*ui.SMS, error) {
	s.nextID++
	now := s.now()
	m := &ui.SMS{ID: "o" + strconv.FormatInt(now.UnixNano(), 36) + "-" + strconv.Itoa(s.nextID), Peer: to, Dir: "out", Text: text,
		At: now.Unix(), Read: true, State: ui.SMSSending, Thread: thread(to)}
	if err := s.commit(change{Op: "add", Msgs: []*ui.SMS{m}}); err != nil {
		return nil, err
	}
	return m, nil
}

// importOld takes over messages telephonyd kept before smsd, in one
// change; it skips those it has. One left sending never will be sent.
func (s *store) importOld(old []ui.SMS) (int, error) {
	var add []*ui.SMS
	for i := range old {
		m := old[i]
		if m.ID == "" || s.byID[m.ID] != nil {
			continue
		}
		m.Thread, m.Name, m.Folder = thread(m.Peer), "", ""
		if m.Dir == "in" {
			m.State = ""
			m.Code = findCode(m.Text)
		} else if m.State == ui.SMSSending || m.State == "" {
			m.State = ui.SMSFailed
		}
		add = append(add, &m)
	}
	if len(add) == 0 {
		return 0, nil
	}
	return len(add), s.commit(change{Op: "add", Msgs: add})
}

var stateRank = map[string]int{ui.SMSSending: 0, ui.SMSSent: 1, ui.SMSDelivered: 2, ui.SMSFailed: 2}

// setState records an outgoing message's progress; reports that come late
// or twice do not take it back. resend allows sending again after failed.
func (s *store) setState(id, state string, resend bool) (*ui.SMS, error) {
	m := s.byID[id]
	if m == nil || m.Dir != "out" || m.State == state {
		return nil, nil
	}
	if r, ok := stateRank[state]; !ok || (!resend && r < stateRank[m.State]) {
		return nil, nil
	}
	c := change{Op: "state", IDs: []string{id}, State: state}
	if resend {
		c.At = s.now().Unix()
	}
	if err := s.commit(c); err != nil {
		return nil, err
	}
	return m, nil
}

// markRead marks a conversation's received messages read.
func (s *store) markRead(peer string) (int, error) {
	t := thread(peer)
	var ids []string
	for _, m := range s.msgs {
		if m.Thread == t && m.Dir == "in" && !m.Read {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return len(ids), s.commit(change{Op: "read", IDs: ids})
}

func (s *store) remove(ids []string, peer string) (int, error) {
	var gone []string
	if peer != "" {
		t := thread(peer)
		for _, m := range s.msgs {
			if m.Thread == t {
				gone = append(gone, m.ID)
			}
		}
	}
	for _, id := range ids {
		if s.byID[id] != nil {
			gone = append(gone, id)
		}
	}
	if len(gone) == 0 {
		return 0, nil
	}
	return len(gone), s.commit(change{Op: "delete", IDs: gone})
}

func (s *store) block(number string, on bool) error {
	t := thread(number)
	if t == "" {
		return errors.New("no number")
	}
	if s.blocked[t] == on {
		return nil
	}
	op := "unblock"
	if on {
		op = "block"
	}
	return s.commit(change{Op: op, Thread: t})
}

func (s *store) blockedList() []string {
	out := []string{}
	for b := range s.blocked {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// addAlert stores an emergency alert (a.ID is telephonyd's); one it has,
// or a repeat (the network sends an alert again and again while it
// lasts), gives nil.
func (s *store) addAlert(a ui.SMSAlert, serial int) (*ui.SMSAlert, error) {
	if s.seenSet[a.ID] {
		return nil, nil
	}
	key := fmt.Sprintf("cb:%d:%d", a.Channel, serial)
	for i := len(s.alerts) - 1; i >= 0; i-- {
		if old := s.alerts[i]; alertKey(old) == key && time.Duration(a.At-old.At)*time.Second < alertRepeat {
			return nil, nil
		}
	}
	tel := a.ID
	a.ID = key + ":" + tel
	if err := s.commit(change{Op: "alert", Alert: &a, Seen: []string{tel}, Notify: []string{a.ID}}); err != nil {
		return nil, err
	}
	return &s.alerts[len(s.alerts)-1], nil
}

// unnotified is what the shell has not shown yet.
func (s *store) unnotified() (msgs []ui.SMS, alerts []ui.SMSAlert) {
	msgs, alerts = []ui.SMS{}, []ui.SMSAlert{}
	for _, id := range s.notify {
		if m := s.byID[id]; m != nil {
			msgs = append(msgs, *m)
			continue
		}
		for _, a := range s.alerts {
			if a.ID == id {
				alerts = append(alerts, a)
			}
		}
	}
	return msgs, alerts
}

func (s *store) notified(ids []string) error {
	var known []string
	for _, id := range ids {
		if slices.Contains(s.notify, id) {
			known = append(known, id)
		}
	}
	if len(known) == 0 {
		return nil
	}
	return s.commit(change{Op: "notified", IDs: known})
}

// alertKey is the channel and serial number an alert's ID starts with.
func alertKey(a ui.SMSAlert) string {
	f := strings.SplitN(a.ID, ":", 4)
	if len(f) < 3 {
		return ""
	}
	return strings.Join(f[:3], ":")
}

func (s *store) alertList() []ui.SMSAlert {
	out := make([]ui.SMSAlert, 0, len(s.alerts))
	for i := len(s.alerts) - 1; i >= 0; i-- {
		out = append(out, s.alerts[i])
	}
	return out
}

// list answers sms.list, oldest first.
func (s *store) list(a ui.SMSListArgs) []ui.SMS {
	out := []ui.SMS{}
	t, q := thread(a.Peer), strings.ToLower(strings.TrimSpace(a.Query))
	for _, m := range s.msgs {
		if m.Folder != a.Folder || (a.Peer != "" && m.Thread != t) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(m.Text), q) && !strings.Contains(m.Peer, q) && !strings.Contains(m.Thread, digitsOf(q)) {
			continue
		}
		out = append(out, *m)
	}
	return out
}

func digitsOf(s string) string {
	d := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
	if len(d) < 3 {
		return "\x00"
	}
	return d
}

// threads answers sms.threads, newest first.
func (s *store) threads(folder string) []ui.SMSThread {
	by := map[string]*ui.SMSThread{}
	for _, m := range s.msgs {
		if m.Folder != folder {
			continue
		}
		t := by[m.Thread]
		if t == nil {
			t = &ui.SMSThread{Thread: m.Thread, Blocked: s.blocked[m.Thread]}
			by[m.Thread] = t
		}
		t.Count++
		if m.Dir == "in" && !m.Read {
			t.Unread++
		}
		if m.At >= t.Last.At {
			t.Last, t.Peer = *m, m.Peer
		}
	}
	out := make([]ui.SMSThread, 0, len(by))
	for _, t := range by {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Last.At != out[j].Last.At {
			return out[i].Last.At > out[j].Last.At
		}
		return out[i].Thread < out[j].Thread
	})
	return out
}

// stale is outgoing messages left sending since before cutoff: telephonyd
// went away while sending them (ModemManager gives up after two minutes).
func (s *store) stale(cutoff int64) []string {
	var ids []string
	for _, m := range s.msgs {
		if m.Dir == "out" && m.State == ui.SMSSending && m.At < cutoff {
			ids = append(ids, m.ID)
		}
	}
	return ids
}
