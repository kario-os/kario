package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/cts"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// broker mediates every capability request. The caller's identity is the
// socket the shell handed to the app process, so it cannot be forged.
type broker struct {
	c       *Core
	mu      sync.Mutex
	clients map[string]*wire.Client
	perms   map[string]map[string]string
	path    string
	stats   map[string]*CapStat
	// usage: when each grant (key as perms) was last used, and how often.
	usage map[string]map[string]*permUse
}

type permUse struct {
	Last  int64 `json:"last"` // unix ms
	Count int   `json:"count"`
}

func (b *broker) usagePath() string { return filepath.Join(filepath.Dir(b.path), "perm-usage.json") }

// permLabel names a permission of ui.RuntimePerms or ui.DeclaredPerms in the
// device's language; the English matches those tables.
func permLabel(p string) string {
	switch p {
	case ui.PermCamera:
		return tr.T("Camera")
	case ui.PermMicrophone:
		return tr.T("Microphone")
	case ui.PermContacts:
		return tr.T("Contacts")
	case ui.PermPhotos:
		return tr.T("Photos")
	case ui.PermLocation:
		return tr.T("Location")
	case ui.PermNet:
		return tr.T("Network")
	case ui.PermNotify:
		return tr.T("Notifications")
	case ui.PermVoIP:
		return tr.T("Internet calls (answered in the background)")
	case ui.PermPhone:
		return tr.T("Phone calls (confirmed before each call)")
	case ui.PermSMS:
		return tr.T("SMS (confirmed before each message)")
	case ui.PermKeystore:
		return tr.T("App keys")
	case ui.PermAttest:
		return tr.T("Device attestation")
	case ui.PermNFC:
		return tr.T("Contactless cards (NFC)")
	case ui.PermNFCReader:
		return tr.T("Reading NFC cards")
	}
	return p
}

// used records that a capability needing runtime permissions went through.
func (b *broker) used(key string, need []string) {
	changed := false
	for _, p := range need {
		if _, runtime := ui.RuntimePerms[p]; !runtime {
			continue
		}
		if b.usage[key] == nil {
			b.usage[key] = map[string]*permUse{}
		}
		u := b.usage[key][p]
		if u == nil {
			u = &permUse{}
			b.usage[key][p] = u
		}
		u.Last, u.Count, changed = b.c.now().UnixMilli(), u.Count+1, true
	}
	if changed {
		b.saveUsage()
	}
}

func (b *broker) saveUsage() {
	raw, _ := json.Marshal(b.usage)
	_ = os.MkdirAll(filepath.Dir(b.usagePath()), 0o700)
	_ = os.WriteFile(b.usagePath(), raw, 0o600)
}

// forgetUsage drops the records of an app and its mini-programs.
func (b *broker) forgetUsage(id string) {
	for key := range b.usage {
		if key == id || strings.HasPrefix(key, id+"/") {
			delete(b.usage, key)
		}
	}
	b.saveUsage()
}

func (b *broker) entry(key, name, perm, label, state string) ui.PermEntry {
	e := ui.PermEntry{App: key, Name: name, Perm: perm, Label: label, State: state}
	if u := b.usage[key][perm]; u != nil {
		e.LastUsed, e.Uses = u.Last, u.Count
	}
	if state == "ask" {
		e.Reset = b.c.resetAt(key, perm)
	}
	return e
}

type callInfo struct {
	id, peer      string
	local, remote string
	app           *App
	started       time.Time
}

func (ci *callInfo) onCallScreen(c *Core) bool {
	if v := ci.app.top(); v != nil {
		for _, n := range v.S.Body {
			if n.Kind == ui.KindMedia && n.Stream == ci.remote {
				return true
			}
		}
	}
	return false
}

func newBroker(c *Core) (*broker, error) {
	b := &broker{c: c, clients: map[string]*wire.Client{}, perms: map[string]map[string]string{}, stats: map[string]*CapStat{},
		path: filepath.Join(c.cfg.StateDir, "shell", "perms.json")}
	if raw, err := os.ReadFile(b.path); err == nil {
		if err := json.Unmarshal(raw, &b.perms); err != nil {
			return nil, fmt.Errorf("%s: %w", b.path, err)
		}
	}
	b.usage = map[string]map[string]*permUse{}
	if raw, err := os.ReadFile(b.usagePath()); err == nil {
		_ = json.Unmarshal(raw, &b.usage)
	}
	return b, nil
}

func (b *broker) save() {
	raw, _ := json.MarshalIndent(b.perms, "", "  ")
	_ = os.MkdirAll(filepath.Dir(b.path), 0o700)
	_ = os.WriteFile(b.path, raw, 0o600)
}

// call performs a service RPC off the loop and posts the result back.
func (b *broker) call(service, method string, params any, out any, done func(error)) {
	go func() {
		err := b.rpc(service, method, params, out)
		b.c.Post(func() { done(err) })
	}()
}

func (b *broker) client(service string) (*wire.Client, error) {
	b.mu.Lock()
	cl := b.clients[service]
	b.mu.Unlock()
	if cl != nil {
		return cl, nil
	}
	var err error
	for i := 0; i < 20; i++ {
		if cl, err = wire.Dial(filepath.Join(b.c.cfg.RunDir, service+".sock")); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("%s unavailable: %w", service, err)
	}
	b.mu.Lock()
	b.clients[service] = cl
	b.mu.Unlock()
	return cl, nil
}

func (b *broker) rpc(service, method string, params, out any) error {
	files, err := b.rpcFiles(service, method, params, out)
	closeFiles(files)
	return err
}

// rpcFiles is rpc for methods that answer with open files.
func (b *broker) rpcFiles(service, method string, params, out any) ([]*os.File, error) {
	cl, err := b.client(service)
	if err != nil {
		return nil, err
	}
	files, err := cl.CallFiles(method, params, out)
	if err != nil && strings.Contains(err.Error(), "connection lost") {
		b.mu.Lock()
		delete(b.clients, service)
		b.mu.Unlock()
	}
	return files, err
}

var capPerms = map[string][]string{
	ui.CapCallStart:      {ui.PermMicrophone},
	ui.CapCallControl:    {},
	ui.CapCallEnd:        {},
	ui.CapScanQR:         {ui.PermCamera},
	ui.CapAuthConfirm:    {ui.PermKeystore},
	ui.CapKeySign:        {ui.PermKeystore},
	ui.CapAttest:         {ui.PermAttest},
	ui.CapPermList:       {ui.PermSystem},
	ui.CapPermSet:        {ui.PermSystem},
	ui.CapTelDial:        {},
	ui.CapTelAnswer:      {ui.PermPhone},
	ui.CapTelHangup:      {ui.PermPhone},
	ui.CapTelHold:        {ui.PermPhone},
	ui.CapTelMute:        {ui.PermPhone},
	ui.CapTelCalls:       {ui.PermPhone},
	ui.CapTelLog:         {ui.PermPhone},
	ui.CapSMSSend:        {ui.PermSMS},
	ui.CapTelUSSD:        {ui.PermPhone},
	ui.CapSMSList:        {ui.PermSMS},
	ui.CapSMSRead:        {ui.PermSMS},
	ui.CapSMSThreads:     {ui.PermSMS},
	ui.CapSMSAlerts:      {ui.PermSMS},
	ui.CapSMSDelete:      {ui.PermSMS},
	ui.CapSMSBlock:       {ui.PermSMS},
	ui.CapSMSBlocked:     {ui.PermSMS},
	ui.CapContacts:       {ui.PermContacts},
	ui.CapAlarmSet:       {},
	ui.CapAlarmCancel:    {},
	ui.CapAlarmList:      {},
	ui.CapMediaList:      {},
	ui.CapAudioPlay:      {},
	ui.CapAudioControl:   {},
	ui.CapAudioState:     {},
	ui.CapAudioRoute:     {ui.PermPhone},
	ui.CapPushRegister:   {},
	ui.CapAudioStream:    {},
	ui.CapAudioRecord:    {ui.PermMicrophone},
	ui.CapCameraCapture:  {ui.PermCamera},
	ui.CapMediaPick:      {},
	ui.CapMediaSave:      {},
	ui.CapFileSave:       {},
	ui.CapFilePick:       {},
	ui.CapFileOpen:       {},
	ui.CapAppSettings:    {},
	ui.CapWidgetSet:      {},
	ui.CapSettingsApps:   {ui.PermSystem},
	ui.CapSettingsAppSet: {ui.PermSystem},
	ui.CapMediaDelete:    {ui.PermPhotos},
	ui.CapSettingsGet:    {ui.PermSystem},
	ui.CapWifiState:      {ui.PermSystem},
	ui.CapWifiSet:        {ui.PermSystem},
	ui.CapWifiConnect:    {ui.PermSystem},
	ui.CapWifiForget:     {ui.PermSystem},
	ui.CapBTState:        {ui.PermSystem},
	ui.CapBTSet:          {ui.PermSystem},
	ui.CapBTPair:         {ui.PermSystem},
	ui.CapBTForget:       {ui.PermSystem},
	ui.CapBTSend:         {ui.PermSystem},
	ui.CapRadioState:     {ui.PermSystem},
	ui.CapRadioSet:       {ui.PermSystem},
	ui.CapClipboardSet:   {},
	ui.CapLocationGet:    {ui.PermLocation},
	ui.CapShareOpen:      {},
	ui.CapPushKey:        {},
	ui.CapPushOpen:       {},
	ui.CapPkgInstall:     {ui.PermInstall},
	ui.CapPkgUninstall:   {ui.PermInstall},
	ui.CapPkgList:        {ui.PermInstall},
	ui.CapPkgRevocations: {ui.PermInstall},
	ui.CapPkgCatalog:     {ui.PermInstall},
	ui.CapIntentInvoke:   {},
	ui.CapIntentReply:    {},
	ui.CapJobSchedule:    {},
	ui.CapJobCancel:      {},
	ui.CapSettingsSet:    {ui.PermSystem},
	ui.CapSystemUpdate:   {ui.PermSystem},
	ui.CapWebControl:     {},
	ui.CapWallpaperSet:   {},
	ui.CapVideoOpen:      {},
	ui.CapVideoControl:   {},
	ui.CapNFCReply:       {ui.PermNFC},
	ui.CapNFCState:       {ui.PermSystem},
	ui.CapNFCSet:         {ui.PermSystem},
	ui.CapNFCRead:        {ui.PermNFCReader},
	ui.CapNFCTransceive:  {ui.PermNFCReader},
}

// holds reports whether the app has a permission now: declared, and for a
// runtime permission granted (by the user, or preset for system apps).
func (b *broker) holds(a *App, perm string) bool {
	if !a.M.Has(perm) {
		return false
	}
	if _, runtime := ui.RuntimePerms[perm]; !runtime {
		return true
	}
	preset := ""
	for _, g := range a.M.Granted {
		if g == perm {
			preset = "granted"
		}
	}
	return firstNonEmpty(b.perms[a.M.ID][perm], preset) == "granted"
}

// withFiles is a capability result that hands open files to the app.
type withFiles struct {
	data  any
	files []*os.File
}

// CapStat counts how a capability was used (the compatibility report).
type CapStat = cts.CapStat

// CapStats are the capability calls so far, for a successor Shell.
func (c *Core) CapStats() map[string]CapStat {
	out := map[string]CapStat{}
	for k, v := range c.broker.stats {
		out[k] = *v
	}
	return out
}

// AddCapStats carries a previous Shell's calls into this one's counts.
func (c *Core) AddCapStats(m map[string]CapStat) {
	for k, v := range m {
		st := c.broker.stats[k]
		if st == nil {
			st = &CapStat{}
			c.broker.stats[k] = st
		}
		st.Calls, st.OK, st.Errors = st.Calls+v.Calls, st.OK+v.OK, st.Errors+v.Errors
	}
}

func (b *broker) handle(a *App, m ui.Msg) { b.handleFor(a, m, a.sendFiles) }

// handleFor is handle with the reply going to send: content the app hosts
// but does not run itself (a web view's page) calls under the app's
// permissions and its own origin, and gets the answer back directly.
func (b *broker) handleFor(a *App, m ui.Msg, send func(ui.Msg, []*os.File)) {
	stat := b.stats[m.Cap]
	if _, known := capPerms[m.Cap]; known {
		if stat == nil {
			stat = &CapStat{}
			b.stats[m.Cap] = stat
		}
		stat.Calls++
	}
	a.waiting++
	replied := false
	reply := func(data any, err error) {
		if !replied && a.waiting > 0 {
			a.waiting--
		}
		replied = true
		if stat != nil {
			if err != nil {
				stat.Errors++
			} else {
				stat.OK++
			}
		}
		res := ui.Msg{T: ui.MsgRes, Req: m.Req}
		var files []*os.File
		if wf, ok := data.(withFiles); ok {
			data, files = wf.data, wf.files
		}
		if err != nil {
			res.Err = err.Error()
			closeFiles(files)
			files = nil
		} else {
			res.OK = true
			res.Data, _ = json.Marshal(data)
		}
		send(res, files)
	}
	need, known := capPerms[m.Cap]
	if !known {
		reply(nil, fmt.Errorf("unknown capability %q", m.Cap))
		return
	}
	need = append([]string{}, need...)
	if m.Cap == ui.CapCallStart {
		var args ui.CallStartArgs
		_ = json.Unmarshal(m.Args, &args)
		if args.Video {
			need = append(need, ui.PermCamera)
		}
	}
	if m.Cap == ui.CapMediaList {
		var args ui.MediaArgs
		_ = json.Unmarshal(m.Args, &args)
		if args.Kind == ui.MediaImage || args.Kind == ui.MediaVideo {
			need = append(need, ui.PermPhotos)
		}
	}
	for _, p := range need {
		if !a.M.Has(p) {
			reply(nil, fmt.Errorf("permission not declared in manifest: %s", p))
			return
		}
	}
	b.consent(a, m.Origin, m.Purposes, need, func(ok bool) {
		if !ok {
			reply(nil, errors.New("permission denied by user"))
			return
		}
		b.used(permKey(a, m.Origin), need)
		b.dispatch(a, m, reply)
	})
}

// consent asks the user once per runtime permission and remembers the answer.
// permKey is where a grant is kept: hosted content (a mini-program, named by
// its origin) holds its own grants, apart from its host's and from the other
// mini-programs', so the user decides per mini-program and can revoke one
// alone. Presets of the host never pass to its mini-programs.
func permKey(a *App, origin string) string {
	if origin == "" {
		return a.M.ID
	}
	return a.M.ID + "/" + origin
}

const maxOrigin = keystore.MaxOrigin

func (b *broker) consent(a *App, origin string, purposes map[string]string, need []string, done func(bool)) {
	if len([]rune(origin)) > maxOrigin {
		done(false)
		return
	}
	key := permKey(a, origin)
	var ask []string
	for _, p := range need {
		if _, runtime := ui.RuntimePerms[p]; !runtime {
			continue
		}
		preset := ""
		for _, g := range a.M.Granted {
			if g == p && origin == "" {
				preset = "granted"
			}
		}
		switch firstNonEmpty(b.perms[key][p], preset) {
		case "granted":
		case "denied":
			done(false)
			return
		default:
			ask = append(ask, p)
		}
	}
	if len(ask) == 0 {
		done(true)
		return
	}
	b.c.pushOverlay(&permOverlay{app: a, origin: origin, purposes: purposes, perms: ask, yes: true, decide: func(ok bool) {
		if b.perms[key] == nil {
			b.perms[key] = map[string]string{}
		}
		state := "denied"
		if ok {
			state = "granted"
		}
		for _, p := range ask {
			b.perms[key][p] = state
			b.c.clearReset(key, p)
		}
		b.save()
		done(ok)
	}})
}

func (b *broker) dispatch(a *App, m ui.Msg, reply func(any, error)) {
	c := b.c
	// A capability the product routes to a provider service (profile.go):
	// the permission was checked above; the provider learns who asks.
	if svc := c.profile.provider(m.Cap); svc != "" {
		var out json.RawMessage
		b.call(svc, "capability", map[string]any{"cap": m.Cap, "app": permKey(a, m.Origin), "args": m.Args}, &out, func(err error) {
			reply(out, err)
		})
		return
	}
	switch m.Cap {
	case ui.CapCallStart:
		var args ui.CallStartArgs
		_ = json.Unmarshal(m.Args, &args)
		var res ui.CallStartResult
		b.call(svc.Mediad, svc.MediaCallStart, map[string]any{"app": a.M.ID, "peer": args.Peer, "video": args.Video}, &res, func(err error) {
			if err == nil {
				c.calls[res.CallID] = &callInfo{id: res.CallID, peer: args.Peer, local: res.Local, remote: res.Remote, app: a, started: time.Now()}
			}
			reply(res, err)
		})
	case ui.CapCallControl, ui.CapCallEnd:
		var args ui.CallControlArgs
		_ = json.Unmarshal(m.Args, &args)
		ci := c.calls[args.CallID]
		if ci == nil || ci.app != a {
			reply(nil, errors.New("no such call for this app"))
			return
		}
		if m.Cap == ui.CapCallEnd {
			delete(c.calls, args.CallID)
			var res map[string]int
			b.call(svc.Mediad, svc.MediaCallEnd, args, &res, func(err error) { reply(res, err) })
			return
		}
		var res map[string]bool
		b.call(svc.Mediad, svc.MediaCallControl, args, &res, func(err error) { reply(res, err) })
	case ui.CapScanQR:
		var open struct {
			ScanID string `json:"scan_id"`
			Stream string `json:"stream"`
		}
		b.call(svc.Mediad, svc.MediaScanOpen, map[string]string{"app": a.M.ID}, &open, func(err error) {
			if err != nil {
				reply(nil, err)
				return
			}
			o := &scanOverlay{app: a, origin: m.Origin, stream: open.Stream, t0: time.Now(), cancel: func() {
				go func() { _ = b.rpc(svc.Mediad, svc.MediaScanClose, map[string]string{"scan_id": open.ScanID}, nil) }()
			}}
			c.pushOverlay(o)
			var res ui.ScanResult
			b.call(svc.Mediad, svc.MediaScanWait, map[string]string{"scan_id": open.ScanID}, &res, func(err error) {
				c.popOverlay(o)
				reply(res, err)
			})
		})
	case ui.CapAuthConfirm:
		var args ui.AuthConfirmArgs
		_ = json.Unmarshal(m.Args, &args)
		// With fingerprints on for payments, a match signs with the
		// biometric key; the PIN signs with Key and (re)makes that key.
		bioKey := c.payBioKey(args)
		var o *authOverlay
		o = &authOverlay{app: a, origin: m.Origin, prompt: args.Prompt, started: time.Now(),
			cancel: func() { c.stopBioConfirm(o); reply(nil, errors.New(ui.ErrCancelled)) },
			submit: func(pin string) {
				var proof ui.KeyProof
				b.call(svc.Keystored, svc.KeyConfirm, map[string]string{"app": permKey(a, m.Origin), "key": args.Key, "alg": args.Alg, "prompt": args.Prompt, "payload": args.Payload, "pin": pin, "bio_key": bioKey}, &proof, func(err error) {
					o.busy = false
					if o.done {
						return
					}
					switch {
					case err == nil:
						c.stopBioConfirm(o)
						c.popOverlay(o)
						reply(proof, nil)
					case strings.HasPrefix(err.Error(), "wrong_pin:"):
						o.err = tr.T("Wrong password, %s tries left", strings.TrimPrefix(err.Error(), "wrong_pin:"))
					default:
						c.stopBioConfirm(o)
						c.popOverlay(o)
						reply(nil, err)
					}
				})
			}}
		c.pushOverlay(o)
		if bioKey != "" {
			c.startBioConfirm(o, permKey(a, m.Origin), args, reply)
		}
	case ui.CapKeySign:
		var args ui.SignArgs
		_ = json.Unmarshal(m.Args, &args)
		var proof ui.KeyProof
		b.call(svc.Keystored, svc.KeySign, map[string]string{"app": permKey(a, m.Origin), "key": args.Key, "alg": args.Alg, "payload": args.Payload}, &proof, func(err error) { reply(proof, err) })
	case ui.CapAttest:
		var args ui.AttestArgs
		_ = json.Unmarshal(m.Args, &args)
		var att ui.Attestation
		b.call(svc.Keystored, svc.KeyAttest, map[string]string{"app": permKey(a, m.Origin), "nonce": args.Nonce, "alg": args.Alg}, &att, func(err error) { reply(att, err) })
	case ui.CapPermList:
		var out []ui.PermEntry
		for _, x := range c.apps {
			for _, p := range x.M.Permissions {
				if _, runtime := ui.RuntimePerms[p]; !runtime {
					continue
				}
				state := b.perms[x.M.ID][p]
				if state == "" {
					state = "ask"
				}
				out = append(out, b.entry(x.M.ID, x.M.Name, p, permLabel(p), state))
			}
			// The network and notifications: granted at install, the user
			// can switch them off.
			if x.M.Has(ui.PermNet) {
				state := firstNonEmpty(b.perms[x.M.ID][ui.PermNet], "granted")
				out = append(out, b.entry(x.M.ID, x.M.Name, ui.PermNet, permLabel(ui.PermNet), state))
			}
			if x.M.Has(ui.PermNotify) {
				state := firstNonEmpty(b.perms[x.M.ID][ui.PermNotify], "granted")
				out = append(out, b.entry(x.M.ID, x.M.Name, ui.PermNotify, permLabel(ui.PermNotify), state))
			}
			// Each mini-program's own grants.
			for key, grants := range b.perms {
				origin, ok := strings.CutPrefix(key, x.M.ID+"/")
				if !ok {
					continue
				}
				for p, state := range grants {
					if _, runtime := ui.RuntimePerms[p]; runtime {
						out = append(out, b.entry(key, tr.T("%s “%s”", x.M.Name, origin), p, permLabel(p), state))
					}
				}
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].App+out[i].Perm < out[j].App+out[j].Perm })
		reply(out, nil)
	case ui.CapPermSet:
		var args ui.PermSetArgs
		_ = json.Unmarshal(m.Args, &args)
		valid := map[string]bool{"granted": true, "denied": true, "ask": args.Perm != ui.PermNet && args.Perm != ui.PermNotify, "wifi": args.Perm == ui.PermNet}
		if !valid[args.State] {
			reply(nil, fmt.Errorf("%s cannot be %q", args.Perm, args.State))
			return
		}
		if b.perms[args.App] == nil {
			b.perms[args.App] = map[string]string{}
		}
		wasDenied := b.perms[args.App][ui.PermNet] == "denied"
		c.clearReset(args.App, args.Perm)
		if args.State == "ask" {
			delete(b.perms[args.App], args.Perm)
		} else {
			b.perms[args.App][args.Perm] = args.State
		}
		b.save()
		if args.Perm == ui.PermNet {
			if err := c.applyNetPolicy(); err != nil {
				c.log.Printf("traffic: %v", err)
			}
			// The network namespace is fixed when the app starts: going to
			// or from "denied" ends a running app, which starts again
			// under the new rule. Wi-Fi only is a firewall rule, live.
			if t := c.appByID(args.App); t != nil && t.running && wasDenied != (args.State == "denied") {
				c.log.Printf("%s: network %s; restarting it", args.App, args.State)
				c.kill(t)
			}
		}
		reply(map[string]string{"state": args.State}, nil)
	case ui.CapPkgInstall, ui.CapPkgUninstall:
		var args ui.PkgArgs
		_ = json.Unmarshal(m.Args, &args)
		if m.Cap == ui.CapPkgInstall {
			c.installPkg(a, args, reply)
		} else {
			c.uninstallPkg(a, args.ID, reply)
		}
	case ui.CapPkgRevocations:
		var args ui.RevocationArgs
		_ = json.Unmarshal(m.Args, &args)
		n, err := c.applyRevocations(args.Data)
		reply(map[string]int{"revoked_installed": n}, err)
	case ui.CapIntentInvoke:
		var args ui.IntentArgs
		_ = json.Unmarshal(m.Args, &args)
		c.invokeIntent(a, args, reply)
	case ui.CapIntentReply:
		var args ui.IntentReplyArgs
		_ = json.Unmarshal(m.Args, &args)
		reply(nil, c.replyIntent(a, args))
	case ui.CapPkgCatalog:
		var args ui.CatalogArgs
		_ = json.Unmarshal(m.Args, &args)
		res, err := c.checkCatalog(args.Data)
		reply(res, err)
	case ui.CapPkgList:
		reply(c.listPkgs(), nil)
	case ui.CapPushKey:
		var args ui.PushKeyArgs
		_ = json.Unmarshal(m.Args, &args)
		var res ui.PushKeyRes
		b.call(svc.Keystored, svc.KeyPushKey, map[string]any{"app": a.M.ID, "rotate": args.Rotate}, &res, func(err error) { reply(res, err) })
	case ui.CapPushOpen:
		var args ui.PushOpenArgs
		_ = json.Unmarshal(m.Args, &args)
		var res ui.PushOpenRes
		b.call(svc.Keystored, svc.KeyPushOpen, map[string]string{"app": a.M.ID, "payload": args.Data}, &res, func(err error) { reply(res, err) })
	case ui.CapAppSettings:
		reply(settingValues(c.appSettings(a)), nil)
	case ui.CapWidgetSet:
		var args ui.WidgetArgs
		_ = json.Unmarshal(m.Args, &args)
		reply(nil, c.setWidget(a, args.Text, args.Arg))
	case ui.CapSettingsApps:
		out := []ui.AppSettings{}
		for _, x := range c.apps {
			if len(x.M.Settings) > 0 {
				out = append(out, ui.AppSettings{App: x.M.ID, Name: x.M.Name, Icon: x.M.Icon, Settings: c.appSettings(x)})
			}
		}
		reply(out, nil)
	case ui.CapSettingsAppSet:
		var args ui.AppSettingArgs
		_ = json.Unmarshal(m.Args, &args)
		reply(nil, c.setAppSetting(args.App, args.Key, args.Value))
	case ui.CapShareOpen:
		var args ui.ShareArgs
		_ = json.Unmarshal(m.Args, &args)
		c.share(a, m.Origin, args, reply)
	case ui.CapLocationGet:
		reply(c.location(a))
	case ui.CapJobSchedule, ui.CapJobCancel:
		var args ui.JobArgs
		_ = json.Unmarshal(m.Args, &args)
		if m.Cap == ui.CapJobCancel {
			c.cancelJob(a, args.ID)
			reply(map[string]bool{"ok": true}, nil)
			return
		}
		reply(map[string]bool{"ok": true}, c.scheduleJob(a, args))
	case ui.CapClipboardSet:
		var args ui.ClipArgs
		_ = json.Unmarshal(m.Args, &args)
		reply(nil, c.clipSet(a, args.Text))
	case ui.CapWallpaperSet:
		var args ui.WallpaperArgs
		_ = json.Unmarshal(m.Args, &args)
		c.setWallpaper(a, args, reply)
	case ui.CapVideoOpen, ui.CapVideoControl:
		var args ui.VideoArgs
		_ = json.Unmarshal(m.Args, &args)
		if m.Cap == ui.CapVideoOpen {
			c.openVideo(a, args, reply)
		} else {
			c.controlVideo(a, args, reply)
		}
	case ui.CapWebControl:
		var args ui.WebControlArgs
		_ = json.Unmarshal(m.Args, &args)
		reply(nil, c.webControl(a, args))
	case ui.CapSettingsGet:
		reply(c.settingsGet(), nil)
	case ui.CapSettingsSet:
		var args ui.SettingArgs
		_ = json.Unmarshal(m.Args, &args)
		if c.securitySetting(args, func(err error) { reply(c.settingsGet(), err) }) {
			return
		}
		if args.Key == ui.SettingSimPinLock || args.Key == ui.SettingSimPinChange {
			// The PIN is asked for on a system screen; the answer waits for it.
			op := map[bool]string{true: "on", false: "off"}[args.Value != 0]
			if args.Key == ui.SettingSimPinChange {
				op = "change"
			}
			c.simPinSetting(op, func(err error) { reply(c.settingsGet(), err) })
			return
		}
		err := c.settingsSet(args.Key, args.Value)
		reply(c.settingsGet(), err)
	case ui.CapSystemUpdate:
		var args ui.UpdateArgs
		_ = json.Unmarshal(m.Args, &args)
		switch args.Action {
		case "status":
			reply(c.updateStatus(), nil)
		case "check":
			c.updateCheck(reply)
		case "install":
			c.updateInstall(reply)
		case "reboot":
			c.updateReboot(reply)
		default:
			reply(nil, errors.New("action must be status, check, install or reboot"))
		}
	case ui.CapPushRegister:
		var tok ui.PushToken
		b.call(svc.Pushd, svc.PushRegister, map[string]string{"app": a.M.ID}, &tok, func(err error) { reply(tok, err) })
	case ui.CapWifiState, ui.CapWifiSet, ui.CapWifiConnect, ui.CapWifiForget, ui.CapBTState, ui.CapBTSet, ui.CapBTPair, ui.CapBTForget:
		b.dispatchRadios(m, reply)
	case ui.CapRadioState, ui.CapRadioSet:
		b.dispatchAirplane(m, reply)
	case ui.CapBTSend:
		b.c.btSend(a, m, reply)
	case ui.CapNFCReply, ui.CapNFCState, ui.CapNFCSet, ui.CapNFCRead, ui.CapNFCTransceive:
		b.dispatchNFC(a, m, reply)
	default:
		b.dispatchAudio(a, m, reply)
	}
}

func (b *broker) endCall(id string) {
	delete(b.c.calls, id)
	go func() { _ = b.rpc(svc.Mediad, svc.MediaCallEnd, map[string]string{"call_id": id}, nil) }()
}
