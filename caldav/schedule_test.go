package caldav

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/caldav/davtest"
)

var testNow = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

func invitation(uid, summary, organizer string, attendees ...string) string {
	return ev(uid, summary, "ORGANIZER:mailto:"+organizer+"\r\n"+strings.Join(attendees, "\r\n")+"\r\n")
}

// A server that schedules (RFC 6638), from discovery on: what the user
// organizes goes up for the server to send, its SCHEDULE-STATUS and the
// attendees' answers come back to the app; an organizer's edit while an
// answer came in is written into the server's (no copy, the answer kept);
// an invitation from someone else comes in, the user's answer goes up
// into the organizer's event (only the answer and the alarm), a deletion
// declines; the inbox's messages are deleted once read, but for an
// invitation the calendar lacks. A server that does not schedule is told
// apart.
func TestScheduling(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if cal, err := r.sync.Client.Scheduling(ctx, r.sync.Calendar); err != nil || cal.Schedule || cal.Inbox != "" {
		t.Fatalf("a server that does not schedule: %+v %v", cal, err)
	}
	r.srv.Schedule = true
	cal, err := r.sync.Client.Scheduling(ctx, r.sync.Calendar)
	if err != nil || !cal.Schedule || cal.Inbox != r.ts.URL+davtest.Inbox || !slices.Equal(cal.Addresses, []string{"mailto:alice@kario.test"}) {
		t.Fatalf("scheduling: %+v %v", cal, err)
	}
	r.sync.Calendar = cal

	// Alice organizes: the server sends to Bob, not to Carol (her phone
	// sent Carol a file).
	meet := invitation("meet", "meeting", "alice@kario.test", "ATTENDEE;PARTSTAT=ACCEPTED:mailto:alice@kario.test",
		"ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@x.test", "ATTENDEE;SCHEDULE-AGENT=CLIENT;PARTSTAT=NEEDS-ACTION:mailto:carol@x.test")
	r.local.q.Push(Change{UID: "meet", ICS: meet})
	r.run()
	if sent := r.srv.SentLog(); !slices.Equal(sent, []string{"REQUEST meet bob@x.test"}) {
		t.Fatalf("sent: %v", sent)
	}
	got := r.local.take()
	if !strings.Contains(got["meet"].ICS, "SCHEDULE-STATUS=1.2:mailto:bob@x.test") {
		t.Fatalf("the app gets what the server stored: %+v", got)
	}

	// Bob accepts while Alice renames it: no copy, his answer kept.
	if !r.srv.Reply("meet", "bob@x.test", "ACCEPTED") {
		t.Fatal("no invitation to answer")
	}
	r.local.q.Push(Change{UID: "meet", ICS: strings.Replace(meet, "SUMMARY:meeting", "SUMMARY:planning", 1)})
	res := r.run()
	name, _ := r.srv.Find("UID:meet")
	stored := strings.ReplaceAll(string(r.srv.Object(name)), "\r\n ", "")
	if res.Conflicts != 0 || len(r.srv.Objects()) != 1 || !strings.Contains(stored, "SUMMARY:planning") || !strings.Contains(stored, "PARTSTAT=ACCEPTED:mailto:bob@x.test") || !strings.Contains(stored, "SCHEDULE-STATUS=1.2") {
		t.Fatalf("the edit with the answer (%+v):\n%s", res, stored)
	}
	got = r.local.take()
	if !strings.Contains(got["meet"].ICS, "PARTSTAT=ACCEPTED") || !strings.Contains(got["meet"].ICS, "planning") || got["meet"].Copy {
		t.Fatalf("the app sees the answer: %+v", got)
	}
	if r.srv.InboxLen() != 0 || res.Inbox != 1 {
		t.Fatalf("the reply read and deleted: %d left, %d read", r.srv.InboxLen(), res.Inbox)
	}

	// Bob invites Alice.
	inv := invitation("bobs", "bob's party", "bob@x.test", "ATTENDEE;PARTSTAT=ACCEPTED:mailto:bob@x.test",
		"ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:Alice@kario.test")
	r.srv.Invite("bobs.ics", []byte(inv))
	r.srv.Invite("gone.ics", []byte(invitation("gone", "gone", "bob@x.test", "ATTENDEE:mailto:alice@kario.test")))
	r.srv.Delete("gone.ics")
	res = r.run()
	got = r.local.take()
	if !strings.Contains(got["bobs"].ICS, "bob's party") || r.srv.InboxLen() != 1 || res.Inbox != 1 {
		t.Fatalf("the invitation comes in (%+v), its message read: %d left", got, r.srv.InboxLen())
	}
	// Alice accepts, renames it on the phone and adds an alarm: only the
	// answer and the alarm go up.
	answered := strings.Replace(strings.Replace(inv, "PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:Alice", "PARTSTAT=ACCEPTED:mailto:Alice", 1), "SUMMARY:bob's party", "SUMMARY:my name for it", 1)
	answered = strings.Replace(answered, "END:VEVENT", "BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT30M\r\nEND:VALARM\r\nEND:VEVENT", 1)
	r.local.q.Push(Change{UID: "bobs", ICS: answered})
	r.run()
	b := strings.ReplaceAll(string(r.srv.Object("bobs.ics")), "\r\n ", "")
	if !strings.Contains(b, "PARTSTAT=ACCEPTED;RSVP=TRUE:mailto:Alice") || !strings.Contains(b, "SUMMARY:bob's party") || !strings.Contains(b, "TRIGGER:-PT30M") ||
		!strings.Contains(b, "ORGANIZER;SCHEDULE-STATUS=1.2:mailto:bob@x.test") {
		t.Fatalf("the answer:\n%s", b)
	}
	if sent := r.srv.SentLog(); sent[len(sent)-1] != "REPLY bobs ACCEPTED" {
		t.Fatalf("sent: %v", sent)
	}
	if got := r.local.take(); !strings.Contains(got["bobs"].ICS, "bob's party") {
		t.Fatalf("the organizer's name comes back: %+v", got)
	}
	// Alice deletes it: declined.
	r.local.q.Push(Change{UID: "bobs", Gone: true})
	r.run()
	if sent := r.srv.SentLog(); sent[len(sent)-1] != "REPLY bobs DECLINED" || r.srv.Object("bobs.ics") != nil {
		t.Fatalf("declined: %v", sent)
	}
}

// Answer takes the user's answer and alarm, nothing else; PatchScheduled
// keeps the other attendees' answers the server has.
func TestAnswerAndPatchScheduled(t *testing.T) {
	server := []byte(invitation("x", "theirs", "bob@x.test", "ATTENDEE;PARTSTAT=TENTATIVE:mailto:carol@x.test", "ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:me@k.test"))
	phone := []byte(invitation("x", "mine", "bob@x.test", "ATTENDEE;PARTSTAT=DECLINED:mailto:carol@x.test", "ATTENDEE;PARTSTAT=ACCEPTED:mailto:ME@k.test"))
	got, err := Answer(server, phone, []string{"mailto:me@k.test"})
	if err != nil || !strings.Contains(string(got), "PARTSTAT=ACCEPTED:mailto:me@k.test") || !strings.Contains(string(got), "PARTSTAT=TENTATIVE:mailto:carol") ||
		!strings.Contains(string(got), "SUMMARY:theirs") {
		t.Fatalf("Answer: %v\n%s", err, got)
	}
	if !Attends(server, []string{"mailto:me@k.test"}) || Attends(server, []string{"bob@x.test"}) || !Invitation(server) || Invitation([]byte(ev("y", "y"))) {
		t.Fatal("Attends, Invitation")
	}
	org := []byte(invitation("o", "o", "me@k.test", "ATTENDEE;PARTSTAT=ACCEPTED:mailto:carol@x.test", "ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:me@k.test"))
	stale := []byte(invitation("o", "renamed", "me@k.test", "ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:carol@x.test", "ATTENDEE;PARTSTAT=ACCEPTED:mailto:me@k.test"))
	got, err = PatchScheduled(org, stale, testNow, []string{"mailto:me@k.test"})
	if err != nil || !strings.Contains(string(got), "PARTSTAT=ACCEPTED:mailto:carol") || !strings.Contains(string(got), "PARTSTAT=ACCEPTED:mailto:me@k.test") || !strings.Contains(string(got), "renamed") {
		t.Fatalf("PatchScheduled: %v\n%s", err, got)
	}
	if got, _ := Patch(org, stale, testNow); !strings.Contains(string(got), "PARTSTAT=NEEDS-ACTION:mailto:carol") {
		t.Fatalf("Patch takes the phone's answers:\n%s", got)
	}
	if Method([]byte("BEGIN:VCALENDAR\r\nMETHOD:reply\r\nBEGIN:VEVENT\r\nUID:a\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")) != "REPLY" {
		t.Fatal("Method")
	}
}
