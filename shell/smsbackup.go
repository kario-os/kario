package shell

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/smsbackup"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Message backups (32-messaging.md §4.1) in the XML of Android's "SMS
// Backup & Restore", the file people bring from Android: sms.backup writes
// every message into one in Files once the user agreed, as
// contacts.export does the contacts; sms.restore lists the .xml files of
// Files, reads the one the user picks (smsbackup: untrusted input), says
// how many messages it holds and, once the user agreed, hands them to
// phone-smsd's import, which leaves out those it has. The default SMS app
// asks; it learns the counts, never the file. A vCard a multimedia message
// holds goes the way of a vCard in Files (sms.vcard).

// smsOwn is this phone's number, "" while unknown.
func (c *Core) smsOwn() string {
	if c.tel != nil {
		return c.tel.Own
	}
	return ""
}

func (c *Core) smsBackup(a *App, origin string, reply func(any, error)) {
	go func() {
		var in, blocked []ui.SMS
		err := c.smsRPC(svc.SMSList, ui.SMSListArgs{}, &in)
		if err == nil {
			err = c.smsRPC(svc.SMSList, ui.SMSListArgs{Folder: ui.SMSFolderBlocked}, &blocked)
		}
		c.Post(func() {
			if err != nil {
				reply(nil, err)
				return
			}
			msgs := append(in, blocked...)
			sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].At < msgs[j].At })
			var b bytes.Buffer
			n, err := smsbackup.Write(&b, msgs, c.contactName, c.smsOwn(), c.now())
			switch {
			case err != nil:
				reply(nil, err)
				return
			case n == 0:
				reply(nil, errors.New(tr.T("There are no messages to back up")))
				return
			}
			name := c.files.freeName("sms-" + c.now().Format("20060102150405") + ".xml")
			c.pushOverlay(&confirmOverlay{text: tr.T("Save all %d messages to Files as “%s”?\nFrom: %s\nAn app you give the file to can read them all", n, name, c.fileOrigin(a, origin)),
				yes: tr.T("Save"),
				onYes: func() {
					f, err := c.files.add(name, b.Bytes(), c.now())
					if err != nil {
						reply(nil, err)
						return
					}
					c.log.Printf("%s: backed up %d messages to file %s", a.M.ID, n, f.ID)
					c.showToast(tr.T("Saved to Files"))
					reply(ui.SMSFile{File: f.Name, Count: n}, nil)
				},
				onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
		})
	}()
}

func (c *Core) smsRestore(a *App, origin string, reply func(any, error)) {
	from := c.fileOrigin(a, origin)
	var es []menuEntry
	for i := len(c.files.Items) - 1; i >= 0; i-- {
		f := c.files.Items[i]
		if !strings.EqualFold(filepath.Ext(f.Name), ".xml") {
			continue
		}
		es = append(es, menuEntry{group: tr.T("Files"), label: f.Name + " · " + sizeLabel(f.Size), find: f.Name, run: func() { c.smsRestoreFile(f, from, reply) }})
	}
	if len(es) == 0 {
		reply(nil, errors.New(tr.T("There is no message backup (.xml) in Files")))
		return
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Restore messages · for “%s”", from), entries: es,
		onCancel: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// smsRestoreFile reads a backup off the loop (it may be large) and asks
// whether its messages go in.
func (c *Core) smsRestoreFile(f *fileItem, from string, reply func(any, error)) {
	path, name, own := c.files.path(f), f.Name, c.smsOwn()
	c.showToast(tr.T("Reading “%s”…", name))
	go func() {
		var msgs []ui.SMS
		var st smsbackup.Stats
		fh, err := os.Open(path)
		if err == nil {
			msgs, st, err = smsbackup.Parse(fh, own)
			fh.Close()
		}
		c.Post(func() {
			switch {
			case errors.Is(err, smsbackup.ErrNotBackup):
				reply(nil, errors.New(tr.T("“%s” is not a message backup", name)))
				return
			case err != nil:
				reply(nil, err)
				return
			case len(msgs) == 0:
				c.showToast(tr.T("“%s” holds no messages", name))
				reply(ui.SMSFile{File: name, Skipped: st.Skipped}, nil)
				return
			}
			c.log.Printf("restore: %s holds %d messages (%d left out, truncated %v)", name, len(msgs), st.Skipped, st.Truncated)
			c.pushOverlay(&confirmOverlay{text: tr.T("“%s” holds %d messages.\nFrom: %s\nRestore them to Messages? Those already there are left out.", name, len(msgs), from),
				yes:   tr.T("Restore"),
				onYes: func() { c.smsImportBackup(name, msgs, st, reply) },
				onNo:  func() { reply(nil, errors.New(ui.ErrCancelled)) }})
		})
	}()
}

// smsImportBackup hands the messages to phone-smsd in its import's
// batches.
func (c *Core) smsImportBackup(name string, msgs []ui.SMS, st smsbackup.Stats, reply func(any, error)) {
	c.showToast(tr.T("Restoring messages…"))
	go func() {
		added := 0
		var err error
		for i := 0; i < len(msgs) && err == nil; i += smsImportBatch {
			var res svc.SMSImportRes
			err = c.smsRPC(svc.SMSImport, svc.SMSImportArgs{Messages: msgs[i:min(i+smsImportBatch, len(msgs))]}, &res)
			for _, id := range res.IDs {
				if id != "" {
					added++
				}
			}
		}
		c.Post(func() {
			res := ui.SMSFile{File: name, Count: added, Skipped: len(msgs) - added + st.Skipped}
			c.log.Printf("restore: %d messages of %s added, %d left out (%v)", added, name, res.Skipped, err)
			if err != nil {
				reply(nil, err)
				return
			}
			if res.Skipped > 0 {
				c.showToast(tr.T("Restored %d messages; %d were on the phone already or unreadable", added, res.Skipped))
			} else {
				c.showToast(tr.T("Restored %d messages", added))
			}
			reply(res, nil)
		})
	}()
}

// smsImportBatch: messages a phone-smsd import takes.
const smsImportBatch = 500

// smsVCard offers the contacts of a vCard part of a multimedia message.
func (c *Core) smsVCard(a *App, origin string, args ui.SMSPartArgs, reply func(any, error)) {
	from := c.fileOrigin(a, origin)
	go func() {
		var part svc.SMSPartRes
		err := c.smsRPC(svc.SMSPart, svc.SMSPartArgs{ID: args.ID, Index: args.Index}, &part)
		c.Post(func() {
			if err == nil && !contacts.IsVCard(part.Name, part.Type) {
				err = contacts.ErrNotVCard
			}
			var list []ui.Contact
			bad := 0
			if err == nil {
				list, bad, err = contacts.Parse(part.Data)
			}
			if err != nil {
				reply(nil, contactsErr(err))
				return
			}
			c.offerContacts(list, bad, firstNonEmpty(part.Name, tr.T("Contact card")), from, func(res ui.ContactsFile, err error) { reply(res, err) })
		})
	}()
}
