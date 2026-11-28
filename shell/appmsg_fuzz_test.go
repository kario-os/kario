package shell

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// What an app's messages may not reach here, on a machine others share: a
// TCP listener (switch.start), a connection to any host (net.keepalive), a
// CardDAV server's discovery (account.add, past keystored's seal), and the
// settings that hold for the whole test process and start the app again
// (language, region).
var (
	appFuzzDenied         = map[string]bool{ui.CapSwitchStart: true, ui.CapNetKeepalive: true, ui.CapAccountAdd: true}
	appFuzzDeniedSettings = map[string]bool{ui.SettingLanguage: true, ui.SettingRegion: true}
)

// appFuzzSlow are the capabilities whose answer waits by design for the
// outside world: an SMS with a code (up to 300 s), a location fix, a tag.
var appFuzzSlow = map[string]bool{ui.CapSMSOTP: true, ui.CapLocationGet: true, ui.CapNFCRead: true}

// appFuzzKinds are the messages other than requests a line may pick.
var appFuzzKinds = []string{ui.MsgPush, ui.MsgReplace, ui.MsgReset, ui.MsgUpdate, ui.MsgPop, ui.MsgState,
	ui.MsgToast, ui.MsgNotify, ui.MsgPong, ui.MsgHello}

const (
	appFuzzID    = "fuzz.app"
	appFuzzLines = 64
	// appFuzzAnswer bounds the wait for every answer after the last line;
	// appFuzzLoop, how long the Shell's loop may take to come round.
	appFuzzAnswer = 20 * time.Second
	appFuzzLoop   = 5 * time.Second
	// What an input may allocate, a Shell started for it included.
	appFuzzAlloc        = 256 << 20
	appFuzzAllocPerLine = 16 << 20
)

// appFuzzCaps are the capabilities a line may pick: every one the broker
// knows, but those appFuzzDenied names.
func appFuzzCaps() []string {
	var caps []string
	for c := range capPerms {
		if !appFuzzDenied[c] {
			caps = append(caps, c)
		}
	}
	slices.Sort(caps)
	return caps
}

// appFuzzLine is the message a line of the input stands for: one starting
// with { as it is; else its first byte picks a capability or, past them,
// another kind of message, and the rest is its arguments, screen,
// notification or text.
func appFuzzLine(caps []string, req int, l []byte) []byte {
	if len(l) == 0 || l[0] == '{' {
		return l
	}
	i, rest := int(l[0])%(len(caps)+len(appFuzzKinds)), l[1:]
	obj := json.RawMessage(rest)
	if !json.Valid(rest) {
		obj, _ = json.Marshal(string(rest))
	}
	var m map[string]any
	if i < len(caps) {
		m = map[string]any{"t": ui.MsgReq, "req": req, "cap": caps[i]}
		if len(rest) > 0 {
			m["args"] = obj
		}
	} else {
		switch k := appFuzzKinds[i-len(caps)]; k {
		case ui.MsgPush, ui.MsgReplace, ui.MsgReset, ui.MsgUpdate:
			m = map[string]any{"t": k, "screen": obj}
		case ui.MsgNotify:
			m = map[string]any{"t": k, "notif": obj}
		case ui.MsgState:
			m = map[string]any{"t": k, "data": obj}
		case ui.MsgToast:
			m = map[string]any{"t": k, "text": string(rest)}
		default:
			m = map[string]any{"t": k, "value": string(rest), "proto": string(rest)}
		}
	}
	b, _ := json.Marshal(m)
	return b
}

// appFuzzRead is line as the Shell reads it (wire.Conn.Recv into ui.Msg),
// the capability resolved as the broker resolves it; ok is false for a
// line the Shell drops as malformed.
func appFuzzRead(line []byte) (m ui.Msg, ok bool) {
	if json.Unmarshal(line, &m) != nil {
		return m, false
	}
	if m.T == ui.MsgReq {
		if key, err := ui.ResolveCap(m.Cap); err == nil {
			m.Cap = key
		}
	}
	return m, true
}

// appFuzzSafe: the line asks for nothing appFuzzDenied names.
func appFuzzSafe(m ui.Msg) bool {
	if m.T != ui.MsgReq {
		return true
	}
	if appFuzzDenied[m.Cap] {
		return false
	}
	if m.Cap == ui.CapSettingsSet {
		var a ui.SettingArgs
		_ = json.Unmarshal(m.Args, &a)
		return !appFuzzDeniedSettings[a.Key]
	}
	return true
}

func appFuzzSeed(f *testing.F, lines ...string) {
	f.Add([]byte(strings.Join(lines, "\n")))
}

func appFuzzReq(req int, cap string, args any) string {
	m := map[string]any{"t": ui.MsgReq, "req": req, "cap": cap}
	if args != nil {
		m["args"] = args
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// appFuzzRig is what every input shares in one process: the app's manifest
// and stand-ins for the system services, each answering every call with
// an error at once.
type appFuzzRig struct {
	apps, bin, run string
	mu             sync.Mutex
	conns          []net.Conn
}

// hangUp ends every connection to the services, so what a Shell that
// shut down kept open to them ends too.
func (r *appFuzzRig) hangUp() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, nc := range r.conns {
		nc.Close()
	}
	r.conns = nil
}

func newAppFuzzRig(f *testing.F) *appFuzzRig {
	if testing.Short() {
		f.Skip("the Shell's app messages are not fuzzed in -short mode")
	}
	if os.Geteuid() == 0 {
		f.Skip("as root the Shell drives the host's network, clock and radios")
	}
	// No host radios over the system bus, no web view, no USB gadget; and
	// temporary files of the Shell's go to a directory of the test's.
	f.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path=/nonexistent")
	for _, v := range []string{"PHONE_SIM_RADIOS", "PHONE_APP_SCOPES", "PHONE_WEBVIEW"} {
		f.Setenv(v, "")
	}
	f.Setenv("TMPDIR", f.TempDir())
	sysRoot := hal.SysRoot
	hal.SysRoot = f.TempDir()
	f.Cleanup(func() { hal.SysRoot = sysRoot })
	run, err := os.MkdirTemp("", "appfuzz-")
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { os.RemoveAll(run) })
	r := &appFuzzRig{apps: f.TempDir(), bin: f.TempDir(), run: run}
	perms := map[string]bool{ui.PermCamera: true, ui.PermPhotos: true, ui.PermContacts: true, ui.PermNotify: true, ui.PermVoIP: true}
	for _, need := range capPerms {
		for _, p := range need {
			perms[p] = true
		}
	}
	delete(perms, ui.PermKeepalive)
	var declared, granted []string
	for p := range perms {
		declared = append(declared, p)
		if _, runtime := ui.RuntimePerms[p]; runtime {
			granted = append(granted, p)
		}
	}
	slices.Sort(declared)
	slices.Sort(granted)
	m, _ := json.Marshal(map[string]any{"id": appFuzzID, "name": "Fuzz", "exec": "./nonexistent", "permissions": declared, "granted": granted})
	dir := filepath.Join(r.apps, appFuzzID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0o644); err != nil {
		f.Fatal(err)
	}
	for _, sock := range []string{svc.MediadSock, svc.AudiodSock, svc.KeystoredSock, svc.ImedSock, svc.PushdSock,
		svc.TelephonydSock, svc.SmsdSock, svc.NfcdSock, svc.DisplaydSock} {
		ln, err := net.Listen("unix", filepath.Join(run, sock))
		if err != nil {
			f.Fatal(err)
		}
		f.Cleanup(func() { ln.Close() })
		why := fmt.Errorf("fuzz: no %s", strings.TrimSuffix(sock, ".sock"))
		go func() {
			for {
				nc, err := ln.Accept()
				if err != nil {
					return
				}
				r.mu.Lock()
				r.conns = append(r.conns, nc)
				r.mu.Unlock()
				go wire.ServeConn(wire.New(nc), func(string, json.RawMessage) (any, error) { return nil, why })
			}
		}()
	}
	return r
}

// appFuzzRun is one input's Shell, its loop on a goroutine of its own.
type appFuzzRun struct {
	t    *testing.T
	c    *Core
	stop chan struct{}
	done chan struct{}
}

// on runs f on the loop and fails if the loop does not come round in time.
func (r *appFuzzRun) on(what string, f func()) {
	r.t.Helper()
	ran := make(chan struct{})
	timeout := time.After(appFuzzLoop)
	select {
	case r.c.ev <- func() { f(); close(ran) }:
	case <-timeout:
		r.t.Fatalf("the Shell's loop took no event for %v (%s)", appFuzzLoop, what)
	}
	select {
	case <-ran:
	case <-timeout:
		r.t.Fatalf("the Shell's loop did not come round in %v (%s)", appFuzzLoop, what)
	}
}

// FuzzAppMsg is what an app process writes to the Shell: each line a
// message of phone.ui (22-developer-api.md), a request going through
// every check of the broker (busy limits, the manifest's permissions,
// consent, all granted) to the capability with what arguments the input
// gives. The Shell does not panic, answers each request once and in its
// time, keeps its loop coming round and allocates within a bound.
func FuzzAppMsg(f *testing.F) {
	rig := newAppFuzzRig(f)
	caps := appFuzzCaps()
	screen := ui.Screen{ID: "home", Title: "Notes", Body: []ui.Node{{Kind: ui.KindText, Text: "caf\u00e9"},
		{Kind: ui.KindInput, ID: "q"}, {Kind: ui.KindButton, ID: "go", Text: "Go"}}}
	sb, _ := json.Marshal(screen)
	appFuzzSeed(f, `{"t":"push","screen":`+string(sb)+`}`, `{"t":"update","screen":`+string(sb)+`}`,
		`{"t":"replace","screen":{"id":"b","title":"B","body":[]}}`, `{"t":"pop"}`, `{"t":"toast","text":"saved"}`,
		`{"t":"notify","notif":{"id":"n1","title":"Hi","body":"there"}}`, `{"t":"notify","notif":{"id":"c1","kind":"call","title":"Ann"}}`,
		`{"t":"state","data":{"draft":"x"}}`, `{"t":"pong","value":"1"}`, `{"t":"hello","proto":"phone.ui/1"}`, `{"t":"nope"}`)
	appFuzzSeed(f, appFuzzReq(1, ui.CapTelDial, ui.DialArgs{Number: "16900000000"}), appFuzzReq(2, ui.CapTelCalls, nil),
		appFuzzReq(3, ui.CapTelLog, nil), appFuzzReq(4, ui.CapSMSSend, json.RawMessage(`{"to":"10690000","text":"TD unsubscribe"}`)),
		appFuzzReq(5, ui.CapSMSBlock, json.RawMessage(`{"number":"10086","on":true}`)), appFuzzReq(6, ui.CapSMSDelete, json.RawMessage(`{"peer":"10086"}`)),
		appFuzzReq(7, ui.CapSMSThreads, nil), appFuzzReq(8, ui.CapBlockList, nil), appFuzzReq(9, ui.CapTelUSSD, ui.USSDArgs{}))
	appFuzzSeed(f, appFuzzReq(1, ui.CapContacts, nil), appFuzzReq(2, ui.CapContactsGroup, ui.ContactGroupArgs{Op: ui.ContactGroupCreate, Name: "Family"}),
		appFuzzReq(3, ui.CapContactsMerge, ui.ContactMergeArgs{IDs: []string{"c1", "c2"}}), appFuzzReq(4, ui.CapContactsGroups, nil),
		appFuzzReq(5, ui.CapContactsBooks, nil), appFuzzReq(6, ui.CapContactsDates, nil), appFuzzReq(7, ui.CapContactsSave, ui.ContactIDArgs{ID: "c1"}))
	appFuzzSeed(f, appFuzzReq(1, ui.CapSettingsGet, nil), appFuzzReq(2, ui.CapSettingsSet, ui.SettingArgs{Key: ui.SettingBrightness, Value: 50}),
		appFuzzReq(3, ui.CapSettingsSet, ui.SettingArgs{Key: ui.SettingTheme, Value: 1}), appFuzzReq(4, ui.CapPermList, nil),
		appFuzzReq(5, ui.CapPermSet, ui.PermSetArgs{App: appFuzzID, Perm: ui.PermContacts, State: "ask"}), appFuzzReq(6, ui.CapContacts, nil),
		appFuzzReq(7, ui.CapClipboardSet, ui.ClipArgs{}), appFuzzReq(8, ui.CapLocaleGet, nil), appFuzzReq(9, ui.CapTimeOffsets, ui.ZoneOffsetsArgs{}))
	appFuzzSeed(f, appFuzzReq(1, ui.CapLocationGet, ui.LocationGetArgs{TimeoutS: 1}), appFuzzReq(2, ui.CapWifiState, nil),
		appFuzzReq(3, ui.CapWifiConnect, ui.WifiArgs{SSID: "Cafe", Passphrase: "12345678"}), appFuzzReq(4, ui.CapBTState, nil),
		appFuzzReq(5, ui.CapBTSet, ui.BTArgs{}), appFuzzReq(6, ui.CapRadioState, nil), appFuzzReq(7, ui.CapHotspotState, nil),
		appFuzzReq(8, ui.CapVPNList, nil), appFuzzReq(9, ui.CapVPNSet, ui.VPNArgs{}), appFuzzReq(10, ui.CapNetState, nil), appFuzzReq(11, ui.CapAPNList, nil))
	appFuzzSeed(f, appFuzzReq(1, ui.CapMediaList, ui.MediaArgs{Kind: ui.MediaImage}), appFuzzReq(2, ui.CapMediaList2, ui.MediaListArgs{}),
		appFuzzReq(3, ui.CapAudioPlay, ui.AudioPlayArgs{}), appFuzzReq(4, ui.CapAudioState, nil), appFuzzReq(5, ui.CapAudioControl, ui.AudioControlArgs{}),
		appFuzzReq(6, ui.CapFileSave, ui.FileArgs{}), appFuzzReq(7, ui.CapCameraCapture, nil), appFuzzReq(8, ui.CapWallpaperSet, ui.WallpaperArgs{}),
		appFuzzReq(9, ui.CapVideoOpen, ui.VideoArgs{}), appFuzzReq(10, ui.CapDisplayHint, ui.DisplayHintArgs{}))
	appFuzzSeed(f, appFuzzReq(1, ui.CapPkgList, nil), appFuzzReq(2, ui.CapPkgCatalog, ui.CatalogArgs{}), appFuzzReq(3, ui.CapPkgRevocations, ui.RevocationArgs{}),
		appFuzzReq(4, ui.CapPkgInstall, ui.PkgArgs{}), appFuzzReq(5, ui.CapSystemUpdate, ui.UpdateArgs{}), appFuzzReq(6, ui.CapIntentInvoke, ui.IntentArgs{}),
		appFuzzReq(7, ui.CapShareOpen, ui.ShareArgs{}), appFuzzReq(8, ui.CapJobSchedule, ui.JobArgs{}), appFuzzReq(9, ui.CapAlarmList, nil),
		appFuzzReq(10, ui.CapNotifyList, nil), appFuzzReq(11, ui.CapPushKey, ui.PushKeyArgs{}), appFuzzReq(12, ui.CapKeySign, ui.SignArgs{}),
		appFuzzReq(13, ui.CapAttest, ui.AttestArgs{}), appFuzzReq(14, ui.CapAuthConfirm, ui.AuthConfirmArgs{}), appFuzzReq(15, ui.CapScanQR, nil),
		appFuzzReq(16, ui.CapNFCState, nil), appFuzzReq(17, ui.CapAccountState, nil), appFuzzReq(18, ui.CapCalendarPull, ui.CalPullArgs{}))
	appFuzzSeed(f, `not json`, `{"t":"req","req":1,"cap":7}`, `{"t":"req","req":2,"cap":"nope"}`, `{"t":"req","req":3,"cap":"settings.get@9"}`,
		`{"t":"req","req":4,"cap":"settings.get@1","origin":"mini.app","purposes":{"location":"maps"}}`, `{"t":"push","screen":"x"}`,
		"\x00{}", "\x05[1,2]", "\xa0{\"id\":\"s\",\"body\":[{\"kind\":\"list\"}]}", "\x90\"text\"")
	f.Fuzz(func(t *testing.T, data []byte) {
		rig.input(t, caps, data)
	})
}

func (rig *appFuzzRig) input(t *testing.T, caps []string, data []byte) {
	var lines [][]byte
	var want, slow map[int]int = map[int]int{}, map[int]int{}
	for i, l := range bytes.Split(data, []byte("\n")) {
		if i == appFuzzLines {
			break
		}
		l = appFuzzLine(caps, i+1, bytes.TrimSuffix(l, []byte("\r")))
		m, ok := appFuzzRead(l)
		if ok && !appFuzzSafe(m) {
			continue
		}
		if ok && m.T == ui.MsgReq {
			if appFuzzSlow[m.Cap] {
				slow[m.Req]++
			} else {
				want[m.Req]++
			}
		}
		lines = append(lines, l)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	alloc0 := ms.TotalAlloc

	c, err := New(Config{W: 40, H: 24, AppsDir: rig.apps, AppBinDir: rig.bin, RunDir: rig.run, StateDir: t.TempDir(),
		Log: discardLog{}, FreezeAfter: -1})
	if err != nil {
		t.Fatal(err)
	}
	r := &appFuzzRun{t: t, c: c, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for {
			select {
			case f := <-c.ev:
				f()
			case <-r.stop:
				return
			}
		}
	}()
	sleep := exec.Command("sleep", "120")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Non-blocking, so closing it ends the read below.
	if err := syscall.SetNonblock(fds[0], true); err != nil {
		t.Fatal(err)
	}
	app, shell := os.NewFile(uintptr(fds[0]), "app"), os.NewFile(uintptr(fds[1]), "shell")
	t.Cleanup(func() { app.Close() })
	var a *App
	r.on("adopt", func() {
		c.SetScreenAuto(false)
		c.Adopt([]Inherited{{ID: appFuzzID, Pid: sleep.Process.Pid, File: shell, Major: ui.ProtoMajor}})
		a = c.appByID(appFuzzID)
		c.setForeground(a)
	})
	var mu sync.Mutex
	got := map[int]int{}
	read := make(chan struct{})
	go func() {
		defer close(read)
		sc := bufio.NewScanner(app)
		sc.Buffer(nil, wire.MaxLine+1)
		for sc.Scan() {
			var m ui.Msg
			if json.Unmarshal(sc.Bytes(), &m) == nil && m.T == ui.MsgRes {
				mu.Lock()
				got[m.Req]++
				mu.Unlock()
			}
		}
	}()
	go func() {
		for _, l := range lines {
			if _, err := app.Write(append(l, '\n')); err != nil {
				return
			}
		}
	}()

	// The user says no to every dialog, as soon as one shows.
	for deadline := time.Now().Add(appFuzzAnswer); ; {
		var running bool
		r.on("a dialog", func() {
			running = a.running && a.guard.violation == ""
			if len(c.overlays) > 0 {
				c.Key(Named(KeyEsc))
			}
		})
		mu.Lock()
		missing, twice := appFuzzMissing(want, slow, got)
		mu.Unlock()
		if twice != "" {
			t.Fatalf("answered more often than asked: %s", twice)
		}
		if missing == "" {
			break
		}
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no answer in %v to %s", appFuzzAnswer, missing)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The app goes as a crashed one goes; its process is reaped once the
	// Shell is done with it, so its pid is no other's meanwhile.
	_ = sleep.Process.Kill()
	app.Close()
	<-read
	for deadline := time.Now().Add(appFuzzAnswer); ; time.Sleep(20 * time.Millisecond) {
		var running bool
		r.on("the app's end", func() { running = a.running })
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the app's end not seen in %v", appFuzzAnswer)
		}
	}
	r.on("shutdown", c.Shutdown)
	close(r.stop)
	<-r.done
	// What the loop fed and a Shell's Shutdown leaves open: with the loop
	// gone nothing sends on them.
	rig.hangUp()
	if c.dev.ln != nil {
		c.dev.ln.Close()
	}
	close(c.nfc.out)
	close(c.display.out)
	runtime.ReadMemStats(&ms)
	if d, limit := ms.TotalAlloc-alloc0, uint64(appFuzzAlloc+appFuzzAllocPerLine*len(lines)); d > limit {
		t.Fatalf("allocated %d MB for %d lines (limit %d MB)", d>>20, len(lines), limit>>20)
	}
}

// appFuzzMissing names the requests still without their answers, and
// those answered more often than asked.
func appFuzzMissing(want, slow, got map[int]int) (missing, twice string) {
	var m, tw []string
	for id, n := range want {
		if got[id] < n {
			m = append(m, fmt.Sprint(id))
		}
	}
	for id, n := range got {
		if n > want[id]+slow[id] {
			tw = append(tw, fmt.Sprint(id))
		}
	}
	slices.Sort(m)
	slices.Sort(tw)
	return strings.Join(m, " "), strings.Join(tw, " ")
}

type discardLog struct{}

func (discardLog) Write(p []byte) (int, error) { return len(p), nil }
