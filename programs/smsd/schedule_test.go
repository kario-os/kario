package smsd

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// A conversation keeps what the user typed, across restarts; a draft
// alone is a conversation of its own; sending to it drops the draft.
func TestDrafts(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	if _, err := s.receive(ui.SMS{ID: "m1", Peer: "13800000001", Text: "hi", At: 1699999000}); err != nil {
		t.Fatal(err)
	}
	sv := newService(s, func(svc.SMSEvent) {})
	raw, _ := json.Marshal(ui.SMSDraftArgs{Peer: "+86 138 0000 0001", Text: "see you at"})
	if _, err := sv.handle(svc.SMSDraft, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.setDraft("13900000002, 13900000003", "group plan"); err != nil {
		t.Fatal(err)
	}
	if err := s.setDraft("", "x"); err == nil {
		t.Error("a draft without a number kept")
	}
	s = reopen(t, s)
	s.now = func() time.Time { return now }
	th := s.threads("")
	if len(th) != 2 || th[0].Thread != "+8613800000001" || th[0].Draft != "see you at" || th[0].Count != 1 || th[0].DraftAt != now.Unix() {
		t.Fatalf("threads after a restart: %+v", th)
	}
	if th[1].Thread != "+8613900000002,+8613900000003" || th[1].Draft != "group plan" || th[1].Count != 0 {
		t.Fatalf("a draft alone: %+v", th[1])
	}
	if len(s.threads(ui.SMSFolderBlocked)) != 0 {
		t.Fatal("drafts in the blocked folder")
	}
	if _, err := s.addOutgoing("13800000001", "see you at 8", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.setDraft("13900000003,13900000002", "  "); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s)
	if th := s.threads(""); len(th) != 1 || th[0].Draft != "" || th[0].Count != 2 {
		t.Fatalf("drafts after sending and clearing: %+v", th)
	}
}

// A scheduled message waits, across a restart, and goes at its time: it
// is then sent as any other and is the conversation's latest. One that
// came due while smsd was down goes as soon as it runs.
func TestScheduledSending(t *testing.T) {
	tmp := t.TempDir()
	sock, dir := filepath.Join(tmp, "telephonyd.sock"), filepath.Join(tmp, "smsd")
	tel := newFakeTel(t, sock)
	defer tel.close()
	d := startSMSD(t, dir, sock)
	now := time.Unix(1700000000, 0)
	d.s.mu.Lock()
	d.s.st.now = func() time.Time { return now }
	d.s.mu.Unlock()
	waitFor(t, "telephonyd", func() bool { return d.s.tel.up.Load() })
	if _, err := d.s.send(svc.SMSSendReq{To: "10086", Text: "past", At: now.Add(-time.Hour).Unix()}); err == nil {
		t.Error("a time that has passed taken")
	}
	if _, err := d.s.send(svc.SMSSendReq{To: "10086", Text: "far", At: now.Add(400 * 24 * time.Hour).Unix()}); err == nil {
		t.Error("a time more than a year ahead taken")
	}
	due := now.Add(10 * time.Minute).Unix()
	out, err := d.s.send(svc.SMSSendReq{To: "10086", Text: "happy birthday", At: due, Zone: "Asia/Shanghai"})
	if err != nil || out.State != ui.SMSScheduled || out.Due != due || out.Zone != "Asia/Shanghai" {
		t.Fatalf("scheduled: %+v %v", out, err)
	}
	cancel, _ := d.s.send(svc.SMSSendReq{To: "10086", Text: "never", At: due})
	d.s.mu.Lock()
	if n, _ := d.s.st.remove([]string{cancel.ID}, ""); n != 1 {
		t.Fatal("a scheduled message not cancelled")
	}
	d.s.mu.Unlock()
	d.s.runDue()
	tel.mu.Lock()
	early := len(tel.sent)
	tel.mu.Unlock()
	if early != 0 {
		t.Fatal("sent before its time")
	}
	d.s.mu.Lock()
	_, _ = d.s.st.receive(ui.SMS{ID: "r1", Peer: "10086", Text: "reply", At: now.Unix() + 60})
	d.s.mu.Unlock()
	d.halt()

	// smsd is down past the time.
	now = now.Add(time.Hour)
	d = startSMSD(t, dir, sock)
	defer d.halt()
	d.s.mu.Lock()
	d.s.st.now = func() time.Time { return now }
	if m := d.s.st.byID[out.ID]; m == nil || m.State != ui.SMSScheduled || m.Due != due {
		t.Fatalf("after a restart: %+v", m)
	}
	d.s.mu.Unlock()
	waitFor(t, "telephonyd", func() bool { return d.s.tel.up.Load() })
	if next := d.s.runDue(); next != 0 {
		t.Fatalf("another one due at %d", next)
	}
	waitFor(t, "sent", func() bool { m, _ := d.s.message(out.ID); return m.State == ui.SMSSent })
	tel.mu.Lock()
	sent := slices.Clone(tel.sent)
	tel.mu.Unlock()
	if !slices.Equal(sent, []string{out.ID}) {
		t.Fatalf("telephonyd got %v", sent)
	}
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	conv := d.s.st.list(ui.SMSListArgs{Peer: "10086"})
	if len(conv) != 2 || conv[1].ID != out.ID || conv[1].At != now.Unix() {
		t.Fatalf("the conversation: %+v", conv)
	}
}

// The next scheduled message is due when the earliest one is; one may
// also go at once (sms.send with its ID).
func TestScheduledNextAndNow(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	a, _ := s.addOutgoing("10086", "a", now.Unix()+600, "")
	b, _ := s.addOutgoing("10010", "b", now.Unix()+300, "")
	if ids, next := s.dueScheduled(now.Unix()); len(ids) != 0 || next != b.Due {
		t.Fatalf("due %v next %d", ids, next)
	}
	if ids, next := s.dueScheduled(now.Unix() + 300); !slices.Equal(ids, []string{b.ID}) || next != a.Due {
		t.Fatalf("due %v next %d", ids, next)
	}
	sv := newService(s, func(svc.SMSEvent) {})
	if m, err := sv.send(svc.SMSSendReq{ID: a.ID}); m.State != ui.SMSFailed || err == nil {
		t.Fatalf("sent now without telephonyd: %+v %v", m, err)
	}
	if ids, _ := s.dueScheduled(now.Unix() + 600); !slices.Equal(ids, []string{b.ID}) {
		t.Fatalf("still scheduled: %v", ids)
	}
}

// scheduled says how many messages still wait for their time: not one
// sent (or failed), nor one deleted.
func TestScheduledCount(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	sv := newService(s, func(svc.SMSEvent) {})
	count := func() int {
		t.Helper()
		res, err := sv.handle(svc.SMSScheduled, nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res)
		var got struct {
			Scheduled *int `json:"scheduled"`
		}
		if err := json.Unmarshal(raw, &got); err != nil || got.Scheduled == nil {
			t.Fatalf("answer %s %v", raw, err)
		}
		return *got.Scheduled
	}
	if n := count(); n != 0 {
		t.Fatalf("%d scheduled in a new store", n)
	}
	now := s.now()
	a, _ := s.addOutgoing("10086", "a", now.Unix()+600, "")
	_, _ = s.addOutgoing("10010", "b", now.Unix()+300, "")
	_, _ = s.addOutgoing("10010", "c", 0, "")
	if n := count(); n != 2 {
		t.Fatalf("%d scheduled, want 2", n)
	}
	_, _ = sv.send(svc.SMSSendReq{ID: a.ID})
	if n := count(); n != 1 {
		t.Fatalf("%d scheduled after one went, want 1", n)
	}
	raw, _ := json.Marshal(ui.SMSDeleteArgs{Peer: "10010"})
	if _, err := sv.handle(svc.SMSDelete, raw); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("%d scheduled after the conversation was deleted", n)
	}
}

// Words of the block list send a message holding one to the blocked
// folder, whoever sent it, without a notification; written in other
// cases, widths and spacing they still match; across restarts.
func TestBlockWords(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"Free Gift", "free-gift", "\u53d1\u7968"} {
		if err := s.setBlock(ui.BlockSetArgs{Word: w, On: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.setBlock(ui.BlockSetArgs{Word: " - ", On: true}); err == nil {
		t.Error("a word of marks only taken")
	}
	if got := s.blockList().Words; !slices.Equal(got, []string{"Free Gift", "\u53d1\u7968"}) {
		t.Fatalf("words %q", got)
	}
	s = reopen(t, s)
	for _, c := range []struct {
		id, text string
		blocked  bool
	}{
		{"m1", "Claim your FREE  gift now", true},
		{"m2", "\u4ee3\u5f00 \u53d1 * \u7968", true},
		{"m3", "\uff26\uff32\uff25\uff25\u3000\uff27\uff49\uff46\uff54", true},
		{"m4", "a free ride, a gift", false},
	} {
		m, err := s.receive(ui.SMS{ID: c.id, Peer: "13800000001", Text: c.text})
		if err != nil || (m.Folder == ui.SMSFolderBlocked) != c.blocked {
			t.Errorf("%q: %+v %v", c.text, m, err)
		}
	}
	if msgs, _ := s.unnotified(); len(msgs) != 1 || msgs[0].ID != "m4" {
		t.Fatalf("notified %+v", msgs)
	}
	if ids, _ := s.importMessages([]ui.SMS{{Peer: "10086", Dir: "in", Text: "free gift", At: 5}}); s.byID[ids[0]].Folder != ui.SMSFolderBlocked {
		t.Fatal("an imported message with a word")
	}
	sv := newService(s, func(svc.SMSEvent) {})
	if hold, err := sv.flashIn(ui.SMS{ID: "f1", Peer: "10086", Text: "free gift"}, "k"); hold || err != nil || s.byID["f1"].Folder != ui.SMSFolderBlocked {
		t.Fatalf("a class 0 message with a word: hold %v %v", hold, err)
	}
	if err := s.setBlock(ui.BlockSetArgs{Word: "FREE GIFT"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.receive(ui.SMS{ID: "m5", Peer: "13800000001", Text: "free gift"}); m.Folder != "" {
		t.Fatal("a word taken off still blocks")
	}
}

// A multimedia message's download holding a word goes to the blocked
// folder, unnotified.
func TestMMSBlockWord(t *testing.T) {
	e := newMMSEnv(t)
	s := e.s
	_ = s.st.setBlock(ui.BlockSetArgs{Word: "lottery", On: true})
	_, _ = s.receivedTel(e.offer(t, "w1", "13800138000", nil, "You won the LOTTERY", nil), "k")
	s.mmsRun()
	m := s.st.byID["w1"]
	if m.MMS != "" || m.Folder != ui.SMSFolderBlocked || !m.Read {
		t.Fatalf("downloaded: %+v", m)
	}
	if msgs, _ := s.st.unnotified(); len(msgs) != 0 {
		t.Fatal("notified")
	}
	if last := e.last(); last.New {
		t.Fatalf("event %+v", last)
	}
}

// A restored multimedia message keeps its subject, text and group; the
// same file again adds nothing; a message that was not sent stays so.
func TestImportForRestore(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	in := []ui.SMS{
		{Peer: "13900000002", Dir: "in", Text: "Dinner?", Subject: "Plan", Kind: ui.SMSKindMMS, Members: []string{"13900000002", "13900000003"}, At: 100},
		{Peer: "13900000002, 13900000003", Dir: "out", Text: "Yes", Kind: ui.SMSKindMMS, Members: []string{"13900000002", "13900000003"}, At: 200},
		{Peer: "10086", Dir: "out", Text: "balance", State: ui.SMSFailed, At: 300},
		{Peer: "10086", Dir: "in", Kind: ui.SMSKindMMS, Subject: "Bill", At: 400},
	}
	ids, err := s.importMessages(in)
	if err != nil || slices.Contains(ids, "") {
		t.Fatalf("ids %q %v", ids, err)
	}
	th := s.threads("")
	if len(th) != 2 || th[1].Thread != "+8613900000002,+8613900000003" || th[1].Count != 2 {
		t.Fatalf("threads %+v", th)
	}
	if m := s.byID[ids[0]]; m.Subject != "Plan" || m.Kind != ui.SMSKindMMS || m.Read {
		t.Fatalf("group message: %+v", m)
	}
	if m := s.byID[ids[2]]; m.State != ui.SMSFailed {
		t.Fatalf("not sent: %+v", m)
	}
	again, _ := s.importMessages(in)
	if slices.ContainsFunc(again, func(id string) bool { return id != "" }) {
		t.Fatalf("restored twice: %q", again)
	}
}
