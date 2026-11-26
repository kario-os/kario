package main

import (
	"slices"
	"strconv"
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Mobile network › Access point names (apn.list, apn.set): the
// SIM's APNs as the system chose them for its carrier, the internet one in
// use, and pages to view, add and change them, delete one or go back to the
// carrier's. A password never comes back from the system: its field stays
// empty, and an empty field keeps the one there is.

var apnState struct {
	ui.APNState
	loaded bool
	err    string
}

// apnDraft is the APN on the edit page ("apnedit:<id>", "apnedit:" for a
// new one): what the user typed is kept here as the page changes.
var apnDraft struct {
	ui.APN
	pass, port string
}

// apnTypes are the choices of the edit page's type line, in turn.
var apnTypes = [][]string{{"default", "supl"}, {"default", "mms", "supl"}, {"mms"}, {"ims"}, {"ia"}, {"default", "ia", "supl", "xcap"}}

func (p *app) loadAPN(then func()) {
	var st ui.APNState
	p.a.Go(ui.CapAPNList, nil, &st, func(err error) {
		apnState.err = ""
		switch {
		case err == nil:
			apnState.APNState, apnState.loaded = st, true
		case !ui.Cancelled(err):
			apnState.err = err.Error()
		}
		if then != nil {
			then()
		}
	})
}

func apnTypeLabel(types []string) string {
	names := map[string]string{"default": tr.T("Internet"), "mms": "MMS", "ims": "IMS", "ia": tr.T("LTE attach"), "supl": "SUPL", "xcap": "XCAP", "emergency": tr.T("Emergency")}
	var out []string
	for _, t := range types {
		out = append(out, firstOf(names[t], t))
	}
	return strings.Join(out, ", ")
}

// apnUse is what the list says an APN is for: the internet, MMS or IMS
// before the rest of its types.
func apnUse(types []string) string {
	for _, t := range []string{"default", "mms", "ims"} {
		if slices.Contains(types, t) {
			return apnTypeLabel([]string{t})
		}
	}
	return apnTypeLabel(types)
}

func apnIPLabel(ip string) string {
	return map[string]string{"ipv4": "IPv4", "ipv6": "IPv6", "ipv4v6": "IPv4/IPv6"}[ip]
}

func apnAuthLabel(auth string) string {
	return map[string]string{"none": tr.T("None"), "pap": "PAP", "chap": "CHAP", "pap-chap": tr.T("PAP or CHAP")}[auth]
}

// apnItem is the row on the Mobile network page: the APN in use.
func (p *app) apnItem() ui.Node {
	sub := tr.T("The network's default")
	if p.radio.APN != "" {
		sub = p.radio.APN
	}
	return ui.Node{Kind: ui.KindItem, ID: "apn", Title: tr.T("Access point names"), Subtitle: sub, Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func (p *app) apnScreen() ui.Screen {
	st := &apnState
	s := ui.Screen{ID: "apn", Title: tr.T("Access point names")}
	switch {
	case st.err != "":
		s.Body = []ui.Node{sdk.Styled(ui.StyleWarn, st.err)}
		return s
	case !st.loaded:
		return s
	case st.MCC == "":
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("The SIM's network is not known yet: there is no SIM, or it is locked"))}
		return s
	}
	head := tr.T("SIM network %s %s", st.MCC, st.MNC)
	if st.Carrier != "" {
		head = st.Carrier + " · " + head
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, head))
	if len(st.APNs) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("The phone has no APNs for this network: mobile data uses the network's default. Add the one your carrier gives if that does not work.")))
	}
	for _, a := range st.APNs {
		n := ui.Node{Kind: ui.KindItem, ID: "apn:" + a.ID, Title: a.Name, Subtitle: a.APN + " · " + apnUse(a.Types) + " · " + apnIPLabel(a.IPType)}
		edit := ui.Action{ID: "edit", Label: tr.T("View and edit"), Key: "e"}
		del := ui.Action{ID: "delete", Label: tr.T("Delete"), Key: "d", Group: ui.GroupDanger, Confirm: tr.T("Delete the APN “%s”?", a.Name)}
		switch {
		case a.ID == st.Selected:
			n.Trailing = tr.T("In use")
			edit.Primary = true
			n.Actions = []ui.Action{edit, del}
		case slices.Contains(a.Types, "default"):
			n.Actions = []ui.Action{{ID: "use", Label: tr.T("Use"), Primary: true}, edit, del}
		default:
			edit.Primary = true
			n.Actions = []ui.Action{edit, del}
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "apn-add", Title: tr.T("Add an APN"), Actions: []ui.Action{{ID: "add", Label: tr.T("Add"), Primary: true}}})
	if st.Edited {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "apn-reset", Title: tr.T("Reset to default"), Subtitle: tr.T("The carrier's APNs again"),
			Actions: []ui.Action{{ID: "reset", Label: tr.T("Reset"), Primary: true, Confirm: tr.T("Reset the APNs to the carrier's? The ones you added or changed are deleted.")}}})
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Mobile data goes through the internet APN in use; the MMS and IMS (VoLTE) ones are kept for those services.")))
	return s
}

func apnEditScreen() ui.Screen {
	d := &apnDraft
	s := ui.Screen{ID: "apnedit:" + d.ID, Title: tr.T("Edit the APN"), Actions: []ui.Action{{ID: "save", Label: tr.T("Save")}}}
	if d.ID == "" {
		s.Title = tr.T("Add an APN")
	}
	cycle := []ui.Action{{ID: "cycle", Label: tr.T("Change"), Primary: true}}
	pass := ui.Node{Kind: ui.KindInput, ID: "pass", Value: d.pass, Placeholder: tr.T("Password"), Secret: true}
	if d.PasswordSet {
		pass.Placeholder = tr.T("Password (kept unless you type a new one)")
	}
	s.Body = []ui.Node{
		{Kind: ui.KindInput, ID: "name", Value: d.Name, Placeholder: tr.T("Name")},
		{Kind: ui.KindInput, ID: "apn", Value: d.APN.APN, Placeholder: "APN", Keyboard: ui.KeyboardEmail, Submit: "save"},
		{Kind: ui.KindItem, ID: "types", Title: tr.T("APN type"), Trailing: apnTypeLabel(d.Types), Actions: cycle},
		{Kind: ui.KindItem, ID: "iptype", Title: tr.T("IP type"), Trailing: apnIPLabel(d.IPType), Actions: cycle},
		{Kind: ui.KindItem, ID: "auth", Title: tr.T("Authentication"), Trailing: apnAuthLabel(d.Auth), Actions: cycle},
		{Kind: ui.KindInput, ID: "user", Value: d.User, Placeholder: tr.T("User name"), Keyboard: ui.KeyboardEmail},
		pass,
		sdk.Section("MMS"),
		{Kind: ui.KindInput, ID: "mmsc", Value: d.MMSC, Placeholder: "MMSC", Keyboard: ui.KeyboardURL},
		{Kind: ui.KindInput, ID: "mmsproxy", Value: d.MMSProxy, Placeholder: tr.T("MMS proxy"), Keyboard: ui.KeyboardURL},
		{Kind: ui.KindInput, ID: "mmsport", Value: d.port, Placeholder: tr.T("MMS port"), Keyboard: ui.KeyboardNumber},
		sdk.Button("save", tr.T("Save"), "save"),
	}
	return s
}

// apnDraftOf starts the edit page's draft from an APN of the list ("" a
// new one).
func apnDraftOf(id string) {
	apnDraft.APN, apnDraft.pass, apnDraft.port = ui.APN{ID: id, Types: []string{"default", "supl"}, IPType: "ipv4v6", Auth: "none"}, "", ""
	for _, a := range apnState.APNs {
		if a.ID == id {
			apnDraft.APN = a
			apnDraft.APN.Types = slices.Clone(a.Types)
		}
	}
	if apnDraft.MMSPort != 0 {
		apnDraft.port = strconv.Itoa(apnDraft.MMSPort)
	}
}

// apnEdit opens the edit page for an APN of the list ("" a new one).
func (p *app) apnEdit(id string) {
	apnDraftOf(id)
	p.push(apnEditScreen())
}

// apnFields keeps what the edit page's inputs hold.
func apnFields(f map[string]string) {
	d := &apnDraft
	for key, field := range map[string]*string{"name": &d.Name, "apn": &d.APN.APN, "user": &d.User, "pass": &d.pass, "mmsc": &d.MMSC, "mmsproxy": &d.MMSProxy, "mmsport": &d.port} {
		if v, ok := f[key]; ok {
			*field = strings.TrimSpace(v)
		}
	}
}

func apnNext[T any](list []T, cur T, eq func(a, b T) bool) T {
	for i, x := range list {
		if eq(x, cur) {
			return list[(i+1)%len(list)]
		}
	}
	return list[0]
}

func (p *app) apnSet(args ui.APNArgs, done func()) {
	var st ui.APNState
	p.a.Go(ui.CapAPNSet, args, &st, func(err error) {
		if err != nil {
			if !ui.Cancelled(err) {
				p.a.Toast(err.Error())
			}
			return
		}
		apnState.APNState, apnState.loaded, apnState.err = st, true, ""
		if done != nil {
			done()
		}
		p.a.Update(p.apnScreen())
		// The data link follows the APN: the rows show the one in use.
		p.a.Go(ui.CapRadioState, nil, &p.radio, func(error) { p.updateMenu() })
	})
}

func (p *app) apnSave() {
	d := apnDraft
	port := 0
	if d.port != "" {
		n, err := strconv.Atoi(d.port)
		if err != nil || n < 1 || n > 65535 {
			p.a.Toast(tr.T("The MMS port is a number from 1 to 65535"))
			return
		}
		port = n
	}
	switch {
	case d.Name == "":
		p.a.Toast(tr.T("Enter a name"))
		return
	case d.APN.APN == "":
		p.a.Toast(tr.T("Enter the APN"))
		return
	}
	a := d.APN
	a.MMSPort, a.Password, a.PasswordSet, a.Edited = port, nil, false, false
	if d.pass != "" {
		a.Password = &d.pass
	}
	if a.Auth != "none" && a.User == "" {
		p.a.Toast(tr.T("Authentication needs a user name"))
		return
	}
	p.apnSet(ui.APNArgs{Op: ui.APNSave, APN: &a}, func() {
		p.a.Pop()
		p.a.Toast(tr.T("Saved the APN “%s”", a.Name))
	})
}

// apnScreenByID builds one of these pages again (a restored process).
func (p *app) apnScreenByID(id string) (ui.Screen, bool) {
	switch {
	case id == "apn":
		p.loadAPN(func() { p.a.Update(p.apnScreen()) })
		return p.apnScreen(), true
	case strings.HasPrefix(id, "apnedit:"):
		id = strings.TrimPrefix(id, "apnedit:")
		apnDraftOf(id)
		p.loadAPN(func() {
			apnDraftOf(id)
			p.a.Update(apnEditScreen())
		})
		return apnEditScreen(), true
	}
	return ui.Screen{}, false
}

// apnAction handles the APN pages; false for the rest.
func (p *app) apnAction(e sdk.ActionEvent) bool {
	id := strings.TrimPrefix(e.Node, "apn:")
	switch {
	case onMenu(e.Screen) && e.Node == "apn":
		p.loadAPN(func() { p.push(p.apnScreen()) })
	case e.Screen == "apn" && e.Node == "apn-add":
		p.apnEdit("")
	case e.Screen == "apn" && e.Node == "apn-reset":
		p.apnSet(ui.APNArgs{Op: ui.APNReset}, func() { p.a.Toast(tr.T("The carrier's APNs are back")) })
	case e.Screen == "apn" && e.Action == "use":
		p.apnSet(ui.APNArgs{Op: ui.APNSelect, ID: id}, nil)
	case e.Screen == "apn" && (e.Action == "edit" || e.Action == ui.ActionOpen):
		p.apnEdit(id)
	case e.Screen == "apn" && e.Action == "delete":
		p.apnSet(ui.APNArgs{Op: ui.APNDelete, ID: id}, nil)
	case strings.HasPrefix(e.Screen, "apnedit:"):
		apnFields(e.Fields)
		d := &apnDraft
		switch {
		case e.Action == "save":
			p.apnSave()
		case e.Node == "types":
			d.Types = apnNext(apnTypes, d.Types, slices.Equal)
			p.a.Update(apnEditScreen())
		case e.Node == "iptype":
			d.IPType = apnNext([]string{"ipv4v6", "ipv4", "ipv6"}, d.IPType, func(a, b string) bool { return a == b })
			p.a.Update(apnEditScreen())
		case e.Node == "auth":
			d.Auth = apnNext([]string{"none", "pap", "chap", "pap-chap"}, d.Auth, func(a, b string) bool { return a == b })
			p.a.Update(apnEditScreen())
		}
	default:
		return false
	}
	return true
}
