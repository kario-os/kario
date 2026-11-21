package shell

import (
	_ "embed"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The contacts store (package contacts) is the Shell's, as the alarms
// are: incoming calls, messages and the call log are named from it on the
// loop, car kits read it over Bluetooth (btobex.go), apps through the
// broker. A service of its own would add a round trip to each of those and
// would have nothing to do on its own: contacts change only through the
// user, and the user reaches them through the Shell. Apps read them with
// the contacts permission; what writes goes past the user in a system
// dialog, except the system's Contacts app, where the user edits (and
// moves a contact to another address book). A contact of a read-only
// address book (accounts.go) is the server's: nothing changes, moves or
// deletes it. The system's calendar app reads the birthdays and
// anniversaries alone (contacts.dates), to show them.

const appContacts = "com.phone.contacts"

func (c *Core) contactsPath() string { return filepath.Join(c.cfg.StateDir, "shell", "contacts.json") }

// sampleContacts are the demo contacts a new store starts with on a
// developer machine and in the selftest, whose cases call them.
//
//go:embed sample_contacts.json
var sampleContacts []byte

// demoContacts: a new store gets the demo contacts on a developer machine
// (no SKU) or when the command line asks (the selftest), never on a phone.
func (c *Core) demoContacts() bool { return c.cfg.SKU.ID == "" || c.cfg.DemoContacts }

func (c *Core) contactBook() *contacts.Store {
	if c.addressBook == nil {
		s := contacts.Open(c.contactsPath(), ime.Pinyin)
		if s.Kept != "" {
			c.log.Printf("contacts: %s did not parse; kept it as %s and started empty", c.contactsPath(), s.Kept)
		}
		if s.Created && c.demoContacts() {
			var list []ui.Contact
			_ = json.Unmarshal(sampleContacts, &list)
			if err := s.Seed(list); err != nil {
				c.log.Printf("contacts: %v", err)
			}
		}
		c.addressBook = s
		s.NewBook = c.accountDefault()
	}
	return c.addressBook
}

func (c *Core) contacts() []ui.Contact { return c.contactBook().List() }

func (c *Core) contactName(number string) string {
	if x, ok := c.contactBook().ByNumber(number); ok {
		return x.Name
	}
	return ""
}

// firstApp is the first of apps the product has (profile.go leaves some
// out).
func firstApp(apps ...*App) *App {
	for _, a := range apps {
		if a != nil {
			return a
		}
	}
	return nil
}

// contactsManager: the system's Contacts app, which writes without the
// system's dialogs, as the default SMS app deletes messages: the user
// edits in it, and it asks before deleting (its action's confirmation).
func contactsManager(a *App, origin string) bool {
	return a.M.ID == appContacts && !a.M.installed && origin == ""
}

func contactsErr(err error) error {
	switch {
	case errors.Is(err, contacts.ErrEmpty):
		return errors.New(tr.T("A contact needs a name or a number"))
	case errors.Is(err, contacts.ErrFull):
		return errors.New(tr.T("Contacts are full (%d)", contacts.MaxContacts))
	case errors.Is(err, contacts.ErrNotFound):
		return errors.New(tr.T("No such contact"))
	case errors.Is(err, contacts.ErrTooBig):
		return errors.New(tr.T("The vCard file is too big (at most %d MB)", contacts.MaxVCardBytes>>20))
	case errors.Is(err, contacts.ErrNotVCard):
		return errors.New(tr.T("Not a vCard file"))
	case errors.Is(err, contacts.ErrGroupName):
		return errors.New(tr.T("A group needs a name"))
	case errors.Is(err, contacts.ErrGroupExists):
		return errors.New(tr.T("There is a group of that name"))
	case errors.Is(err, contacts.ErrGroupsFull):
		return errors.New(tr.T("There are %d groups already", contacts.MaxGroups))
	case errors.Is(err, contacts.ErrMergeTwo):
		return errors.New(tr.T("Merging takes two contacts or more"))
	case errors.Is(err, contacts.ErrMergeFull):
		return errors.New(tr.T("A contact holds at most %d numbers", contacts.MaxNumbers))
	case errors.Is(err, contacts.ErrNoPhoto):
		return errors.New(tr.T("No such photo"))
	case errors.Is(err, contacts.ErrPhoto):
		return errors.New(tr.T("Not a photo the contacts take"))
	}
	return err
}

func numberLabel(l string) string {
	switch l {
	case ui.NumberHome:
		return tr.T("Home")
	case ui.NumberWork:
		return tr.T("Work")
	case ui.NumberOther:
		return tr.T("Other")
	}
	return tr.T("Mobile")
}

// contactLines is a contact as the system's dialogs show it.
func contactLines(x ui.Contact) string {
	lines := []string{x.Name}
	for _, n := range x.Numbers {
		lines = append(lines, numberLabel(n.Label)+" "+n.Number)
	}
	for _, e := range x.Emails {
		lines = append(lines, tr.T("E-mail (%s) %s", placeLabel(e.Label), e.Address))
	}
	for _, a := range x.Addresses {
		lines = append(lines, tr.T("Address (%s) %s", placeLabel(a.Label), addressLine(a)))
	}
	if x.Photo != "" {
		lines = append(lines, tr.T("With a photo"))
	}
	if x.Notes != "" {
		lines = append(lines, tr.T("Notes: %s", firstLine(x.Notes, 40)))
	}
	for _, f := range []struct{ value, label string }{{x.Nickname, tr.T("Nickname: %s", x.Nickname)}, {x.Org, tr.T("Organization: %s", x.Org)},
		{x.Title, tr.T("Job title: %s", x.Title)}, {x.Birthday, tr.T("Birthday: %s", x.Birthday)}, {x.Anniversary, tr.T("Anniversary: %s", x.Anniversary)}} {
		if f.value != "" {
			lines = append(lines, f.label)
		}
	}
	for _, u := range x.URLs {
		lines = append(lines, tr.T("Website: %s", u))
	}
	return strings.Join(lines, "\n")
}

func placeLabel(l string) string {
	switch l {
	case ui.NumberHome:
		return tr.T("Home")
	case ui.NumberWork:
		return tr.T("Work")
	}
	return tr.T("Other")
}

// addressLine is a postal address on one line, its parts as written.
func addressLine(a ui.ContactAddress) string {
	var parts []string
	for _, p := range []string{a.POBox, a.Extended, a.Street, a.City, a.Region, a.Postcode, a.Country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

func firstLine(s string, n int) string {
	s, _, cut := strings.Cut(s, "\n")
	if r := []rune(s); len(r) > n {
		s, cut = string(r[:n]), true
	}
	if cut {
		s += "…"
	}
	return s
}

// contactsChanged: the user changed the contacts. The running apps that
// read them read them again, and the contacts account syncs soon.
func (c *Core) contactsChanged() {
	c.notifyContacts()
	c.accountsLocalChange()
}

// notifyContacts tells the running apps that read contacts to read them
// again.
func (c *Core) notifyContacts() {
	for _, a := range c.apps {
		if a.running && c.broker.holds(a, ui.PermContacts) {
			a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventContacts})
		}
	}
	if c.sms.up {
		c.smsPeers()
	}
}

func (b *broker) dispatchContacts(a *App, m ui.Msg, reply func(any, error)) {
	c := b.c
	switch m.Cap {
	case ui.CapContacts:
		reply(c.contactsShown(), nil)
	case ui.CapContactsSave:
		var args ui.Contact
		_ = json.Unmarshal(m.Args, &args)
		c.saveContact(a, m.Origin, args, reply)
	case ui.CapContactsDelete:
		var args ui.ContactIDArgs
		_ = json.Unmarshal(m.Args, &args)
		c.deleteContact(a, m.Origin, args.ID, reply)
	case ui.CapContactsImport:
		c.importContacts(a, m.Origin, reply)
	case ui.CapContactsExport:
		c.exportContacts(a, m.Origin, reply)
	case ui.CapContactsGroups:
		reply(c.contactBook().Groups(), nil)
	case ui.CapContactsGroup:
		var args ui.ContactGroupArgs
		_ = json.Unmarshal(m.Args, &args)
		c.changeGroup(a, m.Origin, args, reply)
	case ui.CapContactsMerge:
		var args ui.ContactMergeArgs
		_ = json.Unmarshal(m.Args, &args)
		c.mergeContacts(a, m.Origin, args.IDs, reply)
	case ui.CapContactsPick:
		c.pickContact(a, m.Origin, reply)
	case ui.CapContactsPhoto:
		var args ui.ContactPhotoArgs
		_ = json.Unmarshal(m.Args, &args)
		c.contactPhoto(a, args, reply)
	case ui.CapContactsBooks:
		reply(c.contactBooks(a.system), nil)
	case ui.CapContactsDates:
		if !systemCalendar(a, m.Origin) {
			reply(nil, errors.New("only the system's calendar app reads the contacts' dates"))
			return
		}
		reply(c.contactDates(), nil)
	}
}

// contactsShown are the contacts as apps read them, those of a read-only
// book marked.
func (c *Core) contactsShown() []ui.Contact {
	list := c.contacts()
	for i := range list {
		list[i].ReadOnly = c.readOnlyBook(list[i].Book)
	}
	return list
}

// contactDates are the birthdays and anniversaries, by contact.
func (c *Core) contactDates() []ui.ContactDate {
	out := []ui.ContactDate{}
	for _, x := range c.contacts() {
		if x.Birthday != "" || x.Anniversary != "" {
			out = append(out, ui.ContactDate{ID: x.ID, Name: x.Name, Birthday: x.Birthday, Anniversary: x.Anniversary})
		}
	}
	return out
}

// readOnlyErr: the contact is in a read-only book.
func readOnlyErr(name string) error {
	return errors.New(tr.T("“%s” is in a read-only address book: it changes only on its server", name))
}

func (c *Core) saveContact(a *App, origin string, in ui.Contact, reply func(any, error)) {
	book := c.contactBook()
	in.PhotoData = nil
	shown, err := contacts.Clean(in, nil)
	if err != nil {
		reply(nil, contactsErr(err))
		return
	}
	old, ok := book.Get(in.ID)
	if in.ID != "" && !ok {
		reply(nil, contactsErr(contacts.ErrNotFound))
		return
	}
	if in.ID != "" && c.readOnlyBook(old.Book) {
		reply(nil, readOnlyErr(old.Name))
		return
	}
	move := false
	switch {
	case in.ID != "" && contactsManager(a, origin) && in.Book != "" && in.Book != old.Book && !(in.Book == ui.BookPhone && old.Book == ""):
		if err := c.bookTakes(in.Book); err != nil {
			reply(nil, err)
			return
		}
		move = true
	case in.ID != "":
		in.Book = old.Book
	case in.Book == "" || in.Book == ui.BookPhone:
	default:
		if !contactsManager(a, origin) || c.bookTakes(in.Book) != nil {
			in.Book = ""
		}
	}
	if in.Photo != "" && in.Photo != old.Photo && !book.HasPhoto(in.Photo) {
		reply(nil, contactsErr(contacts.ErrNoPhoto))
		return
	}
	save := func() {
		saved, err := book.Save(in)
		if err != nil {
			reply(nil, contactsErr(err))
			return
		}
		if move {
			c.log.Printf("%s: moved contact %s to %s", a.M.ID, saved.ID, firstNonEmpty(saved.Book, "the phone"))
		} else {
			c.log.Printf("%s: saved contact %s", a.M.ID, saved.ID)
		}
		c.contactsChanged()
		reply(saved, nil)
	}
	if contactsManager(a, origin) {
		save()
		return
	}
	text := tr.T("“%s” asks to add a contact:\n%s", c.fileOrigin(a, origin), contactLines(shown))
	if in.ID != "" {
		text = tr.T("“%s” asks to change the contact “%s” to:\n%s", c.fileOrigin(a, origin), old.Name, contactLines(shown))
	}
	c.pushOverlay(&confirmOverlay{text: text, yes: tr.T("Save"), onYes: save,
		onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

func (c *Core) deleteContact(a *App, origin, id string, reply func(any, error)) {
	book := c.contactBook()
	old, ok := book.Get(id)
	if !ok {
		reply(nil, contactsErr(contacts.ErrNotFound))
		return
	}
	if c.readOnlyBook(old.Book) {
		reply(nil, readOnlyErr(old.Name))
		return
	}
	del := func() {
		if err := book.Delete(id); err != nil {
			reply(nil, contactsErr(err))
			return
		}
		c.log.Printf("%s: deleted contact %s", a.M.ID, id)
		c.contactsChanged()
		reply(map[string]bool{"ok": true}, nil)
	}
	if contactsManager(a, origin) {
		del()
		return
	}
	c.pushOverlay(&confirmOverlay{text: tr.T("“%s” asks to delete the contact:\n%s", c.fileOrigin(a, origin), contactLines(old)),
		yes: tr.T("Delete"), onYes: del, onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// importContacts lists the vCard files in Files and, with a SIM card in,
// the SIM's phone book; what the user picks is read by the system, which
// asks before adding its contacts. The app learns how many it added,
// nothing of the file or the card.
func (c *Core) importContacts(a *App, origin string, reply func(any, error)) {
	var es []menuEntry
	for i := len(c.files.Items) - 1; i >= 0; i-- {
		f := c.files.Items[i]
		if !contacts.IsVCard(f.Name, f.Mime) {
			continue
		}
		es = append(es, menuEntry{group: tr.T("Files"), label: f.Name + " · " + sizeLabel(f.Size), find: f.Name, run: func() {
			c.offerVCard(f, c.fileOrigin(a, origin), func(res ui.ContactsFile, err error) { reply(res, err) })
		}})
	}
	if c.tel != nil && c.tel.Sim == "ready" {
		es = append(es, menuEntry{group: tr.T("SIM card"), label: tr.T("Contacts on the SIM card"), key: "s", run: func() {
			c.importSIM(c.fileOrigin(a, origin), func(res ui.ContactsFile, err error) { reply(res, err) })
		}})
	}
	if len(es) == 0 {
		reply(nil, errors.New(tr.T("There is no vCard file (.vcf) in Files")))
		return
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Import contacts · for “%s”", c.fileOrigin(a, origin)), entries: es,
		onCancel: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// importSIM reads the SIM's phone book through telephonyd (a modem may take
// a while: AT commands) and offers its contacts as a file's.
func (c *Core) importSIM(from string, done func(ui.ContactsFile, error)) {
	c.showToast(tr.T("Reading the SIM card…"))
	go func() {
		var out struct {
			Contacts []struct {
				Name   string `json:"name"`
				Number string `json:"number"`
			} `json:"contacts"`
		}
		err := c.telRPC(svc.TelPhonebook, nil, &out)
		c.Post(func() {
			if err != nil {
				c.log.Printf("contacts: reading the SIM card: %v", err)
				done(ui.ContactsFile{}, errors.New(tr.T("Cannot read the SIM card's contacts")))
				return
			}
			var list []ui.Contact
			for _, x := range out.Contacts {
				list = append(list, ui.Contact{Name: x.Name, Number: x.Number, Numbers: []ui.ContactNumber{{Number: x.Number, Label: ui.NumberMobile}}})
			}
			c.offerContacts(list, 0, tr.T("The SIM card"), from, done)
		})
	}()
}

// offerVCard reads a vCard file of Files and asks whether its contacts go
// into the contacts: from an app's import, or as it came over Bluetooth.
func (c *Core) offerVCard(f *fileItem, from string, done func(ui.ContactsFile, error)) {
	var raw []byte
	err := contacts.ErrTooBig
	if f.Size <= contacts.MaxVCardBytes {
		raw, err = readLimited(c.files.path(f), contacts.MaxVCardBytes)
	}
	var list []ui.Contact
	bad := 0
	if err == nil {
		list, bad, err = contacts.Parse(raw)
	}
	if err != nil {
		done(ui.ContactsFile{}, contactsErr(err))
		return
	}
	c.offerContacts(list, bad, f.Name, from, done)
}

// offerContacts asks whether the contacts of source (a file's name, the
// SIM card) not in Contacts yet go in; bad were unreadable.
func (c *Core) offerContacts(list []ui.Contact, bad int, source, from string, done func(ui.ContactsFile, error)) {
	book := c.contactBook()
	var fresh []ui.Contact
	for _, x := range list {
		if !book.Duplicate(x) {
			fresh = append(fresh, x)
		}
	}
	res := ui.ContactsFile{File: source, Skipped: len(list) - len(fresh) + bad}
	if len(fresh) == 0 {
		c.showToast(tr.T("“%s” has no contacts that are not in Contacts already", source))
		done(res, nil)
		return
	}
	var names []string
	for i, x := range fresh {
		if i == 3 {
			names = append(names, tr.T("and %d more", len(fresh)-3))
			break
		}
		names = append(names, x.Name)
	}
	text := tr.T("“%s” holds %d new contacts:\n%s\nFrom: %s\nAdd them to Contacts?", source, len(fresh), strings.Join(names, tr.T(", ")), from)
	if res.Skipped > 0 {
		text += "\n" + tr.T("%d others are in Contacts already or have no name and number", res.Skipped)
	}
	c.pushOverlay(&confirmOverlay{text: text, yes: tr.T("Add"),
		onYes: func() {
			added, skipped, err := book.AddAll(fresh)
			if err != nil {
				done(res, contactsErr(err))
				return
			}
			res.Count, res.Skipped = added, res.Skipped+skipped
			c.log.Printf("contacts: added %d from %s (%d left out)", added, source, res.Skipped)
			c.contactsChanged()
			c.showToast(tr.T("Added %d contacts", added))
			done(res, nil)
		},
		onNo: func() { done(res, errors.New(ui.ErrCancelled)) }})
}

// exportData is the contacts as a vCard file, with their photos when the
// file stays within what a phone reads back (contacts.MaxVCardBytes;
// photos says whether it has them), without any otherwise.
func (c *Core) exportData(list []ui.Contact) (data []byte, photos bool) {
	book := c.contactBook()
	with := make([]ui.Contact, len(list))
	size := 0
	for i, x := range list {
		with[i] = x
		if x.Photo != "" {
			if raw, err := book.Photo(x.Photo); err == nil {
				with[i].PhotoData = raw
				size += len(raw)*4/3 + len(raw)/40
			}
		}
	}
	plain := contacts.Write(list)
	if size == 0 {
		return plain, true
	}
	if len(plain)+size > contacts.MaxVCardBytes {
		return plain, false
	}
	return contacts.Write(with), true
}

// exportContacts writes every contact into a vCard file in Files once the
// user agrees. The file is not given to the app that asked: it is the
// user's, to share where they choose.
func (c *Core) exportContacts(a *App, origin string, reply func(any, error)) {
	list := c.contacts()
	if len(list) == 0 {
		reply(nil, errors.New(tr.T("There are no contacts to export")))
		return
	}
	name := tr.T("Contacts") + " " + c.now().Format("2006-01-02") + ".vcf"
	data, photos := c.exportData(list)
	text := tr.T("Save all %d contacts to Files as “%s”?\nFrom: %s\nAn app you give the file to can read them all", len(list), name, c.fileOrigin(a, origin))
	if !photos {
		text += "\n" + tr.T("Without their photos: with them the file would be larger than a phone reads back (%d MB)", contacts.MaxVCardBytes>>20)
	}
	c.pushOverlay(&confirmOverlay{text: text,
		yes: tr.T("Save"),
		onYes: func() {
			f, err := c.files.add(name, data, c.now())
			if err != nil {
				reply(nil, err)
				return
			}
			c.log.Printf("%s: exported %d contacts to file %s", a.M.ID, len(list), f.ID)
			c.showToast(tr.T("Saved to Files"))
			reply(ui.ContactsFile{File: f.Name, Count: len(list)}, nil)
		},
		onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}
