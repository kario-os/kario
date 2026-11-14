package shell

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// Push goes through phone-pushd, which holds the device's one gateway
// connection; the shell decides what a message may do:
//   - title/body: a system notification, shown without waking the app
//     (one per thread; none while the app is in the foreground);
//   - data: handed to the app. A frozen app is thawed for pushWakeBudget, a
//     stopped one starts in the background; past pushWakesPerHour the data
//     waits until the app next comes to the foreground;
//   - kind "voip" (voip permission): always wakes the app, which must report
//     the call (a "call" notification) within pushVoIPBudget. An app that
//     misses voipStrikes times in voipStrikeWindow loses the privilege, as
//     on iOS: its VoIP pushes arrive as data pushes, within the wake budget;
//   - collapse_id: a newer push with the same key replaces the notification
//     and any undelivered data of an earlier one.
const (
	pushWakeBudget   = 3 * time.Second
	pushVoIPBudget   = 10 * time.Second
	pushWakesPerHour = 20
	voipStrikes      = 3
	voipStrikeWindow = 24 * time.Hour
)

type pushEvent struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Event  string          `json:"event"`
	Msg    *push.Message   `json:"msg"`
}

func (c *Core) pushSock() string { return filepath.Join(c.cfg.RunDir, svc.PushdSock) }

func (c *Core) startPush() {
	go func() {
		for !c.stopping.Load() {
			nc, err := net.Dial("unix", c.pushSock())
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			conn := wire.New(nc)
			_ = conn.Send(wire.RPC{ID: 1, Method: wire.Subscribe})
			_ = conn.Send(wire.RPC{ID: 2, Method: svc.PushPending})
			c.Post(func() { c.pushUp = true })
			for {
				var ev pushEvent
				if err := conn.Recv(&ev); err != nil {
					break
				}
				var msgs []push.Message
				switch {
				case ev.ID == 2:
					_ = json.Unmarshal(ev.Result, &msgs)
				case ev.Event == svc.PushEvent && ev.Msg != nil:
					msgs = append(msgs, *ev.Msg)
				}
				for _, m := range msgs {
					c.Post(func() { c.pushMsg(m) })
				}
			}
			nc.Close()
			c.Post(func() { c.pushUp = false })
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

func (c *Core) pushRPC(method string, params, out any) error {
	if _, err := os.Stat(c.pushSock()); err != nil {
		return errors.New("no push service")
	}
	return c.broker.rpc(svc.Pushd, method, params, out)
}

func (c *Core) pushMsg(m push.Message) {
	ack := func() { go func() { _ = c.pushRPC(svc.PushAck, map[string]string{"id": m.ID}, nil) }() }
	if c.pushSeen[m.ID] {
		ack()
		return
	}
	// The radio is up for this message: due jobs ride along.
	c.lastPushAt = c.now()
	defer c.runJobs()
	if c.pushSeen == nil {
		c.pushSeen = map[string]bool{}
	}
	c.pushSeen[m.ID] = true
	defer ack()
	a := c.appByID(m.App)
	if a == nil {
		c.log.Printf("push %s for unknown app %s dropped", m.ID, m.App)
		return
	}
	// A foreground app gets the push silently and decides itself how to
	// show it (iOS does the same).
	notified := m.Title != "" && c.fg != a && a.M.Has(ui.PermNotify) && !c.notifsOff(a)
	if notified {
		id := "push:" + m.ID
		switch {
		case m.Collapse != "":
			id = "collapse:" + m.Collapse
		case m.Thread != "":
			id = "thread:" + m.Thread
		}
		c.notify(a, ui.Notification{ID: id, Title: m.Title, Body: m.Body, Actions: a.M.NotifyActions})
	}
	if m.Collapse != "" {
		c.dropCollapsed(a, m.Collapse)
	}
	if m.Data == "" && m.Kind != "voip" {
		return
	}
	voip := m.Kind == "voip"
	if voip && !a.M.Has(ui.PermVoIP) {
		c.log.Printf("push %s: voip push for %s without voip permission dropped", m.ID, a.M.ID)
		return
	}
	if voip && c.voipDemoted(a) {
		c.log.Printf("push %s: %s missed reporting calls %d times; voip push delivered as data", m.ID, a.M.ID, len(a.voipStrikes))
		voip = false
	}
	if voip && a.voipDue.IsZero() {
		a.voipDue = c.now().Add(pushVoIPBudget)
	}
	if m.Data != "" {
		if c.pushSealed == nil {
			c.pushSealed = map[string]int{}
		}
		if push.IsSealed(m.Data) {
			c.pushSealed[a.M.ID]++
		}
	}
	raw, _ := json.Marshal(ui.PushEvent{ID: m.ID, Kind: m.Kind, Data: m.Data, Sent: m.Sent, Notified: notified, Collapse: m.Collapse})
	ev := ui.Msg{T: ui.MsgEvent, Event: ui.EventPush, Value: string(raw)}
	if m.Priority == "normal" && !voip && !(a.running && !a.frozen && a.ready) {
		// It waits for a moment the app is awake anyway (the foreground,
		// another wake), at the latest normalPushWait.
		a.pushQ = append(a.pushQ, ev)
		if a.normalDue.IsZero() {
			a.normalDue = c.now().Add(normalPushWait)
		}
		c.log.Printf("push %s for %s: normal priority, held", m.ID, a.M.ID)
		return
	}
	budget := map[bool]time.Duration{false: pushWakeBudget, true: pushVoIPBudget}[voip]
	c.deliverBackground(a, ev, budget, voip, "push "+m.ID)
}

// normalPushWait is the longest normal-priority data waits before the system
// wakes the app for it (the same window as background jobs).
const normalPushWait = 15 * time.Minute

// checkNormalPush wakes apps whose held normal-priority data waited long
// enough; one wake delivers all of it, within the wake budget.
func (c *Core) checkNormalPush() {
	now := c.now()
	for _, a := range c.apps {
		if a.normalDue.IsZero() || now.Before(a.normalDue) {
			continue
		}
		a.normalDue = time.Time{}
		if len(a.pushQ) == 0 {
			continue
		}
		last := a.pushQ[len(a.pushQ)-1]
		a.pushQ = a.pushQ[:len(a.pushQ)-1]
		c.deliverBackground(a, last, pushWakeBudget, false, "held normal-priority pushes")
	}
}

// dropCollapsed removes undelivered pushes the newer one with key replaces.
func (c *Core) dropCollapsed(a *App, key string) {
	out := a.pushQ[:0]
	for _, ev := range a.pushQ {
		var pe ui.PushEvent
		if ev.Event == ui.EventPush && json.Unmarshal([]byte(ev.Value), &pe) == nil && pe.Collapse == key {
			c.log.Printf("push %s for %s replaced (collapse_id %s)", pe.ID, a.M.ID, key)
			continue
		}
		out = append(out, ev)
	}
	a.pushQ = out
}

// voipDemoted: the app keeps taking VoIP wakes without reporting calls.
func (c *Core) voipDemoted(a *App) bool {
	cut := c.now().Add(-voipStrikeWindow)
	kept := a.voipStrikes[:0]
	for _, t := range a.voipStrikes {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	a.voipStrikes = kept
	return len(kept) >= voipStrikes
}

// checkVoIP counts a strike for every app past its deadline to report a call.
func (c *Core) checkVoIP() {
	now := c.now()
	for _, a := range c.apps {
		if a.voipDue.IsZero() || now.Before(a.voipDue) {
			continue
		}
		a.voipDue = time.Time{}
		a.voipStrikes = append(a.voipStrikes, now)
		c.log.Printf("%s was woken by a voip push but reported no call (%d in %s)", a.M.ID, len(a.voipStrikes), voipStrikeWindow)
	}
}

// deliverBackground hands an event to an app that may be frozen or not
// running (push data, scheduled jobs): within the hourly wake budget it is
// thawed for budget, or started in the background; past it the event waits
// until the app comes to the foreground.
func (c *Core) deliverBackground(a *App, ev ui.Msg, budget time.Duration, unlimited bool, why string) {
	switch {
	case a.running && !a.frozen && a.ready:
		a.send(ev)
		c.wake(a, budget)
	case !unlimited && !c.pushBudget(a):
		c.log.Printf("%s: %s over its wake budget, held until foreground", why, a.M.ID)
		a.pushQ = append(a.pushQ, ev)
	case a.running:
		a.pushQ = append(a.pushQ, ev)
		c.wake(a, budget)
		c.flushPush(a)
	case c.crashLooping(a):
		c.log.Printf("%s: %s keeps crashing; not started in the background, held until foreground", why, a.M.ID)
		a.pushQ = append(a.pushQ, ev)
	default:
		a.pushQ = append(a.pushQ, ev)
		a.arg = ""
		if err := c.spawn(a); err != nil {
			c.log.Printf("%s: start %s: %v", why, a.M.ID, err)
			return
		}
		c.log.Printf("%s: started %s in the background", why, a.M.ID)
		c.wake(a, budget)
	}
}

func (c *Core) pushBudget(a *App) bool {
	cut := time.Now().Add(-time.Hour)
	kept := a.pushWakes[:0]
	for _, t := range a.pushWakes {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	a.pushWakes = kept
	if len(kept) >= pushWakesPerHour {
		return false
	}
	a.pushWakes = append(a.pushWakes, time.Now())
	return true
}

// wake runs a background app for about d, then lets it freeze again.
func (c *Core) wake(a *App, d time.Duration) {
	if a == c.fg {
		return
	}
	if a.frozen && a.proc != nil {
		_ = c.freezeApp(a, false)
		c.log.Printf("woke %s for %s", a.M.ID, d)
	}
	a.frozen = false
	// A wake never shortens one the app is in already.
	if until := time.Now().Add(d - c.freezeAfter()); until.After(a.bgSince) {
		a.bgSince = until
	}
}

// pushKeyChanged tells every running app, frozen ones too, that the keystore
// replaced its push key. Apps not running fetch the key when they start.
func (c *Core) pushKeyChanged() {
	ev := ui.Msg{T: ui.MsgEvent, Event: ui.EventPushKeyChanged}
	for _, a := range c.apps {
		if a.running {
			c.deliverBackground(a, ev, pushWakeBudget, true, "push key changed")
		}
	}
}

// deliver sends m now, or after start while the app is still starting (a
// message ahead of start would reach an app that has no pages yet).
func (c *Core) deliver(a *App, m ui.Msg) {
	if a.ready {
		a.send(m)
		return
	}
	a.pushQ = append(a.pushQ, m)
}

func (c *Core) flushPush(a *App) {
	if !a.ready || !a.running {
		return
	}
	for _, ev := range a.pushQ {
		a.send(ev)
	}
	a.pushQ, a.normalDue = nil, time.Time{}
}

func (c *Core) HasPush() bool { return c.pushUp }

// SealedPushes counts the data pushes an app received end-to-end encrypted
// (selftest: the shell only ever handled ciphertext).
func (c *Core) SealedPushes(app string) int { return c.pushSealed[app] }

// TestPush is how SendTestPush sends: the kind ("" or "voip"), the priority,
// and whether the data is sealed to the app's push key as its server would.
type TestPush struct {
	Kind, Priority string
	Seal           bool
	// Other posts to a gateway instance the device is not connected to.
	Other bool
}

// PushGateways lists the gateway instances pushd knows and the one it is
// connected to ("" when offline).
func (c *Core) PushGateways() ([]string, string) {
	var st struct {
		Gateway   string   `json:"gateway"`
		Gateways  []string `json:"gateways"`
		Connected bool     `json:"connected"`
	}
	if err := c.pushRPC(svc.PushState, nil, &st); err != nil {
		return nil, ""
	}
	if !st.Connected {
		st.Gateway = ""
	}
	return st.Gateways, st.Gateway
}

// SendTestPush plays an app server: it gets the app's token from pushd and
// posts to the gateway, so the message travels the whole production path.
// Fields: title, body, data (any may be empty).
func (c *Core) SendTestPush(appID string, o TestPush, fields ...string) error {
	var tok struct {
		Token string `json:"token"`
	}
	if err := c.pushRPC(svc.PushRegister, map[string]string{"app": appID}, &tok); err != nil {
		return err
	}
	var st struct {
		Gateway  string   `json:"gateway"`
		Gateways []string `json:"gateways"`
		Cert     string   `json:"gateway_cert"`
	}
	// target re-reads the instances: one that was just restarted listens on
	// a new port.
	target := func() (string, error) {
		if err := c.pushRPC(svc.PushState, nil, &st); err != nil || st.Gateway == "" {
			return "", fmt.Errorf("push gateway unknown: %v", err)
		}
		if o.Other {
			for _, gw := range st.Gateways {
				if gw != st.Gateway {
					return gw, nil
				}
			}
		}
		return st.Gateway, nil
	}
	if _, err := target(); err != nil {
		return err
	}
	client, err := push.Client(st.Cert, 10*time.Second)
	if err != nil {
		return err
	}
	f := append(fields, "", "", "")
	if o.Seal && f[2] != "" {
		// The app's server would have the key from the app (push.key).
		var k ui.PushKeyRes
		if err := c.broker.rpc(svc.Keystored, svc.KeyPushKey, map[string]string{"app": appID}, &k); err != nil {
			return err
		}
		sealed, err := push.Seal(k.Key, []byte(f[2]))
		if err != nil {
			return err
		}
		f[2] = sealed
	}
	raw, _ := json.Marshal(push.Send{Token: tok.Token, Kind: o.Kind, Priority: o.Priority, Title: f[0], Body: f[1], Data: f[2]})
	var resp *http.Response
	for try := 0; ; try++ {
		gw, terr := target()
		if terr != nil {
			return terr
		}
		if resp, err = client.Post(gw+"/v1/push", "application/json", bytes.NewReader(raw)); err == nil || try == 20 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("gateway: %s %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// radioActive tells pushd the radio is up for other traffic, so the push
// connection's keep-alive can ride along (off the event loop).
func (c *Core) radioActive() {
	go func() { _ = c.pushRPC(svc.PushRadioActive, nil, nil) }()
}
