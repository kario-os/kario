package main

import (
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Privacy and security › Blocked numbers (32-messaging.md §6):
// the system's block list for calls and messages, and the words that send
// a message to Blocked, which Phone and the default SMS app change too.

type blockInfo struct {
	Blocking ui.BlockList `json:"blocking"`
}

func (p *app) blockingItem() ui.Node {
	n := len(p.s.Blocking.Numbers)
	return ui.Node{Kind: ui.KindItem, ID: "blocking", Title: tr.T("Blocked numbers"), Subtitle: tr.T("%d numbers · calls and messages", n),
		Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func (p *app) blockingScreen() ui.Screen {
	b := p.s.Blocking
	s := ui.Screen{ID: "blocking", Title: tr.T("Blocked numbers"), Actions: []ui.Action{{ID: "block-add", Label: tr.T("Block a number"), Key: "a"},
		{ID: "word-add", Label: tr.T("Block a word"), Key: "w"}}, Body: []ui.Node{
		{Kind: ui.KindItem, ID: "block-hidden", Title: tr.T("Block hidden numbers"), Subtitle: tr.T("Callers who withhold their number"),
			Trailing: onOff(b.Hidden), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
		{Kind: ui.KindItem, ID: "block-unknown", Title: tr.T("Block numbers not in Contacts"), Subtitle: tr.T("Short codes and named senders too"),
			Trailing: onOff(b.Unknown), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
		sdk.Section(tr.T("Blocked numbers (%d)", len(b.Numbers))),
	}}
	for _, e := range b.Numbers {
		title := ui.NumberText(e)
		if pre, ok := strings.CutSuffix(e, "*"); ok {
			title = tr.T("Numbers starting with %s", pre)
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "blocked:" + e, Title: title,
			Actions: []ui.Action{{ID: "unblock", Label: tr.T("Unblock"), Primary: true}}})
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Calls from these numbers are declined without ringing and listed in Phone as blocked; their messages go to Blocked in Messages, without a notification.")))
	s.Body = append(s.Body, sdk.Section(tr.T("Blocked words (%d)", len(b.Words))))
	for _, w := range b.Words {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "word:" + w, Title: w,
			Actions: []ui.Action{{ID: "unblock-word", Label: tr.T("Unblock"), Primary: true}}})
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("A message holding one of these words goes to Blocked in Messages, whoever sent it; case, spaces and marks between the letters do not matter.")))
	return s
}

func (p *app) wordAddScreen() ui.Screen {
	return ui.Screen{ID: "word-add", Title: tr.T("Block a word"), Focus: "word", Body: []ui.Node{
		{Kind: ui.KindInput, ID: "word", Placeholder: tr.T("A word or words"), Submit: "word-save"},
		sdk.Button("save", tr.T("Block"), "word-save"),
	}}
}

func (p *app) blockAddScreen() ui.Screen {
	return ui.Screen{ID: "block-add", Title: tr.T("Block a number"), Focus: "number", Body: []ui.Node{
		{Kind: ui.KindInput, ID: "number", Placeholder: tr.T("A number, or a prefix ending in *"), Submit: "block-save"},
		sdk.Button("save", tr.T("Block"), "block-save"),
	}}
}

// setBlock changes the list and shows the answer.
func (p *app) setBlock(args ui.BlockSetArgs) {
	var bl ui.BlockList
	p.a.Go(ui.CapBlockSet, args, &bl, func(err error) {
		if err != nil {
			p.a.Toast(err.Error())
			return
		}
		p.s.Blocking = bl
		p.a.Update(p.blockingScreen())
		p.updateMenu()
	})
}

func (p *app) blockingAction(e sdk.ActionEvent) bool {
	b := p.s.Blocking
	switch {
	case onMenu(e.Screen) && e.Node == "blocking":
		p.push(p.blockingScreen())
	case e.Screen == "blocking" && e.Action == "toggle" && e.Node == "block-hidden":
		on := !b.Hidden
		p.setBlock(ui.BlockSetArgs{Hidden: &on})
	case e.Screen == "blocking" && e.Action == "toggle" && e.Node == "block-unknown":
		on := !b.Unknown
		p.setBlock(ui.BlockSetArgs{Unknown: &on})
	case e.Screen == "blocking" && e.Action == "unblock":
		p.setBlock(ui.BlockSetArgs{Number: strings.TrimPrefix(e.Node, "blocked:")})
	case e.Screen == "blocking" && e.Action == "unblock-word":
		p.setBlock(ui.BlockSetArgs{Word: strings.TrimPrefix(e.Node, "word:")})
	case e.Action == "word-add":
		p.a.Push(p.wordAddScreen())
	case e.Action == "word-save":
		if w := strings.TrimSpace(e.Fields["word"]); w != "" {
			p.a.Pop()
			p.setBlock(ui.BlockSetArgs{Word: w, On: true})
		}
	case e.Action == "block-add":
		p.a.Push(p.blockAddScreen())
	case e.Action == "block-save":
		if n := strings.TrimSpace(e.Fields["number"]); n != "" {
			p.a.Pop()
			p.setBlock(ui.BlockSetArgs{Number: n, On: true})
		}
	default:
		return false
	}
	return true
}
