package shell

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

// A phone starts with no contacts: the demo ones are for the developer
// machine and the selftest.
func TestNoDemoContactsOnAPhone(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir(), SKU: SKU{ID: "kario-qwerty-sd662-4g"}}}
	if got := c.contacts(); len(got) != 0 {
		t.Fatalf("a phone has %d contacts it was not given: %v", len(got), got)
	}
	c = &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir(), SKU: SKU{ID: "qemu-t9"}, DemoContacts: true}}
	if got := c.contacts(); len(got) != 5 || c.contactName("+86 138 0000 0002") != "\u674e\u56db" {
		t.Fatalf("the selftest's contacts: %v", got)
	}
}

// A contacts file the Shell cannot read is the user's data all the same:
// it stays on disk instead of being replaced.
func TestDamagedContactsFileIsKept(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir()}}
	damaged := `[{"name": "` + "\u5f20\u4e09" + `", "number": "13800000001"` // cut off mid-write
	if err := os.MkdirAll(filepath.Dir(c.contactsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.contactsPath(), []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	c.contacts()
	files, _ := filepath.Glob(c.contactsPath() + "*")
	for _, f := range files {
		if raw, _ := os.ReadFile(f); string(raw) == damaged {
			return
		}
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	t.Fatalf("the damaged contacts file is gone (left: %s)", strings.Join(names, ", "))
}

func contactsRig(t *testing.T, apps ...*App) (*Core, *broker) {
	t.Helper()
	dir := t.TempDir()
	c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: dir, SKU: SKU{ID: "phone"}}, apps: apps, files: openFiles(filepath.Join(dir, "files"))}
	b := &broker{c: c, perms: map[string]map[string]string{}, stats: map[string]*CapStat{}, usage: map[string]map[string]*permUse{},
		path: filepath.Join(dir, "shell", "perms.json")}
	c.broker = b
	return c, b
}

type answer struct {
	data any
	err  error
	n    int
}

func (r *answer) reply(data any, err error) { r.data, r.err, r.n = data, err, r.n+1 }

func req(cap string, args any) ui.Msg {
	raw, _ := json.Marshal(args)
	return ui.Msg{T: ui.MsgReq, Cap: cap, Args: raw}
}

func confirm(t *testing.T, c *Core, want string) *confirmOverlay {
	t.Helper()
	if len(c.overlays) != 1 {
		t.Fatalf("%d dialogs, want one", len(c.overlays))
	}
	o, ok := c.overlays[0].(*confirmOverlay)
	if !ok || !strings.Contains(o.text, want) {
		t.Fatalf("dialog %+v, want one saying %q", c.overlays[0], want)
	}
	c.popOverlay(o)
	return o
}

// What an app writes into the contacts waits for the user, who sees what
// it is; no yes, no change.
func TestContactWritesAskTheUser(t *testing.T) {
	app := &App{M: &Manifest{ID: "com.example.crm", Name: "CRM", Permissions: []string{ui.PermContacts}, installed: true}, running: true}
	c, b := contactsRig(t, app)
	var r answer
	b.dispatchContacts(app, req(ui.CapContactsSave, ui.Contact{Name: "Ann", Numbers: []ui.ContactNumber{{Number: "13700001234", Label: ui.NumberWork}}}), r.reply)
	o := confirm(t, c, tr.T("“%s” asks to add a contact:\n%s", "CRM", "Ann\n"+tr.T("Work")+" 13700001234"))
	o.onNo()
	if r.n != 1 || !ui.Cancelled(r.err) || len(c.contacts()) != 0 {
		t.Fatalf("declined: %+v, %v", r, c.contacts())
	}
	b.dispatchContacts(app, req(ui.CapContactsSave, ui.Contact{Name: "Ann", Number: "13700001234"}), r.reply)
	confirm(t, c, "Ann").onYes()
	ann, ok := r.data.(ui.Contact)
	if r.err != nil || !ok || ann.ID == "" || c.contactName("13700001234") != "Ann" {
		t.Fatalf("saved: %+v", r)
	}
	ann.Name = "Ann Lee"
	b.dispatchContacts(app, req(ui.CapContactsSave, ann), r.reply)
	confirm(t, c, tr.T("“%s” asks to change the contact “%s” to:\n%s", "CRM", "Ann", "Ann Lee")).onYes()
	if c.contactName("13700001234") != "Ann Lee" {
		t.Fatal("not changed")
	}
	b.dispatchContacts(app, req(ui.CapContactsDelete, ui.ContactIDArgs{ID: ann.ID}), r.reply)
	confirm(t, c, tr.T("“%s” asks to delete the contact:\n%s", "CRM", "Ann Lee")).onNo()
	if c.contactName("13700001234") != "Ann Lee" {
		t.Fatal("deleted without a yes")
	}
	b.dispatchContacts(app, req(ui.CapContactsExport, nil), r.reply)
	name := tr.T("Contacts") + " " + c.now().Format("2006-01-02") + ".vcf"
	confirm(t, c, tr.T("Save all %d contacts to Files as “%s”?\nFrom: %s\nAn app you give the file to can read them all", 1, name, "CRM")).onYes()
	if res, ok := r.data.(ui.ContactsFile); !ok || res.Count != 1 || len(c.files.Items) != 1 || c.files.Grants[app.M.ID] != nil {
		t.Fatalf("export: %+v, files %v, grants %v", r, c.files.Items, c.files.Grants)
	}
	raw, _ := os.ReadFile(c.files.path(c.files.Items[0]))
	if list, _, err := contacts.Parse(raw); err != nil || len(list) != 1 || list[0].Name != "Ann Lee" {
		t.Fatalf("the exported file: %v %v", list, err)
	}
	b.dispatchContacts(app, req(ui.CapContactsDelete, ui.ContactIDArgs{ID: ann.ID}), r.reply)
	confirm(t, c, "Ann Lee").onYes()
	if len(c.contacts()) != 0 || r.err != nil {
		t.Fatalf("delete: %+v", r)
	}
}

// The system's Contacts app is where the user edits: it writes without a
// dialog. A store app that takes its ID does not.
func TestContactsAppWritesDirectly(t *testing.T) {
	mgr := &App{M: &Manifest{ID: appContacts, Name: "Contacts", Permissions: []string{ui.PermContacts}}, running: true}
	fake := &App{M: &Manifest{ID: appContacts, Name: "Contacts", Permissions: []string{ui.PermContacts}, installed: true}, running: true}
	c, b := contactsRig(t, mgr)
	var r answer
	b.dispatchContacts(mgr, req(ui.CapContactsSave, ui.Contact{Name: "Bo", Number: "1"}), r.reply)
	if len(c.overlays) != 0 || r.err != nil || len(c.contacts()) != 1 {
		t.Fatalf("%d dialogs, %+v", len(c.overlays), r)
	}
	b.dispatchContacts(fake, req(ui.CapContactsDelete, ui.ContactIDArgs{ID: c.contacts()[0].ID}), r.reply)
	confirm(t, c, "Bo").onNo()
	b.dispatchContacts(mgr, req(ui.CapContactsDelete, ui.ContactIDArgs{ID: c.contacts()[0].ID}), r.reply)
	if len(c.overlays) != 0 || len(c.contacts()) != 0 {
		t.Fatal("the Contacts app's delete")
	}
	b.dispatchContacts(mgr, req(ui.CapContactsSave, ui.Contact{Name: " "}), r.reply)
	if r.err == nil || r.err.Error() != tr.T("A contact needs a name or a number") {
		t.Fatalf("an empty contact: %v", r.err)
	}
	// It calls as Phone does: no dialog (the impostor gets one).
	b.dispatchPhone(mgr, req(ui.CapTelDial, ui.DialArgs{Number: "10086"}), r.reply)
	if len(c.overlays) != 0 {
		t.Fatalf("the Contacts app's call asked: %+v", c.overlays)
	}
	b.dispatchPhone(fake, req(ui.CapTelDial, ui.DialArgs{Number: "10086"}), r.reply)
	confirm(t, c, tr.T("“%s” asks to call %s", "Contacts", "10086"))
}

// Reading, changing, deleting and exporting need the contacts permission;
// adding and importing only the user's yes.
func TestContactCapabilitiesPermissions(t *testing.T) {
	app := &App{M: &Manifest{ID: "com.example.game", Name: "Game", installed: true}, running: true, ready: true,
		out: make(chan outbound, appOutQueue)}
	c, b := contactsRig(t, app)
	for _, m := range []struct {
		msg     ui.Msg
		refused bool
	}{
		{req(ui.CapContacts, nil), true},
		{req(ui.CapContactsSave, ui.Contact{ID: "c1", Name: "x"}), true},
		{req(ui.CapContactsDelete, ui.ContactIDArgs{ID: "c1"}), true},
		{req(ui.CapContactsExport, nil), true},
		{req(ui.CapContactsSave, ui.Contact{Name: "x", Number: "1"}), false},
		{req(ui.CapContactsImport, nil), false},
	} {
		var got ui.Msg
		c.overlays = nil
		b.handleFor(app, m.msg, func(res ui.Msg, _ []*os.File) { got = res })
		refused := got.T == ui.MsgRes && strings.Contains(got.Err, "permission not declared")
		if refused != m.refused {
			t.Errorf("%s %s: refused %v (%+v, %d dialogs)", m.msg.Cap, m.msg.Args, refused, got, len(c.overlays))
		}
	}
}

// Import lists the vCards of Files; the one picked is read by the system,
// which says what is new before adding it.
func TestImportContacts(t *testing.T) {
	mgr := &App{M: &Manifest{ID: appContacts, Name: "Contacts", Permissions: []string{ui.PermContacts}}, running: true}
	c, b := contactsRig(t, mgr)
	c.contactBook().Save(ui.Contact{Name: "Ann", Number: "1"})
	var r answer
	b.dispatchContacts(mgr, req(ui.CapContactsImport, nil), r.reply)
	if r.err == nil || r.err.Error() != tr.T("There is no vCard file (.vcf) in Files") {
		t.Fatalf("no vCard: %v", r.err)
	}
	c.files.add("notes.txt", []byte("hello"), c.now())
	vcf := "BEGIN:VCARD\r\nFN:Ann\r\nTEL:1\r\nEND:VCARD\r\nBEGIN:VCARD\r\nFN:Bo\r\nTEL:2\r\nEND:VCARD\r\nBEGIN:VCARD\r\nEND:VCARD\r\n"
	c.files.add("friends.vcf", []byte(vcf), c.now())
	b.dispatchContacts(mgr, req(ui.CapContactsImport, nil), r.reply)
	m, ok := c.overlays[0].(*menuOverlay)
	if !ok || len(m.entries) != 1 || !strings.HasPrefix(m.entries[0].label, "friends.vcf") {
		t.Fatalf("the picker: %+v", c.overlays[0])
	}
	c.popOverlay(m)
	m.entries[0].run()
	confirm(t, c, tr.T("“%s” holds %d new contacts:\n%s\nFrom: %s\nAdd them to Contacts?", "friends.vcf", 1, "Bo", "Contacts")).onYes()
	res, _ := r.data.(ui.ContactsFile)
	if r.err != nil || res.Count != 1 || res.Skipped != 2 || c.contactName("2") != "Bo" {
		t.Fatalf("import: %+v %+v", r, res)
	}
}
