package shell

import (
	"encoding/json"
	"errors"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The SIM's access point names for Settings (apn.list, apn.set):
// telephonyd keeps the carriers' database and the user's changes and
// connects with the chosen APN (docs/04); the Shell passes the calls on.
// Passwords stay in telephonyd, and the log names operations and ids only.
func (b *broker) dispatchAPN(m ui.Msg, reply func(any, error)) {
	c := b.c
	if !c.HasTelephony() {
		reply(nil, errors.New(tr.T("No service")))
		return
	}
	var out ui.APNState
	if m.Cap == ui.CapAPNList {
		b.call(svc.Telephonyd, svc.TelAPNList, nil, &out, func(err error) { reply(out, err) })
		return
	}
	var args ui.APNArgs
	if err := json.Unmarshal(m.Args, &args); err != nil {
		reply(nil, err)
		return
	}
	id := args.ID
	if args.APN != nil {
		id = firstNonEmpty(args.APN.ID, "new")
	}
	b.call(svc.Telephonyd, svc.TelAPNSet, args, &out, func(err error) {
		if err == nil {
			c.log.Printf("apn: %s %s; %s in use", args.Op, id, firstNonEmpty(out.Selected, "the network's default"))
		}
		reply(out, err)
	})
}
