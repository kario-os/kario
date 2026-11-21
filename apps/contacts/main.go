// Contacts: the system's contacts manager (the Nokia "Contacts" the idle
// screen's right soft key opens). The list searches as you type: letters,
// pinyin initials, keypad digits, e-mail addresses; it shows each
// contact's photo. A contact's page calls, writes a message or copies a
// number, an e-mail, a postal or web address; contacts are added, changed
// and deleted here (numbers, e-mail and postal addresses, a photo from the
// gallery, notes, nickname, organization and title, birthday and
// anniversary, web addresses, and for a new one the address book it goes
// to), moved to another address book, imported from a vCard in Files and
// exported to one, put in groups (the list of a group is the contacts list
// narrowed to it), and duplicates (the same number or name) merged once
// the user confirms. A contact of a read-only address book is the
// server's: its page offers to edit a copy, which goes to another book.
// Other apps save a number through it (the contact.save intent). The
// contacts are the system's (contacts.*); this app keeps none of its own.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Limits of the editor, the store's.
const (
	maxNumbers   = 8
	maxEmails    = 8
	maxAddresses = 4
	maxURLs      = 4
)

var labels = []string{ui.NumberMobile, ui.NumberHome, ui.NumberWork, ui.NumberOther}

func labelName(l string) string {
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

// editor is the contact on the edit page: what its inputs started with
// (an update must keep those values, or it would replace what was typed),
// each number's label and what the number inputs hold now.
type editor struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	Numbers []string `json:"numbers"`
	Labels  []string `json:"labels"`
	Typed   []string `json:"typed"`
	// The e-mail addresses as the numbers.
	Emails      []string `json:"emails"`
	EmailLabels []string `json:"email_labels"`
	EmailTyped  []string `json:"email_typed"`
	// Addresses are edited on a page of their own (addressScreen): Address
	// is the one open there (-1 a new one).
	Addresses []ui.ContactAddress `json:"addresses,omitempty"`
	Address   int                 `json:"address"`
	AddrLabel string              `json:"addr_label,omitempty"`
	Photo     string              `json:"photo,omitempty"`
	// Book is where a new contact goes ("" the default).
	Book  string `json:"book,omitempty"`
	Notes string `json:"notes"`
	// Nickname, Org, Title as typed at the start; the dates as their date
	// fields hold them (a date without its year in a year of the field's,
	// NoYear set).
	Nickname    string   `json:"nickname,omitempty"`
	Org         string   `json:"org,omitempty"`
	Title       string   `json:"title,omitempty"`
	Birthday    string   `json:"birthday,omitempty"`
	BirthNoYear bool     `json:"birth_no_year,omitempty"`
	Anniversary string   `json:"anniversary,omitempty"`
	AnnivNoYear bool     `json:"anniv_no_year,omitempty"`
	URLs        []string `json:"urls"`
	URLTyped    []string `json:"url_typed"`
	// CopyOf is the read-only contact the page saves a copy of.
	CopyOf string `json:"copy_of,omitempty"`
	// Intent is the contact.save request the page answers.
	Intent string `json:"intent,omitempty"`
}

// saved is what a new process needs to rebuild the pages (OnSave).
type saved struct {
	Query string  `json:"query"`
	Open  string  `json:"open"`
	Edit  *editor `json:"edit,omitempty"`
}

type book struct {
	app   *sdk.App
	list  []ui.Contact
	query string
	open  string // the contact page's contact
	edit  *editor
	// groups; group: the group page's group; naming: the group the name
	// page renames ("" makes one); groupsOpen, dupsOpen: those pages are
	// on the stack.
	groups     []ui.ContactGroup
	group      string
	naming     string
	groupsOpen bool
	dupsOpen   bool
	// addQuery: what the search of the page adding to a group holds.
	addQuery string
	// books are the address books contacts sync with (contacts.books).
	books []ui.ContactBook
}

func initial(s string) string {
	if r, _ := utf8.DecodeRuneInString(s); r != utf8.RuneError {
		return string(r)
	}
	return "?"
}

func (b *book) load() {
	b.list, b.groups = nil, nil
	if err := b.app.Call(ui.CapContacts, nil, &b.list); err != nil {
		log.Print(err)
	}
	if err := b.app.Call(ui.CapContactsGroups, nil, &b.groups); err != nil {
		log.Print(err)
	}
	b.books = nil
	if err := b.app.Call(ui.CapContactsBooks, nil, &b.books); err != nil {
		log.Print(err)
	}
}

func (b *book) findGroup(id string) (ui.ContactGroup, bool) {
	for _, g := range b.groups {
		if g.ID == id {
			return g, true
		}
	}
	return ui.ContactGroup{}, false
}

// groupNames are the names of the groups a contact is in.
func (b *book) groupNames(c ui.Contact) []string {
	var out []string
	for _, id := range c.Groups {
		if g, ok := b.findGroup(id); ok {
			out = append(out, g.Name)
		}
	}
	return out
}

func (b *book) find(id string) (ui.Contact, bool) {
	for _, c := range b.list {
		if c.ID == id {
			return c, true
		}
	}
	return ui.Contact{}, false
}

func numbersLine(c ui.Contact) string {
	nums := ui.NumbersOf(c)
	switch len(nums) {
	case 0:
		return ""
	case 1:
		return nums[0].Number
	}
	return tr.T("%s · %d more", nums[0].Number, len(nums)-1)
}

func contactActions(c ui.Contact) []ui.Action {
	acts := []ui.Action{{ID: ui.ActionOpen, Label: tr.T("Open"), Primary: true}}
	if c.Number != "" {
		acts = append(acts, ui.Action{ID: ui.ActionCall, Label: tr.T("Call")}, ui.Action{ID: "sms", Label: tr.T("Send message")})
	}
	if c.ReadOnly {
		return append(acts, ui.Action{ID: "edit", Label: tr.T("Edit a copy")})
	}
	return append(acts, ui.Action{ID: "edit", Label: tr.T("Edit")},
		ui.Action{ID: "delete", Label: tr.T("Delete"), Group: ui.GroupDanger, Confirm: tr.T("Delete the contact “%s”?", c.Name)})
}

func (b *book) listScreen() ui.Screen {
	s := ui.Screen{ID: "list", Title: tr.T("Contacts"), Right: "new", Focus: "q", Actions: []ui.Action{
		{ID: "new", Label: tr.T("New")},
		{ID: "groups", Label: tr.T("Groups"), Key: "g"},
		{ID: "dups", Label: tr.T("Merge duplicates"), Key: "m"},
		{ID: "import", Label: tr.T("Import from Files (.vcf)")},
		{ID: "export", Label: tr.T("Export all to Files")},
	}}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindInput, ID: "q", Placeholder: tr.T("Search: name, pinyin initials, keypad digits or e-mail"),
		Keyboard: "phone", Live: true})
	var hits []ui.Node
	for _, c := range b.list {
		if ui.ContactMatches(c, b.query) {
			hits = append(hits, ui.Node{Kind: ui.KindItem, ID: "c:" + c.ID, Icon: initial(c.Name), Src: photoSrc(c), Title: c.Name,
				Subtitle: numbersLine(c), Actions: contactActions(c)})
		}
	}
	switch {
	case len(b.list) == 0:
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No contacts yet. Press the right soft key to add one, or import a .vcf file from Files (Options).")))
	case b.query != "":
		s.Body = append(s.Body, sdk.Section(tr.T("Found (%d)", len(hits))))
		if len(hits) == 0 {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No contact matches")))
		}
	default:
		s.Body = append(s.Body, sdk.Section(tr.T("All (%d)", len(hits))))
	}
	s.Body = append(s.Body, hits...)
	return s
}

func (b *book) viewScreen(c ui.Contact) ui.Screen {
	s := ui.Screen{ID: "view", Title: c.Name, Actions: []ui.Action{
		{ID: "edit", Label: tr.T("Edit"), Key: "e"},
		{ID: "photo_set", Label: tr.T("Photo from Gallery"), Key: "p"},
		{ID: "delete", Label: tr.T("Delete"), Key: "d", Group: ui.GroupDanger, Confirm: tr.T("Delete the contact “%s”?", c.Name)},
	}}
	switch {
	case c.ReadOnly:
		s.Actions = []ui.Action{{ID: "edit", Label: tr.T("Edit a copy"), Key: "e"}}
	case len(b.moveChoices(c)) > 0:
		s.Actions = append(s.Actions, ui.Action{ID: "move", Label: tr.T("Move to another address book"), Key: "v"})
	}
	if c.Photo != "" {
		if !c.ReadOnly {
			s.Actions = append(s.Actions, ui.Action{ID: "photo_remove", Label: tr.T("Remove photo"), Key: "x", Group: ui.GroupDanger})
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindImage, ID: "photo", Src: ui.ContactPhotoPrefix + c.Photo, Rows: 6})
	}
	if c.Nickname != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Nickname: %s", c.Nickname)))
	}
	if line := joinNonEmpty(" · ", c.Title, c.Org); line != "" {
		s.Body = append(s.Body, sdk.Text(line))
	}
	if c.Number != "" {
		s.Actions = append([]ui.Action{{ID: ui.ActionCall, Label: tr.T("Call"), Key: "c"}, {ID: "sms", Label: tr.T("Send message"), Key: "m"}}, s.Actions...)
	}
	for i, n := range ui.NumbersOf(c) {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprintf("n:%d", i), Icon: initial(labelName(n.Label)), Title: n.Number,
			Subtitle: labelName(n.Label), Actions: []ui.Action{
				{ID: ui.ActionCall, Label: tr.T("Call"), Primary: true},
				{ID: "sms", Label: tr.T("Send message")},
				{ID: "copy", Label: tr.T("Copy number"), Key: "y"},
			}})
	}
	if len(ui.NumbersOf(c)) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No number")))
	}
	if len(c.Emails) > 0 {
		s.Body = append(s.Body, sdk.Section(tr.T("E-mail")))
	}
	for i, e := range c.Emails {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprintf("e:%d", i), Icon: "@", Title: e.Address, Subtitle: placeName(e.Label),
			Actions: []ui.Action{{ID: "copy_text", Label: tr.T("Copy e-mail address"), Primary: true}}})
	}
	if len(c.Addresses) > 0 {
		s.Body = append(s.Body, sdk.Section(tr.T("Address")))
	}
	for i, a := range c.Addresses {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprintf("a:%d", i), Icon: initial(placeName(a.Label)), Title: addressLine(a),
			Subtitle: placeName(a.Label), Actions: []ui.Action{{ID: "copy_text", Label: tr.T("Copy address"), Primary: true}}})
	}
	if c.Birthday != "" || c.Anniversary != "" {
		s.Body = append(s.Body, sdk.Section(tr.T("Dates")))
	}
	for _, d := range []struct{ id, date, what string }{{"bd", c.Birthday, tr.T("Birthday")}, {"an", c.Anniversary, tr.T("Anniversary")}} {
		if d.date != "" {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: d.id, Icon: initial(d.what), Title: dateText(d.date), Subtitle: d.what + yearsText(d.date, d.id == "bd")})
		}
	}
	if len(c.URLs) > 0 {
		s.Body = append(s.Body, sdk.Section(tr.T("Websites")))
	}
	for i, u := range c.URLs {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprintf("u:%d", i), Icon: "↗", Title: u,
			Actions: []ui.Action{{ID: "copy_text", Label: tr.T("Copy web address"), Primary: true}}})
	}
	if c.Notes != "" {
		s.Body = append(s.Body, sdk.Section(tr.T("Notes")))
		for _, l := range strings.Split(c.Notes, "\n") {
			s.Body = append(s.Body, sdk.Text(l))
		}
	}
	if names := b.groupNames(c); len(names) > 0 {
		s.Body = append(s.Body, sdk.Section(tr.T("Groups")), sdk.Text(strings.Join(names, tr.T(", "))))
	}
	if c.ReadOnly {
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("Read-only: it comes from %s and changes only on its server. Options › Edit a copy saves one in another address book.", b.bookName(c.Book))))
	} else if len(b.books) > 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("In: %s", b.bookName(c.Book))))
	}
	return s
}

// moveScreen offers the address books the open contact may move to.
func (b *book) moveScreen(c ui.Contact) ui.Screen {
	s := ui.Screen{ID: "move", Title: tr.T("Move “%s” to", c.Name)}
	for _, id := range b.moveChoices(c) {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "to:" + id, Title: b.bookName(id),
			Actions: []ui.Action{{ID: "move_to", Label: tr.T("Move here"), Primary: true}}})
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Now in: %s. The contact leaves that book (on its server too) and goes into the other with its photo and groups.", b.bookName(c.Book))))
	return s
}

func (b *book) groupsScreen() ui.Screen {
	s := ui.Screen{ID: "groups", Title: tr.T("Groups"), Right: "group-new", Actions: []ui.Action{{ID: "group-new", Label: tr.T("New group"), Key: "n"}}}
	for _, g := range b.groups {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "g:" + g.ID, Icon: initial(g.Name), Title: g.Name, Subtitle: tr.T("%d contacts", len(g.Members)),
			Actions: []ui.Action{
				{ID: "group-open", Label: tr.T("Open"), Primary: true},
				{ID: "group-rename", Label: tr.T("Rename"), Key: "r"},
				{ID: "group-delete", Label: tr.T("Delete group"), Key: "d", Group: ui.GroupDanger, Confirm: tr.T("Delete the group “%s”? Its contacts stay.", g.Name)},
			}})
	}
	if len(b.groups) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No groups yet. Press the right soft key to make one.")))
	}
	return s
}

func (b *book) groupNameScreen(name string) ui.Screen {
	title := tr.T("New group")
	if b.naming != "" {
		title = tr.T("Rename group")
	}
	return ui.Screen{ID: "group-name", Title: title, Focus: "name", Body: []ui.Node{
		{Kind: ui.KindInput, ID: "name", Placeholder: tr.T("Group name"), Value: name, Submit: "group-save"},
		sdk.Button("save", tr.T("Save"), "group-save"),
	}}
}

// groupScreen is the contacts list narrowed to a group.
func (b *book) groupScreen(g ui.ContactGroup) ui.Screen {
	s := ui.Screen{ID: "group", Title: g.Name, Actions: []ui.Action{{ID: "group-add", Label: tr.T("Add contacts"), Key: "a"}}}
	s.Body = append(s.Body, sdk.Section(tr.T("All (%d)", len(g.Members))))
	for _, c := range b.list {
		if slices.Contains(g.Members, c.ID) {
			acts := contactActions(c)
			if !c.ReadOnly {
				acts = append(acts, ui.Action{ID: "group-out", Label: tr.T("Remove from group"), Key: "x"})
			}
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "c:" + c.ID, Icon: initial(c.Name), Src: photoSrc(c), Title: c.Name, Subtitle: numbersLine(c), Actions: acts})
		}
	}
	if len(g.Members) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No contacts in this group. Options › Add contacts.")))
	}
	return s
}

// groupAddScreen lists the contacts not in the group, to add, searched
// as the list is.
func (b *book) groupAddScreen(g ui.ContactGroup) ui.Screen {
	s := ui.Screen{ID: "group-add", Title: tr.T("Add to “%s”", g.Name), Focus: "q", Body: []ui.Node{
		{Kind: ui.KindInput, ID: "q", Placeholder: tr.T("Search: name, pinyin initials or keypad digits"), Keyboard: "phone", Live: true},
	}}
	for _, c := range b.list {
		if !slices.Contains(g.Members, c.ID) && !c.ReadOnly && ui.ContactMatches(c, b.addQuery) {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "c:" + c.ID, Icon: initial(c.Name), Title: c.Name, Subtitle: numbersLine(c),
				Actions: []ui.Action{{ID: "group-in", Label: tr.T("Add"), Primary: true}}})
		}
	}
	if len(s.Body) == 1 && b.addQuery == "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Every contact is in this group")))
	}
	return s
}

// mergedName is the name merging keeps (as the system merges): the
// first's, unless it is only its number and another has a name.
func mergedName(set []ui.Contact) string {
	name := set[0].Name
	for _, c := range set {
		if name == set[0].Number && c.Name != c.Number {
			return c.Name
		}
	}
	return name
}

func (b *book) dupsScreen() ui.Screen {
	s := ui.Screen{ID: "dups", Title: tr.T("Merge duplicates")}
	var writable []ui.Contact
	for _, c := range b.list {
		if !c.ReadOnly {
			writable = append(writable, c)
		}
	}
	for _, ids := range ui.ContactDuplicates(writable) {
		var set []ui.Contact
		var names, nums []string
		for _, id := range ids {
			c, _ := b.find(id)
			set = append(set, c)
			names = append(names, c.Name)
			for _, n := range ui.NumbersOf(c) {
				if !slices.Contains(nums, n.Number) {
					nums = append(nums, n.Number)
				}
			}
		}
		name := mergedName(set)
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "d:" + strings.Join(ids, ","), Icon: initial(name), Title: strings.Join(names, " / "),
			Subtitle: strings.Join(nums, " · "), Actions: []ui.Action{{ID: "merge", Label: tr.T("Merge"), Primary: true,
				Confirm: tr.T("Merge these %d contacts into “%s”? Their numbers, notes and groups go together.", len(ids), name)}}})
	}
	if len(s.Body) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No duplicates: no two contacts share a number or a name")))
	} else {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Contacts with the same number or the same name")))
	}
	return s
}

// changeGroup makes a change to the groups and shows the pages again.
func (b *book) changeGroup(args ui.ContactGroupArgs) bool {
	var g ui.ContactGroup
	if err := b.app.Call(ui.CapContactsGroup, args, &g); err != nil {
		b.app.Toast(tr.T("Failed: %v", err))
		return false
	}
	b.load()
	b.refresh()
	return true
}

func newEditor(c ui.Contact) *editor {
	e := &editor{ID: c.ID, Name: c.Name, Notes: c.Notes, Photo: c.Photo, Addresses: append([]ui.ContactAddress(nil), c.Addresses...), Address: -1,
		Nickname: c.Nickname, Org: c.Org, Title: c.Title}
	e.Birthday, e.BirthNoYear = fieldDate(c.Birthday, time.Now())
	e.Anniversary, e.AnnivNoYear = fieldDate(c.Anniversary, time.Now())
	if c.ID == "" && c.Name == c.Number {
		e.Name = ""
	}
	if c.ReadOnly {
		e.ID, e.CopyOf = "", c.ID
	}
	for _, u := range c.URLs {
		e.URLs = append(e.URLs, u)
		e.URLTyped = append(e.URLTyped, u)
	}
	for _, n := range ui.NumbersOf(c) {
		e.Numbers = append(e.Numbers, n.Number)
		e.Labels = append(e.Labels, firstNonEmpty(n.Label, ui.NumberMobile))
		e.Typed = append(e.Typed, n.Number)
	}
	for _, m := range c.Emails {
		e.Emails = append(e.Emails, m.Address)
		e.EmailLabels = append(e.EmailLabels, firstNonEmpty(m.Label, ui.NumberOther))
		e.EmailTyped = append(e.EmailTyped, m.Address)
	}
	e.slot()
	return e
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// slot keeps one empty number input and one empty e-mail input at the
// end, for another.
func (e *editor) slot() {
	if n := len(e.Typed); n < maxNumbers && (n == 0 || strings.TrimSpace(e.Typed[n-1]) != "") {
		next := ui.NumberMobile
		if n > 0 {
			next = ui.NumberHome
		}
		e.Numbers, e.Labels, e.Typed = append(e.Numbers, ""), append(e.Labels, next), append(e.Typed, "")
	}
	if n := len(e.EmailTyped); n < maxEmails && (n == 0 || strings.TrimSpace(e.EmailTyped[n-1]) != "") {
		e.Emails, e.EmailLabels, e.EmailTyped = append(e.Emails, ""), append(e.EmailLabels, ui.NumberHome), append(e.EmailTyped, "")
	}
	if n := len(e.URLTyped); n < maxURLs && (n == 0 || strings.TrimSpace(e.URLTyped[n-1]) != "") {
		e.URLs, e.URLTyped = append(e.URLs, ""), append(e.URLTyped, "")
	}
}

func (b *book) editScreen() ui.Screen {
	e := b.edit
	title := tr.T("Edit contact")
	switch {
	case e.CopyOf != "":
		title = tr.T("Copy of a contact")
	case e.ID == "":
		title = tr.T("New contact")
	}
	s := ui.Screen{ID: "edit", Title: title, Right: "save", Focus: "name", Actions: []ui.Action{{ID: "save", Label: tr.T("Save")}}}
	if e.CopyOf != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("The contact is in a read-only address book: this saves a copy in another one")))
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindInput, ID: "name", Placeholder: tr.T("Name"), Value: e.Name})
	for i := range e.Numbers {
		s.Body = append(s.Body, sdk.Section(tr.T("%s number", labelName(e.Labels[i]))))
		n := ui.Node{Kind: ui.KindInput, ID: fmt.Sprintf("num:%d", i), Placeholder: tr.T("Number (Options: its type)"), Keyboard: "phone",
			Value: e.Numbers[i], Live: true}
		for k, l := range labels {
			label := tr.T("Type: %s", labelName(l))
			if l == e.Labels[i] {
				label += " ✓"
			}
			n.Actions = append(n.Actions, ui.Action{ID: "label:" + l, Label: label, Key: fmt.Sprint(k + 1)})
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Notes")),
		ui.Node{Kind: ui.KindInput, ID: "notes", Placeholder: tr.T("Notes"), Value: e.Notes})
	for i := range e.Emails {
		s.Body = append(s.Body, sdk.Section(tr.T("%s e-mail", placeName(e.EmailLabels[i]))))
		n := ui.Node{Kind: ui.KindInput, ID: fmt.Sprintf("mail:%d", i), Placeholder: tr.T("E-mail address (Options: its type)"), Keyboard: ui.KeyboardEmail,
			Value: e.Emails[i], Live: true}
		for k, l := range places {
			label := tr.T("Type: %s", placeName(l))
			if l == e.EmailLabels[i] {
				label += " ✓"
			}
			n.Actions = append(n.Actions, ui.Action{ID: "mlabel:" + l, Label: label, Key: fmt.Sprint(k + 1)})
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Address")))
	for i, a := range e.Addresses {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprintf("adr:%d", i), Icon: initial(placeName(a.Label)), Title: addressLine(a),
			Subtitle: placeName(a.Label), Actions: []ui.Action{{ID: "adr_edit", Label: tr.T("Edit"), Primary: true},
				{ID: "adr_delete", Label: tr.T("Delete"), Group: ui.GroupDanger}}})
	}
	if len(e.Addresses) < maxAddresses {
		s.Body = append(s.Body, sdk.Button("adr_add", tr.T("Add an address"), "adr_add"))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Organization")),
		ui.Node{Kind: ui.KindInput, ID: "org", Placeholder: tr.T("Organization"), Value: e.Org},
		ui.Node{Kind: ui.KindInput, ID: "jobtitle", Placeholder: tr.T("Job title"), Value: e.Title},
		ui.Node{Kind: ui.KindInput, ID: "nick", Placeholder: tr.T("Nickname"), Value: e.Nickname})
	for _, d := range []struct {
		id, value, what, whatNoYear string
		noYear                      bool
	}{
		{"birthday", e.Birthday, tr.T("Birthday"), tr.T("Birthday (without the year)"), e.BirthNoYear},
		{"anniversary", e.Anniversary, tr.T("Anniversary"), tr.T("Anniversary (without the year)"), e.AnnivNoYear},
	} {
		head, label := d.what, tr.T("Without the year")
		if d.noYear {
			head, label = d.whatNoYear, label+" ✓"
		}
		s.Body = append(s.Body, sdk.Section(head), ui.Node{Kind: ui.KindInput, ID: d.id, Placeholder: tr.T("%s (Options: without the year)", d.what),
			Keyboard: ui.KeyboardDate, Value: d.value, Actions: []ui.Action{{ID: "noyear", Label: label, Key: "1"}}})
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Websites")))
	for i := range e.URLs {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindInput, ID: fmt.Sprintf("url:%d", i), Placeholder: tr.T("Web address"), Keyboard: ui.KeyboardURL,
			Value: e.URLs[i], Live: true})
	}
	photo := ui.Node{Kind: ui.KindItem, ID: "photo", Icon: initial(e.Name), Title: tr.T("Photo"), Subtitle: tr.T("None"),
		Actions: []ui.Action{{ID: "photo_pick", Label: tr.T("Photo from Gallery"), Primary: true}}}
	if e.Photo != "" {
		photo.Src, photo.Subtitle = ui.ContactPhotoPrefix+e.Photo, tr.T("Set")
		photo.Actions = append(photo.Actions, ui.Action{ID: "photo_clear", Label: tr.T("Remove photo"), Group: ui.GroupDanger})
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Photo")), photo)
	if e.ID == "" && len(b.bookChoices()) > 1 {
		n := ui.Node{Kind: ui.KindItem, ID: "book", Title: tr.T("Save to"), Subtitle: b.bookName(b.newBook(e.Book)),
			Actions: []ui.Action{{ID: "book_next", Label: tr.T("Change"), Primary: true}}}
		for k, id := range b.bookChoices() {
			label := b.bookName(id)
			if id == b.newBook(e.Book) {
				label += " ✓"
			}
			n.Actions = append(n.Actions, ui.Action{ID: "book:" + id, Label: label, Key: fmt.Sprint(k + 1)})
		}
		s.Body = append(s.Body, sdk.Section(tr.T("Address book")), n)
	}
	s.Body = append(s.Body, sdk.Button("save", tr.T("Save"), "save"))
	return s
}

// addressScreen edits one of the editor's postal addresses (Address; -1
// a new one).
func (b *book) addressScreen() ui.Screen {
	e := b.edit
	a := ui.ContactAddress{Label: ui.NumberHome}
	title := tr.T("New address")
	if e.Address >= 0 && e.Address < len(e.Addresses) {
		a, title = e.Addresses[e.Address], tr.T("Edit address")
	}
	field := func(id, placeholder, value string) ui.Node {
		return ui.Node{Kind: ui.KindInput, ID: id, Placeholder: placeholder, Value: value}
	}
	s := ui.Screen{ID: "address", Title: title, Right: "adr_save", Focus: "street", Actions: []ui.Action{{ID: "adr_save", Label: tr.T("Save")}}}
	for k, l := range places {
		label := tr.T("Type: %s", placeName(l))
		if l == firstNonEmpty(a.Label, ui.NumberOther) {
			label += " ✓"
		}
		s.Actions = append(s.Actions, ui.Action{ID: "alabel:" + l, Label: label, Key: fmt.Sprint(k + 1)})
	}
	s.Body = []ui.Node{
		sdk.Styled(ui.StyleDim, tr.T("%s address (Options: its type)", placeName(a.Label))),
		field("street", tr.T("Street and number"), a.Street),
		field("extended", tr.T("Building, floor, room"), a.Extended),
		field("city", tr.T("City"), a.City),
		field("region", tr.T("Province or state"), a.Region),
		field("postcode", tr.T("Postcode"), a.Postcode),
		field("country", tr.T("Country"), a.Country),
		sdk.Button("adr_save", tr.T("Save"), "adr_save"),
	}
	return s
}

// saveAddress takes the address page's fields into the editor.
func (b *book) saveAddress(fields map[string]string, label string) bool {
	e := b.edit
	a := ui.ContactAddress{Label: label, Street: strings.TrimSpace(fields["street"]), Extended: strings.TrimSpace(fields["extended"]),
		City: strings.TrimSpace(fields["city"]), Region: strings.TrimSpace(fields["region"]), Postcode: strings.TrimSpace(fields["postcode"]),
		Country: strings.TrimSpace(fields["country"])}
	if e.Address >= 0 && e.Address < len(e.Addresses) {
		a.POBox = e.Addresses[e.Address].POBox
	}
	if a.Street == "" && a.Extended == "" && a.City == "" && a.Region == "" && a.Postcode == "" && a.Country == "" && a.POBox == "" {
		b.app.Toast(tr.T("Enter the address"))
		return false
	}
	if e.Address >= 0 && e.Address < len(e.Addresses) {
		e.Addresses[e.Address] = a
	} else {
		e.Addresses = append(e.Addresses, a)
	}
	return true
}

func (b *book) openEdit(c ui.Contact, intent string) {
	b.edit = newEditor(c)
	b.edit.Intent = intent
	b.app.Push(b.editScreen())
}

// edited is the contact the edit page's inputs hold.
func (b *book) edited(fields map[string]string) ui.Contact {
	e := b.edit
	c := ui.Contact{ID: e.ID, Name: strings.TrimSpace(fields["name"]), Notes: strings.TrimSpace(fields["notes"]), Photo: e.Photo,
		Addresses: e.Addresses, Book: e.Book, Nickname: strings.TrimSpace(fields["nick"]), Org: strings.TrimSpace(fields["org"]),
		Title: strings.TrimSpace(fields["jobtitle"]), Birthday: contactDate(fields["birthday"], e.BirthNoYear),
		Anniversary: contactDate(fields["anniversary"], e.AnnivNoYear)}
	for i := range e.URLs {
		if u := strings.TrimSpace(fields[fmt.Sprintf("url:%d", i)]); u != "" {
			c.URLs = append(c.URLs, u)
		}
	}
	for i := range e.Numbers {
		if n := strings.TrimSpace(fields[fmt.Sprintf("num:%d", i)]); n != "" {
			c.Numbers = append(c.Numbers, ui.ContactNumber{Number: n, Label: e.Labels[i]})
		}
	}
	for i := range e.Emails {
		if m := strings.TrimSpace(fields[fmt.Sprintf("mail:%d", i)]); m != "" {
			c.Emails = append(c.Emails, ui.ContactEmail{Address: m, Label: e.EmailLabels[i]})
		}
	}
	return c
}

// save takes what the edit page's inputs hold.
func (b *book) save(fields map[string]string) {
	e := b.edit
	c := b.edited(fields)
	if c.Name == "" && len(c.Numbers) == 0 {
		b.app.Toast(tr.T("Enter a name or a number"))
		return
	}
	var got ui.Contact
	if err := b.app.Call(ui.CapContactsSave, c, &got); err != nil {
		b.app.Toast(tr.T("Not saved: %v", err))
		return
	}
	intent, existing := e.Intent, e.ID != ""
	if e.CopyOf != "" {
		existing = false
		b.app.Pop()
	}
	b.edit = nil
	b.load()
	b.app.Toast(tr.T("Saved “%s”", got.Name))
	switch {
	case intent != "":
		res, _ := json.Marshal(map[string]string{"id": got.ID})
		b.app.Pop()
		b.app.Go(ui.CapIntentReply, ui.IntentReplyArgs{ID: intent, OK: true, Result: string(res)}, nil, nil)
	case existing:
		b.app.Pop()
	default:
		b.open = got.ID
		b.app.Replace(b.viewScreen(got))
	}
	b.refresh()
}

// refresh redraws the pages the app holds after the contacts changed.
func (b *book) refresh() {
	b.app.Update(b.listScreen())
	if c, ok := b.find(b.open); ok {
		b.app.Update(b.viewScreen(c))
	}
	if b.groupsOpen {
		b.app.Update(b.groupsScreen())
	}
	if g, ok := b.findGroup(b.group); ok {
		b.app.Update(b.groupScreen(g))
		b.app.Update(b.groupAddScreen(g))
	}
	if b.dupsOpen {
		b.app.Update(b.dupsScreen())
	}
}

func (b *book) remove(c ui.Contact) bool {
	if err := b.app.Call(ui.CapContactsDelete, ui.ContactIDArgs{ID: c.ID}, nil); err != nil {
		b.app.Toast(tr.T("Not deleted: %v", err))
		return false
	}
	b.app.Toast(tr.T("Deleted “%s”", c.Name))
	b.load()
	return true
}

func (b *book) call(number string) {
	b.app.Go(ui.CapTelDial, ui.DialArgs{Number: number}, nil, func(err error) {
		if err != nil && !ui.Cancelled(err) {
			b.app.Toast(tr.T("Cannot call: %v", err))
		}
	})
}

// message opens the conversation with the number in the messages app.
func (b *book) message(c ui.Contact, number string) {
	payload, _ := json.Marshal(ui.Contact{Name: c.Name, Number: number})
	var res ui.IntentRes
	b.app.Go(ui.CapIntentInvoke, ui.IntentArgs{Action: ui.IntentMessageCompose, Payload: string(payload), What: tr.T("write to %s", c.Name)}, &res, func(err error) {
		if err != nil && strings.HasSuffix(err.Error(), ui.ErrNoHandler) {
			b.app.Toast(tr.T("No app writes messages"))
		}
	})
}

// link opens a deep link: "new" a new contact, an intent:<json> a save
// asked by another app, anything else the list.
func (b *book) link(arg string) {
	if raw, ok := strings.CutPrefix(arg, ui.IntentPrefix); ok {
		var in ui.Intent
		var c ui.Contact
		if json.Unmarshal([]byte(raw), &in) != nil || in.Action != ui.IntentContactSave || json.Unmarshal([]byte(in.Payload), &c) != nil {
			return
		}
		c.ID = ""
		b.openEdit(c, in.ID)
		return
	}
	if arg == "new" {
		b.openEdit(ui.Contact{}, "")
	}
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	b := &book{app: app}
	app.OnSave = func() any { return saved{Query: b.query, Open: b.open, Edit: b.edit} }
	app.OnStart = func(arg string) {
		b.load()
		var st saved
		restored := app.Restore(&st)
		if restored {
			b.query = st.Query
		}
		app.Reset(b.listScreen())
		if c, ok := b.find(st.Open); restored && ok {
			b.open = c.ID
			app.Push(b.viewScreen(c))
		}
		if restored && st.Edit != nil {
			b.edit = st.Edit
			app.Push(b.editScreen())
			return
		}
		b.link(arg)
	}
	app.OnEvent = func(name, value string) {
		switch name {
		case ui.EventOpen:
			b.link(value)
		case ui.EventContacts:
			b.load()
			b.refresh()
		}
	}
	app.OnLifecycle = func(state string) {
		if state == ui.StateForeground {
			b.load()
			b.refresh()
		}
	}
	app.OnPopped = func(screen string) {
		switch screen {
		case "groups":
			b.groupsOpen = false
		case "group":
			b.group = ""
		case "dups":
			b.dupsOpen = false
		case "view":
			b.open = ""
		case "edit":
			if b.edit != nil && b.edit.Intent != "" {
				app.Go(ui.CapIntentReply, ui.IntentReplyArgs{ID: b.edit.Intent}, nil, nil)
			}
			b.edit = nil
		case "address":
			if b.edit != nil {
				b.edit.Address = -1
			}
		}
	}
	app.OnAction = func(e sdk.ActionEvent) {
		id, onContact := strings.CutPrefix(e.Node, "c:")
		if !onContact {
			id = b.open
		}
		c, known := b.find(id)
		number := c.Number
		var i int
		if _, err := fmt.Sscanf(e.Node, "n:%d", &i); err == nil {
			if nums := ui.NumbersOf(c); i < len(nums) {
				number = nums[i].Number
			}
		}
		switch {
		case e.Screen == "list" && e.Node == "q" && e.Action == "changed":
			b.query = e.Value
			app.Update(b.listScreen())
		case e.Screen == "edit" && strings.HasPrefix(e.Node, "num:") && e.Action == "changed" && b.edit != nil:
			fmt.Sscanf(e.Node, "num:%d", &i)
			if i < len(b.edit.Typed) {
				n := len(b.edit.Typed)
				b.edit.Typed[i] = e.Value
				if b.edit.slot(); len(b.edit.Typed) != n {
					app.Update(b.editScreen())
				}
			}
		case e.Screen == "edit" && strings.HasPrefix(e.Action, "label:") && b.edit != nil:
			fmt.Sscanf(e.Node, "num:%d", &i)
			if i < len(b.edit.Labels) {
				b.edit.Labels[i] = strings.TrimPrefix(e.Action, "label:")
				app.Update(b.editScreen())
			}
		case e.Screen == "edit" && strings.HasPrefix(e.Node, "mail:") && e.Action == "changed" && b.edit != nil:
			fmt.Sscanf(e.Node, "mail:%d", &i)
			if i < len(b.edit.EmailTyped) {
				n := len(b.edit.EmailTyped)
				b.edit.EmailTyped[i] = e.Value
				if b.edit.slot(); len(b.edit.EmailTyped) != n {
					app.Update(b.editScreen())
				}
			}
		case e.Screen == "edit" && strings.HasPrefix(e.Node, "url:") && e.Action == "changed" && b.edit != nil:
			fmt.Sscanf(e.Node, "url:%d", &i)
			if i < len(b.edit.URLTyped) {
				n := len(b.edit.URLTyped)
				b.edit.URLTyped[i] = e.Value
				if b.edit.slot(); len(b.edit.URLTyped) != n {
					app.Update(b.editScreen())
				}
			}
		case e.Screen == "edit" && e.Action == "noyear" && b.edit != nil:
			if e.Node == "anniversary" {
				b.edit.AnnivNoYear = !b.edit.AnnivNoYear
			} else {
				b.edit.BirthNoYear = !b.edit.BirthNoYear
			}
			app.Update(b.editScreen())
		case e.Screen == "edit" && strings.HasPrefix(e.Action, "mlabel:") && b.edit != nil:
			fmt.Sscanf(e.Node, "mail:%d", &i)
			if i < len(b.edit.EmailLabels) {
				b.edit.EmailLabels[i] = strings.TrimPrefix(e.Action, "mlabel:")
				app.Update(b.editScreen())
			}
		case e.Screen == "edit" && (e.Action == "adr_add" || e.Action == "adr_edit") && b.edit != nil:
			b.edit.Address = -1
			if _, err := fmt.Sscanf(e.Node, "adr:%d", &i); err == nil && e.Action == "adr_edit" && i < len(b.edit.Addresses) {
				b.edit.Address = i
			}
			b.edit.AddrLabel = ui.NumberHome
			if b.edit.Address >= 0 {
				b.edit.AddrLabel = firstNonEmpty(b.edit.Addresses[i].Label, ui.NumberOther)
			}
			app.Push(b.addressScreen())
		case e.Screen == "edit" && e.Action == "adr_delete" && b.edit != nil:
			if _, err := fmt.Sscanf(e.Node, "adr:%d", &i); err == nil && i < len(b.edit.Addresses) {
				b.edit.Addresses = append(b.edit.Addresses[:i], b.edit.Addresses[i+1:]...)
				app.Update(b.editScreen())
			}
		case e.Screen == "address" && strings.HasPrefix(e.Action, "alabel:") && b.edit != nil:
			b.edit.AddrLabel = strings.TrimPrefix(e.Action, "alabel:")
			s := b.addressScreen()
			for j := range s.Actions {
				s.Actions[j].Label = strings.TrimSuffix(s.Actions[j].Label, " ✓")
				if s.Actions[j].ID == e.Action {
					s.Actions[j].Label += " ✓"
				}
			}
			s.Body[0] = sdk.Styled(ui.StyleDim, tr.T("%s address (Options: its type)", placeName(b.edit.AddrLabel)))
			app.Update(s)
		case e.Screen == "address" && e.Action == "adr_save" && b.edit != nil:
			if b.saveAddress(e.Fields, b.edit.AddrLabel) {
				app.Pop()
				app.Update(b.editScreen())
			}
		case e.Screen == "edit" && e.Action == "photo_pick" && b.edit != nil:
			b.pickPhoto(func(photo string) {
				if b.edit != nil {
					b.edit.Photo = photo
					app.Update(b.editScreen())
				}
			})
		case e.Screen == "edit" && e.Action == "photo_clear" && b.edit != nil:
			b.edit.Photo = ""
			app.Update(b.editScreen())
		case e.Screen == "edit" && strings.HasPrefix(e.Action, "book:") && b.edit != nil:
			b.edit.Book = strings.TrimPrefix(e.Action, "book:")
			app.Update(b.editScreen())
		case e.Screen == "edit" && e.Action == "book_next" && b.edit != nil:
			choices := b.bookChoices()
			b.edit.Book = choices[(slices.Index(choices, b.newBook(b.edit.Book))+1)%len(choices)]
			app.Update(b.editScreen())
		case e.Screen == "edit" && e.Action == "save" && b.edit != nil:
			b.save(e.Fields)
		case e.Action == "new":
			b.openEdit(ui.Contact{}, "")
		case e.Action == "import":
			var res ui.ContactsFile
			app.Go(ui.CapContactsImport, nil, &res, func(err error) {
				if err != nil && !ui.Cancelled(err) {
					app.Toast(err.Error())
				}
				b.load()
				b.refresh()
			})
		case e.Action == "export":
			app.Go(ui.CapContactsExport, nil, nil, func(err error) {
				if err != nil && !ui.Cancelled(err) {
					app.Toast(err.Error())
				}
			})
		case e.Action == "groups":
			b.groupsOpen = true
			app.Push(b.groupsScreen())
		case e.Action == "group-new":
			b.naming = ""
			app.Push(b.groupNameScreen(""))
		case e.Action == "group-rename":
			if g, ok := b.findGroup(strings.TrimPrefix(e.Node, "g:")); ok {
				b.naming = g.ID
				app.Push(b.groupNameScreen(g.Name))
			}
		case e.Action == "group-save":
			args := ui.ContactGroupArgs{Op: ui.ContactGroupCreate, Name: strings.TrimSpace(e.Fields["name"])}
			if b.naming != "" {
				args = ui.ContactGroupArgs{Op: ui.ContactGroupRename, ID: b.naming, Name: args.Name}
			}
			if b.changeGroup(args) {
				app.Pop()
			}
		case e.Action == "group-delete":
			b.changeGroup(ui.ContactGroupArgs{Op: ui.ContactGroupDelete, ID: strings.TrimPrefix(e.Node, "g:")})
		case e.Action == "group-open":
			if g, ok := b.findGroup(strings.TrimPrefix(e.Node, "g:")); ok {
				b.group = g.ID
				app.Push(b.groupScreen(g))
			}
		case e.Action == "group-add":
			if g, ok := b.findGroup(b.group); ok {
				b.addQuery = ""
				app.Push(b.groupAddScreen(g))
			}
		case e.Screen == "group-add" && e.Node == "q" && e.Action == "changed":
			b.addQuery = e.Value
			if g, ok := b.findGroup(b.group); ok {
				app.Update(b.groupAddScreen(g))
			}
		case e.Action == "group-in" || e.Action == "group-out":
			op := ui.ContactGroupAdd
			if e.Action == "group-out" {
				op = ui.ContactGroupRemove
			}
			if b.changeGroup(ui.ContactGroupArgs{Op: op, ID: b.group, Contacts: []string{id}}) && op == ui.ContactGroupAdd {
				app.Toast(tr.T("Added “%s”", c.Name))
			}
		case e.Action == "dups":
			b.dupsOpen = true
			app.Push(b.dupsScreen())
		case e.Action == "merge":
			var got ui.Contact
			ids := strings.Split(strings.TrimPrefix(e.Node, "d:"), ",")
			if err := app.Call(ui.CapContactsMerge, ui.ContactMergeArgs{IDs: ids}, &got); err != nil {
				app.Toast(tr.T("Not merged: %v", err))
				return
			}
			app.Toast(tr.T("Merged into “%s”", got.Name))
			b.load()
			b.refresh()
		case e.Screen == "move" && e.Action == "move_to":
			c, ok := b.find(b.open)
			to := strings.TrimPrefix(e.Node, "to:")
			if !ok {
				return
			}
			c.Book = to
			if err := app.Call(ui.CapContactsSave, c, nil); err != nil {
				app.Toast(tr.T("Not moved: %v", err))
				return
			}
			app.Toast(tr.T("Moved to %s", b.bookName(to)))
			app.Pop()
			b.load()
			b.refresh()
		case !known:
		case e.Action == "move":
			app.Push(b.moveScreen(c))
		case e.Action == ui.ActionOpen:
			b.open = c.ID
			app.Push(b.viewScreen(c))
		case e.Action == ui.ActionCall && number != "":
			b.call(number)
		case e.Action == "sms" && number != "":
			b.message(c, number)
		case e.Action == "copy" && number != "":
			if err := app.Call(ui.CapClipboardSet, ui.ClipArgs{Text: number}, nil); err == nil {
				app.Toast(tr.T("Copied %s", number))
			}
		case e.Action == "copy_text":
			text := ""
			if _, err := fmt.Sscanf(e.Node, "e:%d", &i); err == nil && i < len(c.Emails) {
				text = c.Emails[i].Address
			} else if _, err := fmt.Sscanf(e.Node, "a:%d", &i); err == nil && i < len(c.Addresses) {
				text = addressLine(c.Addresses[i])
			} else if _, err := fmt.Sscanf(e.Node, "u:%d", &i); err == nil && i < len(c.URLs) {
				text = c.URLs[i]
			}
			if text != "" {
				if err := app.Call(ui.CapClipboardSet, ui.ClipArgs{Text: text}, nil); err == nil {
					app.Toast(tr.T("Copied %s", text))
				}
			}
		case e.Action == "photo_set" || e.Action == "photo_remove":
			set := func(photo string) {
				c, ok := b.find(c.ID)
				if !ok {
					return
				}
				c.Photo = photo
				if err := app.Call(ui.CapContactsSave, c, nil); err != nil {
					app.Toast(tr.T("Not saved: %v", err))
					return
				}
				if photo == "" {
					app.Toast(tr.T("Photo removed"))
				} else {
					app.Toast(tr.T("Photo saved"))
				}
				b.load()
				b.refresh()
			}
			if e.Action == "photo_remove" {
				set("")
			} else {
				b.pickPhoto(set)
			}
		case e.Action == "edit":
			b.openEdit(c, "")
		case e.Action == "delete":
			if b.remove(c) {
				if e.Screen == "view" {
					app.Pop()
				}
				b.refresh()
			}
		}
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
