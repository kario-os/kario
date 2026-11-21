package shell

import (
	"io"
	"log"
	"testing"

	"github.com/kario-os/kario/ui"
)

// Only the app a request went to answers it, once; its answer reaches the
// caller. A handler that ends, or a request left too long, cancels.
func TestIntentReplies(t *testing.T) {
	chat := &App{M: &Manifest{ID: "com.phone.chat", Name: "Chat"}, running: true}
	wallet := &App{M: &Manifest{ID: "com.phone.wallet", Name: "Wallet"}, running: true}
	other := &App{M: &Manifest{ID: "com.example.other"}, running: true}
	c := &Core{log: log.New(io.Discard, "", 0), apps: []*App{chat, wallet, other}}
	var got []any
	var gotErr []error
	reply := func(v any, err error) { got, gotErr = append(got, v), append(gotErr, err) }
	c.intents = map[string]*pendingIntent{"i1": {id: "i1", caller: chat, target: wallet, reply: reply, due: c.now().Add(intentTimeout)}}
	if err := c.replyIntent(other, ui.IntentReplyArgs{ID: "i1", OK: true}); err == nil {
		t.Fatal("another app answered")
	}
	if err := c.replyIntent(wallet, ui.IntentReplyArgs{ID: "i1", OK: true, Result: `{"paid":"¥10.00"}`}); err != nil {
		t.Fatal(err)
	}
	if res, ok := got[0].(ui.IntentRes); !ok || !res.OK || res.App != "com.phone.wallet" || res.Result == "" {
		t.Fatalf("the caller got %+v", got[0])
	}
	if err := c.replyIntent(wallet, ui.IntentReplyArgs{ID: "i1", OK: true}); err == nil {
		t.Fatal("answered twice")
	}
	c.intents["i2"] = &pendingIntent{id: "i2", caller: chat, target: wallet, reply: reply, due: c.now().Add(intentTimeout)}
	c.intents["i3"] = &pendingIntent{id: "i3", caller: chat, target: other, reply: reply, due: c.now().Add(-1)}
	c.endIntents(wallet)
	c.endIntents(nil)
	if len(c.intents) != 0 || len(gotErr) != 3 || !ui.Cancelled(gotErr[1]) || !ui.Cancelled(gotErr[2]) {
		t.Fatalf("left %d, answers %v", len(c.intents), gotErr)
	}
}

// Nothing handles an unknown action; the request says so.
func TestIntentWithoutHandler(t *testing.T) {
	chat := &App{M: &Manifest{ID: "com.phone.chat"}, running: true}
	c := &Core{log: log.New(io.Discard, "", 0), apps: []*App{chat}}
	var err error
	c.invokeIntent(chat, ui.IntentArgs{Action: "pay"}, func(_ any, e error) { err = e })
	if err == nil || err.Error() != ui.ErrNoHandler {
		t.Fatalf("got %v", err)
	}
}
