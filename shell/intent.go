package shell

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"time"

	"github.com/kario-os/kario/ui"
)

// Intents (Android's startActivityForResult, iOS's payment sheet handed to
// another app): an app asks for an action another app performs, such as
// paying from a chat with the wallet, and gets the result back. Apps declare
// the actions they handle in their manifest (intents); the system lists
// them (one is opened at once), opens the chosen one with an
// "intent:<json>" deep link naming the caller, and hands its intent.reply
// back to the caller, whom it brings back to the screen. The handler
// confirms with the user itself (the wallet with the trusted confirmation);
// the caller learns which app answered, nothing about the others.

// intentTimeout: a request the handler did not answer by then is cancelled.
const (
	intentTimeout    = 5 * time.Minute
	intentPayloadMax = 4 << 10
	maxIntents       = 8
)

var intentAction = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,31}$`)

// IntentDecl is an action an app's manifest says it handles.
type IntentDecl struct {
	Action string `json:"action"`
	Label  string `json:"label"`
}

func (m *Manifest) handles(action string) (IntentDecl, bool) {
	for _, d := range m.Intents {
		if d.Action == action {
			return d, true
		}
	}
	return IntentDecl{}, false
}

type pendingIntent struct {
	id             string
	caller, target *App
	reply          func(any, error)
	due            time.Time
}

func (c *Core) invokeIntent(a *App, args ui.IntentArgs, reply func(any, error)) {
	switch {
	case args.Action == "" || len(args.Action) > 32:
		reply(nil, errors.New("an intent needs an action of at most 32 bytes"))
		return
	case len(args.Payload) > intentPayloadMax || len([]rune(args.What)) > 64:
		reply(nil, errors.New("the payload is at most 4096 bytes, what at most 64 characters"))
		return
	}
	var targets []*App
	label := ""
	for _, t := range c.apps {
		if d, ok := t.M.handles(args.Action); ok && t != a {
			targets = append(targets, t)
			label = d.Label
		}
	}
	if len(targets) == 0 {
		reply(nil, errors.New(ui.ErrNoHandler))
		return
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].M.Order < targets[j].M.Order })
	open := func(t *App) {
		if t == nil {
			reply(nil, errors.New(ui.ErrCancelled))
			return
		}
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		p := &pendingIntent{id: hex.EncodeToString(b), caller: a, target: t, reply: reply, due: c.now().Add(intentTimeout)}
		if c.intents == nil {
			c.intents = map[string]*pendingIntent{}
		}
		c.intents[p.id] = p
		raw, _ := json.Marshal(ui.Intent{ID: p.id, Action: args.Action, Payload: args.Payload, From: a.M.ID, FromName: a.M.Name})
		c.log.Printf("%s: intent %s (%s) to %s", a.M.ID, args.Action, p.id, t.M.ID)
		c.Launch(t, ui.IntentPrefix+string(raw))
	}
	if len(targets) == 1 {
		open(targets[0])
		return
	}
	what := args.What
	if what == "" {
		what = args.Action
	}
	title := tr.T("Choose an app")
	if label != "" {
		title = tr.T("Which app to %s?", label)
	}
	c.pushOverlay(&shareOverlay{from: a, title: title, said: tr.T("“%s” asks to %s", a.M.Name, what), targets: targets, done: open})
}

// replyIntent is the handler's answer: only the app the request went to
// gives it, once; the caller comes back to the screen.
func (c *Core) replyIntent(t *App, args ui.IntentReplyArgs) error {
	p := c.intents[args.ID]
	if p == nil || p.target != t {
		return errors.New("no such intent for this app")
	}
	delete(c.intents, args.ID)
	if len(args.Result) > intentPayloadMax {
		args.Result = ""
		args.OK = false
	}
	p.reply(ui.IntentRes{App: t.M.ID, OK: args.OK, Result: args.Result}, nil)
	c.log.Printf("%s: intent %s answered by %s (ok %v)", p.caller.M.ID, p.id, t.M.ID, args.OK)
	if p.caller.running && c.fg == t {
		c.setForeground(p.caller)
	}
	return nil
}

// endIntents cancels the requests that went to t (it ended), or, with t
// nil, those past their time.
func (c *Core) endIntents(t *App) {
	now := c.now()
	for id, p := range c.intents {
		if p.target == t || (t == nil && now.After(p.due)) || !p.caller.running {
			delete(c.intents, id)
			p.reply(nil, errors.New(ui.ErrCancelled))
		}
	}
}
