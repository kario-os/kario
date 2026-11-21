package smsd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

func reopen(t *testing.T, s *store) *store {
	t.Helper()
	dir := s.dir
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	n, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestThreadNormalizesNumbers(t *testing.T) {
	s := &store{}
	for in, want := range map[string]string{"+86 138-0013-8000": "+8613800138000", "008613800138000": "+8613800138000",
		"13800138000": "+8613800138000", "(010) 6552 9988": "+861065529988", "10086": "10086", "+1 650 555 0100": "+16505550100",
		"Kario Bank": "kario bank", "13900000002, +86 138 0013 8000; 13900000002": "+8613800138000,+8613900000002"} {
		if got := s.thread(in); got != want {
			t.Errorf("thread(%q) = %q, want %q", in, got, want)
		}
	}
	s.region = "ES"
	if got := s.thread("612 34 56 78"); got != "+34612345678" {
		t.Errorf("a Spanish number read in Spain: %q", got)
	}
}

func TestStoreSurvivesRestartsAndTornWrites(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	if m, err := s.receive(ui.SMS{ID: "m1", Peer: "+8613800000001", Text: "hi", At: 10}); err != nil || m == nil || m.Thread != "+8613800000001" {
		t.Fatalf("receive: %+v %v", m, err)
	}
	if m, _ := s.receive(ui.SMS{ID: "m1", Peer: "13800000001", Text: "hi", At: 10}); m != nil {
		t.Fatal("the same message stored twice")
	}
	out, err := s.addOutgoing("138 0000 0001", "hello", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := s.setState(out.ID, ui.SMSDelivered, false); m == nil {
		t.Fatal("delivered not recorded")
	}
	if m, _ := s.setState(out.ID, ui.SMSSent, false); m != nil || s.byID[out.ID].State != ui.SMSDelivered {
		t.Fatal("a late report took the state back")
	}
	for _, p := range []string{s.dir, s.logPath()} {
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s: %v %v", p, st.Mode(), err)
		}
	}
	// A crash in the middle of writing the next line.
	if _, err := s.log.WriteString(`{"seq":99,"op":"delete","ids":["m1"`); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s)
	threads := s.threads("")
	if len(threads) != 1 || threads[0].Count != 2 || threads[0].Unread != 1 || threads[0].Last.ID != out.ID {
		t.Fatalf("threads after a restart: %+v", threads)
	}
	if m, _ := s.receive(ui.SMS{ID: "m1", Peer: "13800000001", Text: "hi"}); m != nil {
		t.Fatal("a message received before the restart stored again")
	}
	if n, err := s.markRead("+86 13800000001"); n != 1 || err != nil {
		t.Fatalf("markRead: %d %v", n, err)
	}
	if got := s.list(ui.SMSListArgs{Query: "HELL"}); len(got) != 1 || got[0].ID != out.ID {
		t.Fatalf("search: %+v", got)
	}
	if n, _ := s.remove(nil, "13800000001"); n != 2 || len(s.threads("")) != 0 {
		t.Fatalf("delete the conversation: %d", n)
	}
	s = reopen(t, s)
	if len(s.msgs) != 0 {
		t.Fatalf("deleted messages back: %+v", s.msgs)
	}
}

func TestStoreCompacts(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < compactEvery+10; i++ {
		if _, err := s.receive(ui.SMS{ID: fmt.Sprint("m", i), Peer: "10086", Text: fmt.Sprint("n", i), At: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if s.logged != 10 {
		t.Fatalf("%d lines in the log after compaction", s.logged)
	}
	if st, err := os.Stat(s.snapPath()); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot: %v %v", st, err)
	}
	s = reopen(t, s)
	if len(s.msgs) != compactEvery+10 || s.threads("")[0].Last.Text != fmt.Sprint("n", compactEvery+9) {
		t.Fatalf("%d messages after compaction and a restart", len(s.msgs))
	}
}

func TestBlockedSendersGoToTheirFolder(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.block("+86 10690000", true); err != nil {
		t.Fatal(err)
	}
	m, err := s.receive(ui.SMS{ID: "m1", Peer: "10690000", Text: "Win a prize", At: 5})
	if err != nil || m.Folder != ui.SMSFolderBlocked || !m.Read {
		t.Fatalf("blocked sender's message: %+v %v", m, err)
	}
	if len(s.threads("")) != 0 || len(s.threads(ui.SMSFolderBlocked)) != 1 || !s.threads(ui.SMSFolderBlocked)[0].Blocked {
		t.Fatal("the blocked folder")
	}
	if len(s.list(ui.SMSListArgs{})) != 0 || len(s.list(ui.SMSListArgs{Folder: ui.SMSFolderBlocked})) != 1 {
		t.Fatal("sms.list shows blocked messages outside their folder")
	}
	s = reopen(t, s)
	if got := s.blockedList(); len(got) != 1 || got[0] != "10690000" {
		t.Fatalf("block list after a restart: %v", got)
	}
	_ = s.block("10690000", false)
	if m, _ := s.receive(ui.SMS{ID: "m2", Peer: "10690000", Text: "Again"}); m.Folder != "" {
		t.Fatal("unblocked sender still blocked")
	}
}

// Minor mode lets messages through only from the numbers it allows,
// whichever way they are written, across a restart (one arriving before
// the shell is up is held too); off, everyone's come in again.
func TestMinorModeAllowsOnlyItsNumbers(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.setOnly(true, []string{"13900001111", "110"}); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s)
	for i, c := range []struct {
		peer, folder string
	}{{"+86 139 0000 1111", ""}, {"110", ""}, {"13700000088", ui.SMSFolderBlocked}, {"10086", ui.SMSFolderBlocked}} {
		m, err := s.receive(ui.SMS{ID: fmt.Sprint("m", i), Peer: c.peer, Text: "hi"})
		if err != nil || m.Folder != c.folder {
			t.Errorf("from %s: %+v %v", c.peer, m, err)
		}
	}
	if why := s.blockedWhy("13700000088"); why != ui.BlockedMinor {
		t.Fatalf("why %q", why)
	}
	if err := s.setOnly(false, []string{"13900001111"}); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s)
	if m, _ := s.receive(ui.SMS{ID: "m9", Peer: "13700000088", Text: "hi"}); m.Folder != "" {
		t.Fatalf("minor mode off: %+v", m)
	}
}

// What the shell has not shown waits for it, across restarts, until it
// says it showed it (or the message was read or deleted).
func TestUnnotifiedWaitsForTheShell(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.block("10690000", true)
	for _, m := range []ui.SMS{{ID: "m1", Peer: "10086", Text: "a"}, {ID: "m2", Peer: "10010", Text: "b"}, {ID: "m3", Peer: "10690000", Text: "spam"}} {
		if _, err := s.receive(m); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := s.addAlert(ui.SMSAlert{ID: "b1", Level: ui.AlertPresidential, Channel: 4370, Text: "Drill", At: 1}, 1)
	s = reopen(t, s)
	msgs, alerts := s.unnotified()
	if len(msgs) != 2 || msgs[0].ID != "m1" || len(alerts) != 1 || alerts[0].ID != a.ID {
		t.Fatalf("unnotified: %+v %+v", msgs, alerts)
	}
	_ = s.notified([]string{"m1", a.ID, "unknown"})
	_, _ = s.markRead("10010")
	if msgs, alerts := s.unnotified(); len(msgs) != 0 || len(alerts) != 0 {
		t.Fatalf("still unnotified: %+v %+v", msgs, alerts)
	}
}

// Messages from another phone go in by their time, read or unread as they
// were, marked imported and announced to nobody; the same ones again are
// left out; undoing deletes only imported ones; all of it across restarts.
func TestImportMessages(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.receive(ui.SMS{ID: "m1", Peer: "13655550101", Text: "today", At: 1000}); err != nil {
		t.Fatal(err)
	}
	_ = s.block("10690000", true)
	var events []svc.SMSEvent
	sv := newService(s, func(ev svc.SMSEvent) { events = append(events, ev) })
	in := []ui.SMS{
		{Peer: "+86 136 5555 0101", Dir: "in", Text: "last week, code 482913", At: 500, Read: false},
		{Peer: "13655550101", Dir: "out", Text: "ok", At: 600},
		{Peer: "10690000", Dir: "in", Text: "spam", At: 700, Read: false},
		{Peer: "", Dir: "in", Text: "no sender", At: 1},
		{Peer: "13655550101", Dir: "sideways", Text: "x", At: 1},
	}
	raw, _ := json.Marshal(svc.SMSImportArgs{Messages: in})
	out, err := sv.handle(svc.SMSImport, raw)
	if err != nil {
		t.Fatal(err)
	}
	ids := out.(svc.SMSImportRes).IDs
	if len(ids) != 5 || ids[0] == "" || ids[1] == "" || ids[2] == "" || ids[3] != "" || ids[4] != "" {
		t.Fatalf("ids %q", ids)
	}
	if len(events) != 1 || events[0].Event != svc.SMSEventChanged {
		t.Fatalf("events %+v", events)
	}
	if msgs, _ := s.unnotified(); len(msgs) != 1 || msgs[0].ID != "m1" {
		t.Fatalf("imported messages to notify: %+v", msgs)
	}
	conv := s.list(ui.SMSListArgs{Peer: "13655550101"})
	if len(conv) != 3 || conv[0].ID != ids[0] || conv[1].ID != ids[1] || conv[2].ID != "m1" {
		t.Fatalf("by time: %+v", conv)
	}
	if m := conv[0]; !m.Imported || m.Read || m.Code != "482913" || m.Thread != "+8613655550101" || m.Dir != "in" {
		t.Fatalf("imported in: %+v", m)
	}
	if m := conv[1]; !m.Imported || !m.Read || m.State != ui.SMSSent {
		t.Fatalf("imported out: %+v", m)
	}
	if m := s.byID[ids[2]]; m.Folder != ui.SMSFolderBlocked {
		t.Fatalf("a blocked sender's: %+v", m)
	}
	again, _ := s.importMessages(in[:2])
	if again[0] != "" || again[1] != "" {
		t.Fatalf("imported twice: %q", again)
	}
	s = reopen(t, s)
	if conv := s.list(ui.SMSListArgs{Peer: "13655550101"}); len(conv) != 3 || conv[0].ID != ids[0] || !conv[0].Imported {
		t.Fatalf("after a restart: %+v", conv)
	}
	if n, err := s.removeImported([]string{ids[0], ids[1], ids[2], "m1", "nope"}); n != 3 || err != nil {
		t.Fatalf("undo: %d %v", n, err)
	}
	s = reopen(t, s)
	if len(s.msgs) != 1 || s.msgs[0].ID != "m1" {
		t.Fatalf("left: %+v", s.msgs)
	}
}

func TestAlertsAndImport(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	a, err := s.addAlert(ui.SMSAlert{ID: "b1", Level: ui.AlertExtreme, Channel: 4371, Text: "Flood", At: now}, 7)
	if err != nil || a == nil {
		t.Fatal("alert not stored", err)
	}
	if a, _ := s.addAlert(ui.SMSAlert{ID: "b2", Channel: 4371, Text: "Flood", At: now + 60}, 7); a != nil {
		t.Fatal("a repeat stored as a new alert")
	}
	if a, _ := s.addAlert(ui.SMSAlert{ID: "b1", Channel: 4371, Text: "Flood", At: now}, 7); a != nil {
		t.Fatal("an alert offered again stored twice")
	}
	if a, _ := s.addAlert(ui.SMSAlert{ID: "b3", Channel: 4371, Text: "Flood, update", At: now + 60}, 8); a == nil {
		t.Fatal("an update (new serial) dropped")
	}
	n, err := s.importOld([]ui.SMS{{ID: "s1", Peer: "10086", Dir: "in", Text: "old", At: 1}, {ID: "s2", Peer: "10086", Dir: "out", Text: "q", State: "sending"}})
	if n != 2 || err != nil || s.byID["s2"].State != ui.SMSFailed {
		t.Fatalf("import: %d %v", n, err)
	}
	if n, _ := s.importOld([]ui.SMS{{ID: "s1", Peer: "10086", Dir: "in", Text: "old"}}); n != 0 {
		t.Fatal("imported twice")
	}
	s = reopen(t, s)
	if got := s.alertList(); len(got) != 2 || got[0].Text != "Flood, update" {
		t.Fatalf("alerts after a restart: %+v", got)
	}
}
