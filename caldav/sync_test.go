package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/caldav/davtest"
	"github.com/kario-os/kario/carddav"
)

// memLocal is the Shell's queue without the Shell.
type memLocal struct{ q Queue }

func (m *memLocal) Pending() ([]Change, error) { return m.q.Pending(), nil }
func (m *memLocal) Done(ch Change) error       { m.q.Done(ch); return nil }
func (m *memLocal) Queue(ch Change) error {
	m.q.Push(ch)
	return nil
}
func (m *memLocal) Deliver(chs []Change) error { m.q.Deliver(chs); return nil }

// take is what the app takes of the inbox, by UID (the last change of each).
func (m *memLocal) take() map[string]Change {
	out := map[string]Change{}
	var seqs []int
	for _, c := range m.q.Inbox {
		out[c.UID] = c
		seqs = append(seqs, c.Seq)
	}
	m.q.Take(seqs)
	return out
}

func ev(uid, summary string, extra ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20261101T000000Z\r\n" +
		"DTSTART;TZID=Asia/Shanghai:20261201T090000\r\nDTEND;TZID=Asia/Shanghai:20261201T100000\r\nSUMMARY:" + summary + "\r\n" +
		strings.Join(extra, "") + "END:VEVENT\r\nEND:VCALENDAR\r\n"
}

type rig struct {
	t     *testing.T
	srv   *davtest.Server
	ts    *httptest.Server
	local *memLocal
	sync  *Sync
	n     int
}

func newRig(t *testing.T) *rig {
	srv := davtest.New("alice@kario.test", "app-pass")
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	r := &rig{t: t, srv: srv, ts: ts, local: &memLocal{}}
	client := &Client{Client: &carddav.Client{User: "alice@kario.test", Password: "app-pass"}}
	cal, err := client.Discover(context.Background(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	r.sync = &Sync{Client: client, Calendar: cal, Local: r.local, State: &State{},
		NewUID: func() string { r.n++; return "new-" + string(rune('a'+r.n-1)) }}
	return r
}

func (r *rig) run() Result {
	r.t.Helper()
	res, err := r.sync.Run(context.Background())
	if err != nil {
		r.t.Fatalf("run: %v", err)
	}
	return res
}

func TestDiscover(t *testing.T) {
	r := newRig(t)
	if !strings.HasSuffix(r.sync.Calendar.URL, davtest.Calendar) || !r.sync.Calendar.Sync || r.sync.Calendar.Name != "Calendar" {
		t.Fatalf("not the events calendar: %+v", r.sync.Calendar)
	}
	if r.srv.Count("well-known") != 1 {
		t.Fatalf("not found from .well-known/caldav: %v", r.srv.Requests)
	}
	bad := &Client{Client: &carddav.Client{User: "alice@kario.test", Password: "wrong"}}
	if _, err := bad.Discover(context.Background(), r.ts.URL); !errors.Is(err, carddav.ErrAuth) {
		t.Fatalf("a wrong password: %v", err)
	}
	direct := &Client{Client: &carddav.Client{User: "alice@kario.test", Password: "app-pass"}}
	if cal, err := direct.Discover(context.Background(), r.ts.URL+davtest.Calendar); err != nil || cal.URL != r.sync.Calendar.URL {
		t.Fatalf("the calendar's own address: %+v %v", cal, err)
	}
}

func TestFirstSyncBothWays(t *testing.T) {
	r := newRig(t)
	repeating := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:weekly\r\nDTSTART:20261201T090000Z\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:weekly\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:weekly\r\nRECURRENCE-ID:20261208T090000Z\r\nDTSTART:20261208T110000Z\r\nSUMMARY:weekly moved\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	r.srv.Put("weekly.ics", []byte(repeating))
	r.srv.Put("both.ics", []byte(ev("both", "server's")))
	r.srv.Put("task.ics", []byte("BEGIN:VCALENDAR\r\nBEGIN:VTODO\r\nUID:t1\r\nSUMMARY:task\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"))
	r.local.q.Push(Change{UID: "both", ICS: ev("both", "phone's"), All: true})
	r.local.q.Push(Change{UID: "mine", ICS: ev("mine", "made on the phone"), All: true})
	res := r.run()
	got := r.local.take()
	if len(got) != 2 || !strings.Contains(got["weekly"].ICS, "weekly moved") || !strings.Contains(got["both"].ICS, "server's") {
		t.Fatalf("the app gets the server's events, the repeating one whole: %+v", got)
	}
	if _, ok := r.srv.Find("made on the phone"); !ok || len(r.srv.Objects()) != 4 {
		t.Fatalf("the phone's new event is on the server: %v", r.srv.Objects())
	}
	if res.Conflicts != 0 || len(r.local.q.Outbox) != 0 || r.sync.State.Events() != 3 {
		t.Fatalf("the same UID on both sides at the first sync is the server's, no copy: %+v, outbox %v, %d events", res, r.local.q.Outbox, r.sync.State.Events())
	}
	puts := r.srv.Count("PUT")
	r.run()
	if len(r.local.take()) != 0 || r.srv.Count("PUT") != puts {
		t.Fatal("a second run with nothing changed changes nothing")
	}
}

func TestEditsDeletesAndTheServersWords(t *testing.T) {
	r := newRig(t)
	r.srv.Put("lunch.ics", []byte(ev("lunch", "lunch", "ATTENDEE;CN=Bob;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@x\r\nX-OTHER:kept\r\n",
		"BEGIN:VALARM\r\nACTION:EMAIL\r\nTRIGGER:-P1D\r\nEND:VALARM\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT5M\r\nEND:VALARM\r\n")))
	r.run()
	r.local.take()
	// The phone moves it an hour, renames it and answers for Bob.
	edited := strings.Replace(strings.Replace(ev("lunch", "long lunch", "ATTENDEE;PARTSTAT=ACCEPTED:mailto:BOB@x\r\n", "BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\n"),
		"20261201T100000", "20261201T110000", 1), "20261201T090000", "20261201T100000", 1)
	r.local.q.Push(Change{UID: "lunch", ICS: edited})
	if res := r.run(); res.Uploaded != 1 || res.Conflicts != 0 {
		t.Fatalf("written: %+v", res)
	}
	data := string(r.srv.Object("lunch.ics"))
	for _, want := range []string{"SUMMARY:long lunch", "DTSTART;TZID=Asia/Shanghai:20261201T100000", "X-OTHER:kept", "CN=Bob", "RSVP=TRUE", "PARTSTAT=ACCEPTED", "ACTION:EMAIL", "TRIGGER:-PT15M", "SEQUENCE:1"} {
		if !strings.Contains(data, want) {
			t.Errorf("the server's object lacks %q:\n%s", want, data)
		}
	}
	if strings.Contains(data, "TRIGGER:-PT5M") || strings.Count(data, "ATTENDEE") != 1 {
		t.Errorf("the old display alarm or a second attendee:\n%s", data)
	}
	r.run()
	if len(r.local.take()) != 0 {
		t.Fatal("the phone's own write is not handed back")
	}
	// Another device changes it; the next run hands it to the app.
	r.srv.Put("lunch.ics", []byte(ev("lunch", "lunch on the web")))
	r.run()
	if got := r.local.take(); !strings.Contains(got["lunch"].ICS, "lunch on the web") {
		t.Fatalf("the server's change: %+v", got)
	}
	// Deleted on the phone: deleted on the server.
	r.local.q.Push(Change{UID: "lunch", Gone: true})
	if res := r.run(); res.Removed != 1 || r.srv.Object("lunch.ics") != nil {
		t.Fatalf("deleted: %+v", res)
	}
	// Deleted on the server: the app is told.
	r.srv.Put("tea.ics", []byte(ev("tea", "tea")))
	r.run()
	r.local.take()
	r.srv.Delete("tea.ics")
	r.run()
	if got := r.local.take(); !got["tea"].Gone {
		t.Fatalf("deleted on the server: %+v", got)
	}
}

func TestConflictsKeepBoth(t *testing.T) {
	r := newRig(t)
	r.srv.Put("a.ics", []byte(ev("a", "a")))
	r.srv.Put("b.ics", []byte(ev("b", "b")))
	r.srv.Put("c.ics", []byte(ev("c", "c")))
	r.run()
	r.local.take()
	// a: both edited before the run (seen in the pull).
	r.srv.Put("a.ics", []byte(ev("a", "a on the web")))
	r.local.q.Push(Change{UID: "a", ICS: ev("a", "a on the phone")})
	// b: deleted on the phone, edited on the server: the server's comes back.
	r.srv.Put("b.ics", []byte(ev("b", "b on the web")))
	r.local.q.Push(Change{UID: "b", Gone: true})
	// c: edited on the phone, deleted on the server: the phone's goes up again.
	r.srv.Delete("c.ics")
	r.local.q.Push(Change{UID: "c", ICS: ev("c", "c on the phone")})
	res := r.run()
	got := r.local.take()
	if !strings.Contains(got["a"].ICS, "a on the web") || !got["new-a"].Copy || !strings.Contains(got["new-a"].ICS, "UID:new-a") || !strings.Contains(got["new-a"].ICS, "a on the phone") {
		t.Fatalf("a: the server's on the phone, the phone's edit a copy: %+v", got)
	}
	if !strings.Contains(got["b"].ICS, "b on the web") {
		t.Fatalf("b comes back: %+v", got)
	}
	if _, ok := r.srv.Find("a on the phone"); !ok {
		t.Fatal("the copy is on the server")
	}
	if _, ok := r.srv.Find("c on the phone"); !ok {
		t.Fatal("c is on the server again")
	}
	if res.Conflicts != 3 || len(r.local.q.Outbox) != 0 {
		t.Fatalf("%+v, outbox %v", res, r.local.q.Outbox)
	}
	// A write that meets another device's edit (412).
	r.srv.BeforeWrite = func(name string) {
		if name == "a.ics" {
			r.srv.BeforeWrite = nil
			r.srv.Put("a.ics", []byte(ev("a", "a again on the web")))
		}
	}
	r.local.q.Push(Change{UID: "a", ICS: ev("a", "a again on the phone")})
	r.run()
	got = r.local.take()
	if !strings.Contains(got["a"].ICS, "a again on the web") || !got["new-c"].Copy {
		t.Fatalf("a 412: %+v", got)
	}
}

func TestWithoutSyncCollectionAndForgottenTokens(t *testing.T) {
	r := newRig(t)
	r.sync.Limit = 2
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		r.srv.Put(n+".ics", []byte(ev("e"+n, "event "+n)))
	}
	r.run()
	if len(r.local.take()) != 5 || r.srv.Count("REPORT sync") != 3 {
		t.Fatalf("five in pages of two: %v", r.srv.Requests)
	}
	r.srv.Delete("3.ics")
	r.srv.ForgetTokens()
	r.run()
	if got := r.local.take(); len(got) != 1 || !got["e3"].Gone {
		t.Fatalf("a token forgotten: from the start, the deletion seen: %+v", got)
	}
	r.srv.NoSync = true
	r.sync.Calendar.Sync = false
	r.srv.Delete("4.ics")
	r.srv.Put("6.ics", []byte(ev("e6", "event 6")))
	r.run()
	if got := r.local.take(); len(got) != 2 || !got["e4"].Gone || got["e6"].ICS == "" {
		t.Fatalf("ETags compared: %+v", got)
	}
	gets := r.srv.Count("REPORT multiget")
	r.run()
	if r.srv.Count("REPORT multiget") != gets {
		t.Fatal("the CTag did not move: nothing read")
	}
}

func TestServerRewritesAndFails(t *testing.T) {
	r := newRig(t)
	r.srv.Rewrite = true
	r.local.q.Push(Change{UID: "x", ICS: ev("x", "x")})
	r.run()
	if !strings.Contains(string(r.srv.Object(strings.TrimPrefix(r.sync.State.Items[0].Href, davtest.Calendar))), "X-DAVTEST:stored") || r.sync.State.Items[0].ETag == "" {
		t.Fatalf("the ETag read after a rewrite: %+v", r.sync.State.Items[0])
	}
	r.run()
	if len(r.local.take()) != 0 {
		t.Fatal("the rewrite of its own write is not handed back")
	}
	r.srv.MaxSize = 10
	r.sync.Calendar.MaxSize = 10
	r.local.q.Push(Change{UID: "big", ICS: ev("big", strings.Repeat("b", 100))})
	r.local.q.Push(Change{UID: "small", ICS: "not iCalendar"})
	res := r.run()
	if res.Failed != 2 || !errors.Is(res.Problem, carddav.ErrTooLarge) || len(r.local.q.Outbox) != 2 {
		t.Fatalf("too large and unreadable: counted, kept for later: %+v %v", res, r.local.q.Outbox)
	}
	r.srv.Down, r.srv.RetryAfter = true, 120
	_, err := r.sync.Run(context.Background())
	if !carddav.Temporary(err) || carddav.RetryAfter(err) != 2*time.Minute {
		t.Fatalf("down: %v", err)
	}
}

func TestQueue(t *testing.T) {
	var q Queue
	q.Push(Change{UID: "a", ICS: "1"})
	q.Push(Change{UID: "b", ICS: "1"})
	q.Push(Change{UID: "a", ICS: "2"})
	if len(q.Outbox) != 2 || q.Outbox[1].ICS != "2" {
		t.Fatalf("a later change of a UID replaces the earlier: %+v", q.Outbox)
	}
	sent := q.Outbox[1]
	q.Push(Change{UID: "a", Gone: true})
	q.Done(sent)
	if len(q.Outbox) != 2 || !q.Outbox[1].Gone {
		t.Fatalf("a change sent while the app changed the UID again stays: %+v", q.Outbox)
	}
	q.Deliver([]Change{{UID: "x", ICS: "1"}, {UID: "y", ICS: "1"}, {UID: "x", ICS: "2"}, {UID: "z", ICS: "1", Copy: true}})
	offer, more := q.Offer(2, 1<<20)
	if len(offer) != 2 || !more || offer[0].UID != "y" || offer[1].ICS != "2" {
		t.Fatalf("offered in order, the later x only: %+v", offer)
	}
	q.Take([]int{offer[0].Seq, offer[1].Seq})
	if len(q.Inbox) != 1 || q.Inbox[0].UID != "z" {
		t.Fatalf("taken: %+v", q.Inbox)
	}
	if offer, _ := q.Offer(5, 0); len(offer) != 1 {
		t.Fatal("one at least, however large")
	}
}

func TestPatchMatchesOccurrences(t *testing.T) {
	server := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nX-WR-CALNAME:Work\r\nBEGIN:VTIMEZONE\r\nTZID:America/New_York\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:w\r\nDTSTART;TZID=America/New_York:20261201T090000\r\nRRULE:FREQ=WEEKLY\r\nCATEGORIES:work\r\nSUMMARY:w\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:w\r\nRECURRENCE-ID:20261208T140000Z\r\nDTSTART:20261208T150000Z\r\nX-KEEP:yes\r\nSUMMARY:moved\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:w\r\nRECURRENCE-ID:20261215T140000Z\r\nDTSTART:20261215T150000Z\r\nSUMMARY:gone\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	phone := "BEGIN:VCALENDAR\r\nMETHOD:PUBLISH\r\nBEGIN:VEVENT\r\nUID:w\r\nDTSTART;TZID=America/New_York:20261201T090000\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:w renamed\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:w\r\nRECURRENCE-ID;TZID=America/New_York:20261208T090000\r\nDTSTART;TZID=America/New_York:20261208T100000\r\nSUMMARY:moved\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	out, err := Patch([]byte(server), []byte(phone), time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"X-WR-CALNAME:Work", "TZID:America/New_York", "CATEGORIES:work", "SUMMARY:w renamed", "X-KEEP:yes", "RECURRENCE-ID;TZID=America/New_York:20261208T090000", "DTSTAMP:20261120T000000Z"} {
		if !strings.Contains(s, want) {
			t.Errorf("lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "SUMMARY:gone") || strings.Contains(s, "METHOD") || strings.Count(s, "SEQUENCE:1") != 1 {
		t.Errorf("the occurrence the phone dropped, METHOD, or SEQUENCE:\n%s", s)
	}
	if UID(out) != "w" || !HasEvents(out) || HasEvents([]byte("BEGIN:VCALENDAR\r\nBEGIN:VTODO\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")) {
		t.Error("UID, HasEvents")
	}
	if c := WithUID(out, "w2"); UID(c) != "w2" || strings.Contains(string(c), "UID:w\r\n") {
		t.Errorf("WithUID:\n%s", c)
	}
}

// An organizer's edit on the phone: the attendees it took out go, the one
// it added comes, the others keep the server's parameters with the phone's
// SCHEDULE-AGENT; RDATE is the phone's and moves SEQUENCE. An attendee's
// copy without an organizer, or with as many attendees as the app keeps,
// takes out none.
func TestPatchOrganizerAndRDate(t *testing.T) {
	server := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:m\r\nDTSTART:20301104T020000Z\r\nRDATE:20301106T020000Z\r\nSEQUENCE:2\r\nSUMMARY:m\r\n" +
		"ORGANIZER:mailto:me@x.test\r\nATTENDEE;PARTSTAT=ACCEPTED;SCHEDULE-STATUS=2.0:mailto:a@x.test\r\nATTENDEE;PARTSTAT=TENTATIVE:mailto:b@x.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	phone := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:m\r\nDTSTART:20301104T020000Z\r\nRDATE:20301107T020000Z\r\nSUMMARY:m\r\nORGANIZER:mailto:me@x.test\r\n" +
		"ATTENDEE;PARTSTAT=ACCEPTED;SCHEDULE-AGENT=CLIENT:mailto:A@x.test\r\nATTENDEE;PARTSTAT=NEEDS-ACTION;SCHEDULE-AGENT=CLIENT:mailto:c@x.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	out, err := Patch([]byte(server), []byte(phone), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(out), "\r\n ", "")
	for _, want := range []string{"ATTENDEE;PARTSTAT=ACCEPTED;SCHEDULE-STATUS=2.0;SCHEDULE-AGENT=CLIENT:mailto:a@x.test", "mailto:c@x.test", "RDATE:20301107T020000Z", "SEQUENCE:3"} {
		if !strings.Contains(s, want) {
			t.Errorf("lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "b@x.test") || strings.Contains(s, "20301106") {
		t.Errorf("the attendee taken out, or the old RDATE:\n%s", s)
	}
	attendee := strings.Replace(phone, "ORGANIZER:mailto:me@x.test\r\n", "", 1)
	if out, _ := Patch([]byte(server), []byte(attendee), time.Unix(0, 0)); !strings.Contains(string(out), "b@x.test") {
		t.Errorf("without an organizer the server's attendees stay:\n%s", out)
	}
	var many strings.Builder
	for i := 0; i < MaxAttendees; i++ {
		fmt.Fprintf(&many, "ATTENDEE:mailto:p%d@x.test\r\n", i)
	}
	full := strings.Replace(phone, "ATTENDEE;PARTSTAT=ACCEPTED;SCHEDULE-AGENT=CLIENT:mailto:A@x.test\r\n", many.String(), 1)
	if out, _ := Patch([]byte(server), []byte(full), time.Unix(0, 0)); !strings.Contains(string(out), "b@x.test") {
		t.Error("an event at the app's limit may lack attendees the app did not keep")
	}
}
