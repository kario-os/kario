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
// message becomes a notification of the default SMS app (with "Copy code"
// when smsd found a verification code), a message that could not be sent
// one too, a class 0 message a system dialog (mmsshell.go), the pictures
// of a multimedia message go into the media library, and an emergency
// alert is a system screen over everything, the lock screen included,
// with its own sound, when the phone is in the alert's area or does not
// know where it is. Apps reach messages through the broker; deleting,
// drafts, backups and the block list are the default SMS app's
// (smsrole.go); an app waiting for a verification code gets it once the
// user agrees (smsotp.go).

const smsCopyCode = "copy_code:"

type smsShell struct {
	up bool
	// alerts: the emergency alerts shown (selftest); flashes: the class 0
	// messages shown; outside: the alerts not shown, the phone being
	// outside their area.
	alerts, flashes, outside int
	// shown: the new messages and alerts handled, which smsd may still
	// offer once more (an event and the unnotified list can cross).
	shown map[string]bool
	// blocks: smsd's block list (blocklist.go); blockedCalls the calls
	// declined for it, until they ended; region: the one smsd was told.
	blocks       ui.BlockList
	blockedCalls map[string]bool
	region       string
	// importing: parts of multimedia messages on their way into the media
	// library (mmsshell.go).
	importing map[string]bool
	// otp: the apps waiting for a verification code (smsotp.go), by ID.
	otp    map[string]*otpWait
	otpSeq int
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
			// An SMS app that asked while phone-smsd was away (a restart,
			// a slow boot) got an error and shows nothing: it asks again.
			c.Post(func() {
				c.sms.up = true
				c.smsPeers()
				c.smsBlocksRefresh()
				c.smsTellApps("{}")
			})
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
		c.smsBlocksRefresh()
		c.smsTellApps("{}")
	case ev.Event == svc.SMSEventAlert && ev.Alert != nil:
		c.smsAlert(*ev.Alert)
	case ev.Event == svc.SMSEventFlash && ev.SMS != nil:
		c.smsFlash(*ev.SMS)
	}
}

// smsTellApps sends the "sms" event to the default SMS app and the
// system's Messages, the ones of them that run.
func (c *Core) smsTellApps(value string) {
	role, msgs := c.smsApp(), c.appByID(appMessages)
	for i, a := range []*App{role, msgs} {
		if a != nil && a.running && (i == 0 || a != role) {
			a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventSMS, Value: value})
		}
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
	for _, m := range res.Flash {
		c.smsFlash(m)
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
	a := c.smsApp()
	if a == nil {
		return
	}
	c.smsImportParts(m)
	if isNew && !c.smsShown(m.ID) {
		return
	}
	who := firstNonEmpty(m.Name, ui.NumberText(m.Peer))
	switch {
	case isNew && m.MMS == ui.MMSFailed:
		c.notifyAs(a, ui.Notification{ID: "sms:" + m.Thread, Title: who, Body: tr.T("A multimedia message could not be downloaded")})
	case isNew:
		n := ui.Notification{ID: "sms:" + m.Thread, Title: who, Body: smsPreview(m)}
		if m.Code != "" {
			n.Actions = []ui.Action{{ID: smsCopyCode + m.Code, Label: tr.T("Copy code %s", m.Code), Key: "c"}}
			c.smsOTPOffer(m)
		}
		c.notifyAs(a, n)
	case m.Dir == "out" && m.State == ui.SMSFailed:
		c.notifyAs(a, ui.Notification{ID: "sms:" + m.Thread, Title: who, Body: tr.T("Message not sent: %s", smsPreview(m))})
	}
	raw, _ := json.Marshal(m)
	c.smsTellApps(string(raw))
}

// smsPreview is a message in one line: a multimedia message's subject and
// text, or what it holds.
func smsPreview(m ui.SMS) string {
	text := strings.TrimSpace(strings.Join(strings.Fields(m.Subject+" "+m.Text), " "))
	if text != "" || m.Kind != ui.SMSKindMMS {
		return text
	}
	for _, p := range m.Parts {
		if strings.HasPrefix(p.Type, "image/") {
			return tr.T("Picture")
		}
	}
	return tr.T("Multimedia message")
}

// smsNotifAction copies a verification code from its notification; the
// Messages app need not start for it.
func (c *Core) smsNotifAction(n *notif, action string) bool {
	code, ok := strings.CutPrefix(action, smsCopyCode)
	if !ok || !c.defaultSMSApp(n.app) {
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

// smsAlert shows an emergency alert smsd stored, unless its level is off
// or the phone is outside its area (alertWhere).
func (c *Core) smsAlert(al ui.SMSAlert) {
	if !c.smsShown(al.ID) {
		return
	}
	if !c.alertOn(al.Level) {
		c.log.Printf("emergency alert %s (%s) not shown: switched off", al.ID, al.Level)
		return
	}
	if where := c.alertWhere(al); where == alertOutside {
		c.sms.outside++
		c.log.Printf("emergency alert %s (%s) not shown: the phone is outside its area", al.ID, al.Level)
		return
	} else if where != "" {
		c.log.Printf("emergency alert %s (%s): %s", al.ID, al.Level, where)
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
	phase, period := c.overlayPhase(o.at, alarmCycle)
	return &FrameOverlay{Kind: "alarm", Title: alertTitle(o.alert.Level), Lines: []string{tr.T("Emergency alert"), o.alert.Text},
		Choices: []string{tr.T("Dismiss")}, Phase: phase, Period: period,
		Footer: []string{tr.T("OK or Back dismisses")}}
}

// SMSKill ends phone-smsd abruptly (its supervisor starts it again) and,
// with wait, waits until the new process answers (selftest).
func (c *Core) SMSKill(wait func(cond func() bool) bool) error {
	var st svc.SMSStateRes
	if err := c.smsRPC(svc.SMSState, nil, &st); err != nil {
		return err
	}
	if st.Pid == 0 {
		return errors.New("phone-smsd did not say its process id")
	}
	if err := syscall.Kill(st.Pid, syscall.SIGKILL); err != nil {
		return err
	}
	if wait != nil && !wait(func() bool { now, err := c.SMSState(); return err == nil && now.Pid != 0 && now.Pid != st.Pid }) {
		return errors.New("phone-smsd did not come back")
	}
	return nil
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
	case ui.CapSMSDelete, ui.CapSMSBlock, ui.CapSMSBlocked, ui.CapSMSDownload, ui.CapSMSDraft, ui.CapSMSBackup, ui.CapSMSRestore:
		if !c.defaultSMSApp(a) {
			reply(nil, errors.New(tr.T("Only the default SMS app can do this")))
			return
		}
	}
	switch m.Cap {
	case ui.CapSMSDraft:
		var args ui.SMSDraftArgs
		_ = json.Unmarshal(m.Args, &args)
		b.smsCall(svc.SMSDraft, args, nil, ok)
	case ui.CapSMSBackup:
		c.smsBackup(a, m.Origin, reply)
	case ui.CapSMSRestore:
		c.smsRestore(a, m.Origin, reply)
	case ui.CapSMSVCard:
		var args ui.SMSPartArgs
		_ = json.Unmarshal(m.Args, &args)
		c.smsVCard(a, m.Origin, args, reply)
	case ui.CapSMSOTP:
		var args ui.SMSOTPArgs
		_ = json.Unmarshal(m.Args, &args)
		c.smsOTPWait(a, args, reply)
	case ui.CapSMSSend:
		var args ui.SMSSendArgs
		_ = json.Unmarshal(m.Args, &args)
		if !c.defaultSMSApp(a) {
			if args.At != 0 {
				reply(nil, errors.New(tr.T("Only the default SMS app can do this")))
				return
			}
			args.ID = ""
		}
		req, err := c.smsSendReq(a, args)
		if err == nil {
			err = c.minorRecipients(args.To)
		}
		if err != nil {
			reply(nil, err)
			return
		}
		send := func() {
			var res ui.SMS
			b.smsCall(svc.SMSSend, req, &res, func(err error) { reply(map[string]string{"id": res.ID}, err) })
		}
		if !a.M.installed || c.defaultSMSApp(a) {
			send()
			return
		}
		text := []rune(args.Text)
		if len(text) > 70 {
			text = append(text[:70], '…')
		}
		what := string(text)
		if len(args.Media) > 0 {
			what = tr.T("%s (%d pictures)", what, len(args.Media))
		}
		c.pushOverlay(&confirmOverlay{text: tr.T("“%s” wants to text %s: %s", a.M.Name, c.peerLabel(args.To), what),
			onYes: send, onNo: func() { reply(nil, errors.New("cancelled by user")) }, trust: true})
	case ui.CapSMSDownload:
		var args ui.SMSIDArgs
		_ = json.Unmarshal(m.Args, &args)
		b.smsCall(svc.SMSDownload, args, nil, ok)
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
