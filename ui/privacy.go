package ui

import (
	"fmt"
	"slices"
	"strings"
)

// Privacy labels (33-app-guidelines.md §3): a manifest's "privacy" says
// which data the app sends off the phone ("collects"), what for, whether it
// is tied to the user and whether others get it, and which data it reads
// but keeps on the phone ("local"). The store shows it on the app's page.
// The system checks what it can: without the net permission an app cannot
// send anything, and with it the data of every runtime permission it
// declares has to be accounted for.

// The kinds of data a label names, in the order the store lists them.
const (
	DataAccount     = "account"
	DataContacts    = "contacts"
	DataLocation    = "location"
	DataPhotos      = "photos"
	DataAudio       = "audio"
	DataMessages    = "messages"
	DataPayment     = "payment"
	DataIdentifiers = "identifiers"
	DataUsage       = "usage"
	DataDiagnostics = "diagnostics"
)

var PrivacyData = []string{DataAccount, DataContacts, DataLocation, DataPhotos, DataAudio, DataMessages,
	DataPayment, DataIdentifiers, DataUsage, DataDiagnostics}

// What collected data is used for.
const (
	UseApp       = "app"
	UseAnalytics = "analytics"
	UseAds       = "ads"
)

var PrivacyUses = []string{UseApp, UseAnalytics, UseAds}

// PermData is the data a runtime permission opens to the app.
var PermData = map[string]string{
	PermCamera:     DataPhotos,
	PermMicrophone: DataAudio,
	PermContacts:   DataContacts,
	PermPhotos:     DataPhotos,
	PermLocation:   DataLocation,
	PermNFCReader:  DataPayment,
}

// Privacy is a manifest's privacy label.
type Privacy struct {
	Collects []Collected `json:"collects,omitempty"`
	Local    []string    `json:"local,omitempty"`
}

// Collected is one kind of data the app sends off the phone. Linked: tied
// to who the user is (an account, a phone number); Shared: given to third
// parties (an analytics or advertising company).
type Collected struct {
	Data   string   `json:"data"`
	Use    []string `json:"use"`
	Linked bool     `json:"linked,omitempty"`
	Shared bool     `json:"shared,omitempty"`
}

// Sends says whether the label lists data as collected.
func (p *Privacy) Sends(data string) bool {
	return p != nil && slices.ContainsFunc(p.Collects, func(c Collected) bool { return c.Data == data })
}

// CheckPrivacy is the build's, the installer's and the store's check of a
// manifest's label. None at all passes: the store says it was not provided.
func CheckPrivacy(p *Privacy, permissions []string) error {
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	kind := func(d string) error {
		switch {
		case !slices.Contains(PrivacyData, d):
			return fmt.Errorf("privacy: %q is not a kind of data (%s)", d, strings.Join(PrivacyData, ", "))
		case seen[d]:
			return fmt.Errorf("privacy: %s is listed twice", d)
		}
		seen[d] = true
		return nil
	}
	for _, c := range p.Collects {
		if err := kind(c.Data); err != nil {
			return err
		}
		if len(c.Use) == 0 {
			return fmt.Errorf("privacy: say what %s is collected for (use: %s)", c.Data, strings.Join(PrivacyUses, ", "))
		}
		for i, u := range c.Use {
			if !slices.Contains(PrivacyUses, u) || slices.Contains(c.Use[:i], u) {
				return fmt.Errorf("privacy: %s: use %q is not one of %s, or is repeated", c.Data, u, strings.Join(PrivacyUses, ", "))
			}
		}
	}
	for _, d := range p.Local {
		if err := kind(d); err != nil {
			return err
		}
	}
	if !slices.Contains(permissions, PermNet) {
		if len(p.Collects) > 0 {
			return fmt.Errorf("privacy: without the %q permission the app cannot send data off the phone", PermNet)
		}
		return nil
	}
	for _, perm := range permissions {
		if d, ok := PermData[perm]; ok && !seen[d] {
			return fmt.Errorf("privacy: the %q permission opens %s to the app: list it under collects or local", perm, d)
		}
	}
	return nil
}
