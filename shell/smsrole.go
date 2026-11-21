package shell

import (
	"errors"
	"sort"

	"github.com/kario-os/kario/ui"
)

// The default SMS app is a role, as on Android (RoleManager's
// ROLE_SMS): one app holds "sms", the system's Messages unless the user
// gives it to another app with the sms permission in Settings › Apps ›
// Default apps. The holder deletes messages, keeps the block list, sends
// without a confirmation per message, posts the notifications of new
// messages and gets the pictures of received multimedia messages; every
// other app with the sms permission reads, and sends after the user
// confirmed each message. An app that goes away gives the role back.

// smsApp is the app holding the sms role.
func (c *Core) smsApp() *App {
	if id := c.settings.SMSApp; id != "" {
		if a := c.appByID(id); a != nil && a.M.Has(ui.PermSMS) {
			return a
		}
	}
	if a := c.appByID(appMessages); a != nil && !a.M.installed {
		return a
	}
	return nil
}

// defaultSMSApp: the app holds the sms role (Android's default SMS app).
func (c *Core) defaultSMSApp(a *App) bool {
	if r := c.smsApp(); r != nil {
		return a == r
	}
	return a.M.ID == appMessages && !a.M.installed
}

// smsCandidates are the apps that may hold the role: the system's Messages
// first, then the others with the sms permission, by name.
func (c *Core) smsCandidates() []*App {
	var out []*App
	for _, a := range c.apps {
		if a.M.Has(ui.PermSMS) && !(a.M.ID == appMessages && a.M.installed) {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].M.ID == appMessages) != (out[j].M.ID == appMessages) {
			return out[i].M.ID == appMessages
		}
		return out[i].M.Name < out[j].M.Name
	})
	return out
}

// rolesView is settings.get's "roles": the sms role's holder and choices.
func (c *Core) rolesView() map[string]any {
	holder := ""
	if a := c.smsApp(); a != nil {
		holder = a.M.ID
	}
	var choices []map[string]any
	for _, a := range c.smsCandidates() {
		choices = append(choices, map[string]any{"id": a.M.ID, "name": a.M.Name, "system": !a.M.installed})
	}
	return map[string]any{"sms": map[string]any{"app": holder, "choices": choices}}
}

// setSMSApp is settings.set sms_app: an app outside the system image gets
// the role once the user agreed in a system dialog. done gets the outcome.
func (c *Core) setSMSApp(id string, done func(error)) {
	if id == "" {
		id = appMessages
	}
	a := c.appByID(id)
	if a == nil || !a.M.Has(ui.PermSMS) || (a.M.ID == appMessages && a.M.installed) {
		done(errors.New(tr.T("This app cannot be the default SMS app")))
		return
	}
	set := func() {
		c.settings.SMSApp = ""
		if a.M.ID != appMessages {
			c.settings.SMSApp = a.M.ID
		}
		c.saveSettings()
		c.log.Printf("default SMS app: %s", a.M.ID)
		done(nil)
	}
	if !a.M.installed {
		set()
		return
	}
	c.pushOverlay(&confirmOverlay{text: tr.T("Make “%s” the default SMS app?\nIt can then read, send, delete and block messages without asking, and posts the notifications of new ones.", a.M.Name),
		onYes: set, onNo: func() { done(errors.New(ui.ErrCancelled)) }, trust: true})
}

// smsAppRemoved gives the role back to Messages when its holder is
// uninstalled: an app installed later under the same ID asks again.
func (c *Core) smsAppRemoved(id string) {
	if c.settings.SMSApp == id {
		c.settings.SMSApp = ""
		c.saveSettings()
		c.log.Printf("default SMS app: %s uninstalled, %s again", id, appMessages)
	}
}

// messagingSetting is settings.set for the default SMS app; false for the
// other keys.
func (c *Core) messagingSetting(args ui.SettingArgs, reply func(error)) bool {
	if args.Key != ui.SettingSMSApp {
		return false
	}
	c.setSMSApp(args.Text, reply)
	return true
}
