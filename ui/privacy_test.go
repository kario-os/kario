package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckPrivacy(t *testing.T) {
	for _, c := range []struct {
		label string
		perms []string
		err   string
	}{
		{``, []string{PermNet, PermLocation}, ""},
		{`{}`, nil, ""},
		{`{}`, []string{PermNet}, ""},
		{`{"collects": [{"data": "location", "use": ["app"]}]}`, []string{PermNet, PermLocation}, ""},
		{`{"local": ["contacts"], "collects": [{"data": "usage", "use": ["analytics", "ads"], "shared": true}]}`, []string{PermNet, PermContacts}, ""},
		{`{"local": ["photos"]}`, []string{PermNet, PermCamera, PermPhotos}, ""},
		{`{"local": ["contacts"]}`, []string{PermContacts}, ""},
		{`{}`, []string{PermContacts}, ""},
		{`{"collects": [{"data": "usage", "use": ["app"]}]}`, nil, "cannot send data"},
		{`{}`, []string{PermNet, PermContacts}, `"contacts" permission opens contacts`},
		{`{"local": ["photos"]}`, []string{PermNet, PermNFCReader}, "opens payment"},
		{`{"collects": [{"data": "dna", "use": ["app"]}]}`, []string{PermNet}, "not a kind of data"},
		{`{"collects": [{"data": "usage"}]}`, []string{PermNet}, "what usage is collected for"},
		{`{"collects": [{"data": "usage", "use": ["resale"]}]}`, []string{PermNet}, `use "resale"`},
		{`{"collects": [{"data": "usage", "use": ["app", "app"]}]}`, []string{PermNet}, "repeated"},
		{`{"collects": [{"data": "usage", "use": ["app"]}], "local": ["usage"]}`, []string{PermNet}, "listed twice"},
	} {
		var p *Privacy
		if c.label != "" {
			if err := json.Unmarshal([]byte(c.label), &p); err != nil {
				t.Fatal(err)
			}
		}
		err := CheckPrivacy(p, c.perms)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s %v: %v", c.label, c.perms, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s %v: got %v, want %q", c.label, c.perms, err, c.err)
		}
	}
}

func TestPrivacySends(t *testing.T) {
	var none *Privacy
	p := &Privacy{Collects: []Collected{{Data: DataUsage, Use: []string{UseAnalytics}}}, Local: []string{DataContacts}}
	if none.Sends(DataUsage) || !p.Sends(DataUsage) || p.Sends(DataContacts) {
		t.Error("Sends: only collected data is sent")
	}
	for perm, d := range PermData {
		if _, ok := RuntimePerms[perm]; !ok {
			t.Errorf("PermData: %s is not a runtime permission", perm)
		}
		found := false
		for _, k := range PrivacyData {
			found = found || k == d
		}
		if !found {
			t.Errorf("PermData: %s opens %s, which is not in PrivacyData", perm, d)
		}
	}
}
