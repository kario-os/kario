package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// A multimedia message in the list and the thread: its state while it is
// not downloaded, its subject and text on one line, "Picture" for one with
// only a picture.
func TestMMSBody(t *testing.T) {
	pic := []ui.SMSPart{{Type: "image/png", Name: "pic.png"}}
	for _, c := range []struct {
		m    ui.SMS
		want string
	}{
		{ui.SMS{Text: "plain\ntext"}, "plain\ntext"},
		{ui.SMS{Kind: ui.SMSKindMMS, MMS: ui.MMSPending, Subject: "Trip"}, tr.T("Multimedia message, downloading…")},
		{ui.SMS{Kind: ui.SMSKindMMS, MMS: ui.MMSFailed}, tr.T("Multimedia message, not downloaded")},
		{ui.SMS{Kind: ui.SMSKindMMS, MMS: ui.MMSExpired}, tr.T("Multimedia message, expired at the operator")},
		{ui.SMS{Kind: ui.SMSKindMMS, Subject: "Trip", Text: "Look\n here", Parts: pic}, "Trip Look here"},
		{ui.SMS{Kind: ui.SMSKindMMS, Parts: pic}, tr.T("Picture")},
		{ui.SMS{Kind: ui.SMSKindMMS, Parts: []ui.SMSPart{{Type: "text/x-vcard"}}}, tr.T("Multimedia message")},
	} {
		if got := body(c.m); got != c.want {
			t.Errorf("%+v: %q, want %q", c.m, got, c.want)
		}
	}
	if got := preview(ui.SMS{Kind: ui.SMSKindMMS, Dir: "out", State: ui.SMSFailed, Parts: pic}); got != tr.T("Not sent: %s", tr.T("Picture")) {
		t.Errorf("failed picture: %q", got)
	}
}

// In a thread a picture the album took is an image node and a part it did
// not take a line with its name, type and size; in a group a received
// message says who sent it; a download that failed offers Download; a kept
// flash message is marked.
func TestMMSBubble(t *testing.T) {
	t.Setenv("PHONE_REGION", "CN")
	m := &messages{}
	x := ui.SMS{ID: "w1", Dir: "in", Peer: "+8613800138000", Kind: ui.SMSKindMMS, Text: "Dinner?", At: 1,
		Members: []string{"+8613800138000", "+8613700137000"},
		Parts:   []ui.SMSPart{{Type: "image/png", Name: "pic.png", Media: "media:p7"}, {Type: "text/x-vcard", Name: "card.vcf", Size: 1500, Local: true}}}
	nodes := m.bubble(x)
	if len(nodes) != 3 || nodes[0].Title != "Dinner?" || !strings.HasPrefix(nodes[0].Subtitle, "138 0013 8000 · ") {
		t.Fatalf("bubble %+v", nodes)
	}
	if nodes[1].Kind != ui.KindImage || nodes[1].Src != "media:p7" {
		t.Errorf("picture %+v", nodes[1])
	}
	actions := func(n ui.Node) (ids []string) {
		for _, a := range n.Actions {
			ids = append(ids, a.ID)
		}
		return ids
	}
	if nodes[2].Title != tr.T("Contact card: %s", "card.vcf") || nodes[2].ID != "card:w1:1" || !slices.Equal(actions(nodes[2]), []string{"add-card"}) {
		t.Errorf("contact card %+v", nodes[2])
	}
	other := m.bubble(ui.SMS{ID: "w4", Dir: "in", Peer: "10086", Kind: ui.SMSKindMMS, Parts: []ui.SMSPart{{Type: "audio/amr", Name: "a.amr", Size: 1500, Local: true}}})
	if other[1].Text != tr.T("Attachment: %s (%s, %d KB)", "a.amr", "audio/amr", 2) {
		t.Errorf("attachment %q", other[1].Text)
	}
	failed := m.bubble(ui.SMS{ID: "w2", Dir: "in", Peer: "10086", Kind: ui.SMSKindMMS, MMS: ui.MMSFailed})
	if !slices.Contains(actions(failed[0]), "download") {
		t.Errorf("a failed download: %v", actions(failed[0]))
	}
	if pending := m.bubble(ui.SMS{ID: "w3", Dir: "in", Peer: "10086", Kind: ui.SMSKindMMS, MMS: ui.MMSPending}); slices.Contains(actions(pending[0]), "download") {
		t.Error("Download offered while smsd is downloading it")
	}
	if flash := m.bubble(ui.SMS{ID: "f1", Dir: "in", Peer: "10086", Text: "Balance", Flash: true}); flash[0].Badge != tr.T("Flash") {
		t.Errorf("flash %+v", flash[0])
	}
}

// A thread opened by its key (a notification's: the E.164 form) is titled
// with the number as its messages carry it; a group's has no Call or
// Block, and its compose line says the message goes as MMS.
func TestThreadScreen(t *testing.T) {
	t.Setenv("PHONE_REGION", "CN")
	m := &messages{thread: "+8613800000004", msgs: []ui.SMS{{ID: "a", Dir: "out", Peer: "+8613800000004", Text: "hi"}, {ID: "b", Dir: "in", Peer: "13800000004", Text: "yo"}}}
	s := m.threadScreen()
	if s.Title != "13800000004" {
		t.Errorf("title %q", s.Title)
	}
	has := func(s ui.Screen, id string) bool {
		return slices.ContainsFunc(s.Actions, func(a ui.Action) bool { return a.ID == id })
	}
	if !has(s, ui.ActionCall) || !has(s, "block") || !has(s, "attach") || s.Body[len(s.Body)-1].ID != "counter" {
		t.Errorf("one number: %+v", s)
	}
	m = &messages{thread: "13800000004, +8613700000005", msgs: []ui.SMS{{ID: "g", Dir: "in", Peer: "13800000004", Members: []string{"13800000004", "+8613700000005"}}}}
	s = m.threadScreen()
	if s.Title != "13800000004, 137 0000 0005" || has(s, ui.ActionCall) || has(s, "block") || !has(s, "attach") {
		t.Errorf("group: %q %+v", s.Title, s.Actions)
	}
	if last := s.Body[len(s.Body)-1]; last.Text != tr.T("Multimedia message") {
		t.Errorf("group compose line %+v", last)
	}
	m.attach = []ui.MediaItem{{ID: "p1"}}
	if s = m.threadScreen(); !slices.ContainsFunc(s.Body, func(n ui.Node) bool { return n.ID == "attached" }) {
		t.Error("the attached picture is not shown")
	}
}

// A conversation with a draft shows it in the list and starts its input
// with it; scheduled messages come last in their order, with when they go
// (on the clock they were picked on too, once the phone is in another
// zone) and Send now.
func TestDraftsAndScheduled(t *testing.T) {
	t.Setenv("PHONE_REGION", "CN")
	old := time.Local
	defer func() { time.Local = old }()
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip(err)
	}
	time.Local = sh
	if k := threadKey("13900000003; +86 139 0000 0002"); k != "+8613900000002,+8613900000003" {
		t.Errorf("group key %q", k)
	}
	m := &messages{threads: []ui.SMSThread{{Thread: "+8613800000004", Peer: "+8613800000004", Draft: "see you\nat 8", Last: ui.SMS{Text: "hi"}}}}
	if got := threadPreview(m.threads[0]); got != tr.T("Draft: %s", "see you at 8") {
		t.Errorf("preview %q", got)
	}
	if d := m.draftOf("138 0000 0004"); d != "see you\nat 8" {
		t.Errorf("draft by another way of writing the number: %q", d)
	}
	due := time.Date(2026, 11, 20, 9, 0, 0, 0, sh).Unix()
	m.thread, m.draftInit = "+8613800000004", "see you"
	m.msgs = []ui.SMS{
		{ID: "s2", Dir: "out", Peer: "13800000004", Text: "later", State: ui.SMSScheduled, Due: due + 3600, Zone: "Asia/Shanghai"},
		{ID: "s1", Dir: "out", Peer: "13800000004", Text: "sooner", State: ui.SMSScheduled, Due: due, Zone: "America/New_York"},
		{ID: "a", Dir: "in", Peer: "13800000004", Text: "hi", At: 1},
	}
	s := m.threadScreen()
	var ids []string
	var input ui.Node
	for _, n := range s.Body {
		if strings.HasPrefix(n.ID, "m:") {
			ids = append(ids, n.ID)
		}
		if n.ID == "text" {
			input = n
		}
	}
	if !slices.Equal(ids, []string{"m:a", "m:s1", "m:s2"}) || input.Value != "see you" {
		t.Fatalf("thread %v, input %+v", ids, input)
	}
	b := m.bubble(m.msgs[1])[0]
	if b.Subtitle != tr.T("Scheduled · %s", "2026-11-20 09:00 (20:00 America/New_York)") || b.Actions[0].ID != "send-now" {
		t.Errorf("scheduled in another zone: %+v", b)
	}
	if b := m.bubble(m.msgs[0])[0]; b.Subtitle != tr.T("Scheduled · %s", "2026-11-20 10:00") {
		t.Errorf("scheduled in this zone: %q", b.Subtitle)
	}
	if got := preview(m.msgs[0]); got != tr.T("Scheduled: %s", "later") {
		t.Errorf("preview %q", got)
	}
}
