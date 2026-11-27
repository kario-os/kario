package shell

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/smsbackup"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// An app waiting for a verification code gets it only once the user
// agrees, seeing the app, the message and the code; a new wait replaces
// the app's last; behind a PIN lock nothing is shown; the foreground
// app's wait goes first; an app that ended gets nothing.
func TestOTPGoesWithTheUsersYes(t *testing.T) {
	bank := &App{M: &Manifest{ID: "com.example.bank", Name: "Bank", installed: true}, running: true}
	shop := &App{M: &Manifest{ID: "com.example.shop", Name: "Shop", installed: true}, running: true}
	c, b := contactsRig(t, bank, shop)
	var first, r answer
	b.dispatchSMS(bank, req(ui.CapSMSOTP, ui.SMSOTPArgs{Seconds: 60}), first.reply)
	b.dispatchSMS(bank, req(ui.CapSMSOTP, nil), r.reply)
	if first.n != 1 || !ui.Cancelled(first.err) || r.n != 0 {
		t.Fatalf("a second wait: %+v %+v", first, r)
	}
	msg := ui.SMS{ID: "m1", Peer: "10690000", Text: "Your code is 482913", Code: "482913"}
	c.keylock, c.lock.pin = true, true
	c.smsOTPOffer(msg)
	if len(c.overlays) != 0 || c.sms.otp[bank.M.ID] == nil {
		t.Fatal("a code shown behind the PIN lock")
	}
	c.keylock, c.lock.pin = false, false
	c.smsOTPOffer(msg)
	offer := confirm(t, c, tr.T("“%s” asks for the verification code of this message:\n%s: %s\nGive it the code %s?", "Bank", "10690000", "Your code is 482913", "482913"))
	if c.overlayTakes(offer, Key{Name: KeyEnter, Injected: true}) {
		t.Fatal("a key from the computer handed the code over")
	}
	offer.onYes()
	if got, ok := r.data.(ui.SMSOTP); r.err != nil || !ok || got.Code != "482913" || got.From != "10690000" {
		t.Fatalf("handed over: %+v", r)
	}
	c.smsOTPOffer(msg)
	if len(c.overlays) != 0 {
		t.Fatal("a code offered with nobody waiting")
	}

	var rb, rs answer
	b.dispatchSMS(bank, req(ui.CapSMSOTP, nil), rb.reply)
	b.dispatchSMS(shop, req(ui.CapSMSOTP, nil), rs.reply)
	c.fg = shop
	c.smsOTPOffer(msg)
	confirm(t, c, "Shop").onNo()
	if !ui.Cancelled(rs.err) || rb.n != 0 {
		t.Fatalf("the foreground app first: shop %+v bank %+v", rs, rb)
	}
	bank.running = false
	c.smsOTPOffer(msg)
	if len(c.overlays) != 0 || rb.n != 0 {
		t.Fatal("an app that ended was offered a code")
	}
	for _, s := range c.sms.otp {
		s.timer.Stop()
	}
}

// The picker lists the contacts' numbers, found by what is typed; the
// app gets the name and the one number chosen, nothing else.
func TestContactPicker(t *testing.T) {
	app := &App{M: &Manifest{ID: "com.example.chat", Name: "Chat", installed: true}, running: true}
	c, b := contactsRig(t, app)
	var r answer
	b.dispatchContacts(app, req(ui.CapContactsPick, nil), r.reply)
	if r.err == nil || r.err.Error() != tr.T("There are no contacts with a number") {
		t.Fatalf("no contacts: %+v", r)
	}
	_ = c.contactBook().Seed([]ui.Contact{
		{Name: "Zhang San", Pinyin: "zhang san", Numbers: []ui.ContactNumber{{Number: "13800000001"}, {Number: "010 6552 0000", Label: ui.NumberWork}}, Notes: "private"},
		{Name: "Li Si", Number: "13800000002"},
		{Name: "No Number", Notes: "x"},
	})
	b.dispatchContacts(app, req(ui.CapContactsPick, nil), r.reply)
	m, ok := c.overlays[0].(*menuOverlay)
	if !ok || !m.palette || len(m.entries) != 3 || !strings.Contains(m.title, "Chat") {
		t.Fatalf("picker %+v", c.overlays[0])
	}
	m.query = "zs"
	vis := m.visible()
	if len(vis) != 2 {
		t.Fatalf("typed zs: %+v", vis)
	}
	c.popOverlay(m)
	vis[1].run()
	got, _ := r.data.(ui.Contact)
	if r.err != nil || got.Name != "Zhang San" || got.Number != "010 6552 0000" || len(got.Numbers) != 1 || got.ID != "" || got.Notes != "" || got.Pinyin != "" {
		t.Fatalf("picked %+v", r)
	}
	b.dispatchContacts(app, req(ui.CapContactsPick, nil), r.reply)
	c.overlays[0].(*menuOverlay).key(c, Key{Name: KeyEsc})
	if !ui.Cancelled(r.err) {
		t.Fatalf("closed: %+v", r)
	}
	var got2 ui.Msg
	b.handleFor(&App{M: &Manifest{ID: "x", installed: true}, running: true, ready: true, out: make(chan outbound, appOutQueue)},
		req(ui.CapContactsPick, nil), func(res ui.Msg, _ []*os.File) { got2 = res })
	if strings.Contains(got2.Err, "permission") {
		t.Fatalf("the picker needs a permission: %+v", got2)
	}
}

// Groups and merges by an app other than the system's Contacts wait for
// the user, who sees the change; the Contacts app makes them at once.
func TestContactGroupsAndMerge(t *testing.T) {
	crm := &App{M: &Manifest{ID: "com.example.crm", Name: "CRM", Permissions: []string{ui.PermContacts}, installed: true}, running: true}
	mgr := &App{M: &Manifest{ID: appContacts, Name: "Contacts", Permissions: []string{ui.PermContacts}}, running: true}
	c, b := contactsRig(t, crm, mgr)
	book := c.contactBook()
	ann, _ := book.Save(ui.Contact{Name: "Ann", Number: "13800000001"})
	ann2, _ := book.Save(ui.Contact{Name: "ann", Number: "13900000001"})
	var r answer
	b.dispatchContacts(crm, req(ui.CapContactsGroup, ui.ContactGroupArgs{Op: ui.ContactGroupCreate, Name: "Clients"}), r.reply)
	confirm(t, c, tr.T("“%s” asks to make the contact group “%s”", "CRM", "Clients")).onNo()
	if !ui.Cancelled(r.err) || len(book.Groups()) != 0 {
		t.Fatalf("declined: %+v", r)
	}
	b.dispatchContacts(crm, req(ui.CapContactsGroup, ui.ContactGroupArgs{Op: ui.ContactGroupCreate, Name: "Clients"}), r.reply)
	confirm(t, c, "Clients").onYes()
	g, _ := r.data.(ui.ContactGroup)
	if r.err != nil || g.ID == "" {
		t.Fatalf("made: %+v", r)
	}
	b.dispatchContacts(crm, req(ui.CapContactsGroup, ui.ContactGroupArgs{Op: ui.ContactGroupAdd, ID: g.ID, Contacts: []string{ann2.ID}}), r.reply)
	confirm(t, c, tr.T("“%s” asks to add to the contact group “%s”:\n%s", "CRM", "Clients", "ann")).onYes()
	b.dispatchContacts(mgr, req(ui.CapContactsGroup, ui.ContactGroupArgs{Op: ui.ContactGroupRename, ID: g.ID, Name: "VIP"}), r.reply)
	if len(c.overlays) != 0 || r.err != nil || book.Groups()[0].Name != "VIP" {
		t.Fatalf("the Contacts app renames: %+v %d", r, len(c.overlays))
	}
	b.dispatchContacts(crm, req(ui.CapContactsGroup, ui.ContactGroupArgs{Op: ui.ContactGroupDelete, ID: "g99"}), r.reply)
	if r.err == nil || r.err.Error() != tr.T("No such group") {
		t.Fatalf("no such group: %+v", r)
	}
	b.dispatchContacts(crm, req(ui.CapContactsMerge, ui.ContactMergeArgs{IDs: []string{ann.ID, ann2.ID}}), r.reply)
	confirm(t, c, tr.T("“%s” asks to merge %d contacts into one:\n%s", "CRM", 2, "Ann 13800000001\nann 13900000001")).onYes()
	merged, _ := r.data.(ui.Contact)
	if r.err != nil || merged.ID != ann.ID || len(merged.Numbers) != 2 || !slices.Equal(merged.Groups, []string{g.ID}) || len(c.contacts()) != 1 {
		t.Fatalf("merged: %+v, %v", r, c.contacts())
	}
	b.dispatchContacts(mgr, req(ui.CapContactsMerge, ui.ContactMergeArgs{IDs: []string{ann.ID}}), r.reply)
	if r.err == nil || r.err.Error() != tr.T("Merging takes two contacts or more") {
		t.Fatalf("one contact: %+v", r)
	}
	if got := b.dispatchGroups(mgr); len(got) != 1 || !slices.Equal(got[0].Members, []string{ann.ID}) {
		t.Fatalf("groups %+v", got)
	}
}

func (b *broker) dispatchGroups(a *App) []ui.ContactGroup {
	var r answer
	b.dispatchContacts(a, req(ui.CapContactsGroups, nil), r.reply)
	g, _ := r.data.([]ui.ContactGroup)
	return g
}

// fakeSMSD answers what the backups ask phone-smsd: the messages of each
// folder, the import (which leaves out what it has), a part's bytes.
type fakeSMSD struct {
	mu       sync.Mutex
	inbox    []ui.SMS
	blocked  []ui.SMS
	imported []ui.SMS
	part     svc.SMSPartRes
}

// with runs f on what the fake holds: its requests come over a socket,
// which the race detector does not count as ordering.
func (f *fakeSMSD) with(do func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	do()
}

func (f *fakeSMSD) serve(t *testing.T, dir string) {
	t.Helper()
	_, err := wire.ServeHub(filepath.Join(dir, svc.SmsdSock), func(method string, params json.RawMessage) (any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case svc.SMSList:
			var a ui.SMSListArgs
			_ = json.Unmarshal(params, &a)
			if a.Folder == ui.SMSFolderBlocked {
				return f.blocked, nil
			}
			return f.inbox, nil
		case svc.SMSImport:
			var a svc.SMSImportArgs
			_ = json.Unmarshal(params, &a)
			ids := make([]string, len(a.Messages))
			for i, m := range a.Messages {
				if !slices.ContainsFunc(f.inbox, func(x ui.SMS) bool { return x.Text == m.Text && x.At == m.At }) {
					f.inbox = append(f.inbox, m)
					f.imported = append(f.imported, m)
					ids[i] = "i" + m.Text
				}
			}
			return svc.SMSImportRes{IDs: ids}, nil
		case svc.SMSPart:
			return f.part, nil
		}
		return nil, errors.New("unknown method " + method)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func smsRig(t *testing.T, apps ...*App) (*Core, *broker, *fakeSMSD) {
	t.Helper()
	c, b := contactsRig(t, apps...)
	c.cfg.RunDir = t.TempDir()
	c.ev = make(chan func(), 16)
	b.clients = map[string]*wire.Client{}
	f := &fakeSMSD{}
	f.serve(t, c.cfg.RunDir)
	return c, b, f
}

// runUntilDialog runs what the shell's goroutines hand its loop, until a
// dialog shows, n functions ran or none came for five seconds.
func runUntilDialog(c *Core, n int) {
	for i := 0; i < n && len(c.overlays) == 0; i++ {
		select {
		case f := <-c.ev:
			f()
		case <-time.After(5 * time.Second):
			return
		}
	}
}

// A backup holds every message as SMS Backup & Restore writes them, in
// Files once the user agreed; restoring it lists the .xml files, says how
// many messages the one picked holds and adds, once agreed, what the
// phone does not have.
func TestSMSBackupAndRestore(t *testing.T) {
	msgs := &App{M: &Manifest{ID: appMessages, Name: "Messages", Permissions: []string{ui.PermSMS}}, running: true}
	c, b, f := smsRig(t, msgs)
	f.with(func() {
		f.inbox = []ui.SMS{{ID: "m1", Peer: "10086", Dir: "in", Text: "balance", At: 1763500000, Read: true},
			{ID: "o1", Peer: "10086", Dir: "out", Text: "later", At: 1763500100, State: ui.SMSScheduled, Due: 1763600000}}
		f.blocked = []ui.SMS{{ID: "b1", Peer: "10690000", Dir: "in", Text: "spam", At: 1763400000, Folder: ui.SMSFolderBlocked}}
	})
	var r answer
	b.dispatchSMS(msgs, req(ui.CapSMSBackup, nil), r.reply)
	runUntilDialog(c, 1)
	name := "sms-" + c.now().Format("20060102150405") + ".xml"
	confirm(t, c, tr.T("Save all %d messages to Files as “%s”?\nFrom: %s\nAn app you give the file to can read them all", 2, name, "Messages")).onYes()
	res, _ := r.data.(ui.SMSFile)
	if r.err != nil || res.Count != 2 || len(c.files.Items) != 1 {
		t.Fatalf("backup: %+v", r)
	}
	fh, _ := os.Open(c.files.path(c.files.Items[0]))
	back, _, err := smsbackup.Parse(fh, "")
	fh.Close()
	if err != nil || len(back) != 2 || back[0].Text != "spam" || back[1].Text != "balance" {
		t.Fatalf("the backup holds %+v (%v)", back, err)
	}

	c.files.add("notes.xml", []byte("<notes/>"), c.now())
	c.files.add("android.xml", []byte(`<smses><sms address="13800000001" date="1763000000000" type="1" body="from Android" read="1"/>`+
		`<sms address="10086" date="1763500000000" type="1" body="balance" read="1"/><sms address="1" date="1763000000000" type="3" body="draft"/></smses>`), c.now())
	b.dispatchSMS(msgs, req(ui.CapSMSRestore, nil), r.reply)
	m := c.overlays[0].(*menuOverlay)
	if len(m.entries) != 3 {
		t.Fatalf("restore lists %+v", m.entries)
	}
	c.popOverlay(m)
	m.entries[1].run()
	runUntilDialog(c, 1)
	if r.err == nil || r.err.Error() != tr.T("“%s” is not a message backup", "notes.xml") {
		t.Fatalf("not a backup: %+v", r)
	}
	m.entries[0].run()
	runUntilDialog(c, 1)
	confirm(t, c, tr.T("“%s” holds %d messages.\nFrom: %s\nRestore them to Messages? Those already there are left out.", "android.xml", 2, "Messages")).onYes()
	runUntilDialog(c, 1)
	res, _ = r.data.(ui.SMSFile)
	var imported []ui.SMS
	f.with(func() { imported = f.imported })
	if r.err != nil || res.Count != 1 || res.Skipped != 2 || len(imported) != 1 || imported[0].Text != "from Android" || imported[0].At != 1763000000 {
		t.Fatalf("restored %+v, smsd got %+v", r, imported)
	}

	other := &App{M: &Manifest{ID: "com.example.sms", Name: "SMS+", Permissions: []string{ui.PermSMS}, installed: true}, running: true}
	for _, cap := range []string{ui.CapSMSBackup, ui.CapSMSRestore, ui.CapSMSDraft} {
		b.dispatchSMS(other, req(cap, nil), r.reply)
		if r.err == nil || r.err.Error() != tr.T("Only the default SMS app can do this") {
			t.Errorf("%s by another app: %v", cap, r.err)
		}
	}
	b.dispatchSMS(other, req(ui.CapSMSSend, ui.SMSSendArgs{To: "10086", Text: "x", At: 1763600000}), r.reply)
	if r.err == nil || r.err.Error() != tr.T("Only the default SMS app can do this") {
		t.Errorf("scheduled by another app: %v", r.err)
	}
}

// A contact card in a multimedia message goes the way of one in Files:
// the system reads it and asks before adding what is new.
func TestMMSContactCard(t *testing.T) {
	msgs := &App{M: &Manifest{ID: appMessages, Name: "Messages", Permissions: []string{ui.PermSMS}}, running: true}
	c, b, f := smsRig(t, msgs)
	f.with(func() {
		f.part = svc.SMSPartRes{Type: ui.SMSTypeXVCard, Name: "li.vcf", Data: []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Li Lei\r\nTEL:13700009999\r\nEND:VCARD\r\n")}
	})
	var r answer
	b.dispatchSMS(msgs, req(ui.CapSMSVCard, ui.SMSPartArgs{ID: "w1", Index: 1}), r.reply)
	runUntilDialog(c, 1)
	confirm(t, c, tr.T("“%s” holds %d new contacts:\n%s\nFrom: %s\nAdd them to Contacts?", "li.vcf", 1, "Li Lei", "Messages")).onYes()
	if res, _ := r.data.(ui.ContactsFile); r.err != nil || res.Count != 1 || c.contactName("13700009999") != "Li Lei" {
		t.Fatalf("added %+v", r)
	}
	f.with(func() { f.part = svc.SMSPartRes{Type: "image/png", Name: "a.png", Data: []byte("x")} })
	b.dispatchSMS(msgs, req(ui.CapSMSVCard, ui.SMSPartArgs{ID: "w1"}), r.reply)
	runUntilDialog(c, 1)
	if r.err == nil || r.err.Error() != tr.T("Not a vCard file") {
		t.Fatalf("a picture: %+v", r)
	}
}
