package caldav

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/kario-os/kario/carddav"
)

// A sync, both ways, between the calendar app's events (through Local)
// and one calendar, an object per UID (an event with its changed
// occurrences):
//
//  1. Pull: what changed on the server since the last sync, by its sync
//     token (sync-collection, RFC 6578) in pages of Limit, or, without one,
//     by comparing every object's ETag with the last ones (a CTag that did
//     not move skips that); a token the server no longer knows starts over
//     from none, when an object the phone knows that the whole listing
//     lacks was deleted there. The changed objects are read in batches
//     (calendar-multiget) and go to the app (Deliver); the token moves on
//     once all of them are delivered.
//  2. Push: the app's changes: deleted (DELETE with If-Match), changed (the
//     server's object read again and the phone's edit patched into it, PUT
//     with If-Match: Patch keeps what the app does not show) and new (PUT
//     with If-None-Match: *, under a new random name).
//
// Conflicts: the server wins, and nothing the user did is lost. An event
// both sides changed (seen in the pull, or as a changed ETag or a 412 when
// writing) takes the server's version on the phone, and the phone's edit
// becomes a new event of its own (a new UID) on both sides, which the app
// points out (Change.Copy): the phone's and the server's clocks say nothing
// reliable about which edit came later, and an edit made offline days ago
// must not quietly undo a later one elsewhere. An edit beats a delete, on
// either side: deleted on the server and changed on the phone, the phone's
// goes up again as new; deleted on the phone and changed on the server,
// the server's comes back. In the first sync (the app's hand-over of all
// its events, Change.All) an event the server has by the same UID is the
// server's, without a copy.

// State is what a sync keeps between runs, for one calendar.
type State struct {
	Calendar string `json:"calendar"`
	Token    string `json:"token,omitempty"`
	CTag     string `json:"ctag,omitempty"`
	// Synced: a sync has run to its end at least once.
	Synced bool    `json:"synced,omitempty"`
	Items  []*Item `json:"items"`
}

// Item is an object on the server the phone knows.
type Item struct {
	Href string `json:"href"`
	ETag string `json:"etag,omitempty"`
	UID  string `json:"uid,omitempty"`
	// Remote: the event came from the server (signing out may take those
	// off the phone); New: the phone is creating the object.
	Remote bool `json:"remote,omitempty"`
	New    bool `json:"new,omitempty"`
	// Skip: an object the phone leaves alone (SkipOther, SkipLarge).
	Skip string `json:"skip,omitempty"`
}

// Why an object is left alone: not one with events (a task, a journal), or
// larger than MaxObject.
const (
	SkipOther = "other"
	SkipLarge = "large"
)

// Events is how many events the state knows on the server.
func (st *State) Events() int {
	n := 0
	for _, it := range st.Items {
		if it.Skip == "" && !it.New {
			n++
		}
	}
	return n
}

// Result is what one run did, and the first object it could not write
// (Problem, Failed of them); the run goes on past those.
type Result struct {
	Delivered, Deleted int // to the app
	Uploaded, Removed  int // on the server
	Conflicts          int
	Failed             int
	Problem            error
}

// The phase a run notifies (Progress, as carddav's).
const PhaseCalendar = "calendar"

// Sync is one account's calendar sync; Run is one run of it.
type Sync struct {
	Client   *Client
	Calendar Calendar
	Local    Local
	State    *State
	Save     func(*State) error
	Now      func() time.Time
	NewUID   func() string
	Notify   carddav.Progress
	Limit    int // changes a sync-collection page asks for (0: 500)
	Batch    int // objects a multiget asks for (0: 50)
	noLimit  bool
	cal      *url.URL
	res      Result
	byHref   map[string]*Item
	byUID    map[string]*Item
	pending  map[string]Change
	out      []Change
}

func (s *Sync) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sync) batch() int {
	if s.Batch > 0 {
		return s.Batch
	}
	return 50
}

func (s *Sync) save() error {
	if s.Save == nil {
		return nil
	}
	return s.Save(s.State)
}

func (s *Sync) progress(done, total int) {
	if s.Notify != nil {
		s.Notify(PhaseCalendar, done, total)
	}
}

func (s *Sync) key(href string) string { return carddav.HrefKey(s.cal, href) }

func (s *Sync) index() {
	s.byHref, s.byUID = map[string]*Item{}, map[string]*Item{}
	for _, it := range s.State.Items {
		s.byHref[s.key(it.Href)] = it
		if it.UID != "" && it.Skip == "" {
			s.byUID[it.UID] = it
		}
	}
}

func (s *Sync) add(it *Item) {
	s.State.Items = append(s.State.Items, it)
	s.byHref[s.key(it.Href)] = it
	if it.UID != "" && it.Skip == "" {
		s.byUID[it.UID] = it
	}
}

func (s *Sync) drop(it *Item) {
	for i, x := range s.State.Items {
		if x == it {
			s.State.Items = append(s.State.Items[:i], s.State.Items[i+1:]...)
			break
		}
	}
	delete(s.byHref, s.key(it.Href))
	if s.byUID[it.UID] == it {
		delete(s.byUID, it.UID)
	}
}

// deliver holds a change for the app until the next flush.
func (s *Sync) deliver(ch Change) {
	s.out = append(s.out, ch)
	if ch.Gone {
		s.res.Deleted++
	} else {
		s.res.Delivered++
	}
}

func (s *Sync) flush() error {
	if len(s.out) == 0 {
		return nil
	}
	out := s.out
	s.out = nil
	return s.Local.Deliver(out)
}

func (s *Sync) done(ch Change) error {
	delete(s.pending, ch.UID)
	return s.Local.Done(ch)
}

// failed counts an object that could not be written; an error that is not
// about the object ends the run.
func (s *Sync) failed(err error) error {
	switch {
	case errors.Is(err, carddav.ErrAuth), errors.Is(err, carddav.ErrFull), errors.Is(err, context.Canceled), carddav.Temporary(err):
		return err
	}
	s.res.Failed++
	if s.res.Problem == nil {
		s.res.Problem = err
	}
	return nil
}

// Run is one sync. The state is saved as it goes (Save), and what goes to
// the app is delivered before the state that follows from it is saved, so
// a run cut short loses none of what it did.
func (s *Sync) Run(ctx context.Context) (Result, error) {
	s.res, s.out = Result{}, nil
	u, err := url.Parse(s.Calendar.URL)
	if err != nil {
		return s.res, err
	}
	s.cal = u
	if s.State == nil {
		s.State = &State{}
	}
	if s.State.Calendar != s.Calendar.URL {
		*s.State = State{Calendar: s.Calendar.URL}
	}
	if s.NewUID == nil {
		s.NewUID = carddav.RandomUID
	}
	s.index()
	if err := s.loadPending(); err != nil {
		return s.res, err
	}
	err = s.pull(ctx)
	if err == nil {
		err = s.push(ctx)
	}
	if ferr := s.flush(); err == nil {
		err = ferr
	}
	if err != nil {
		_ = s.save()
		return s.res, err
	}
	s.State.Synced = true
	return s.res, s.save()
}

func (s *Sync) loadPending() error {
	list, err := s.Local.Pending()
	if err != nil {
		return err
	}
	s.pending = map[string]Change{}
	for _, c := range list {
		s.pending[c.UID] = c
	}
	return nil
}

// remoteChanges is what changed on the server: the objects to read, the
// ones deleted, the token to keep once they are in, and whether the
// listing was whole (an object it lacks is gone).
func (s *Sync) remoteChanges(ctx context.Context) (changed []carddav.Card, deleted []string, token, ctag string, whole bool, err error) {
	st := s.State
	if !s.Calendar.Sync {
		_, ctag, err = s.Client.State(ctx, s.cal)
		if err != nil {
			return
		}
		if ctag != "" && ctag == st.CTag && st.Synced {
			return nil, nil, "", ctag, false, nil
		}
		changed, err = s.Client.List(ctx, s.cal)
		return changed, nil, "", ctag, true, err
	}
	token = st.Token
	whole = token == ""
	limit := s.Limit
	if limit <= 0 {
		limit = 500
	}
	for page := 0; ; page++ {
		lim := limit
		if s.noLimit {
			lim = 0
		}
		var ch carddav.Changes
		ch, err = s.Client.SyncCollection(ctx, s.cal, token, lim)
		var se *carddav.StatusError
		switch {
		case errors.Is(err, carddav.ErrToken) && token != "":
			token, whole, changed, deleted, page = "", true, nil, nil, -1
			continue
		case errors.As(err, &se) && !se.Temporary() && page == 0 && lim > 0:
			s.noLimit, page = true, -1
			continue
		case errors.As(err, &se) && !se.Temporary() && page == 0:
			s.Calendar.Sync = false
			return s.remoteChanges(ctx)
		case err != nil:
			return nil, nil, "", "", false, err
		}
		changed = append(changed, ch.Changed...)
		deleted = append(deleted, ch.Deleted...)
		if ch.More && ch.Token == token {
			return nil, nil, "", "", false, errors.New("REPORT: the server says there is more but gives the same sync token")
		}
		token = ch.Token
		if !ch.More {
			return changed, deleted, token, "", whole, nil
		}
		s.progress(len(changed), 0)
	}
}

func (s *Sync) pull(ctx context.Context) error {
	changed, deleted, token, ctag, whole, err := s.remoteChanges(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var fetch []string
	for _, c := range changed {
		k := s.key(c.Href)
		if seen[k] {
			continue
		}
		seen[k] = true
		it := s.byHref[k]
		if it != nil && c.ETag != "" && it.ETag == c.ETag && !it.New {
			continue
		}
		fetch = append(fetch, c.Href)
	}
	if whole {
		for _, it := range append([]*Item(nil), s.State.Items...) {
			if !it.New && !seen[s.key(it.Href)] {
				deleted = append(deleted, it.Href)
			}
		}
	}
	for i := 0; i < len(fetch); i += s.batch() {
		hrefs := fetch[i:min(len(fetch), i+s.batch())]
		objs, missing, large, err := s.Client.Multiget(ctx, s.cal, hrefs)
		if err != nil {
			return err
		}
		for _, o := range objs {
			if err := s.pulled(ctx, o); err != nil {
				return err
			}
		}
		for _, h := range large {
			if it := s.byHref[s.key(h)]; it != nil {
				it.Skip = SkipLarge
				delete(s.byUID, it.UID)
			} else {
				s.add(&Item{Href: h, Skip: SkipLarge})
			}
			s.res.Failed++
			if s.res.Problem == nil {
				s.res.Problem = carddav.ErrTooLarge
			}
		}
		deleted = append(deleted, missing...)
		if err := s.flush(); err != nil {
			return err
		}
		s.progress(i+len(hrefs), len(fetch))
		if err := s.save(); err != nil {
			return err
		}
	}
	for _, href := range deleted {
		if err := s.goneRemote(href); err != nil {
			return err
		}
	}
	if err := s.flush(); err != nil {
		return err
	}
	if s.Calendar.Sync {
		s.State.Token = token
	} else if ctag != "" || whole {
		s.State.CTag = ctag
	}
	return s.save()
}

// pulled takes an object read from the server.
func (s *Sync) pulled(ctx context.Context, o Object) error {
	k := s.key(o.Href)
	it := s.byHref[k]
	uid := UID(o.Data)
	if uid == "" || !HasEvents(o.Data) {
		if it == nil {
			s.add(&Item{Href: o.Href, ETag: o.ETag, Skip: SkipOther})
		} else {
			it.ETag, it.Skip = o.ETag, SkipOther
			delete(s.byUID, it.UID)
		}
		return nil
	}
	if it == nil {
		it = &Item{Href: o.Href, UID: uid, Remote: true}
		s.add(it)
	} else if it.UID != uid {
		delete(s.byUID, it.UID)
		it.UID = uid
		s.byUID[uid] = it
	}
	it.Skip = ""
	p, has := s.pending[uid]
	switch {
	case !has:
		it.ETag, it.New = o.ETag, false
		s.deliver(Change{UID: uid, ICS: string(o.Data)})
	case p.Gone:
		// Deleted on the phone, changed on the server: the server's comes back.
		it.ETag, it.New = o.ETag, false
		s.deliver(Change{UID: uid, ICS: string(o.Data)})
		s.res.Conflicts++
		return s.done(p)
	case it.New && sameAsSent(o.Data, p):
		// The phone's own new object (a PUT whose answer was lost).
		it.ETag, it.New = o.ETag, false
		return s.done(p)
	case p.All && (it.New || it.ETag == ""):
		it.ETag, it.New = o.ETag, false
		s.deliver(Change{UID: uid, ICS: string(o.Data)})
		return s.done(p)
	default:
		return s.conflict(ctx, it, p, o)
	}
	return nil
}

// sameAsSent: the server's object is what pushNew writes of the change.
func sameAsSent(data []byte, p Change) bool {
	sent, err := Clean([]byte(p.ICS), p.UID)
	return err == nil && bytes.Equal(data, sent)
}

// conflict: both sides changed an event. The server's goes to the app; the
// phone's edit becomes a new event (a new UID), written to the server and
// handed to the app as a copy.
func (s *Sync) conflict(ctx context.Context, it *Item, p Change, cur Object) error {
	s.res.Conflicts++
	it.ETag, it.New = cur.ETag, false
	s.deliver(Change{UID: it.UID, ICS: string(cur.Data)})
	if err := s.done(p); err != nil {
		return err
	}
	uid := s.NewUID()
	cp := Change{UID: uid, ICS: string(WithUID([]byte(p.ICS), uid))}
	s.deliver(Change{UID: uid, ICS: cp.ICS, Copy: true})
	if err := s.Local.Queue(cp); err != nil {
		return err
	}
	if err := s.loadPending(); err != nil {
		return err
	}
	if q, ok := s.pending[uid]; ok {
		cp = q
	}
	n := &Item{Href: s.cal.EscapedPath() + s.NewUID() + ".ics", UID: uid, New: true}
	s.add(n)
	if err := s.pushNew(ctx, n, cp); err != nil {
		return s.failed(err)
	}
	return nil
}

// goneRemote: the server deleted the object.
func (s *Sync) goneRemote(href string) error {
	it := s.byHref[s.key(href)]
	if it == nil || it.New {
		return nil
	}
	if it.Skip != "" || it.UID == "" {
		s.drop(it)
		return nil
	}
	p, has := s.pending[it.UID]
	switch {
	case has && !p.Gone:
		// Changed on the phone, deleted on the server: the edit wins, as a
		// new object where the old one was.
		it.New, it.ETag = true, ""
		s.res.Conflicts++
	case has:
		s.drop(it)
		return s.done(p)
	default:
		s.drop(it)
		s.deliver(Change{UID: it.UID, Gone: true})
	}
	return nil
}

func (s *Sync) push(ctx context.Context) error {
	list, err := s.Local.Pending()
	if err != nil {
		return err
	}
	for i, q := range list {
		p, still := s.pending[q.UID]
		if !still {
			continue
		}
		it := s.byUID[p.UID]
		var err error
		switch {
		case p.Gone && (it == nil || it.New):
			if it != nil {
				s.drop(it)
			}
			err = s.done(p)
		case p.Gone:
			err = s.pushDelete(ctx, it, p)
		case p.All && it != nil && !it.New:
			// The first hand-over of an event the server has: the server's.
			err = s.done(p)
		case it == nil:
			it = &Item{Href: s.cal.EscapedPath() + s.NewUID() + ".ics", UID: p.UID, New: true}
			s.add(it)
			err = s.pushNew(ctx, it, p)
		case it.New:
			err = s.pushNew(ctx, it, p)
		default:
			err = s.pushChange(ctx, it, p)
		}
		if err != nil {
			if err := s.failed(err); err != nil {
				return err
			}
		}
		if (i+1)%s.batch() == 0 {
			if err := s.flush(); err != nil {
				return err
			}
			s.progress(i+1, len(list))
			if err := s.save(); err != nil {
				return err
			}
		}
	}
	return s.flush()
}

// pushDelete deletes on the server an event deleted on the phone, if the
// server's is still the one the phone knew; else the server's comes back.
func (s *Sync) pushDelete(ctx context.Context, it *Item, p Change) error {
	for try := 0; try < 2; try++ {
		if it.ETag == "" || try > 0 {
			cur, err := s.Client.Get(ctx, s.cal, it.Href)
			if errors.Is(err, carddav.ErrNotFound) {
				s.drop(it)
				return s.done(p)
			}
			if err != nil {
				return err
			}
			if it.ETag != "" && cur.ETag != it.ETag {
				it.ETag = cur.ETag
				s.deliver(Change{UID: it.UID, ICS: string(cur.Data)})
				s.res.Conflicts++
				return s.done(p)
			}
			it.ETag = cur.ETag
		}
		err := s.Client.Delete(ctx, s.cal, it.Href, it.ETag)
		if errors.Is(err, carddav.ErrPrecondition) {
			continue
		}
		if err != nil {
			return err
		}
		s.drop(it)
		s.res.Removed++
		return s.done(p)
	}
	return carddav.ErrPrecondition
}

func (s *Sync) tooLarge(data []byte) bool {
	return s.Calendar.MaxSize > 0 && int64(len(data)) > s.Calendar.MaxSize
}

// pushNew writes an event new to the server.
func (s *Sync) pushNew(ctx context.Context, it *Item, p Change) error {
	data, err := Clean([]byte(p.ICS), p.UID)
	if err != nil {
		return carddav.ErrRefused
	}
	if s.tooLarge(data) {
		return carddav.ErrTooLarge
	}
	etag, err := s.Client.Put(ctx, s.cal, it.Href, data, "")
	if errors.Is(err, carddav.ErrPrecondition) {
		// Something is there: this event, if an earlier run's PUT went
		// through and its answer was lost; then it is changed as any.
		cur, gerr := s.Client.Get(ctx, s.cal, it.Href)
		if gerr != nil {
			return gerr
		}
		if UID(cur.Data) != p.UID {
			s.drop(it)
			it.Href = s.cal.EscapedPath() + s.NewUID() + ".ics"
			s.add(it)
			return s.pushNew(ctx, it, p)
		}
		it.New, it.ETag = false, cur.ETag
		return s.pushChange(ctx, it, p)
	}
	if err != nil {
		return err
	}
	it.New = false
	it.ETag = s.etagAfter(ctx, it.Href, etag)
	s.res.Uploaded++
	return s.done(p)
}

// pushChange writes an event changed on the phone into the server's object
// read again; one the server changed since the phone knew it is a conflict.
func (s *Sync) pushChange(ctx context.Context, it *Item, p Change) error {
	for try := 0; ; try++ {
		cur, err := s.Client.Get(ctx, s.cal, it.Href)
		if errors.Is(err, carddav.ErrNotFound) {
			// Deleted on the server meanwhile: the edit wins.
			it.New, it.ETag = true, ""
			return s.pushNew(ctx, it, p)
		}
		if err != nil {
			return err
		}
		if it.ETag != "" && cur.ETag != it.ETag {
			return s.conflict(ctx, it, p, cur)
		}
		phone, err := Clean([]byte(p.ICS), p.UID)
		if err != nil {
			return carddav.ErrRefused
		}
		data, err := Patch(cur.Data, phone, s.now())
		if err != nil {
			return carddav.ErrRefused
		}
		if s.tooLarge(data) {
			return carddav.ErrTooLarge
		}
		etag, err := s.Client.Put(ctx, s.cal, it.Href, data, cur.ETag)
		if errors.Is(err, carddav.ErrPrecondition) && try == 0 {
			it.ETag = cur.ETag
			continue
		}
		if err != nil {
			return err
		}
		it.ETag = s.etagAfter(ctx, it.Href, etag)
		s.res.Uploaded++
		return s.done(p)
	}
}

// etagAfter is a written object's ETag, read again when the server changed
// the object as it stored it ("" when that fails: the next sync reads it).
func (s *Sync) etagAfter(ctx context.Context, href, etag string) string {
	if etag != "" {
		return etag
	}
	if got, err := s.Client.Get(ctx, s.cal, href); err == nil {
		return got.ETag
	}
	return ""
}
