package caldav

// The system cannot read the calendar app's storage, nor the app the
// server's password: the events go between them through the Shell, as
// changes of whole objects (an event with its changed occurrences, one
// UID). Queue is what the Shell keeps of them between runs, on its loop:
// the server's changes the app has not taken yet (Inbox; the app takes
// them with calendar.pull) and the app's the server does not have yet
// (Outbox; calendar.push), the app's offline queue. A sync reaches it as
// Local.

// Change is a whole object of one UID, or its deletion (Gone).
type Change struct {
	UID  string `json:"uid"`
	ICS  string `json:"ics,omitempty"`
	Gone bool   `json:"gone,omitempty"`
	// Copy: the phone's edit of an event the server changed meanwhile,
	// kept as a new event of its own (the app says so).
	Copy bool `json:"copy,omitempty"`
	// All: part of the app's first hand-over of all its events; one the
	// server has by the same UID is the server's (no copy).
	All bool `json:"all,omitempty"`
	// Seq numbers the Inbox's changes for the app to say which it took;
	// Rev the Outbox's, so a change sent while the app changed the UID
	// again stays.
	Seq int `json:"seq,omitempty"`
	Rev int `json:"rev,omitempty"`
}

// Local is the phone's side of a run: the app's changes not on the server
// yet (Pending, in order), one of them now on the server (Done, unless the
// app changed the UID again since), a change the system makes for the
// server (Queue: a copy), and the server's changes for the app (Deliver).
type Local interface {
	Pending() ([]Change, error)
	Done(ch Change) error
	Queue(ch Change) error
	Deliver(chs []Change) error
}

// The bounds of the queues: an inbox the app does not take (a calendar
// full) or an outbox the server does not take stops growing there.
const (
	MaxQueued      = 4000
	MaxQueuedBytes = 16 << 20
)

// Queue is the Shell's inbox and outbox, and whether the app is to hand
// over all its events (a first sync).
type Queue struct {
	Inbox   []Change `json:"inbox,omitempty"`
	Outbox  []Change `json:"outbox,omitempty"`
	Seq     int      `json:"seq"`
	Rev     int      `json:"rev"`
	WantAll bool     `json:"want_all,omitempty"`
}

func size(list []Change) int {
	n := 0
	for _, c := range list {
		n += len(c.ICS)
	}
	return n
}

// Push takes a change from the app: the UID's earlier one, if not sent
// yet, gives way to it. False when the outbox is full.
func (q *Queue) Push(ch Change) bool {
	ch.Seq, ch.Copy = 0, false
	q.Rev++
	ch.Rev = q.Rev
	for i, c := range q.Outbox {
		if c.UID == ch.UID {
			ch.All = ch.All && c.All
			q.Outbox = append(q.Outbox[:i], q.Outbox[i+1:]...)
			break
		}
	}
	if len(q.Outbox) >= MaxQueued || size(q.Outbox)+len(ch.ICS) > MaxQueuedBytes {
		return false
	}
	q.Outbox = append(q.Outbox, ch)
	return true
}

// Pending is the outbox, in order.
func (q *Queue) Pending() []Change { return append([]Change(nil), q.Outbox...) }

// Done drops a change sent, unless the app changed its UID since.
func (q *Queue) Done(ch Change) {
	for i, c := range q.Outbox {
		if c.UID == ch.UID && c.Rev == ch.Rev {
			q.Outbox = append(q.Outbox[:i], q.Outbox[i+1:]...)
			return
		}
	}
}

// Deliver adds the server's changes for the app: a UID's earlier change
// not taken yet gives way to a later one.
func (q *Queue) Deliver(chs []Change) {
	for _, ch := range chs {
		for i, c := range q.Inbox {
			if c.UID == ch.UID && !(ch.Copy || c.Copy) {
				q.Inbox = append(q.Inbox[:i], q.Inbox[i+1:]...)
				break
			}
		}
		if len(q.Inbox) >= MaxQueued || size(q.Inbox)+len(ch.ICS) > MaxQueuedBytes {
			continue
		}
		q.Seq++
		ch.Seq, ch.Rev, ch.All = q.Seq, 0, false
		q.Inbox = append(q.Inbox, ch)
	}
}

// Offer is the inbox's first changes for the app, at most n and about
// bytes (one at least), and whether there are more.
func (q *Queue) Offer(n, bytes int) ([]Change, bool) {
	var out []Change
	total := 0
	for _, c := range q.Inbox {
		if len(out) == n || (len(out) > 0 && total+len(c.ICS) > bytes) {
			return out, true
		}
		out = append(out, c)
		total += len(c.ICS)
	}
	return out, false
}

// Take drops the inbox's changes the app took (by Seq).
func (q *Queue) Take(seqs []int) {
	if len(seqs) == 0 {
		return
	}
	taken := map[int]bool{}
	for _, s := range seqs {
		taken[s] = true
	}
	kept := q.Inbox[:0]
	for _, c := range q.Inbox {
		if !taken[c.Seq] {
			kept = append(kept, c)
		}
	}
	q.Inbox = kept
}
