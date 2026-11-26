package main

import (
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

// A sensor in the OK key never unlocks: the fingerprints page has no
// unlocking switch there, asks for presses of OK and says why.
func TestFingersInTheOKKey(t *testing.T) {
	p, _, _ := fakeShell(t)
	page := func(place string) []ui.Node {
		got := make(chan []ui.Node)
		p.a.Post(func() {
			p.s.Security.PIN, p.s.Security.Sensor, p.s.Security.MaxFingers, p.s.Security.Place = true, "simulated", 5, place
			got <- p.fingersScreen().Body
		})
		return <-got
	}
	find := func(body []ui.Node, id string) *ui.Node {
		for i := range body {
			if body[i].ID == id {
				return &body[i]
			}
		}
		return nil
	}
	said := func(body []ui.Node, text string) bool {
		for _, n := range body {
			if strings.Contains(n.Text, text) {
				return true
			}
		}
		return false
	}
	own := page("")
	if find(own, "unlock") == nil || find(own, "pay") == nil || said(own, "OK key") {
		t.Fatalf("a sensor of its own: %+v", own)
	}
	ok := page(ui.FingerprintOKKey)
	if find(ok, "unlock") != nil {
		t.Fatal("the OK key's sensor offers unlocking")
	}
	if find(ok, "pay") == nil || !said(ok, "The sensor is in the OK key") {
		t.Fatalf("the OK key's sensor: %+v", ok)
	}
	if add := find(ok, "add"); add == nil || !strings.Contains(add.Subtitle, "Press OK") {
		t.Fatalf("adding a finger: %+v", add)
	}
}
