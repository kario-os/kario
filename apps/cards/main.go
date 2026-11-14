// Cards, the card pack: a system app listing every card the phone can
// answer a reader as, the door cards the system keeps and the cards apps
// declare, in the order the phone tries them. It adds a door card by
// reading the physical card (reader mode), renames, deletes and orders
// cards, sets the express transit card and the default payment app, says
// per card whether it answers with the phone locked and the screen off,
// presents a card for a minute and shows when each door card is usually
// tapped, all through nfc.state and nfc.set (the system permission);
// phone-nfcd presents the cards (30-nfc.md).
package main

import (
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

type cards struct {
	a      *sdk.App
	st     ui.NFCState
	loaded bool
	// reading counts reads begun; a read whose page was left is ignored.
	reading int
	// renaming is the card the name page renames; "" adds the card just
	// read.
	renaming string
}

// clock is minutes after midnight as the time of day.
func clock(m int) string { return fmt.Sprintf("%d:%02d", m/60, m%60) }

// hoursText is when a door card is usually tapped: "workdays 8:00–10:00,
// 12:00–14:00 · days off 10:00–12:00" (weekends, and the holidays of the
// region's calendar).
func hoursText(hs []ui.NFCHours) string {
	var parts []string
	for _, dayOff := range []bool{false, true} {
		var spans []string
		for _, h := range hs {
			if h.Weekend == dayOff {
				spans = append(spans, clock(h.From)+"–"+clock(h.To))
			}
		}
		switch {
		case len(spans) == 0:
		case dayOff:
			parts = append(parts, tr.T("days off %s", strings.Join(spans, tr.T(", "))))
		default:
			parts = append(parts, tr.T("workdays %s", strings.Join(spans, tr.T(", "))))
		}
	}
	return strings.Join(parts, " · ")
}

// cardLine is what a card's row says under its name: what it is, where
// that is not the default whether it answers locked with the screen off,
// and for a door card when it is usually tapped.
func cardLine(c ui.NFCCardInfo) string {
	switch c.Kind {
	case ui.NFCDoor:
		what := tr.T("Door card")
		if !c.Locked {
			what = tr.T("Door card · unlocked only")
		}
		if h := hoursText(c.Usual); h != "" {
			return what + " · " + h
		}
		return what + " · " + tr.T("UID …%s", c.UID)
	case ui.NFCPayment:
		return tr.T("Payment card · %s", c.AppName)
	}
	if c.Locked {
		return tr.T("%s · works with the screen off", c.AppName)
	}
	return c.AppName
}

func (p *cards) mark(c ui.NFCCardInfo) string {
	switch {
	case c.ID == p.st.Express:
		return tr.T("Express")
	case c.Kind == ui.NFCPayment && c.App == p.st.DefaultPayment:
		return tr.T("Default")
	}
	return ""
}

func (p *cards) name(id string) string {
	for _, c := range p.st.Cards {
		if c.ID == id {
			return c.Name
		}
	}
	return tr.T("None")
}

func (p *cards) payName() string {
	for _, a := range p.st.PaymentApps {
		if a.ID == p.st.DefaultPayment {
			return a.Name
		}
	}
	return tr.T("None")
}

func onOff(on bool) string {
	if on {
		return tr.T("On")
	}
	return tr.T("Off")
}

func (p *cards) home() ui.Screen {
	st := p.st
	s := ui.Screen{ID: "cards", Title: tr.T("Cards")}
	if !p.loaded {
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("This phone has no NFC"))}
		return s
	}
	s.Actions = []ui.Action{{ID: "add", Label: tr.T("Add door card"), Key: "a"}}
	if !st.Enabled {
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("NFC is off · Settings › NFC")))
	}
	for i, c := range st.Cards {
		n := ui.Node{Kind: ui.KindItem, ID: "card:" + c.ID, Title: fmt.Sprintf("%d. %s", i+1, c.Name), Subtitle: cardLine(c),
			Trailing: p.mark(c), Actions: []ui.Action{{ID: "present", Label: tr.T("Present"), Primary: true}}}
		switch {
		case c.CanExpress && c.ID == st.Express:
			n.Actions = append(n.Actions, ui.Action{ID: "express", Label: tr.T("Stop using as the express transit card"), Key: "e"})
		case c.CanExpress:
			n.Actions = append(n.Actions, ui.Action{ID: "express", Label: tr.T("Use as the express transit card"), Key: "e"})
		case c.Kind == ui.NFCDoor:
			n.Actions = append(n.Actions, ui.Action{ID: "rename", Label: tr.T("Rename"), Key: "r"},
				ui.Action{ID: "delete", Label: tr.T("Delete"), Key: "d", Group: ui.GroupDanger,
					Confirm: tr.T("Delete the door card “%s”?", c.Name)})
		}
		switch {
		case c.LockedFixed:
		case c.Locked:
			n.Actions = append(n.Actions, ui.Action{ID: "locked", Label: tr.T("Only with the phone unlocked"), Key: "l"})
		default:
			n.Actions = append(n.Actions, ui.Action{ID: "locked", Label: tr.T("Also with the screen off and the phone locked"), Key: "l"})
		}
		n.Actions = append(n.Actions, ui.Action{ID: "up", Label: tr.T("Move up")}, ui.Action{ID: "down", Label: tr.T("Move down")})
		s.Body = append(s.Body, n)
	}
	if len(st.Cards) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No cards yet. Options › Add door card copies a door card")))
	}
	s.Body = append(s.Body,
		sdk.Section(tr.T("Payment and transit")),
		ui.Node{Kind: ui.KindItem, ID: "pay", Title: tr.T("Default payment app"), Trailing: p.payName(),
			Actions: []ui.Action{sdk.Open(tr.T("Choose"))}},
		ui.Node{Kind: ui.KindItem, ID: "fg", Title: tr.T("Pay with the open app"), Subtitle: tr.T("A payment app in front pays instead of the default"),
			Trailing: onOff(st.Foreground), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
		ui.Node{Kind: ui.KindItem, ID: "express", Title: tr.T("Express transit card"), Trailing: p.name(st.Express),
			Subtitle: tr.T("Works with the screen off and the phone locked"), Actions: []ui.Action{sdk.Open(tr.T("Choose"))}},
		sdk.Styled(ui.StyleDim, tr.T("Door cards and the express transit card work with the screen off and the phone locked, no key needed; a door gets the door card used most around this time first, else the first in this list")),
		sdk.Styled(ui.StyleDim, tr.T("OK presents a card for a minute. Anywhere, locked too, press the power key twice to choose one (without a power key: the end key twice on standby)")))
	if st.Simulated {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Simulated NFC: this machine has no NFC chip")))
	}
	return s
}

func (p *cards) payScreen() ui.Screen {
	var items []ui.Node
	for _, a := range p.st.PaymentApps {
		n := ui.Node{Kind: ui.KindItem, ID: "pay:" + a.ID, Title: a.Name, Actions: []ui.Action{{ID: "choose", Label: tr.T("Choose"), Primary: true}}}
		if a.ID == p.st.DefaultPayment {
			n.Trailing = "✓"
		}
		items = append(items, n)
	}
	if len(items) == 0 {
		items = append(items, sdk.Styled(ui.StyleDim, tr.T("No app has a payment card")))
	}
	return ui.Screen{ID: "pay", Title: tr.T("Default payment app"), Body: items}
}

func (p *cards) expressScreen() ui.Screen {
	choice := func(id, name string) ui.Node {
		n := ui.Node{Kind: ui.KindItem, ID: "x:" + id, Title: name, Actions: []ui.Action{{ID: "choose", Label: tr.T("Choose"), Primary: true}}}
		if id == p.st.Express {
			n.Trailing = "✓"
		}
		return n
	}
	items := []ui.Node{choice("", tr.T("None"))}
	for _, c := range p.st.Cards {
		if c.CanExpress {
			items = append(items, choice(c.ID, c.Name))
		}
	}
	return ui.Screen{ID: "express", Title: tr.T("Express transit card"), Body: items}
}

func nameScreen(title, value string) ui.Screen {
	return ui.Screen{ID: "name", Title: title, Focus: "name", Right: "cancel",
		Actions: []ui.Action{{ID: "save", Label: tr.T("Save")}, {ID: "cancel", Label: tr.T("Cancel")}},
		Body: []ui.Node{
			sdk.Styled(ui.StyleDim, tr.T("A name for the card, such as the door it opens")),
			{Kind: ui.KindInput, ID: "name", Value: value, Submit: "save"},
		}}
}

func (p *cards) show() {
	p.a.Update(p.home())
	p.a.Update(p.payScreen())
	p.a.Update(p.expressScreen())
}

func (p *cards) load(then func()) {
	var st ui.NFCState
	p.a.Go(ui.CapNFCState, nil, &st, func(err error) {
		if err == nil {
			p.st = st
		}
		p.loaded = err == nil
		then()
	})
}

// set changes something and shows the answer; then runs on success.
func (p *cards) set(args ui.NFCSetArgs, then func()) {
	var st ui.NFCState
	p.a.Go(ui.CapNFCSet, args, &st, func(err error) {
		if err != nil {
			p.a.Toast(err.Error())
			return
		}
		st.Read = nil
		p.st, p.loaded = st, true
		if then != nil {
			then()
		}
		p.show()
	})
}

// stopRead leaves "Add door card": the system stops reading at once, and
// the read's answer is ignored.
func (p *cards) stopRead() {
	p.reading++
	p.a.Go(ui.CapNFCSet, ui.NFCSetArgs{Key: ui.NFCSetCancelRead}, nil, nil)
}

// read reads the card held to the phone; a door card that can be copied
// gets a name next.
func (p *cards) read() {
	p.reading++
	n := p.reading
	p.a.Push(ui.Screen{ID: "read", Title: tr.T("Add door card"), Right: "cancel",
		Actions: []ui.Action{{ID: "cancel", Label: tr.T("Cancel")}},
		Body: []ui.Node{
			sdk.Styled(ui.StyleBig, tr.T("Hold the card to the back of the phone")),
			sdk.Styled(ui.StyleDim, tr.T("Only unencrypted UID cards can be copied")),
		}})
	var st ui.NFCState
	p.a.Go(ui.CapNFCSet, ui.NFCSetArgs{Key: ui.NFCSetRead, Seconds: 20}, &st, func(err error) {
		if n != p.reading {
			return
		}
		if err != nil || st.Read == nil {
			p.a.Pop()
			p.a.Toast(tr.T("No card was found"))
			return
		}
		r := *st.Read
		st.Read = nil
		p.st = st
		switch r.Result {
		case ui.NFCReadOK:
			doors := 0
			for _, c := range st.Cards {
				if c.Kind == ui.NFCDoor {
					doors++
				}
			}
			p.renaming = ""
			p.a.Replace(nameScreen(tr.T("Name the door card"), tr.T("Door card %d", doors+1)))
			return
		case ui.NFCReadSmart:
			p.a.Toast(tr.T("A smart card cannot be copied: add it in its issuer's app"))
		case ui.NFCReadEncrypted:
			p.a.Toast(tr.T("This card is encrypted: copying it needs its keys, which the phone does not have"))
		case ui.NFCReadDuplicate:
			p.a.Toast(tr.T("This card is in the card pack already, as “%s”", r.Name))
		default:
			p.a.Toast(tr.T("No card was found"))
		}
		p.a.Pop()
	})
}

func (p *cards) save(name string) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > 24 {
		p.a.Toast(tr.T("A name of 1 to 24 characters"))
		return
	}
	args := ui.NFCSetArgs{Key: ui.NFCSetAddDoor, Name: name}
	if p.renaming != "" {
		args = ui.NFCSetArgs{Key: ui.NFCSetRename, Card: p.renaming, Name: name}
	}
	adding := p.renaming == ""
	p.set(args, func() {
		p.a.Pop()
		if adding {
			p.a.Toast(tr.T("Added “%s” to the card pack", name))
		}
	})
}

func (p *cards) action(e sdk.ActionEvent) {
	id, onCard := strings.CutPrefix(e.Node, "card:")
	switch {
	case e.Action == "add":
		p.read()
	case e.Screen == "read" && e.Action == "cancel":
		p.stopRead()
		p.a.Pop()
	case e.Screen == "name" && e.Action == "save":
		p.save(e.Fields["name"])
	case e.Screen == "name" && e.Action == "cancel":
		p.a.Pop()
	case e.Screen == "cards" && onCard && e.Action == "present":
		p.a.Go(ui.CapNFCSet, ui.NFCSetArgs{Key: ui.NFCSetPresent, Card: id}, nil, nil)
	case e.Screen == "cards" && onCard && e.Action == "rename":
		p.renaming = id
		p.a.Push(nameScreen(tr.T("Rename the door card"), p.name(id)))
	case e.Screen == "cards" && onCard && e.Action == "delete":
		p.set(ui.NFCSetArgs{Key: ui.NFCSetDelete, Card: id}, nil)
	case e.Screen == "cards" && onCard && e.Action == "express":
		if id == p.st.Express {
			id = ""
		}
		p.set(ui.NFCSetArgs{Key: ui.NFCSetExpress, Card: id}, nil)
	case e.Screen == "cards" && onCard && e.Action == "locked":
		for _, c := range p.st.Cards {
			if c.ID == id {
				p.set(ui.NFCSetArgs{Key: ui.NFCSetLocked, Card: id, On: !c.Locked}, nil)
			}
		}
	case e.Screen == "cards" && onCard && (e.Action == "up" || e.Action == "down"):
		p.set(ui.NFCSetArgs{Key: ui.NFCSetMove, Card: id, By: map[string]int{"up": -1, "down": 1}[e.Action]}, nil)
	case e.Screen == "cards" && e.Node == "pay":
		p.a.Push(p.payScreen())
	case e.Screen == "cards" && e.Node == "fg":
		p.set(ui.NFCSetArgs{Key: ui.NFCSetForeground, On: !p.st.Foreground}, nil)
	case e.Screen == "cards" && e.Node == "express":
		p.a.Push(p.expressScreen())
	case e.Screen == "pay" && strings.HasPrefix(e.Node, "pay:"):
		p.set(ui.NFCSetArgs{Key: ui.NFCSetPayment, App: strings.TrimPrefix(e.Node, "pay:")}, p.a.Pop)
	case e.Screen == "express" && strings.HasPrefix(e.Node, "x:"):
		p.set(ui.NFCSetArgs{Key: ui.NFCSetExpress, Card: strings.TrimPrefix(e.Node, "x:")}, p.a.Pop)
	}
}

func main() {
	a, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	p := &cards{a: a}
	a.OnStart = func(string) { p.load(func() { a.Reset(p.home()) }) }
	a.OnLifecycle = func(state string) {
		if state == ui.StateForeground {
			p.load(p.show)
		}
	}
	a.OnPopped = func(id string) {
		if id == "read" {
			p.stopRead()
		}
	}
	a.OnAction = p.action
	if err := a.Run(); err != nil {
		log.Print(err)
	}
}
