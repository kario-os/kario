package smsd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kario-os/kario/phonenum"
	"github.com/kario-os/kario/statefile"
	"github.com/kario-os/kario/ui"
)

// The store is a snapshot and an append-only log of the changes since: one
// JSON object a line, each synced before the change counts (a received
// message before telephonyd is told it may let it go). A line a crash cut
// short is dropped when the log is read; compaction writes a new snapshot
// and empties the log. A change carries its sequence number, so a crash
// between the two does not apply it twice. Files are 0600 in a 0700
// directory. No database: the volumes are a phone's. The parts of
// multimedia messages smsd keeps are files of their own (parts/), written
// before the change that names them; an outgoing one's PDU waits in
// outbox/ until the MMSC took it.

const (
	maxMessages  = 10000
	maxAlerts    = 500
	maxSeen      = 2000 // received ids remembered, for a message telephonyd reports again
	maxNotify    = 200  // new messages and alerts the shell has not shown
	maxSentIDs   = 256  // MMSC message IDs of sent multimedia messages, for their delivery reports
	maxDrafts    = 500
	compactEvery = 256 // log lines
	// A repeat of an alert (same channel and serial number) within this
	// time is the same alert.
	alertRepeat = 24 * time.Hour
)

type change struct {
	Seq    int64        `json:"seq"`
	Op     string       `json:"op"` // add | import | state | read | delete | block | unblock | blockopts | only | word | unword | alert | notified | region | mms | job | sentid | media | draft
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
	// Hidden and Unknown: the block list's switches (blockopts). Only:
	// minor mode's allowed numbers, with State "on" while it is (only).
	Hidden  *bool    `json:"hidden,omitempty"`
	Unknown *bool    `json:"unknown,omitempty"`
	Only    []string `json:"only,omitempty"`
	// Job: a multimedia message's download; MMSID an MMSC's message ID
	// (sentid); Index and Media a part put in the media library (media).
	Job   *mmsJob `json:"job,omitempty"`
	MMSID string  `json:"mms_id,omitempty"`
	Index int     `json:"index,omitempty"`
	Media string  `json:"media,omitempty"`
	// Draft: a conversation's draft, kept or (no text) dropped, by any
	// change; Word a word of the block list (word, unword).
	Draft *draft `json:"draft,omitempty"`
	Word  string `json:"word,omitempty"`
}

// draft is what the user typed for a conversation and left (sms.draft).
type draft struct {
	Peer string `json:"peer"`
	Text string `json:"text,omitempty"`
	At   int64  `json:"at,omitempty"`
}

// mmsJob is a received multimedia message's download: where to fetch it
// (the notification's content location and transaction), until when the
// MMSC keeps it, its size, the tries so far and when to try next (Unix
// seconds); Force: the user asked (a message in the blocked folder).
type mmsJob struct {
	ID       string `json:"id"`
	Location string `json:"location"`
	TxID     string `json:"txid,omitempty"`
	Expiry   int64  `json:"expiry,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Tries    int    `json:"tries,omitempty"`
	Next     int64  `json:"next,omitempty"`
	Force    bool   `json:"force,omitempty"`
}

type sentID struct {
	MMSID string `json:"mms_id"`
	ID    string `json:"id"`
}

type snapshot struct {
	Seq          int64         `json:"seq"`
	Msgs         []*ui.SMS     `json:"messages"`
	Alerts       []ui.SMSAlert `json:"alerts"`
	Blocked      []string      `json:"blocked"`
	Seen         []string      `json:"seen"`
	Notify       []string      `json:"notify"`
	Region       string        `json:"region,omitempty"`
	BlockHidden  bool          `json:"block_hidden,omitempty"`
	BlockUnknown bool          `json:"block_unknown,omitempty"`
	OnlyOn       bool          `json:"only_on,omitempty"`
	Only         []string      `json:"only,omitempty"`
	Jobs         []*mmsJob     `json:"jobs,omitempty"`
	Sent         []sentID      `json:"sent,omitempty"`
	Drafts       []draft       `json:"drafts,omitempty"`
	Words        []string      `json:"block_words,omitempty"`
}

type store struct {
	dir     string
	msgs    []*ui.SMS // oldest first
	byID    map[string]*ui.SMS
	alerts  []ui.SMSAlert
	blocked map[string]bool // block list entries (ui.BlockEntry)
	hidden  bool
	unknown bool
	// onlyOn: minor mode is on, and only allows these numbers, as the
	// shell gave them (SMSOnly).
	onlyOn  bool
	only    []string
	seen    []string
	seenSet map[string]bool
	notify  []string // what the shell has not shown yet, oldest first
	region  string
	jobs    map[string]*mmsJob
	sent    []sentID
	drafts  []draft
	words   []string // the block list's words, as the user wrote them
	// known: the contacts' numbers (keys), which the shell hands over; nil
	// until it did.
	known  map[string]bool
	seq    int64
	log    *os.File
	logged int
	now    func() time.Time
	nextID int
}

func (s *store) snapPath() string { return filepath.Join(s.dir, "snapshot.json") }
func (s *store) logPath() string  { return filepath.Join(s.dir, "log") }

// fileName is an id as a file name: ids are telephonyd's and smsd's own,
// but a name never leaves the directory whatever they hold.
func fileName(id string) string {
	ok := id != ""
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			ok = false
		}
	}
	if ok && len(id) <= 64 {
		return id
	}
	h := fnv.New64a()
	h.Write([]byte(id))
	return fmt.Sprintf("h%016x", h.Sum64())
}

func (s *store) partPath(id string, i int) string {
	return filepath.Join(s.dir, "parts", fileName(id)+"."+strconv.Itoa(i))
}

func (s *store) outboxPath(id string) string {
	return filepath.Join(s.dir, "outbox", fileName(id)+".pdu")
}

func openStore(dir string) (*store, error) {
	s := &store{dir: dir, byID: map[string]*ui.SMS{}, blocked: map[string]bool{}, seenSet: map[string]bool{}, jobs: map[string]*mmsJob{}, now: time.Now}
	for _, d := range []string{dir, filepath.Join(dir, "parts"), filepath.Join(dir, "outbox")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	if raw, err := os.ReadFile(s.snapPath()); err == nil {
		var snap snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return nil, fmt.Errorf("%s: %w", s.snapPath(), err)
		}
		s.seq, s.region, s.hidden, s.unknown, s.sent = snap.Seq, snap.Region, snap.BlockHidden, snap.BlockUnknown, snap.Sent
		s.onlyOn, s.only = snap.OnlyOn, snap.Only
		s.drafts, s.words = snap.Drafts, snap.Words
		s.apply(change{Op: "add", Msgs: snap.Msgs, Seen: snap.Seen, Notify: snap.Notify})
		for _, a := range snap.Alerts {
			s.apply(change{Op: "alert", Alert: &a})
		}
		for _, b := range snap.Blocked {
			s.blocked[b] = true
		}
		for _, j := range snap.Jobs {
			s.jobs[j.ID] = j
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
	s.rekey()
	s.sweepFiles()
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

// sweepFiles removes part and outbox files no message needs: written for a
// change a crash kept from the log, or left by a delete.
func (s *store) sweepFiles() {
	need := map[string]bool{}
	for _, m := range s.msgs {
		for i, p := range m.Parts {
			if p.Local {
				need[s.partPath(m.ID, i)] = true
			}
		}
		if m.Kind == ui.SMSKindMMS && m.Dir == "out" && m.State != ui.SMSSent && m.State != ui.SMSDelivered {
			need[s.outboxPath(m.ID)] = true
		}
	}
	for _, d := range []string{"parts", "outbox"} {
		names, _ := filepath.Glob(filepath.Join(s.dir, d, "*"))
		for _, n := range names {
			if !need[n] {
				_ = os.Remove(n)
			}
		}
	}
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
	snap := snapshot{Seq: s.seq, Msgs: s.msgs, Alerts: s.alerts, Seen: s.seen, Notify: s.notify, Blocked: s.blockedList(),
		Region: s.region, BlockHidden: s.hidden, BlockUnknown: s.unknown, OnlyOn: s.onlyOn, Only: s.only, Sent: s.sent, Drafts: s.drafts, Words: s.words}
	for _, j := range s.jobs {
		snap.Jobs = append(snap.Jobs, j)
	}
	sort.Slice(snap.Jobs, func(i, j int) bool { return snap.Jobs[i].ID < snap.Jobs[j].ID })
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if err := statefile.Write(s.snapPath(), raw, 0o600); err != nil {
		return err
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
	if c.Job != nil {
		j := *c.Job
		s.jobs[j.ID] = &j
	}
	if d := c.Draft; d != nil {
		t := s.thread(d.Peer)
		s.drafts = slices.DeleteFunc(s.drafts, func(x draft) bool { return s.thread(x.Peer) == t })
		if d.Text != "" {
			s.drafts = append(s.drafts, *d)
		}
		if n := len(s.drafts) - maxDrafts; n > 0 {
			s.drafts = append([]draft(nil), s.drafts[n:]...)
		}
	}
	switch c.Op {
	case "add", "import":
		for _, m := range c.Msgs {
			if s.byID[m.ID] != nil {
				continue
			}
			m.Thread = s.threadOf(m)
			s.byID[m.ID] = m
			if c.Op == "add" {
				s.msgs = append(s.msgs, m)
				continue
			}
			// Imported messages are older than most: each goes in by its
			// time, before the first one later than it, the others staying
			// in the order they came.
			i := slices.IndexFunc(s.msgs, func(o *ui.SMS) bool { return o.At > m.At })
			if i < 0 {
				i = len(s.msgs)
			}
			s.msgs = slices.Insert(s.msgs, i, m)
		}
		if n := len(s.msgs) - maxMessages; n > 0 {
			for _, m := range s.msgs[:n] {
				s.forget(m)
			}
			s.msgs = append([]*ui.SMS(nil), s.msgs[n:]...)
		}
	case "state":
		for _, id := range c.IDs {
			if m := s.byID[id]; m != nil {
				m.State = c.State
				if c.At != 0 {
					// Sent again or as scheduled: it is the conversation's
					// latest now.
					m.At = c.At
					if i := slices.Index(s.msgs, m); i >= 0 {
						s.msgs = append(slices.Delete(s.msgs, i, i+1), m)
					}
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
			if m := s.byID[id]; m != nil {
				gone[id] = true
				s.forget(m)
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
	case "word", "unword":
		k := ui.WordKey(c.Word)
		s.words = slices.DeleteFunc(s.words, func(w string) bool { return ui.WordKey(w) == k })
		if c.Op == "word" && k != "" {
			s.words = append(s.words, c.Word)
		}
	case "blockopts":
		if c.Hidden != nil {
			s.hidden = *c.Hidden
		}
		if c.Unknown != nil {
			s.unknown = *c.Unknown
		}
	case "only":
		s.onlyOn, s.only = c.State == "on", c.Only
	case "region":
		s.region = c.State
		s.rekey()
	case "mms":
		for _, u := range c.Msgs {
			m := s.byID[u.ID]
			if m == nil {
				continue
			}
			m.Peer, m.Text, m.Subject, m.Parts, m.Members, m.MMS, m.Code, m.At = u.Peer, u.Text, u.Subject, u.Parts, u.Members, u.MMS, u.Code, u.At
			if u.Folder != "" {
				m.Folder, m.Read = u.Folder, true
			}
			m.Thread = s.threadOf(m)
			if m.MMS == "" || m.MMS == ui.MMSExpired {
				delete(s.jobs, m.ID)
			}
		}
	case "media":
		if len(c.IDs) == 1 {
			if m := s.byID[c.IDs[0]]; m != nil && c.Index >= 0 && c.Index < len(m.Parts) {
				m.Parts[c.Index].Media, m.Parts[c.Index].Local = c.Media, false
				_ = os.Remove(s.partPath(m.ID, c.Index))
			}
		}
	case "sentid":
		if len(c.IDs) == 1 && !slices.ContainsFunc(s.sent, func(x sentID) bool { return x.MMSID == c.MMSID }) {
			s.sent = append(s.sent, sentID{MMSID: c.MMSID, ID: c.IDs[0]})
			if n := len(s.sent) - maxSentIDs; n > 0 {
				s.sent = append([]sentID(nil), s.sent[n:]...)
			}
		}
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

// forget drops a message from the index, with its download and its files.
func (s *store) forget(m *ui.SMS) {
	delete(s.byID, m.ID)
	delete(s.jobs, m.ID)
	for i, p := range m.Parts {
		if p.Local {
			_ = os.Remove(s.partPath(m.ID, i))
		}
	}
	if m.Kind == ui.SMSKindMMS && m.Dir == "out" {
		_ = os.Remove(s.outboxPath(m.ID))
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

// reg is the region numbers written nationally are read in: the shell's
// (SMSRegion), else the default.
func (s *store) reg() string {
	if s.region != "" {
		return s.region
	}
	return phonenum.DefaultRegion
}

// key is how smsd compares numbers (phonenum.Key in its region): E.164
// for a number of a length its country uses, a short code's digits, a
// sender with letters lower-cased.
func (s *store) key(number string) string { return phonenum.Key(number, s.reg()) }

// splitPeers is a recipient list (commas or semicolons between numbers).
func splitPeers(peer string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(peer, func(r rune) bool { return r == ',' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// thread is a conversation's key: one number's key, or a group's keys,
// sorted and joined by commas.
func (s *store) thread(peer string) string {
	peers := splitPeers(peer)
	if len(peers) <= 1 {
		return s.key(peer)
	}
	var keys []string
	for _, p := range peers {
		if k := s.key(p); k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func (s *store) threadOf(m *ui.SMS) string {
	if len(m.Members) > 1 {
		return s.thread(strings.Join(m.Members, ","))
	}
	return s.thread(m.Peer)
}

// rekey reads every number again in the region (it changed, or the store
// comes from before E.164 keys).
func (s *store) rekey() {
	for _, m := range s.msgs {
		m.Thread = s.threadOf(m)
	}
	old := s.blocked
	s.blocked = map[string]bool{}
	for e := range old {
		if !strings.HasSuffix(e, "*") {
			e = s.key(e)
		}
		if e != "" {
			s.blocked[e] = true
		}
	}
}

func (s *store) setRegion(region string) error {
	region = strings.ToUpper(strings.TrimSpace(region))
	if region == "" || region == s.region {
		return nil
	}
	return s.commit(change{Op: "region", State: region})
}

func (s *store) blockList() ui.BlockList {
	return ui.BlockList{Numbers: s.blockedList(), Hidden: s.hidden, Unknown: s.unknown, Words: append([]string(nil), s.words...)}
}

// blockedFor is why a received message goes to the blocked folder: its
// sender (blockedWhy), or a word of the list in its subject or text; "".
func (s *store) blockedFor(m ui.SMS) string {
	if why := s.blockedWhy(m.Peer); why != "" {
		return why
	}
	bl := ui.BlockList{Words: s.words}
	if bl.BlockedText(m.Subject) != "" || bl.BlockedText(m.Text) != "" {
		return ui.BlockedWord
	}
	return ""
}

// blockedWhy is why messages from number go to the blocked folder, "".
func (s *store) blockedWhy(number string) string {
	var known func(string) bool
	if s.known != nil {
		known = func(n string) bool { return s.known[s.key(n)] }
	}
	if why := s.blockList().Blocked(number, s.reg(), known); why != "" {
		return why
	}
	if s.onlyOn && !slices.ContainsFunc(s.only, func(n string) bool { return s.key(n) == s.key(number) }) {
		return ui.BlockedMinor
	}
	return ""
}

// setOnly is SMSOnly: minor mode on with the numbers it allows, or off.
func (s *store) setOnly(on bool, numbers []string) error {
	if !on {
		numbers = nil
	}
	if on == s.onlyOn && slices.Equal(numbers, s.only) {
		return nil
	}
	return s.commit(change{Op: "only", State: map[bool]string{true: "on"}[on], Only: numbers})
}

// receive stores a received message unless it already did; nil when it
// did. A blocked sender's goes to the blocked folder.
func (s *store) receive(m ui.SMS) (*ui.SMS, error) {
	if s.seenSet[m.ID] || s.byID[m.ID] != nil {
		return nil, nil
	}
	m.Dir, m.Read, m.State, m.Name = "in", false, "", ""
	if s.blockedFor(m) != "" {
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

// receiveMMS stores a multimedia message's notification, to be
// downloaded (job); nil for one it has (the same id, or the same content
// location offered again). Nothing is notified before the download.
func (s *store) receiveMMS(m ui.SMS, job mmsJob) (*ui.SMS, error) {
	if s.seenSet[m.ID] || s.byID[m.ID] != nil {
		return nil, nil
	}
	for _, j := range s.jobs {
		if j.Location == job.Location {
			return nil, s.commit(change{Op: "add", Seen: []string{m.ID}})
		}
	}
	m.Dir, m.Read, m.State, m.Name, m.Kind, m.MMS = "in", false, "", "", ui.SMSKindMMS, ui.MMSPending
	if s.blockedFor(m) != "" {
		m.Folder, m.Read = ui.SMSFolderBlocked, true
	}
	job.ID = m.ID
	if err := s.commit(change{Op: "add", Msgs: []*ui.SMS{&m}, Seen: []string{m.ID}, Job: &job}); err != nil {
		return nil, err
	}
	return s.byID[m.ID], nil
}

// mmsUpdate records a received multimedia message's content or download
// state (u: its fields), and the job's next try; notify gives the shell
// the message to show.
func (s *store) mmsUpdate(u ui.SMS, job *mmsJob, notify bool) (*ui.SMS, error) {
	m := s.byID[u.ID]
	if m == nil {
		return nil, errors.New("no such message")
	}
	c := change{Op: "mms", Msgs: []*ui.SMS{&u}, Job: job}
	if notify && m.Folder == "" && u.Folder == "" {
		c.Notify = []string{u.ID}
	}
	if err := s.commit(c); err != nil {
		return nil, err
	}
	return m, nil
}

// dueJobs are the downloads to try now: due, of a message still waiting
// and not in the blocked folder unless the user asked.
func (s *store) dueJobs(now int64) []mmsJob {
	var out []mmsJob
	for _, j := range s.jobs {
		m := s.byID[j.ID]
		if m == nil || j.Next > now || (m.MMS != ui.MMSPending && m.MMS != ui.MMSDownloading) || (m.Folder != "" && !j.Force) {
			continue
		}
		out = append(out, *j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *store) waitingJobs() int {
	n := 0
	for _, j := range s.jobs {
		if m := s.byID[j.ID]; m != nil && (m.MMS == ui.MMSPending || m.MMS == ui.MMSDownloading) {
			n++
		}
	}
	return n
}

// addOutgoing stores a message going out, sending or, with a due time,
// scheduled; the conversation's draft goes.
func (s *store) addOutgoing(to, text string, due int64, zone string) (*ui.SMS, error) {
	s.nextID++
	now := s.now()
	m := &ui.SMS{ID: "o" + strconv.FormatInt(now.UnixNano(), 36) + "-" + strconv.Itoa(s.nextID), Peer: to, Dir: "out", Text: text,
		At: now.Unix(), Read: true, State: ui.SMSSending}
	scheduled(m, due, zone)
	if err := s.commit(change{Op: "add", Msgs: []*ui.SMS{m}, Draft: &draft{Peer: to}}); err != nil {
		return nil, err
	}
	return m, nil
}

func scheduled(m *ui.SMS, due int64, zone string) {
	if due > 0 {
		m.State, m.Due, m.Zone = ui.SMSScheduled, due, zone
	}
}

// addOutgoingMMS stores a multimedia message going out, and its PDU (pdu
// gets the message's ID) until the MMSC takes it.
func (s *store) addOutgoingMMS(to []string, text, subject string, parts []ui.SMSPart, due int64, zone string, pdu func(id string) ([]byte, error)) (*ui.SMS, error) {
	s.nextID++
	now := s.now()
	m := &ui.SMS{ID: "o" + strconv.FormatInt(now.UnixNano(), 36) + "-" + strconv.Itoa(s.nextID), Peer: strings.Join(to, ", "), Dir: "out",
		Text: text, Subject: subject, Parts: parts, Kind: ui.SMSKindMMS, At: now.Unix(), Read: true, State: ui.SMSSending}
	if len(to) > 1 {
		m.Members = to
	}
	scheduled(m, due, zone)
	raw, err := pdu(m.ID)
	if err != nil {
		return nil, err
	}
	if err := statefile.Write(s.outboxPath(m.ID), raw, 0o600); err != nil {
		return nil, err
	}
	if err := s.commit(change{Op: "add", Msgs: []*ui.SMS{m}, Draft: &draft{Peer: m.Peer}}); err != nil {
		_ = os.Remove(s.outboxPath(m.ID))
		return nil, err
	}
	return m, nil
}

func (s *store) recordSent(mmsID, id string) error {
	if mmsID == "" {
		return nil
	}
	return s.commit(change{Op: "sentid", MMSID: mmsID, IDs: []string{id}})
}

func (s *store) bySentID(mmsID string) string {
	for _, x := range s.sent {
		if x.MMSID == mmsID {
			return x.ID
		}
	}
	return ""
}

func (s *store) setPartMedia(id string, index int, media string) error {
	m := s.byID[id]
	if m == nil || index < 0 || index >= len(m.Parts) || !m.Parts[index].Local {
		return errors.New("no such part")
	}
	if !strings.HasPrefix(media, ui.MediaPrefix) {
		return errors.New("not a library photo")
	}
	return s.commit(change{Op: "media", IDs: []string{id}, Index: index, Media: media})
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
		m.Name, m.Folder, m.Kind, m.Parts, m.Members, m.MMS = "", "", "", nil, nil, ""
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

// importMessages stores messages another phone kept (the switch receiver,
// docs/36; a backup restored): read or unread as they were, sent ones as
// sent (or not sent), marked imported, in one change that notifies
// nothing. A multimedia message keeps its subject, its text and a group's
// members. It gives each one's new ID, "" for one it left out: unreadable,
// or one it has already (the same conversation, direction, time and text).
func (s *store) importMessages(in []ui.SMS) ([]string, error) {
	type key struct {
		thread, dir, text string
		at                int64
	}
	have := map[key]bool{}
	for _, m := range s.msgs {
		have[key{m.Thread, m.Dir, m.Text, m.At}] = true
	}
	ids := make([]string, len(in))
	var add []*ui.SMS
	now := s.now()
	for i, x := range in {
		peer := strings.TrimSpace(x.Peer)
		m := &ui.SMS{Peer: peer, Dir: x.Dir, Text: limitText(x.Text, maxMMSText), At: x.At, Read: x.Read || x.Dir == "out", Imported: true}
		if x.Kind == ui.SMSKindMMS {
			m.Kind, m.Subject = ui.SMSKindMMS, limitText(strings.TrimSpace(x.Subject), 200)
			for _, p := range x.Members {
				if p = strings.TrimSpace(p); p != "" && utf8.RuneCountInString(p) <= 20 && len(m.Members) < ui.MaxGroup {
					m.Members = append(m.Members, p)
				}
			}
			if len(m.Members) < 2 {
				m.Members = nil
			}
		}
		k := key{s.threadOf(m), x.Dir, m.Text, x.At}
		if peer == "" || (strings.TrimSpace(m.Text) == "" && m.Subject == "") || (x.Dir != "in" && x.Dir != "out") || x.At <= 0 || have[k] {
			continue
		}
		have[k] = true
		s.nextID++
		m.ID = "i" + strconv.FormatInt(now.UnixNano(), 36) + "-" + strconv.Itoa(s.nextID)
		if m.Dir == "out" {
			m.State = ui.SMSSent
			if x.State == ui.SMSFailed {
				m.State = ui.SMSFailed
			}
		} else {
			m.Code = findCode(m.Text)
			if s.blockedFor(*m) != "" {
				m.Folder, m.Read = ui.SMSFolderBlocked, true
			}
		}
		ids[i] = m.ID
		add = append(add, m)
	}
	if len(add) == 0 {
		return ids, nil
	}
	if err := s.commit(change{Op: "import", Msgs: add}); err != nil {
		return nil, err
	}
	return ids, nil
}

// removeImported deletes the imported messages among ids (an import
// undone); the others are left alone.
func (s *store) removeImported(ids []string) (int, error) {
	var gone []string
	for _, id := range ids {
		if m := s.byID[id]; m != nil && m.Imported {
			gone = append(gone, id)
		}
	}
	if len(gone) == 0 {
		return 0, nil
	}
	return len(gone), s.commit(change{Op: "delete", IDs: gone})
}

var stateRank = map[string]int{ui.SMSScheduled: -1, ui.SMSSending: 0, ui.SMSSent: 1, ui.SMSDelivered: 2, ui.SMSFailed: 2}

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
	t := s.thread(peer)
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
		t := s.thread(peer)
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
	e := ui.BlockEntry(number, s.reg())
	if e == "" {
		return errors.New("no number")
	}
	if s.blocked[e] == on {
		return nil
	}
	op := "unblock"
	if on {
		op = "block"
	}
	return s.commit(change{Op: op, Thread: e})
}

// setBlock is block.set: a number or prefix, a word, and the two switches.
func (s *store) setBlock(a ui.BlockSetArgs) error {
	if strings.TrimSpace(a.Number) != "" {
		if err := s.block(a.Number, a.On); err != nil {
			return err
		}
	}
	if strings.TrimSpace(a.Word) != "" {
		if err := s.blockWord(a.Word, a.On); err != nil {
			return err
		}
	}
	if (a.Hidden != nil && *a.Hidden != s.hidden) || (a.Unknown != nil && *a.Unknown != s.unknown) {
		return s.commit(change{Op: "blockopts", Hidden: a.Hidden, Unknown: a.Unknown})
	}
	return nil
}

// blockWord puts a word on the block list or takes it off; words that
// match alike (ui.WordKey) are one.
func (s *store) blockWord(word string, on bool) error {
	word = strings.Join(strings.Fields(word), " ")
	k := ui.WordKey(word)
	switch {
	case k == "":
		return errors.New("a word needs letters or digits")
	case utf8.RuneCountInString(word) > ui.MaxBlockWord:
		return fmt.Errorf("at most %d characters a word", ui.MaxBlockWord)
	}
	i := slices.IndexFunc(s.words, func(w string) bool { return ui.WordKey(w) == k })
	switch {
	case on == (i >= 0):
		return nil
	case on && len(s.words) >= ui.MaxBlockWords:
		return fmt.Errorf("at most %d words", ui.MaxBlockWords)
	case on:
		return s.commit(change{Op: "word", Word: word})
	}
	return s.commit(change{Op: "unword", Word: s.words[i]})
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
	t, q := s.thread(a.Peer), strings.ToLower(strings.TrimSpace(a.Query))
	for _, m := range s.msgs {
		if m.Folder != a.Folder || (a.Peer != "" && m.Thread != t) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(m.Text), q) && !strings.Contains(strings.ToLower(m.Subject), q) &&
			!strings.Contains(m.Peer, q) && !strings.Contains(m.Thread, digitsOf(q)) {
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

// threads answers sms.threads, newest first. A group's Peer is its
// members, as sms.list and sms.send take them.
func (s *store) threads(folder string) []ui.SMSThread {
	by := map[string]*ui.SMSThread{}
	for _, m := range s.msgs {
		if m.Folder != folder {
			continue
		}
		t := by[m.Thread]
		if t == nil {
			t = &ui.SMSThread{Thread: m.Thread}
			by[m.Thread] = t
		}
		t.Count++
		if m.Dir == "in" && !m.Read {
			t.Unread++
		}
		if m.At >= t.Last.At {
			t.Last, t.Peer = *m, m.Peer
			if len(m.Members) > 1 {
				t.Peer = strings.Join(m.Members, ", ")
			}
		}
	}
	if folder == "" {
		for _, d := range s.drafts {
			k := s.thread(d.Peer)
			t := by[k]
			if t == nil {
				t = &ui.SMSThread{Thread: k, Peer: d.Peer}
				by[k] = t
			}
			t.Draft, t.DraftAt = d.Text, d.At
		}
	}
	out := make([]ui.SMSThread, 0, len(by))
	for _, t := range by {
		if why := s.blockedWhy(t.Peer); why == ui.BlockedNumber || why == ui.BlockedPrefix {
			t.Blocked = true
		}
		out = append(out, *t)
	}
	when := func(t ui.SMSThread) int64 { return max(t.Last.At, t.DraftAt) }
	sort.Slice(out, func(i, j int) bool {
		if when(out[i]) != when(out[j]) {
			return when(out[i]) > when(out[j])
		}
		return out[i].Thread < out[j].Thread
	})
	return out
}

// setDraft keeps a conversation's draft; empty text drops it.
func (s *store) setDraft(peer, text string) error {
	peer = strings.TrimSpace(peer)
	if s.thread(peer) == "" {
		return errors.New("enter a number")
	}
	if utf8.RuneCountInString(text) > maxMMSText {
		return errors.New("the draft is too long")
	}
	if strings.TrimSpace(text) == "" {
		text = ""
	}
	if s.draftOf(peer) == text {
		return nil
	}
	return s.commit(change{Op: "draft", Draft: &draft{Peer: peer, Text: text, At: s.now().Unix()}})
}

func (s *store) draftOf(peer string) string {
	t := s.thread(peer)
	for _, d := range s.drafts {
		if s.thread(d.Peer) == t {
			return d.Text
		}
	}
	return ""
}

// dueScheduled are the scheduled messages due by now, and when the next
// one is (0: none).
func (s *store) dueScheduled(now int64) (ids []string, next int64) {
	for _, m := range s.msgs {
		switch {
		case m.State != ui.SMSScheduled:
		case m.Due <= now:
			ids = append(ids, m.ID)
		case next == 0 || m.Due < next:
			next = m.Due
		}
	}
	return ids, next
}

func (s *store) countScheduled() int {
	n := 0
	for _, m := range s.msgs {
		if m.State == ui.SMSScheduled {
			n++
		}
	}
	return n
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
