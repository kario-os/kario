package shell

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kario-os/kario/audio"
	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// Messages through phone-smsd (32-messaging.md). smsd keeps the messages
// and takes what telephonyd receives; the shell subscribes to it: a new
// message becomes a notification of the Messages app (with "Copy code"
// when smsd found a verification code), a message that could not be sent
// one too, and an emergency alert is a system screen over everything, the
// lock screen included, with its own sound. Apps reach messages through
// the broker; deleting and the block list are the default SMS app's, the
// system's Messages, alone.

const smsCopyCode = "copy_code:"

type smsShell struct {
	up bool
	// alerts: the emergency alerts shown (selftest).
	alerts int
	// shown: the new messages and alerts handled, which smsd may still
	// offer once more (an event and the unnotified list can cross).
	shown map[string]bool
}

func (c *Core) smsSock() string { return filepath.Join(c.cfg.RunDir, svc.SmsdSock) }

// HasSMS: phone-smsd is there.
func (c *Core) HasSMS() bool { return c.sms.up }

func (c *Core) startSMS() {
	go func() {
		for !c.stopping.Load() {
			nc, err := net.Dial("unix", c.smsSock())
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			conn := wire.New(nc)
			_ = conn.Send(wire.RPC{ID: 1, Method: wire.Subscribe})
			_ = conn.Send(wire.RPC{ID: 2, Method: svc.SMSUnnotified})
			c.Post(func() { c.sms.up = true })
			for {
				var ev struct {
					ID     int             `json:"id"`
					OK     bool            `json:"ok"`
					Result json.RawMessage `json:"result"`
					svc.SMSEvent
				}
				if err := conn.Recv(&ev); err != nil {
					break
				}
				if ev.ID == 2 {
					var res svc.SMSUnnotifiedRes
					if ev.OK && json.Unmarshal(ev.Result, &res) == nil {
						c.Post(func() { c.smsUnnotified(res) })
					}
					continue
				}
				c.Post(func() { c.smsEvent(ev.SMSEvent) })
			}
			nc.Close()
			c.Post(func() { c.sms.up = false })
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

func (c *Core) smsEvent(ev svc.SMSEvent) {
	switch {
	case ev.Event == svc.SMSEventMessage && ev.SMS != nil:
		c.smsMessage(*ev.SMS, ev.New)
	case ev.Event == svc.SMSEventChanged:
		if a := c.appByID(appMessages); a != nil && a.running {
			a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventSMS, Value: "{}"})
		}
	case ev.Event == svc.SMSEventAlert && ev.Alert != nil:
		c.smsAlert(*ev.Alert)
	}
}

// smsUnnotified: what came while the shell was not subscribed.
func (c *Core) smsUnnotified(res svc.SMSUnnotifiedRes) {
	for _, m := range res.Messages {
		c.smsMessage(m, true)
	}
	for _, a := range res.Alerts {
		c.smsAlert(a)
	}
}

// smsShown records a new message or alert as handled and tells smsd; false
// when it already was.
func (c *Core) smsShown(id string) bool {
	if c.sms.shown[id] {
		return false
	}
	if c.sms.shown == nil || len(c.sms.shown) > 500 {
		c.sms.shown = map[string]bool{}
	}
	c.sms.shown[id] = true
	go func() { _ = c.smsRPC(svc.SMSNotified, map[string][]string{"ids": {id}}, nil) }()
	return true
}

func (c *Core) smsMessage(m ui.SMS, isNew bool) {
	m.Name = c.contactName(m.Peer)
	a := c.appByID(appMessages)
	if a == nil {
		return
	}
	if isNew && !c.smsShown(m.ID) {
		return
	}
	who := firstNonEmpty(m.Name, m.Peer)
	switch {
	case isNew:
		n := ui.Notification{ID: "sms:" + m.Thread, Title: who, Body: m.Text}
		if m.Code != "" {
			n.Actions = []ui.Action{{ID: smsCopyCode + m.Code, Label: tr.T("Copy code %s", m.Code), Key: "c"}}
		}
		c.notifyAs(a, n)
	case m.Dir == "out" && m.State == ui.SMSFailed:
		c.notifyAs(a, ui.Notification{ID: "sms:" + m.Thread, Title: who, Body: tr.T("Message not sent: %s", m.Text)})
	}
	if a.running {
		raw, _ := json.Marshal(m)
		a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventSMS, Value: string(raw)})
	}
}

// smsNotifAction copies a verification code from its notification; the
// Messages app need not start for it.
func (c *Core) smsNotifAction(n *notif, action string) bool {
	code, ok := strings.CutPrefix(action, smsCopyCode)
	if !ok || n.app.M.ID != appMessages {
		return false
	}
	_ = c.clipSet(n.app, code)
	return true
}

// alertOn: whether an emergency alert of this level is shown; the highest
// level always is, tests only when switched on (Settings).
func (c *Core) alertOn(level string) bool {
	if level == ui.AlertPresidential {
		return true
	}
	if on, ok := c.settings.Alerts[level]; ok {
		return on
	}
	return level != ui.AlertTest
}

// alertSettings are Settings › Emergency alerts' switches.
var alertSettings = []struct{ key, level string }{
	{"", ui.AlertPresidential},
	{ui.SettingAlertExtreme, ui.AlertExtreme},
	{ui.SettingAlertSevere, ui.AlertSevere},
	{ui.SettingAlertAmber, ui.AlertAmber},
	{ui.SettingAlertTest, ui.AlertTest},
}

func (c *Core) alertsView() []map[string]any {
	var out []map[string]any
	for _, s := range alertSettings {
		out = append(out, map[string]any{"level": s.level, "key": s.key, "name": alertTitle(s.level), "on": c.alertOn(s.level), "locked": s.key == ""})
	}
	return out
}

func (c *Core) setAlert(key string, v float64) error {
	for _, s := range alertSettings {
		if s.key == key && key != "" {
			if c.settings.Alerts == nil {
				c.settings.Alerts = map[string]bool{}
			}
			c.settings.Alerts[s.level] = v != 0
			return nil
		}
	}
	return errors.New("no such alert level")
}

func alertTitle(level string) string {
	switch level {
	case ui.AlertPresidential:
		return tr.T("National alert")
	case ui.AlertExtreme:
		return tr.T("Extreme alert")
	case ui.AlertSevere:
		return tr.T("Severe alert")
	case ui.AlertAmber:
		return tr.T("AMBER alert")
	}
	return tr.T("Test alert")
}

// smsAlert shows an emergency alert smsd stored, unless its level is off.
func (c *Core) smsAlert(al ui.SMSAlert) {
	if !c.smsShown(al.ID) {
		return
	}
	if !c.alertOn(al.Level) {
		c.log.Printf("emergency alert %s (%s) not shown: switched off", al.ID, al.Level)
		return
	}
	c.sms.alerts++
	c.pushOverlay(&alertOverlay{alert: al, at: time.Now()})
	c.sound("alert:"+al.ID, "alert", audio.Alert)
	for i := range 3 {
		time.AfterFunc(time.Duration(i)*1200*time.Millisecond, func() { c.Post(func() { c.vibrateMS(800) }) })
	}
}

// AlertsShown counts the emergency alerts shown (selftest).
func (c *Core) AlertsShown() int { return c.sms.alerts }

// alertOverlay is an emergency alert: over every app and the lock screen
// until OK or Back.
type alertOverlay struct {
	alert ui.SMSAlert
	at    time.Time
}

func (o *alertOverlay) key(c *Core, k Key) {
	if k.Name == KeyEnter || k.Name == KeyEsc {
		c.quiet("alert:" + o.alert.ID)
		c.popOverlay(o)
	}
}

func (o *alertOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	bg := cv.RGB{R: 96, G: 14, B: 14}
	can.Fill(0, 1, W, H-1, st(colFG, bg))
	center := func(y int, s string, sty cv.Style) { can.Text((W-cv.StringWidth(s))/2, y, s, sty, W) }
	center(3, tr.T("Emergency alert"), st(colWarn, bg))
	center(5, alertTitle(o.alert.Level), bold(st(colFG, bg)))
	for i, l := range cv.Wrap(o.alert.Text, W-4) {
		if 7+i >= H-4 {
			break
		}
		can.Text(2, 7+i, l, st(colFG, bg), W-4)
	}
	center(H-3, tr.T("Enter or Esc dismiss"), bold(st(colFG, bg)))
}

func (o *alertOverlay) frame(c *Core) *FrameOverlay {
	return &FrameOverlay{Kind: "alarm", Title: alertTitle(o.alert.Level), Lines: []string{tr.T("Emergency alert"), o.alert.Text},
		Choices: []string{tr.T("Dismiss")}, Phase: float64(time.Since(o.at).Milliseconds()%1000) / 1000,
		Footer: []string{tr.T("OK or Back dismisses")}}
}

// SMSKill ends phone-smsd abruptly (its supervisor starts it again).
func (c *Core) SMSKill() error {
	var st svc.SMSStateRes
	if err := c.smsRPC(svc.SMSState, nil, &st); err != nil {
		return err
	}
	if st.Pid == 0 {
		return errors.New("phone-smsd did not say its process id")
	}
	return syscall.Kill(st.Pid, syscall.SIGKILL)
}

// TelKill ends phone-telephonyd abruptly and waits until its supervisor's
// new process answers (selftest). Its process id is the socket's peer: it
// is not socket-activated, so that is not the service manager.
func (c *Core) TelKill(wait func(cond func() bool) bool) error {
	pid, err := c.telPid()
	if err != nil {
		return err
	}
	if pid <= 1 || pid == os.Getpid() {
		return errors.New("phone-telephonyd's socket belongs to another process")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return err
	}
	if !wait(func() bool { p, err := c.telPid(); return err == nil && p != pid }) {
		return errors.New("phone-telephonyd did not come back")
	}
	return nil
}

func (c *Core) telPid() (int, error) {
	nc, err := net.Dial("unix", c.telSock())
	if err != nil {
		return 0, err
	}
	defer nc.Close()
	raw, err := nc.(*net.UnixConn).SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	cerr := raw.Control(func(fd uintptr) {
		cred, err = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if cerr != nil {
		return 0, cerr
	}
	if err != nil {
		return 0, err
	}
	return int(cred.Pid), nil
}

// SMSState is phone-smsd's (selftest).
func (c *Core) SMSState() (svc.SMSStateRes, error) {
	var st svc.SMSStateRes
	err := c.smsRPC(svc.SMSState, nil, &st)
	return st, err
}

func (c *Core) smsRPC(method string, params, out any) error {
	if _, err := os.Stat(c.smsSock()); err != nil {
		return errors.New("no messages service")
	}
	err := c.broker.rpc(svc.Smsd, method, params, out)
	if err != nil && strings.Contains(err.Error(), "connection lost") {
		err = c.broker.rpc(svc.Smsd, method, params, out)
	}
	return err
}

// smsCall is a call to phone-smsd off the loop, made again once when the
// connection was one smsd's restart had broken (the request never reached
// it).
func (b *broker) smsCall(method string, params, out any, done func(error)) {
	go func() {
		err := b.c.smsRPC(method, params, out)
		b.c.Post(func() { done(err) })
	}()
}

// defaultSMSApp: the app that may delete messages and keep the block list
// (Android's default SMS app): the system's Messages.
func defaultSMSApp(a *App) bool { return a.M.ID == appMessages && !a.M.installed }

func (b *broker) dispatchSMS(a *App, m ui.Msg, reply func(any, error)) {
	c := b.c
	named := func(list []ui.SMS) []ui.SMS {
		for i := range list {
			list[i].Name = c.contactName(list[i].Peer)
		}
		return list
	}
	ok := func(err error) { reply(map[string]bool{"ok": err == nil}, err) }
	switch m.Cap {
	case ui.CapSMSDelete, ui.CapSMSBlock, ui.CapSMSBlocked:
		if !defaultSMSApp(a) {
			reply(nil, errors.New(tr.T("Only the default SMS app can do this")))
			return
		}
	}
	switch m.Cap {
	case ui.CapSMSSend:
		var args ui.SMSSendArgs
		_ = json.Unmarshal(m.Args, &args)
		if !defaultSMSApp(a) {
			args.ID = ""
		}
		send := func() {
			var res ui.SMS
			b.smsCall(svc.SMSSend, args, &res, func(err error) { reply(map[string]string{"id": res.ID}, err) })
		}
		if !a.M.installed {
			send()
			return
		}
		text := []rune(args.Text)
		if len(text) > 70 {
			text = append(text[:70], '…')
		}
		c.pushOverlay(&confirmOverlay{text: tr.T("“%s” wants to text %s: %s", a.M.Name, c.peerLabel(args.To), string(text)),
			onYes: send, onNo: func() { reply(nil, errors.New("cancelled by user")) }})
	case ui.CapSMSList:
		var args ui.SMSListArgs
		_ = json.Unmarshal(m.Args, &args)
		var list []ui.SMS
		b.smsCall(svc.SMSList, args, &list, func(err error) { reply(named(list), err) })
	case ui.CapSMSThreads:
		var args ui.SMSListArgs
		_ = json.Unmarshal(m.Args, &args)
		var list []ui.SMSThread
		b.smsCall(svc.SMSThreads, args, &list, func(err error) {
			for i := range list {
				list[i].Name = c.contactName(list[i].Peer)
				list[i].Last.Name = list[i].Name
			}
			reply(list, err)
		})
	case ui.CapSMSRead:
		var args ui.PeerArgs
		_ = json.Unmarshal(m.Args, &args)
		b.smsCall(svc.SMSRead, args, nil, ok)
	case ui.CapSMSAlerts:
		var list []ui.SMSAlert
		b.smsCall(svc.SMSAlerts, nil, &list, func(err error) { reply(list, err) })
	case ui.CapSMSDelete:
		var args ui.SMSDeleteArgs
		_ = json.Unmarshal(m.Args, &args)
		b.smsCall(svc.SMSDelete, args, nil, ok)
	case ui.CapSMSBlock:
		var args ui.SMSBlockArgs
		_ = json.Unmarshal(m.Args, &args)
		var list []string
		b.smsCall(svc.SMSBlock, args, &list, func(err error) { reply(list, err) })
	case ui.CapSMSBlocked:
		var list []string
		b.smsCall(svc.SMSBlocked, nil, &list, func(err error) { reply(list, err) })
	}
}
