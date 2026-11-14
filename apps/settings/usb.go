package main

import (
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › USB (27-usb-typec.md §3.3): what a computer the user trusts
// gets over the cable, network sharing (the default) or file transfer
// over MTP; settings.set usb_mode, from the next computer on.

type usbInfo struct {
	USB struct {
		Mode string `json:"mode"`
	} `json:"usb"`
}

func (p *app) usbModeName() string {
	if p.s.USB.Mode == ui.USBModeMTP {
		return tr.T("File transfer (MTP)")
	}
	return tr.T("Network sharing")
}

// usbItem is the USB page's line on the root page.
func (p *app) usbItem() ui.Node {
	return ui.Node{Kind: ui.KindItem, ID: "usb", Title: "USB", Subtitle: p.usbModeName(), Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func (p *app) usbScreen() ui.Screen {
	s := ui.Screen{ID: "usb", Title: "USB", Body: []ui.Node{sdk.Section(tr.T("A computer you trust gets"))}}
	for _, m := range []struct{ id, title, sub string }{
		{ui.USBModeNetwork, tr.T("Network sharing"), tr.T("In a browser or as a network drive, with a password the phone shows: nothing to install on Windows 11, macOS or Linux. The debug shell (SSH) in developer mode needs it")},
		{ui.USBModeMTP, tr.T("File transfer (MTP)"), tr.T("In the computer's file manager, as other phones are: Windows Explorer, Linux file managers; on a Mac, Android File Transfer or OpenMTP. With a lock-screen PIN, only once the phone is unlocked")},
	} {
		n := ui.Node{Kind: ui.KindItem, ID: "usb:" + m.id, Title: m.title, Subtitle: m.sub, Actions: []ui.Action{{ID: "choose", Label: tr.T("Choose"), Primary: true}}}
		if p.s.USB.Mode == m.id {
			n.Badge = tr.T("Current")
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Either way the computer gets Photos, Videos, Music and Files only once you tap “Trust” on the phone, and only until the cable comes out; it can add files (and folders in Music) but never delete or change what is on the phone. A change applies to the next computer.")))
	return s
}

// usbAction handles the USB page; false for the rest.
func (p *app) usbAction(e sdk.ActionEvent) bool {
	switch {
	case e.Screen == "root" && e.Node == "usb":
		p.push(p.usbScreen())
	case e.Screen == "usb" && e.Action == "choose" && strings.HasPrefix(e.Node, "usb:"):
		v := 0.0
		if strings.TrimPrefix(e.Node, "usb:") == ui.USBModeMTP {
			v = 1
		}
		p.a.Go(ui.CapSettingsSet, ui.SettingArgs{Key: ui.SettingUSBMode, Value: v}, &p.s, func(err error) {
			if err != nil {
				p.a.Toast(err.Error())
			}
			p.a.Update(p.usbScreen())
			p.a.Update(p.root())
		})
	default:
		return false
	}
	return true
}
