package shell

import (
	"encoding/json"
	"io"
	"log"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// Only the low-memory killer leaves a tombstone; a rebuilt page gets the
// typing back, the old pages stop showing at the first new one, and a day
// later nothing is restored.
func TestTombstones(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0)}
	page := ui.Screen{ID: "chat:zs", Body: []ui.Node{{Kind: ui.KindInput, ID: "compose"}, {Kind: ui.KindButton, ID: "send"}}}
	a := &App{M: &Manifest{ID: "com.phone.chat"}}
	a.stack = []*View{newView(ui.Screen{ID: "list"}), newView(page)}
	a.stack[1].inputs["compose"], a.stack[1].focus = "see you tomorrow", "compose"
	a.saved = json.RawMessage(`{"open":"zs"}`)

	c.bury(a)
	if a.tomb != nil || a.saved != nil {
		t.Fatal("a crash or a kill left a tombstone")
	}

	a.saved, a.ending = json.RawMessage(`{"open":"zs"}`), true
	c.bury(a)
	if a.tomb == nil || string(a.tomb.state) != `{"open":"zs"}` {
		t.Fatalf("tombstone %+v", a.tomb)
	}
	a.stack, a.ending = nil, false

	if got := string(c.exhume(a)); got != `{"open":"zs"}` || !a.stale || len(a.stack) != 2 {
		t.Fatalf("exhumed %q, stale %v, %d pages", got, a.stale, len(a.stack))
	}
	a.stack = []*View{a.page(ui.Screen{ID: "list"})}
	if a.stale {
		t.Fatal("the old pages still show after the first new one")
	}
	v := a.page(page)
	if v.inputs["compose"] != "see you tomorrow" || v.focus != "compose" {
		t.Fatalf("rebuilt page: input %q, focus %q", v.inputs["compose"], v.focus)
	}

	a.tomb = &tombstone{state: json.RawMessage(`{}`), views: []*View{newView(page)}, at: c.now().Add(-tombKeep - time.Minute)}
	if c.exhume(a) != nil || a.tomb != nil {
		t.Fatal("a day-old tombstone came back")
	}
}
