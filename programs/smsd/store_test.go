package smsd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	for in, want := range map[string]string{"+86 138-0013-8000": "13800138000", "008613800138000": "13800138000",
		"(010) 6552 9988": "01065529988", "10086": "10086", "+1 650 555 0100": "+16505550100", "Kario Bank": "kario bank"} {
		if got := thread(in); got != want {
			t.Errorf("thread(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStoreSurvivesRestartsAndTornWrites(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	if m, err := s.receive(ui.SMS{ID: "m1", Peer: "+8613800000001", Text: "hi", At: 10}); err != nil || m == nil || m.Thread != "13800000001" {
		t.Fatalf("receive: %+v %v", m, err)
	}
	if m, _ := s.receive(ui.SMS{ID: "m1", Peer: "13800000001", Text: "hi", At: 10}); m != nil {
		t.Fatal("the same message stored twice")
	}
	out, err := s.addOutgoing("138 0000 0001", "hello")
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
