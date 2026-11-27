package main

import (
	"testing"

	"github.com/kario-os/kario/ui"
)

// Settings › Battery lists the apps' own connections with their wakes and
// charge; one with no CPU to speak of is not in the CPU list.
func TestBatteryOwnConnections(t *testing.T) {
	p := &app{}
	p.s.Energy = []ui.EnergyEntry{
		{App: "com.phone.calc", Name: "Calculator", BackgroundMS: 40000},
		{App: "org.example.chat", Name: "Chat", Keepalive: &ui.KeepaliveUse{Own: 3, Shared: 40, Data: 2, MAh: 2.3}},
	}
	s := p.batteryScreen()
	if node(s, "e:org.example.chat").ID != "" || node(s, "e:com.phone.calc").ID == "" {
		t.Fatalf("the CPU list: %+v", s.Body)
	}
	k := node(s, "k:org.example.chat")
	if k.Trailing != tr.T("about %.1f mAh", 2.3) || k.Subtitle != tr.T("Woke the phone %d times · %d heartbeats rode other wakes · woken %d times for data", 3, 40, 2) {
		t.Fatalf("the own connection: %+v", k)
	}

	p.s.Energy = p.s.Energy[:1]
	if s := p.batteryScreen(); node(s, "k:org.example.chat").ID != "" {
		t.Fatal("an own connection without any app having one")
	}
}
