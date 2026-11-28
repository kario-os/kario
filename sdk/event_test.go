package sdk

import (
	"testing"

	"github.com/kario-os/kario/ui"
)

// An event's text (why a keepalive was lost) reaches OnEventText; an app
// with only OnEvent still hears the event.
func TestEventText(t *testing.T) {
	var got []string
	a := &App{}
	a.OnEvent = func(name, value string) { got = append(got, "event "+name+" "+value) }
	a.OnEventText = func(name, value, text string) { got = append(got, "text "+name+" "+value+" "+text) }
	lost := ui.Msg{T: ui.MsgEvent, Event: ui.EventKeepalive, Value: ui.KeepaliveLost, Text: ui.KeepaliveLostFull}
	a.dispatch(lost)
	a.dispatch(ui.Msg{T: ui.MsgEvent, Event: ui.EventKeepalive, Value: ui.KeepaliveData})
	a.OnEventText = nil
	a.dispatch(lost)
	want := []string{
		"text keepalive lost full",
		"event keepalive data",
		"event keepalive lost",
	}
	if len(got) != len(want) {
		t.Fatalf("heard %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("heard %q, want %q", got, want)
			break
		}
	}
}
