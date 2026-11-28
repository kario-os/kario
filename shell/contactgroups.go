package shell

import (
	"errors"
	"slices"
	"strings"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

// Contact groups, merging duplicates and the system's contact picker
// (19-core-apps.md, 12-super-app-capabilities.md §5). Groups and merges
// change the contacts (the accounts sync both): the system's Contacts
// app, where the user edits, writes at once; another app (contacts
// permission) gets the user's yes in a system dialog that says what
// changes. The picker needs no permission: the app gets only the number
// the user chose.

func (c *Core) changeGroup(a *App, origin string, args ui.ContactGroupArgs, reply func(any, error)) {
	book := c.contactBook()
	app := c.fileOrigin(a, origin)
	var g ui.ContactGroup
	if args.Op != ui.ContactGroupCreate {
		groups := book.Groups()
		i := slices.IndexFunc(groups, func(x ui.ContactGroup) bool { return x.ID == args.ID })
		if i < 0 {
			reply(nil, errors.New(tr.T("No such group")))
			return
		}
		g = groups[i]
	}
	name := strings.TrimSpace(args.Name)
	if (args.Op == ui.ContactGroupCreate || args.Op == ui.ContactGroupRename) && name == "" {
		reply(nil, contactsErr(contacts.ErrGroupName))
		return
	}
	var do func() (any, error)
	var text string
	switch args.Op {
	case ui.ContactGroupCreate:
		do = func() (any, error) { return book.AddGroup(name) }
		text = tr.T("“%s” asks to make the contact group “%s”", app, name)
	case ui.ContactGroupRename:
		do = func() (any, error) { return book.RenameGroup(g.ID, name) }
		text = tr.T("“%s” asks to rename the contact group “%s” to “%s”", app, g.Name, name)
	case ui.ContactGroupDelete:
		do = func() (any, error) { return ui.ContactGroup{ID: g.ID, Members: []string{}}, book.DeleteGroup(g.ID) }
		text = tr.T("“%s” asks to delete the contact group “%s” (its contacts stay)", app, g.Name)
	case ui.ContactGroupAdd, ui.ContactGroupRemove:
		var names []string
		for _, id := range args.Contacts {
			if x, ok := book.Get(id); ok {
				if c.readOnlyBook(x.Book) {
					reply(nil, readOnlyErr(x.Name))
					return
				}
				names = append(names, x.Name)
			}
		}
		if len(names) == 0 {
			reply(nil, contactsErr(contacts.ErrNotFound))
			return
		}
		in := args.Op == ui.ContactGroupAdd
		do = func() (any, error) { return book.SetMembers(g.ID, args.Contacts, in) }
		text = tr.T("“%s” asks to take out of the contact group “%s”:\n%s", app, g.Name, nameList(names))
		if in {
			text = tr.T("“%s” asks to add to the contact group “%s”:\n%s", app, g.Name, nameList(names))
		}
	default:
		reply(nil, errors.New("unknown op"))
		return
	}
	run := func() {
		res, err := do()
		if err != nil {
			reply(nil, contactsErr(err))
			return
		}
		c.log.Printf("%s: contact group %s %s", a.M.ID, args.Op, firstNonEmpty(g.ID, name))
		c.contactsChanged()
		reply(res, nil)
	}
	if contactsManager(a, origin) {
		run()
		return
	}
	c.pushOverlay(&confirmOverlay{text: text, onYes: run, onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// nameList is a few names in one line, and how many more.
func nameList(names []string) string {
	if len(names) > 3 {
		names = append(names[:3:3], tr.T("and %d more", len(names)-3))
	}
	return strings.Join(names, tr.T(", "))
}

func (c *Core) mergeContacts(a *App, origin string, ids []string, reply func(any, error)) {
	book := c.contactBook()
	var lines []string
	seen := map[string]bool{}
	for _, id := range ids {
		if x, ok := book.Get(id); ok && !seen[id] {
			if c.readOnlyBook(x.Book) {
				reply(nil, readOnlyErr(x.Name))
				return
			}
			seen[id] = true
			lines = append(lines, strings.TrimSpace(x.Name+" "+x.Number))
		}
	}
	if len(lines) < 2 {
		reply(nil, contactsErr(contacts.ErrMergeTwo))
		return
	}
	run := func() {
		saved, err := book.Merge(ids)
		if err != nil {
			reply(nil, contactsErr(err))
			return
		}
		c.log.Printf("%s: merged %d contacts into %s", a.M.ID, len(lines), saved.ID)
		c.contactsChanged()
		reply(saved, nil)
	}
	if contactsManager(a, origin) {
		run()
		return
	}
	c.pushOverlay(&confirmOverlay{text: tr.T("“%s” asks to merge %d contacts into one:\n%s", c.fileOrigin(a, origin), len(lines), strings.Join(lines, "\n")),
		yes: tr.T("Merge"), onYes: run, onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// pickContact shows the contacts' numbers to choose from, searched by
// name, pinyin (whole or initials) or number as the user types; the app
// gets the name and the one number chosen.
func (c *Core) pickContact(a *App, origin string, reply func(any, error)) {
	var es []menuEntry
	for _, x := range c.contacts() {
		syl := strings.Fields(x.Pinyin)
		initials := ""
		for _, s := range syl {
			initials += s[:1]
		}
		for _, n := range ui.NumbersOf(x) {
			name, n := x.Name, n
			es = append(es, menuEntry{group: tr.T("Contacts"), label: name + " · " + numberLabel(n.Label) + " " + n.Number,
				find: strings.Join([]string{name, x.Pinyin, strings.Join(syl, ""), initials, n.Number}, " "), run: func() {
					c.log.Printf("%s: a contact picked", a.M.ID)
					reply(ui.Contact{Name: name, Number: n.Number, Numbers: []ui.ContactNumber{n}}, nil)
				}})
		}
	}
	if len(es) == 0 {
		reply(nil, errors.New(tr.T("There are no contacts with a number")))
		return
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Choose a contact · for “%s”", c.fileOrigin(a, origin)), entries: es, palette: true,
		onCancel: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}
