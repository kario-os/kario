package shell

import (
	"encoding/json"
	"errors"

	"github.com/kario-os/kario/phonenum"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The block list is one for calls and messages (Android's
// BlockedNumberProvider): phone-smsd keeps it, files a blocked sender's
// messages in the blocked folder, and the shell holds a copy to decline
// blocked calls before they ring (logged as blocked, no missed-call
// notification). The default SMS app, the system's Phone and Settings
// change it (block.list, block.set); smsd hears the region numbers are read
// in and the contacts' numbers (for "not in the contacts") from the shell.
// Minor mode (minor.go) blocks the same way every number it does not allow.

const appSettings = "com.phone.settings"

// blockManager: the app may read and change the block list.
func (c *Core) blockManager(a *App) bool {
	return c.defaultSMSApp(a) || ((a.M.ID == appDialer || a.M.ID == appSettings) && !a.M.installed)
}

// smsBlocksRefresh fetches the block list from smsd (on connecting, and
// after smsd said something changed).
func (c *Core) smsBlocksRefresh() {
	go func() {
		var bl ui.BlockList
		if err := c.smsRPC(svc.SMSBlockList, nil, &bl); err == nil {
			c.Post(func() { c.sms.blocks = bl })
		}
	}()
}

// smsPeers tells smsd the region, the contacts' numbers and minor mode's.
func (c *Core) smsPeers() {
	region := c.region()
	var numbers []string
	for _, x := range c.contacts() {
		for _, n := range ui.NumbersOf(x) {
			numbers = append(numbers, n.Number)
		}
	}
	go func() {
		_ = c.smsRPC(svc.SMSRegion, map[string]string{"region": region}, nil)
		_ = c.smsRPC(svc.SMSKnown, map[string][]string{"numbers": numbers}, nil)
	}()
	c.smsOnly()
}

// numbersRegion puts the region in force for reading numbers: the shell's
// own comparisons, the contacts' index and smsd's.
func (c *Core) numbersRegion() {
	r := c.region()
	if phonenum.Default() == r && c.sms.region == r {
		return
	}
	phonenum.SetDefault(r)
	c.sms.region = r
	if c.addressBook != nil {
		c.addressBook.Reindex()
	}
	if c.sms.up {
		c.smsPeers()
	}
}

// callBlocked is why an incoming call is declined, "" when it rings.
func (c *Core) callBlocked(number string) string {
	if c.emergencyNumber(number) {
		return ""
	}
	if why := c.sms.blocks.Blocked(number, c.region(), func(n string) bool { return c.contactName(n) != "" }); why != "" {
		return why
	}
	if c.minorRefuses(number) {
		return ui.BlockedMinor
	}
	return ""
}

// blockCall declines a ringing call from a blocked number without a sound
// or a screen (telephonyd logs it as blocked); true for a call it keeps
// from the rest of the shell, until it has ended.
func (c *Core) blockCall(call ui.TelCall) bool {
	if c.sms.blockedCalls[call.ID] {
		if call.State == "ended" {
			delete(c.sms.blockedCalls, call.ID)
			if d := c.appByID(appDialer); d != nil && d.running {
				raw, _ := json.Marshal(call)
				d.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventTelCall, Value: string(raw)})
			}
		}
		return true
	}
	if call.Dir != "in" || call.State != "incoming" || c.cells[call.ID].ID != "" {
		return false
	}
	why := c.callBlocked(call.Number)
	if why == "" {
		return false
	}
	if c.sms.blockedCalls == nil {
		c.sms.blockedCalls = map[string]bool{}
	}
	c.sms.blockedCalls[call.ID] = true
	c.log.Printf("call %s from a blocked number declined (%s)", call.ID, why)
	id := call.ID
	go func() { _ = c.telRPC(svc.TelHangup, map[string]any{"call_id": id, "blocked": true}, nil) }()
	return true
}

func (b *broker) dispatchBlock(a *App, m ui.Msg, reply func(any, error)) {
	c := b.c
	if !c.blockManager(a) {
		reply(nil, errors.New(tr.T("Only the default SMS app, Phone and Settings can do this")))
		return
	}
	if m.Cap == ui.CapBlockList {
		var bl ui.BlockList
		b.smsCall(svc.SMSBlockList, nil, &bl, func(err error) {
			if err == nil {
				c.sms.blocks = bl
			}
			reply(bl, err)
		})
		return
	}
	var args ui.BlockSetArgs
	_ = json.Unmarshal(m.Args, &args)
	var bl ui.BlockList
	b.smsCall(svc.SMSBlockSet, args, &bl, func(err error) {
		if err == nil {
			c.sms.blocks = bl
		}
		reply(bl, err)
	})
}
