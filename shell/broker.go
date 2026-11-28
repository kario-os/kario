package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/cts"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/statefile"
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
	// asking: the requests waiting on each consent dialog on screen.
	asking map[string][]func(bool)
	// resolve names the capability version a request asks for
	// (ui.ResolveCap; tests give other tables).
	resolve func(string) (string, error)
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
	case ui.PermLocationBackground:
		return tr.T("Location in the background")
	case ui.PermNet:
		return tr.T("Network")
	case ui.PermKeepalive:
		return tr.T("Its own connection in the background")
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
	_ = statefile.Write(b.usagePath(), raw, 0o600)
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
	b.c.locEntry(&e)
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
	b.loadPerms()
	b.usage = map[string]map[string]*permUse{}
	readState(c.log.Printf, b.usagePath(), &b.usage)
	return b, nil
}

// loadPerms reads the user's answers, kept with a copy. A damaged file
// grants nothing: of its copy only the refusals and restrictions count (a
// grant there may have been taken back since), and with neither whole the
// apps installed from the store or a computer lose the network and
// notifications too, until the user gives them back in Settings.
func (b *broker) loadPerms() {
	logf := b.c.log.Printf
	bak, err := statefile.ReadJSON(b.path, &b.perms)
	if b.perms == nil {
		b.perms = map[string]map[string]string{}
	}
	switch {
	case err == nil && bak:
		for _, grants := range b.perms {
			for p, state := range grants {
				if state == "granted" {
					delete(grants, p)
				}
			}
		}
		logf("permissions: %s was damaged; kept the refusals of its copy, nothing granted", b.path)
	case errors.Is(err, statefile.ErrDamaged):
		b.perms = map[string]map[string]string{}
		for _, a := range b.c.apps {
			for _, p := range []string{ui.PermNet, ui.PermNotify} {
				if a.M.installed && a.M.Has(p) {
					if b.perms[a.M.ID] == nil {
						b.perms[a.M.ID] = map[string]string{}
					}
					b.perms[a.M.ID][p] = "denied"
				}
			}
		}
		logf("permissions: %v; nothing granted, installed apps' network and notifications off", err)
	default:
		return
	}
	b.save()
}

func (b *broker) save() {
	raw, _ := json.MarshalIndent(b.perms, "", "  ")
	_ = statefile.WriteKeep(b.path, raw, 0o600)
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
	// The service restarted since: a new connection, so the first call to
	// the new process (an emergency call, say) does not fail on the old one.
	if cl != nil && cl.Lost() {
		delete(b.clients, service)
		cl.Close()
		cl = nil
	}
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
	defer b.mu.Unlock()
	// Callers dialling at once keep the first connection.
	if first := b.clients[service]; first != nil {
		cl.Close()
		return first, nil
	}
	b.clients[service] = cl
	return cl, nil
}

// Every service call has a deadline, so a hung service fails its callers
// instead of holding them (and a goroutine each) forever: svcTimeout, or the
// method's own in svcDeadlines.
const svcTimeout = 15 * time.Second

// svcDeadlines are the methods that wait for the user or the outside world;
// those without a deadline (0) wait by design (a finger, a QR code) and end
// with their cancel method or with the service's connection.
var svcDeadlines = map[string]time.Duration{
	svc.Keystored + "/" + keystore.MethodFPAuth:       0,
	svc.Keystored + "/" + keystore.MethodFPEnrollNext: 0,
	svc.Mediad + "/" + svc.MediaScanWait:              0,
	svc.Nfcd + "/" + svc.NFCReaderWait:                75 * time.Second,
	svc.Telephonyd + "/" + svc.TelUSSDInitiate:        time.Minute,
	svc.Telephonyd + "/" + svc.TelUSSDRespond:         time.Minute,
	svc.Telephonyd + "/" + svc.TelDial:                time.Minute,
	svc.Telephonyd + "/" + svc.TelSMSSend:             2 * time.Minute,
	// A modem reads its SIM's phone book with AT commands, record by record.
	svc.Telephonyd + "/" + svc.TelPhonebook: 2 * time.Minute,
	svc.Smsd + "/" + svc.SMSSend:            2 * time.Minute,
}

func svcDeadline(service, method string) time.Duration {
	if d, ok := svcDeadlines[service+"/"+method]; ok {
		return d
	}
	return svcTimeout
}

func (b *broker) rpc(service, method string, params, out any) error {
	files, err := b.rpcFiles(service, method, params, out)
	closeFiles(files)
	return err
}

// rpcFiles is rpc for methods that answer with open files.
func (b *broker) rpcFiles(service, method string, params, out any) ([]*os.File, error) {
	return b.callService(service, method, params, nil, out)
}

// callService hands send (still the caller's) along and returns the files
// of the answer; a connection the service broke is dropped for the next call.
func (b *broker) callService(service, method string, params any, send []*os.File, out any) ([]*os.File, error) {
	cl, err := b.client(service)
	if err != nil {
		return nil, err
	}
	files, err := cl.CallFilesTimeout(method, params, send, out, svcDeadline(service, method))
	var broken *net.OpError
	if err != nil && (strings.Contains(err.Error(), "connection lost") || errors.As(err, &broken)) {
		b.mu.Lock()
		if b.clients[service] == cl {
			delete(b.clients, service)
		}
		b.mu.Unlock()
		cl.Close()
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
	ui.CapTelDTMF:        {ui.PermPhone},
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
	ui.CapSMSDownload:    {ui.PermSMS},
	ui.CapBlockList:      {},
	ui.CapBlockSet:       {},
	ui.CapContacts:       {ui.PermContacts},
	ui.CapContactsSave:   {},
	ui.CapContactsDelete: {ui.PermContacts},
	ui.CapContactsImport: {},
	ui.CapContactsExport: {ui.PermContacts},
	ui.CapContactsGroups: {ui.PermContacts},
	ui.CapContactsGroup:  {ui.PermContacts},
	ui.CapContactsMerge:  {ui.PermContacts},
	ui.CapContactsPick:   {},
	ui.CapContactsPhoto:  {ui.PermContacts},
	ui.CapContactsBooks:  {ui.PermContacts},
	ui.CapContactsDates:  {},
	ui.CapSMSDraft:       {ui.PermSMS},
	ui.CapSMSBackup:      {ui.PermSMS},
	ui.CapSMSRestore:     {ui.PermSMS},
	ui.CapSMSVCard:       {ui.PermSMS},
	ui.CapSMSOTP:         {},
	ui.CapAlarmSet:       {},
	ui.CapAlarmCancel:    {},
	ui.CapAlarmList:      {},
	ui.CapNotifySchedule: {ui.PermNotify},
	ui.CapNotifyCancel:   {ui.PermNotify},
	ui.CapNotifyList:     {ui.PermNotify},
	ui.CapLocaleGet:      {},
	ui.CapHolidaysGet:    {},
	ui.CapTimeOffsets:    {},
	ui.CapMediaList:      {},
	ui.CapMediaList2:     {},
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
	ui.CapVPNList:        {ui.PermSystem},
	ui.CapVPNImport:      {ui.PermSystem},
	ui.CapVPNConnect:     {ui.PermSystem},
	ui.CapVPNDisconnect:  {ui.PermSystem},
	ui.CapVPNDelete:      {ui.PermSystem},
	ui.CapVPNKeygen:      {ui.PermSystem},
	ui.CapVPNSet:         {ui.PermSystem},
	ui.CapAccountState:   {ui.PermSystem},
	ui.CapAccountAdd:     {ui.PermSystem},
	ui.CapAccountSync:    {ui.PermSystem},
	ui.CapAccountSet:     {ui.PermSystem},
	ui.CapAccountRemove:  {ui.PermSystem},
	ui.CapCalendarPull:   {},
	ui.CapCalendarPush:   {},
	ui.CapRadioState:     {ui.PermSystem},
	ui.CapRadioSet:       {ui.PermSystem},
	ui.CapAPNList:        {ui.PermSystem},
	ui.CapAPNSet:         {ui.PermSystem},
	ui.CapHotspotState:   {ui.PermSystem},
	ui.CapHotspotSet:     {ui.PermSystem},
	ui.CapSwitchState:    {ui.PermSystem},
	ui.CapSwitchStart:    {ui.PermSystem},
	ui.CapSwitchStop:     {ui.PermSystem},
	ui.CapSwitchUndo:     {ui.PermSystem},
	ui.CapClipboardSet:   {},
	ui.CapLocationGet:    {ui.PermLocation},
	ui.CapLocationWatch:  {ui.PermLocation},
	ui.CapLocationStop:   {},
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
	ui.CapNetState:       {ui.PermNet},
	ui.CapNetKeepalive:   {ui.PermNet, ui.PermKeepalive},
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
	ui.CapDisplayHint:    {},

	ui.CapLocationBackground: {ui.PermLocationBackground},
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
	// From here on a capability is its version's key: "name@1" is "name"
	// to every check below, and a version past 1 is a capability of its own.
	resolve := b.resolve
	if resolve == nil {
		resolve = ui.ResolveCap
	}
	key, unknown := resolve(m.Cap)
	if unknown == nil {
		m.Cap = key
	}
	stat := b.stats[m.Cap]
	if _, known := capPerms[m.Cap]; known && unknown == nil {
		if stat == nil {
			stat = &CapStat{}
			b.stats[m.Cap] = stat
		}
		stat.Calls++
	}
	dialog := dialogCaps[m.Cap]
	if a.waiting >= appMaxPending || dialog && a.guard.dialogs >= appMaxDialogs {
		if stat != nil {
			stat.Errors++
		}
		b.c.appLogf(a, "%s: %s refused: %d requests in flight", a.M.ID, m.Cap, a.waiting)
		send(ui.Msg{T: ui.MsgRes, Req: m.Req, Err: ui.ErrBusy}, nil)
		return
	}
	a.waiting++
	if dialog {
		a.guard.dialogs++
	}
	replied := false
	reply := func(data any, err error) {
		if !replied && a.waiting > 0 {
			a.waiting--
		}
		if !replied && dialog && a.guard.dialogs > 0 {
			a.guard.dialogs--
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
	if !known && unknown == nil {
		unknown = fmt.Errorf("unknown capability %q", m.Cap)
	}
	if unknown != nil {
		reply(nil, unknown)
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
	if m.Cap == ui.CapMediaList || m.Cap == ui.CapMediaList2 {
		var args ui.MediaArgs
		_ = json.Unmarshal(m.Args, &args)
		if args.Kind == ui.MediaImage || args.Kind == ui.MediaVideo {
			need = append(need, ui.PermPhotos)
		}
	}
	if m.Cap == ui.CapContactsSave {
		// Changing a contact needs the permission to read them; adding
		// one, the user's yes in the dialog.
		var args ui.ContactIDArgs
		_ = json.Unmarshal(m.Args, &args)
		if args.ID != "" {
			need = append(need, ui.PermContacts)
		}
	}
	for _, p := range need {
		if !a.M.Has(p) {
			reply(nil, fmt.Errorf("permission not declared in manifest: %s", p))
			return
		}
	}
	if err := b.c.locRefused(a, m.Cap, permKey(a, m.Origin)); err != nil {
		reply(nil, err)
		return
	}
	b.consent(a, m.Origin, m.Purposes, need, locApproxOnly(m), func(ok bool) {
		if !ok {
			reply(nil, errors.New("permission denied by user"))
			return
		}
		// Asking for the location all the time does not use it.
		if m.Cap != ui.CapLocationBackground {
			b.used(permKey(a, m.Origin), need)
		}
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

// approx: a location request for the approximate location only, which the
// dialog then offers alone.
func (b *broker) consent(a *App, origin string, purposes map[string]string, need []string, approx bool, done func(bool)) {
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
	// Requests waiting for the same answer share one dialog.
	asking := key + "\x00" + strings.Join(ask, ",") + map[bool]string{true: "\x00approx"}[approx]
	if waiting, ok := b.asking[asking]; ok {
		b.asking[asking] = append(waiting, done)
		return
	}
	if b.asking == nil {
		b.asking = map[string][]func(bool){}
	}
	b.asking[asking] = []func(bool){done}
	always := slices.Contains(ask, ui.PermLocationBackground)
	o := &permOverlay{app: a, origin: origin, purposes: purposes, perms: ask, yes: !always,
		location: slices.Contains(ask, ui.PermLocation), approxOnly: approx, approx: approx, always: always}
	o.decide = func(ok bool) {
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
		if ok && o.location {
			b.c.locSetApprox(key, o.approx)
		}
		b.save()
		waiting := b.asking[asking]
		delete(b.asking, asking)
		for _, done := range waiting {
			done(ok)
		}
	}
	b.c.pushOverlay(o)
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
			c.callTouchEnded()
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
		if err := c.locPermCheck(args); err != nil {
			reply(nil, err)
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
		c.locPermSet(args.App, args.Perm, args.State)
		if args.Perm == ui.PermNet {
			if err := c.applyNetPolicy(); err != nil {
				c.log.Printf("traffic: %v", err)
			}
			// The network namespace is fixed when the app starts: going to
			// or from "denied" ends a running app, which starts again
			// under the new rule. Wi-Fi only is a firewall rule, live.
			if t := c.appByID(args.App); t != nil && t.running && wasDenied != (args.State == "denied") {
				c.log.Printf("%s: network %s; restarting it", args.App, args.State)
				if wasDenied {
					c.endApp(t, endGrace)
				} else {
					c.kill(t)
				}
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
		var args ui.LocationGetArgs
		_ = json.Unmarshal(m.Args, &args)
		c.locationGet(a, permKey(a, m.Origin), args, reply)
	case ui.CapLocationWatch:
		var args ui.LocationWatchArgs
		_ = json.Unmarshal(m.Args, &args)
		c.locationWatch(a, permKey(a, m.Origin), args, reply)
	case ui.CapLocationStop:
		reply(c.locationStop(a), nil)
	case ui.CapLocationBackground:
		reply(c.locationBackground(a), nil)
	case ui.CapJobSchedule, ui.CapJobCancel:
		var args ui.JobArgs
		_ = json.Unmarshal(m.Args, &args)
		if m.Cap == ui.CapJobCancel {
			c.cancelJob(a, args.ID)
			reply(map[string]bool{"ok": true}, nil)
			return
		}
		reply(map[string]bool{"ok": true}, c.scheduleJob(a, args))
	case ui.CapNetState:
		c.netStateCap(a, reply)
	case ui.CapNetKeepalive:
		c.keepaliveCap(a, m, reply)
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
		if c.resetSetting(args, func(err error) { reply(c.settingsGet(), err) }) || c.securitySetting(args, func(err error) { reply(c.settingsGet(), err) }) {
			return
		}
		if c.minorSetting(args, func(err error) { reply(c.settingsGet(), err) }) || c.screenTimeSetting(args, func(err error) { reply(c.settingsGet(), err) }) {
			return
		}
		if ok, err := c.locationSetting(args); ok {
			reply(c.settingsGet(), err)
			return
		}
		if ok, err := c.appNetSetting(args); ok {
			reply(c.settingsGet(), err)
			return
		}
		if c.messagingSetting(args, func(err error) { reply(c.settingsGet(), err) }) {
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
		b.dispatchRadios(a, m, reply)
	case ui.CapRadioState, ui.CapRadioSet:
		b.dispatchAirplane(m, reply)
	case ui.CapAPNList, ui.CapAPNSet:
		b.dispatchAPN(m, reply)
	case ui.CapHotspotState, ui.CapHotspotSet:
		b.dispatchHotspot(m, reply)
	case ui.CapSwitchState, ui.CapSwitchStart, ui.CapSwitchStop, ui.CapSwitchUndo:
		b.dispatchSwitch(m, reply)
	case ui.CapBTSend:
		b.c.btSend(a, m, reply)
	case ui.CapVPNList, ui.CapVPNImport, ui.CapVPNConnect, ui.CapVPNDisconnect, ui.CapVPNDelete, ui.CapVPNKeygen, ui.CapVPNSet:
		b.c.dispatchVPN(m, reply)
	case ui.CapAccountState, ui.CapAccountAdd, ui.CapAccountSync, ui.CapAccountSet, ui.CapAccountRemove:
		b.c.dispatchAccount(m, reply)
	case ui.CapCalendarPull, ui.CapCalendarPush:
		b.c.dispatchCalendar(a, m, reply)
	case ui.CapNFCReply, ui.CapNFCState, ui.CapNFCSet, ui.CapNFCRead, ui.CapNFCTransceive:
		b.dispatchNFC(a, m, reply)
	case ui.CapNotifySchedule, ui.CapNotifyCancel, ui.CapNotifyList:
		b.dispatchSchedule(a, m, reply)
	case ui.CapDisplayHint:
		var args ui.DisplayHintArgs
		_ = json.Unmarshal(m.Args, &args)
		c.displayHint(a, args, reply)
	case ui.CapLocaleGet:
		reply(c.localeInfo(), nil)
	case ui.CapHolidaysGet:
		reply(c.holidayCalendar(), nil)
	case ui.CapTimeOffsets:
		var args ui.ZoneOffsetsArgs
		_ = json.Unmarshal(m.Args, &args)
		reply(c.sched.zoneOffsets(args))
	default:
		b.dispatchAudio(a, m, reply)
	}
}

func (b *broker) endCall(id string) {
	delete(b.c.calls, id)
	b.c.callTouchEnded()
	go func() { _ = b.rpc(svc.Mediad, svc.MediaCallEnd, map[string]string{"call_id": id}, nil) }()
}
