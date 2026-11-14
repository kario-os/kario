package shell

import (
	"fmt"
	"io"
	"log"
	"testing"

	"github.com/kario-os/kario/ui"
)

// An app's own action from the notification centre: one asking for text
// (a quick reply) opens the reply screen; sending hands the text to the
// app in the background and clears the notification.
func TestQuickReply(t *testing.T) {
	app := &App{M: &Manifest{ID: "com.example.chat", Name: "C", Permissions: []string{ui.PermNotify}}}
	c := &Core{log: log.New(io.Discard, "", 0), sys: &App{system: true, M: &Manifest{ID: "system"}}}
	c.sys.stack = []*View{newView(ui.Screen{ID: "sys.home"})}
	c.notify(app, ui.Notification{ID: "thread:z", Title: "Z", Body: "hi",
		Actions: []ui.Action{{ID: "reply", Label: "Reply", Key: "r", Input: "Reply to Z"}}})
	c.openSystem("sys.notifs")
	c.Key(Key{Name: KeyRunes, Runes: []rune("r")})
	top := c.sys.top()
	if top.S.ID != "sys.reply" {
		t.Fatalf("after r the screen is %s", top.S.ID)
	}
	top.inputs["text"] = "OK"
	c.systemAction(top, nil, ui.Action{ID: "reply_ok"})
	if id := c.sys.top().S.ID; id != "sys.notifs" {
		t.Fatalf("after sending the screen is %s", id)
	}
	if len(c.notifs) != 0 {
		t.Fatalf("the notification stayed: %d", len(c.notifs))
	}
	if len(app.pushQ) != 1 || app.pushQ[0].T != ui.MsgNotifAction || app.pushQ[0].Action != "reply" ||
		app.pushQ[0].Value != "thread:z" || app.pushQ[0].Text != "OK" {
		t.Fatalf("the app gets %+v", app.pushQ)
	}
}

func TestNotificationLimits(t *testing.T) {
	spam := &App{M: &Manifest{ID: "com.example.spam", Name: "S", Permissions: []string{ui.PermNotify}}}
	other := &App{M: &Manifest{ID: "com.example.other", Name: "O", Permissions: []string{ui.PermNotify}}}
	c := &Core{log: log.New(io.Discard, "", 0)}
	c.notify(other, ui.Notification{ID: "keep", Title: "keep"})
	for i := 0; i < 40; i++ {
		c.notify(spam, ui.Notification{ID: fmt.Sprint(i), Title: "spam"})
	}
	if len(spam.alerts) != notifyAlertsPerMinute {
		t.Fatalf("%d alerts in a minute, want %d", len(spam.alerts), notifyAlertsPerMinute)
	}
	n := 0
	for _, x := range c.notifs {
		if x.app == spam {
			n++
		}
	}
	if n != maxNotifsPerApp || c.notifs[len(c.notifs)-1].app != other {
		t.Fatalf("%d kept for the spammer; the other app's notification must stay", n)
	}
	if c.notifs[0].n.ID != "39" {
		t.Fatalf("newest first, got %s", c.notifs[0].n.ID)
	}
}
