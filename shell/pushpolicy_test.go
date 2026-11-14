package shell

import (
	"encoding/json"
	"io"
	"log"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

func TestVoIPStrikes(t *testing.T) {
	a := &App{M: &Manifest{ID: "com.example.voip"}}
	c := &Core{log: log.New(io.Discard, "", 0), apps: []*App{a}}
	for i := 0; i < voipStrikes; i++ {
		if c.voipDemoted(a) {
			t.Fatalf("demoted after %d strikes", i)
		}
		a.voipDue = c.now().Add(-time.Second)
		c.checkVoIP()
	}
	if !c.voipDemoted(a) {
		t.Fatal("not demoted after repeated misses")
	}
	a.voipDue = c.now().Add(time.Minute)
	c.checkVoIP()
	if len(a.voipStrikes) != voipStrikes {
		t.Fatal("a strike before the deadline")
	}
	c.clockSkew = voipStrikeWindow + time.Hour
	if c.voipDemoted(a) {
		t.Fatal("old strikes still count")
	}
}

func TestDropCollapsed(t *testing.T) {
	ev := func(id, key string) ui.Msg {
		raw, _ := json.Marshal(ui.PushEvent{ID: id, Data: "d", Collapse: key})
		return ui.Msg{T: ui.MsgEvent, Event: "push", Value: string(raw)}
	}
	a := &App{M: &Manifest{ID: "com.example.a"}, pushQ: []ui.Msg{ev("1", "unread"), ev("2", ""), ev("3", "score")}}
	c := &Core{log: log.New(io.Discard, "", 0)}
	c.dropCollapsed(a, "unread")
	if len(a.pushQ) != 2 {
		t.Fatalf("%d left, want 2", len(a.pushQ))
	}
	for _, m := range a.pushQ {
		var pe ui.PushEvent
		_ = json.Unmarshal([]byte(m.Value), &pe)
		if pe.Collapse == "unread" {
			t.Fatal("collapsed push kept")
		}
	}
}

// After the keystore was cleared every running app hears that its push key
// changed, even one over its wake budget; stopped apps are not started.
func TestPushKeyChanged(t *testing.T) {
	busy := &App{M: &Manifest{ID: "com.example.busy"}, running: true}
	for i := 0; i < pushWakesPerHour; i++ {
		busy.pushWakes = append(busy.pushWakes, time.Now())
	}
	stopped := &App{M: &Manifest{ID: "com.example.stopped"}}
	c := &Core{log: log.New(io.Discard, "", 0), apps: []*App{busy, stopped}}
	c.pushKeyChanged()
	if len(busy.pushQ) != 1 || busy.pushQ[0].Event != ui.EventPushKeyChanged {
		t.Fatalf("the running app gets %+v", busy.pushQ)
	}
	if stopped.running || len(stopped.pushQ) != 0 {
		t.Fatal("a stopped app was started for the new key")
	}
}
