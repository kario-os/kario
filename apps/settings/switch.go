package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › My device › Move from an old phone (switch.state,
// switch.start, switch.stop, switch.undo): the kinds to take, all ticked
// at first; then the pairing code, which the system draws on a secure
// surface, the way the old phone connects (the Wi-Fi network Kario is on,
// with the hotspot to switch to, or Kario's hotspot) and each kind's
// progress while the old phone sends; then what came in, which one action
// takes out again. Over Bluetooth instead (a page of the paired devices,
// bt.state), nothing goes on the old phone: the system reads its contacts,
// calls and messages once its user allows it there. The page asks the
// system for the state every second while it is open.

// swState is the system's answer; off: the kinds the user unticked; shown:
// the state the page shows.
var swState struct {
	ui.SwitchState
	off     map[string]bool
	polling bool
	shown   string
}

func kindLabel(k string) string {
	switch k {
	case ui.SwitchContacts:
		return tr.T("Contacts")
	case ui.SwitchCallLog:
		return tr.T("Call log")
	case ui.SwitchSMS:
		return tr.T("Messages")
	case ui.SwitchCalendar:
		return tr.T("Calendar")
	case ui.SwitchPhotos:
		return tr.T("Photos")
	case ui.SwitchVideos:
		return tr.T("Videos")
	}
	return k
}

// switchItem is the page's row in My device.
func (p *app) switchItem() ui.Node {
	return ui.Node{Kind: ui.KindItem, ID: "switch", Title: tr.T("Move from an old phone"),
		Subtitle: tr.T("Contacts, messages, calls and photos from an Android phone or an iPhone"), Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func (p *app) switchScreen() ui.Screen {
	st := swState.SwitchState
	s := ui.Screen{ID: "switch", Title: tr.T("Move from an old phone")}
	stop := ui.Action{ID: "sw_stop", Label: tr.T("Stop"), Key: "x", Confirm: tr.T("Stop receiving? What came in so far stays.")}
	hotspot := ui.Action{ID: "sw_hotspot", Label: tr.T("Use the hotspot instead"), Key: "h"}
	if st.Bluetooth != "" && st.State != ui.SwitchIdle {
		return p.switchBTScreen(s, stop)
	}
	switch st.State {
	case ui.SwitchStarting:
		s.Actions = []ui.Action{stop}
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("Getting ready to receive…"))}
		return s
	case ui.SwitchWaiting:
		// The code right under the line saying what to do, so the page
		// opens on it; the network is only needed when joining fails.
		s.Actions = []ui.Action{stop}
		s.Body = []ui.Node{sdk.Styled(ui.StyleAccent, tr.T("Scan this code with “Kario Switch” on the old phone")),
			ui.Node{Kind: ui.KindImage, Src: "qr:" + st.Code, Secure: true},
			sdk.Styled(ui.StyleDim, tr.T("Waiting for the old phone…"))}
		if st.Network != "" {
			s.Body = append(s.Body,
				ui.Node{Kind: ui.KindItem, ID: "sw_via", Title: tr.T("Receiving over Kario's hotspot"), Subtitle: tr.T("The old phone joins it with the name and password below")},
				ui.Node{Kind: ui.KindItem, ID: "sw_net", Title: tr.T("Network name"), Trailing: st.Network},
				ui.Node{Kind: ui.KindItem, ID: "sw_pass", Title: tr.T("Password"), Trailing: st.Passphrase},
				sdk.Styled(ui.StyleDim, tr.T("If the old phone does not join this network by itself, join it there by hand")))
		} else {
			// A page action, not a row: a row would take the focus and
			// scroll the code off the top.
			s.Actions = append(s.Actions, hotspot)
			s.Body = append(s.Body,
				ui.Node{Kind: ui.KindItem, ID: "sw_via", Title: tr.T("Receiving over Wi-Fi"), Trailing: st.Wifi, Subtitle: tr.T("The old phone has to be on the same Wi-Fi network")},
				sdk.Styled(ui.StyleDim, tr.T("If the old phone cannot connect: public, hotel and office Wi-Fi often keep phones from reaching each other. Use the hotspot instead; the old phone then joins Kario's own network.")))
		}
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("Secure surface: the system hides it from screenshots, recordings and remote casting")))
		return s
	case ui.SwitchReceiving:
		s.Actions = []ui.Action{stop}
		s.Body = []ui.Node{sdk.Styled(ui.StyleAccent, tr.T("Receiving from %s", firstOf(st.Device, tr.T("the old phone"))))}
		for _, k := range st.Progress {
			n := ui.Node{Kind: ui.KindItem, ID: "sw_got:" + k.Kind, Title: kindLabel(k.Kind), Trailing: fmt.Sprint(k.Stored)}
			if k.Expected > 0 {
				n.Trailing = tr.T("%d of %d", k.Stored+k.Skipped+k.Failed, k.Expected)
			}
			n.Subtitle = switchCounts(k.Skipped, k.Failed)
			s.Body = append(s.Body, n)
		}
		if st.Network == "" && st.Wifi != "" {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Keep both phones on the Wi-Fi “%s” with their screens on", st.Wifi)))
		} else {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Keep both phones close and their screens on")))
		}
		return s
	}
	if st.Lost != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, st.Error), sdk.Styled(ui.StyleDim, tr.T("What came in so far stays. Start again to go on.")))
		if st.Lost == ui.SwitchLostWifi {
			s.Actions = append(s.Actions, ui.Action{ID: "sw_hotspot", Label: tr.T("Receive over the hotspot"), Key: "h"})
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Over Kario's hotspot the old phone does not depend on the Wi-Fi.")),
				sdk.Button("sw_hotspot", tr.T("Receive over the hotspot"), "sw_hotspot"))
		}
	}
	if l := st.Last; l != nil {
		s.Body = append(s.Body, sdk.Section(tr.T("Moved from %s", firstOf(l.Device, tr.T("the old phone")))))
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, switchEnded(l)))
		for _, k := range l.Added {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "sw_added:" + k.Kind, Title: kindLabel(k.Kind), Trailing: fmt.Sprint(k.Stored), Subtitle: noteLabel(k.Note)})
		}
		if l.Failed > 0 {
			s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("%d items did not come in", l.Failed)))
		}
		if l.Undone {
			s.Body = append(s.Body, sdk.Styled(ui.StyleOK, tr.T("Undone: what this import added was taken out")))
		} else {
			undo := ui.Action{ID: "sw_undo", Label: tr.T("Undo this import"), Key: "u",
				Confirm: tr.T("Take out everything this import added: its contacts, messages, calls and files?")}
			s.Actions = append(s.Actions, undo)
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "sw_undo", Title: tr.T("Undo this import"), Actions: []ui.Action{{ID: "sw_undo", Label: undo.Label, Primary: true, Confirm: undo.Confirm}}})
		}
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Install “Kario Switch” on the old phone (Android or iPhone), start here and scan the code with it. The data goes only between the two phones, encrypted; nothing passes through a cloud.")),
		sdk.Section(tr.T("What to move")))
	for _, k := range ui.SwitchKinds {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "sw:" + k, Title: kindLabel(k), Trailing: onOff(!swState.off[k]),
			Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}})
	}
	s.Actions = append(s.Actions, ui.Action{ID: "sw_start", Label: tr.T("Start receiving"), Key: "s"},
		ui.Action{ID: "sw_bt", Label: tr.T("Move over Bluetooth"), Key: "b"})
	s.Body = append(s.Body, sdk.Button("sw_start", tr.T("Start receiving"), "sw_start"))
	if st.Error != "" && st.Lost == "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, st.Error))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Over Bluetooth")),
		sdk.Styled(ui.StyleDim, tr.T("Nothing to install on the old phone: Kario reads the contacts and the call log of an Android phone or an iPhone, and the messages of an Android phone. Photos, videos and the calendar do not come this way.")),
		sdk.Button("sw_bt", tr.T("Move over Bluetooth"), "sw_bt"))
	return s
}

// switchBTScreen is a move over Bluetooth under way.
func (p *app) switchBTScreen(s ui.Screen, stop ui.Action) ui.Screen {
	st := swState.SwitchState
	s.Actions = []ui.Action{stop}
	switch st.State {
	case ui.SwitchStarting:
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("Connecting to “%s” over Bluetooth…", st.Bluetooth))}
		return s
	case ui.SwitchWaiting:
		s.Body = []ui.Node{sdk.Styled(ui.StyleAccent, tr.T("Allow it on “%s”", st.Bluetooth)),
			sdk.Styled(ui.StyleDim, tr.T("The old phone asks whether Kario may read its contacts and call history, then its messages. Allow each there."))}
	default:
		s.Body = []ui.Node{sdk.Styled(ui.StyleAccent, tr.T("Reading from “%s” over Bluetooth", st.Bluetooth))}
	}
	for _, k := range st.Progress {
		n := ui.Node{Kind: ui.KindItem, ID: "sw_got:" + k.Kind, Title: kindLabel(k.Kind), Trailing: fmt.Sprint(k.Stored)}
		if k.Expected > 0 {
			n.Trailing = tr.T("%d of %d", k.Stored+k.Skipped+k.Failed, k.Expected)
		}
		n.Subtitle = firstOf(noteLabel(k.Note), switchCounts(k.Skipped, k.Failed))
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Keep both phones close and their screens on")))
	return s
}

// noteLabel says why not all of a kind came over Bluetooth.
func noteLabel(note string) string {
	switch note {
	case ui.SwitchNoteNotOffered:
		return tr.T("The old phone does not give these over Bluetooth")
	case ui.SwitchNoteRefused:
		return tr.T("Not allowed on the old phone")
	case ui.SwitchNotePartial:
		return tr.T("Only part of them came over Bluetooth")
	}
	return ""
}

// btPickScreen lists the paired devices to move from.
func (p *app) btPickScreen() ui.Screen {
	s := ui.Screen{ID: "switch_bt", Title: tr.T("Move over Bluetooth")}
	var paired []ui.Node
	for _, d := range p.bt.Devices {
		if d.Paired {
			paired = append(paired, ui.Node{Kind: ui.KindItem, ID: "swbt:" + d.Address, Title: d.Name, Subtitle: d.Address,
				Actions: []ui.Action{{ID: "sw_bt_go", Label: tr.T("Move from this phone"), Primary: true}}})
		}
	}
	switch {
	case p.bt.Backend == "":
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("This device has no Bluetooth"))}
		return s
	case !p.bt.Enabled || len(paired) == 0:
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("Turn Bluetooth on and pair the old phone in Settings › Bluetooth first, then come back here."))}
	default:
		s.Body = append([]ui.Node{sdk.Section(tr.T("Paired devices"))}, paired...)
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "sw_bt_pair", Title: tr.T("Bluetooth"), Subtitle: tr.T("Pair the old phone"),
		Actions: []ui.Action{sdk.Open(tr.T("Open"))}},
		sdk.Styled(ui.StyleDim, tr.T("The old phone asks whether to allow it: allow it there. On an iPhone, turn on “Sync Contacts” for Kario in its Bluetooth settings if it does not ask.")))
	return s
}

func switchCounts(skipped, failed int) string {
	switch {
	case skipped > 0 && failed > 0:
		return tr.T("%d already here · %d failed", skipped, failed)
	case skipped > 0:
		return tr.T("%d already here", skipped)
	case failed > 0:
		return tr.T("%d failed", failed)
	}
	return ""
}

func switchEnded(l *ui.SwitchSummary) string {
	at := time.Unix(l.At, 0).Format("01-02 15:04")
	switch l.Ended {
	case ui.SwitchEndedDone:
		return tr.T("Finished · started %s", at)
	case ui.SwitchEndedIdle:
		return tr.T("Stopped after 30 minutes without the old phone · started %s", at)
	case ui.SwitchEndedNetwork:
		return tr.T("Stopped when the network changed · started %s", at)
	}
	return tr.T("Stopped · started %s", at)
}

// switchUpdate shows the state. On top, a page that moves on to another
// state opens afresh as a new page would, at its top with the first thing
// to press: the code, why it stopped, what came in; an update would keep
// the last page's place, scrolled below them.
func (p *app) switchUpdate() {
	s := p.switchScreen()
	if n := len(p.pages); swState.State != swState.shown && n > 0 && p.pages[n-1] == "switch" {
		p.a.Replace(s)
	} else {
		p.a.Update(s)
	}
	swState.shown = swState.State
}

func (p *app) loadSwitch(then func()) {
	var st ui.SwitchState
	p.a.Go(ui.CapSwitchState, nil, &st, func(err error) {
		if err == nil {
			swState.SwitchState = st
		}
		if then != nil {
			then()
		}
	})
}

// pollSwitch refreshes the page every second while it is open.
func (p *app) pollSwitch() {
	if swState.polling {
		return
	}
	swState.polling = true
	go func() {
		for range time.Tick(time.Second) {
			p.a.Post(func() {
				if slices.Contains(p.pages, "switch") {
					p.loadSwitch(p.switchUpdate)
				}
				if slices.Contains(p.pages, "switch_bt") {
					p.a.Go(ui.CapBTState, nil, &p.bt, func(error) { p.a.Update(p.btPickScreen()) })
				}
			})
		}
	}()
}

// restoreSwitch is the page opened again after a restart.
func (p *app) restoreSwitch() ui.Screen {
	p.loadSwitch(p.switchUpdate)
	p.pollSwitch()
	swState.shown = swState.State
	return p.switchScreen()
}

// switchCall sends a switch.* request and shows the answer.
func (p *app) switchCall(capName string, args any) {
	var st ui.SwitchState
	p.a.Go(capName, args, &st, func(err error) {
		if err != nil {
			if !ui.Cancelled(err) {
				p.a.Toast(err.Error())
			}
			p.loadSwitch(p.switchUpdate)
			return
		}
		swState.SwitchState = st
		p.switchUpdate()
	})
}

// switchAction handles the page's actions; false for the rest.
func (p *app) switchAction(e sdk.ActionEvent) bool {
	switch {
	case onMenu(e.Screen) && e.Node == "switch":
		p.loadSwitch(func() {
			p.push(p.switchScreen())
			swState.shown = swState.State
			p.pollSwitch()
		})
	case e.Screen == "switch_bt" && e.Node == "sw_bt_pair":
		p.radioPage = "bt"
		p.loadRadios(func() { p.a.Push(p.btScreen()) })
	case e.Screen == "switch_bt" && strings.HasPrefix(e.Node, "swbt:"):
		addr := strings.TrimPrefix(e.Node, "swbt:")
		var kinds []string
		for _, k := range ui.SwitchBluetoothKinds {
			if !swState.off[k] {
				kinds = append(kinds, k)
			}
		}
		name := addr
		for _, d := range p.bt.Devices {
			if d.Address == addr {
				name = d.Name
			}
		}
		p.a.Pop()
		p.popped("switch_bt")
		swState.State, swState.Bluetooth = ui.SwitchStarting, name
		p.switchUpdate()
		p.switchCall(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: kinds, Bluetooth: addr})
	case e.Screen != "switch":
		return false
	// A page action comes with the focused row: actions first.
	case e.Action == "sw_bt":
		p.a.Go(ui.CapBTState, nil, &p.bt, func(error) { p.push(p.btPickScreen()) })
	case e.Action == "sw_start" || e.Action == "sw_hotspot":
		var kinds []string
		for _, k := range ui.SwitchKinds {
			if !swState.off[k] {
				kinds = append(kinds, k)
			}
		}
		swState.State = ui.SwitchStarting
		p.switchUpdate()
		p.switchCall(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: kinds, Hotspot: e.Action == "sw_hotspot"})
	case e.Action == "sw_stop":
		p.switchCall(ui.CapSwitchStop, nil)
	case e.Action == "sw_undo":
		p.switchCall(ui.CapSwitchUndo, nil)
	case strings.HasPrefix(e.Node, "sw:") && e.Action == "toggle":
		if swState.off == nil {
			swState.off = map[string]bool{}
		}
		k := strings.TrimPrefix(e.Node, "sw:")
		swState.off[k] = !swState.off[k]
		p.switchUpdate()
	default:
		return false
	}
	return true
}
