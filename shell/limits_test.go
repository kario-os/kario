package shell

import (
	"io"
	"log"
	"testing"

	"github.com/kario-os/kario/ui"
)

func TestScreenLimits(t *testing.T) {
	a := &App{M: &Manifest{ID: "com.example.big"}}
	c := &Core{log: log.New(io.Discard, "", 0)}
	big := ui.Screen{ID: "big"}
	for range maxScreenNodes * 3 {
		big.Body = append(big.Body, ui.Node{Kind: ui.KindText, Text: "x"})
	}
	c.appMsg(a, ui.Msg{T: ui.MsgReset, Screen: &big})
	body := a.stack[0].S.Body
	if len(body) != maxScreenNodes || body[len(body)-1].Text != tr.T("(Too long; only the first %d items are shown)", maxScreenNodes-1) {
		t.Fatalf("screen of %d nodes kept %d", maxScreenNodes*3, len(body))
	}
	for i := range maxStackDepth * 2 {
		c.appMsg(a, ui.Msg{T: ui.MsgPush, Screen: &ui.Screen{ID: string(rune('a' + i%26))}})
	}
	if len(a.stack) != maxStackDepth || a.stack[0].S.ID != "big" {
		t.Fatalf("stack of %d, root %s", len(a.stack), a.stack[0].S.ID)
	}
}
