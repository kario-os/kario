package main

import (
	"fmt"
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Security: the lock-screen PIN, how the lock behaves, and the
// fingerprints (31-lock-and-biometrics.md). PINs are typed on the system's
// own screens, which settings.set raises (lock_pin, fingerprint_auth,
// fingerprint_enroll); this app never sees one, nor a fingerprint.

// securityInfo is settings.get's "security".
type securityInfo struct {
	Security struct {
		PIN          bool   `json:"pin"`
		Grace        int    `json:"grace"`
		GraceChoices []int  `json:"grace_choices"`
		ShowContent  bool   `json:"show_content"`
		Sensor       string `json:"sensor"`
		Fingers      []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"fingers"`
		MaxFingers   int    `json:"max_fingers"`
		FingerUnlock bool   `json:"finger_unlock"`
		FingerPay    bool   `json:"finger_pay"`
		Blocked      string `json:"blocked"`
	} `json:"security"`
}

func graceLabel(s int) string {
	if s == 0 {
		return tr.T("Immediately")
	}
	return lockLabel(s)
}

func (p *app) securityItem() ui.Node {
	sec := p.s.Security
	sub := tr.T("No lock-screen PIN: the keypad lock needs no secret")
	if sec.PIN {
		sub = tr.T("Lock-screen PIN on · %d fingerprints", len(sec.Fingers))
	}
	return ui.Node{Kind: ui.KindItem, ID: "security", Title: tr.T("Security"), Subtitle: sub, Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func (p *app) securityScreen() ui.Screen {
	sec := p.s.Security
	s := ui.Screen{ID: "security", Title: tr.T("Security"), Body: []ui.Node{sdk.Section(tr.T("Screen lock"))}}
	pin := ui.Node{Kind: ui.KindItem, ID: "pin", Title: tr.T("Lock-screen PIN"), Trailing: onOff(sec.PIN),
		Subtitle: tr.T("Off: the keypad lock needs no secret"),
		Actions:  []ui.Action{{ID: "set", Label: tr.T("Set PIN"), Primary: true}}}
	if sec.PIN {
		pin.Subtitle = tr.T("The phone locks when the screen goes off; the PIN or a fingerprint unlocks it")
		pin.Actions = []ui.Action{{ID: "change", Label: tr.T("Change PIN"), Primary: true},
			{ID: "remove", Label: tr.T("Remove PIN"), Key: "d", Group: ui.GroupDanger}}
	}
	s.Body = append(s.Body, pin)
	if sec.PIN {
		fingers := tr.T("%d fingerprints", len(sec.Fingers))
		if sec.Sensor == "" {
			fingers = tr.T("No fingerprint sensor")
		}
		s.Body = append(s.Body,
			ui.Node{Kind: ui.KindItem, ID: "grace", Title: tr.T("Lock after the screen goes off"), Trailing: graceLabel(sec.Grace),
				Actions: []ui.Action{{ID: "next", Label: tr.T("Next option"), Primary: true}}},
			ui.Node{Kind: ui.KindItem, ID: "content", Title: tr.T("Notification contents when locked"),
				Subtitle: tr.T("Off: the lock screen shows only which apps have notifications and how many"),
				Trailing: onOff(sec.ShowContent), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
			ui.Node{Kind: ui.KindItem, ID: "fingers", Title: tr.T("Fingerprints"), Subtitle: fingers,
				Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("The PIN (4 to 16 digits) is typed on the system's screen and checked by the system's key service, which keeps only a slow hash of it. After 5 wrong tries the next waits 30 seconds, after more it waits longer, even across a restart. Emergency calls, answering calls and stopping alarms work without unlocking.")))
	return s
}

func (p *app) fingersScreen() ui.Screen {
	sec := p.s.Security
	s := ui.Screen{ID: "fingers", Title: tr.T("Fingerprints")}
	if sec.Sensor == "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("This phone has no fingerprint sensor the system can use")))
		return s
	}
	for _, f := range sec.Fingers {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("fp:", f.ID), Title: f.Name, Actions: []ui.Action{
			{ID: "rename", Label: tr.T("Rename"), Primary: true},
			{ID: "delete", Label: tr.T("Delete"), Key: "d", Group: ui.GroupDanger, Confirm: tr.T("Delete this fingerprint?")}}})
	}
	if len(sec.Fingers) < sec.MaxFingers {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "add", Title: tr.T("Add a fingerprint"),
			Subtitle: tr.T("Touch the sensor several times with the same finger"), Actions: []ui.Action{{ID: "add", Label: tr.T("Add"), Primary: true}}})
	}
	toggle := func(id, title, sub string, on bool) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Subtitle: sub, Trailing: onOff(on),
			Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Use fingerprints for")),
		toggle("unlock", tr.T("Unlocking the phone"), "", sec.FingerUnlock),
		toggle("pay", tr.T("Payments"), tr.T("Apps that support it (such as Feiqian) confirm payments with a fingerprint; the payment PIN still works, and turns it on the first time"), sec.FingerPay))
	if sec.Blocked != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, map[string]string{
			"restart": tr.T("After a restart the PIN unlocks first"),
			"timeout": tr.T("No PIN for 48 hours: the PIN unlocks first"),
			"lockout": tr.T("Too many failed tries: the PIN unlocks first")}[sec.Blocked]))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("At most %d fingerprints. They stay in the system's key service; no app gets them. Adding or deleting one makes apps set up fingerprint payment again with their payment PIN.", sec.MaxFingers)))
	return s
}

func (p *app) refreshSecurity() {
	p.a.Update(p.root())
	p.a.Update(p.securityScreen())
	p.a.Update(p.fingersScreen())
}

// secSet changes a security setting; then runs once it went through.
func (p *app) secSet(key string, v float64, text string, then func()) {
	p.a.Go(ui.CapSettingsSet, ui.SettingArgs{Key: key, Value: v, Text: text}, &p.s, func(err error) {
		if err != nil && !ui.Cancelled(err) {
			p.a.Toast(err.Error())
		}
		p.refreshSecurity()
		if err == nil && then != nil {
			then()
		}
	})
}

func onOffValue(on bool) float64 { return map[bool]float64{true: 0, false: 1}[on] }

// securityAction handles the security pages' actions.
func (p *app) securityAction(e sdk.ActionEvent) bool {
	sec := p.s.Security
	switch {
	case e.Screen == "root" && e.Node == "security":
		p.push(p.securityScreen())
	case e.Screen == "security" && e.Node == "pin":
		p.secSet(ui.SettingLockPIN, map[bool]float64{true: 0, false: 1}[e.Action == "remove"], "", nil)
	case e.Screen == "security" && e.Node == "grace":
		next := 0
		for i, g := range sec.GraceChoices {
			if g == sec.Grace && i+1 < len(sec.GraceChoices) {
				next = sec.GraceChoices[i+1]
			}
		}
		p.secSet(ui.SettingLockGrace, float64(next), "", nil)
	case e.Screen == "security" && e.Node == "content":
		p.secSet(ui.SettingLockShowContent, onOffValue(sec.ShowContent), "", nil)
	case e.Screen == "security" && e.Node == "fingers":
		// The system asks for the PIN first, as Android does.
		p.secSet(ui.SettingFingerprintAuth, 1, "", func() { p.push(p.fingersScreen()) })
	case e.Screen == "fingers" && (e.Node == "add" || e.Action == "add"):
		p.secSet(ui.SettingFingerprintEnroll, 1, "", nil)
	case e.Screen == "fingers" && e.Node == "unlock":
		p.secSet(ui.SettingFingerprintUnlock, onOffValue(sec.FingerUnlock), "", nil)
	case e.Screen == "fingers" && e.Node == "pay":
		p.secSet(ui.SettingFingerprintPay, onOffValue(sec.FingerPay), "", nil)
	case e.Screen == "fingers" && strings.HasPrefix(e.Node, "fp:"):
		var id int
		fmt.Sscanf(e.Node, "fp:%d", &id)
		if e.Action == "delete" {
			p.secSet(ui.SettingFingerprintDelete, float64(id), "", nil)
			return true
		}
		name := ""
		for _, f := range sec.Fingers {
			if f.ID == id {
				name = f.Name
			}
		}
		p.a.Push(ui.Screen{ID: fmt.Sprint("fprename:", id), Title: tr.T("Rename"), Body: []ui.Node{
			{Kind: ui.KindInput, ID: "name", Value: name, Placeholder: tr.T("Fingerprint name"), Submit: "save"}}})
	case strings.HasPrefix(e.Screen, "fprename:") && e.Action == "save":
		var id int
		fmt.Sscanf(e.Screen, "fprename:%d", &id)
		p.a.Pop()
		p.secSet(ui.SettingFingerprintRename, float64(id), e.Fields["name"], nil)
	default:
		return false
	}
	return true
}
