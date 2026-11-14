package main

import (
	"slices"
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

func permName(p string) string {
	switch p {
	case ui.PermCamera:
		return tr.T("Camera")
	case ui.PermMicrophone:
		return tr.T("Microphone")
	case ui.PermContacts:
		return tr.T("Contacts")
	case ui.PermPhotos:
		return tr.T("Photos")
	case ui.PermLocation:
		return tr.T("Location")
	case ui.PermNFCReader:
		return tr.T("Reading NFC cards")
	case ui.PermNet:
		return tr.T("Network")
	case ui.PermNotify:
		return tr.T("Notifications")
	case ui.PermVoIP:
		return tr.T("Internet calls (answered in the background)")
	case ui.PermPhone:
		return tr.T("Phone calls (confirmed before each call)")
	case ui.PermSMS:
		return tr.T("SMS (confirmed before each message)")
	case ui.PermKeystore:
		return tr.T("App keys")
	case ui.PermAttest:
		return tr.T("Device attestation")
	case ui.PermNFC:
		return tr.T("Contactless cards (NFC)")
	}
	return p
}

func dataName(d string) string {
	switch d {
	case ui.DataAccount:
		return tr.T("Name, phone number and email")
	case ui.DataContacts:
		return tr.T("Contacts")
	case ui.DataLocation:
		return tr.T("Location")
	case ui.DataPhotos:
		return tr.T("Photos and videos")
	case ui.DataAudio:
		return tr.T("Audio recordings")
	case ui.DataMessages:
		return tr.T("Messages")
	case ui.DataPayment:
		return tr.T("Payment and card details")
	case ui.DataIdentifiers:
		return tr.T("Identifiers")
	case ui.DataUsage:
		return tr.T("Usage data")
	case ui.DataDiagnostics:
		return tr.T("Diagnostics")
	}
	return d
}

func useName(u string) string {
	switch u {
	case ui.UseApp:
		return tr.T("app features")
	case ui.UseAnalytics:
		return tr.T("analytics")
	case ui.UseAds:
		return tr.T("advertising")
	}
	return u
}

// permissionNodes list what the app asks for, a runtime permission with
// the developer's purpose (the one the system's dialog shows).
func permissionNodes(e entry) []ui.Node {
	out := []ui.Node{sdk.Section(tr.T("Permissions"))}
	for _, p := range e.Permissions {
		text := permName(p)
		if why := e.Purposes[p]; why != "" {
			text = tr.T("%s: %s", text, why)
		}
		out = append(out, ui.Node{Kind: ui.KindText, Text: text})
	}
	if len(e.Permissions) == 0 {
		out = append(out, sdk.Styled(ui.StyleDim, tr.T("None")))
	}
	return out
}

// privacyNodes are the app's privacy label: what the system knows (an app
// without network access sends nothing), else what the developer declares.
func privacyNodes(e entry) []ui.Node {
	out := []ui.Node{sdk.Section(tr.T("Privacy"))}
	p := e.Privacy
	switch {
	case !slices.Contains(e.Permissions, ui.PermNet):
		return append(out, sdk.Styled(ui.StyleOK, tr.T("No data leaves the phone: the app has no network access (checked by the system)")))
	case p == nil:
		return append(out, sdk.Styled(ui.StyleDim, tr.T("The developer has not provided privacy details")))
	case len(p.Collects) == 0:
		out = append(out, ui.Node{Kind: ui.KindText, Text: tr.T("Collects no data")})
	}
	for _, d := range ui.PrivacyData {
		i := slices.IndexFunc(p.Collects, func(c ui.Collected) bool { return c.Data == d })
		if i < 0 {
			continue
		}
		c := p.Collects[i]
		var how []string
		for _, u := range c.Use {
			how = append(how, useName(u))
		}
		if c.Linked {
			how = append(how, tr.T("linked to you"))
		}
		if c.Shared {
			how = append(how, tr.T("shared with third parties"))
		}
		out = append(out, ui.Node{Kind: ui.KindText, Text: tr.T("Sent: %s (%s)", dataName(d), strings.Join(how, tr.T(", ")))})
	}
	var local []string
	for _, d := range ui.PrivacyData {
		if slices.Contains(p.Local, d) {
			local = append(local, dataName(d))
		}
	}
	if len(local) > 0 {
		out = append(out, ui.Node{Kind: ui.KindText, Text: tr.T("Used on this phone only: %s", strings.Join(local, tr.T(", ")))})
	}
	return append(out, sdk.Styled(ui.StyleDim, tr.T("Declared by the developer and checked in the store's review")))
}
