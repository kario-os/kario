// Package shell is the system shell: it launches app processes, renders their
// phone.Ui screens, owns focus, keys, menus, the back stack, notifications and
// every system dialog, and brokers capability requests to system services.
//
// Concurrency: all state is owned by the loop goroutine. Everything else
// (app sockets, service calls, media frames, timers) posts closures to it.
package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/media"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

type Config struct {
	W, H      int
	Keypad    string // "qwerty" (full keyboard) or "t9" (9-key pad)
	AppsDir   string
	AppBinDir string
	RunDir    string
	StateDir  string
	Log       io.Writer
	// SKU is the hardware variant (sku.go); zero on a developer machine.
	SKU SKU
	// UpdateURL is the update server (latest.json and bundles, update.go);
	// UpdateKey, if set, the file with the Ed25519 public key its
	// latest.json must be signed with (base64).
	UpdateURL string
	UpdateKey string
	// FreezeAfter is how long a background app may keep running before it is
	// suspended; 0 means the default, negative never.
	FreezeAfter time.Duration
	// AppEnv is added to every app's environment: endpoints of the
	// simulated app clouds (PHONE_CHAT_SERVER for the chat app's server).
	AppEnv []string
	// ExtraAppsDirs are scanned for manifests too (the compatibility
	// probe, installed only for phone-shell --cts).
	ExtraAppsDirs []string
	// DevApp is the developer's app directory and DevRunner the program
	// that runs it (devapp.go, kario run).
	DevApp    string
	DevRunner []string
	// ANRAfter is how long the foreground app may take to answer a ping
	// before the system offers to close it; 0 means the default.
	ANRAfter time.Duration
	// FDStore keeps app connections in systemd's fd store, so apps outlive
	// a crashed Shell and the restarted one takes them over.
	FDStore bool
}

type Manifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Icon        string   `json:"icon"`
	Desc        string   `json:"description"`
	Exec        string   `json:"exec"`
	Args        []string `json:"args"`
	Runtime     string   `json:"runtime,omitempty"` // "js": kario-jsrt runs the module Main (jsapp.go)
	Main        string   `json:"main,omitempty"`
	Permissions []string `json:"permissions"`
	// Purposes say why the app needs each runtime permission it declares:
	// the system's dialog shows it, and installing an app without one
	// for each fails (the store's review reads them too).
	Purposes   map[string]string `json:"purposes,omitempty"`
	Background []string          `json:"background"` // background modes, e.g. "voip"
	// Granted pre-grants runtime permissions. Honoured only for apps in the
	// system image (AppsDir); the store installer refuses the field.
	Granted  []string `json:"granted"`
	Requires []string `json:"requires"`
	Order    int      `json:"order"`
	// IconImage is a PNG in the app directory (at most 256x256), drawn by
	// the pixel renderer; Icon stays the text renderer's glyph.
	IconImage string `json:"icon_image"`
	// Share lists what the app accepts from the share sheet: "text", "image".
	Share []string `json:"share"`
	// Intents are the actions the app performs for others (intent.go).
	Intents []IntentDecl `json:"intents,omitempty"`
	// Settings are shown and kept by the system (Settings › App settings);
	// Widget lets the app keep one line on the standby screen.
	Settings []ui.AppSetting `json:"settings"`
	Widget   bool            `json:"widget"`
	// NotifyActions go on the notifications the system shows for the app's
	// pushes (title and body): a quick reply, say. Notifications the app
	// posts itself carry their own.
	NotifyActions []ui.Action `json:"notify_actions"`
	// Commands are entry points the app contributes to the command palette:
	// each opens the app with its deep link.
	Commands []AppCommand `json:"commands"`
	Version  string       `json:"version"`
	// Folder puts the app in a folder of the main menu ("tools", "apps")
	// instead of on it; apps from the store go in "apps" by default.
	Folder string `json:"folder"`
	// NFC lists the contactless cards the app answers readers as (nfc.go).
	NFC []ui.NFCCard `json:"nfc,omitempty"`
	// Lang is the language of the text above (default zh-Hans), Locales
	// the same text in other languages; the fields above show the
	// device's language (localize), base keeps the manifest's own.
	Lang    string                  `json:"lang,omitempty"`
	Locales map[string]i18n.AppText `json:"locales,omitempty"`
	base    *Manifest
	dir     string
	// installed: from a package (install.go), not the system image.
	installed bool
}

// AppCommand is one contributed command: its label in the palette and the
// deep link it opens the app with.
type AppCommand struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Arg   string `json:"arg"`
}

const maxCommands = 8

// IconPath is the absolute path of the app's icon image, or "" without one.
func (m *Manifest) IconPath() string {
	if m.IconImage == "" || m.dir == "" {
		return ""
	}
	p := filepath.Join(m.dir, filepath.Base(m.IconImage))
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() || st.Size() > 512<<10 {
		return ""
	}
	return p
}

func (m *Manifest) Has(perm string) bool {
	if m.installed && systemOnly[perm] {
		return false
	}
	for _, p := range m.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

type View struct {
	S        ui.Screen
	focus    string
	focusIdx int
	scroll   int
	ensure   bool
	stick    bool
	inputs   map[string]string
	appVals  map[string]string
	links    map[string][]docLink
	geom     map[int][2]int
	bodyH    int
	// The last frame's screen, focus index and node count, matched against
	// the renderer's reported layout; pixel/pfrac: a tall focused node is
	// being stepped through by pixel geometry (frame scroll.frac).
	frameScreen string
	frameFocus  int
	frameNodes  int
	// frameIDs: the node behind each of the frame's body nodes (a grid's,
	// one per tile), for a pointer's click (Tap).
	frameIDs [][]string
	pixel    bool
	pfrac    float64
}

type App struct {
	M       *Manifest
	started time.Time
	proc    *os.Process
	// adopted: taken over from a previous Shell, so not this one's child.
	adopted bool
	conn    *wire.Conn
	stack   []*View
	ready   bool
	running bool
	system  bool
	arg     string
	out     chan outbound
	frozen  bool
	// focusLost: the app was told a dialog or the screen off took its keys.
	focusLost bool
	bgSince   time.Time
	// ending: the low-memory killer has ended it; the exit is on its way.
	ending bool
	// killed: the system ended it (not a crash, crash.go).
	killed bool
	// alerts: when its notifications last alerted (rate limit).
	alerts []time.Time
	// overLimit: a screen of it was cut (logged once).
	overLimit bool
	// verified: an installed app's files passed the check this boot.
	verified bool
	// ping: the responsiveness probe in flight (anr.go).
	ping struct {
		seq  int
		sent time.Time
	}
	// waiting counts its capability requests the system has not answered
	// yet (a consent dialog, the trusted confirmation): not an ANR.
	waiting int
	// Push events waiting for the app to be ready or back in the foreground,
	// and recent background wakes (see push.go).
	pushQ     []ui.Msg
	pushWakes []time.Time
	// voip: the deadline to report a call after a VoIP push, and the
	// recent misses (push.go).
	voipDue     time.Time
	voipStrikes []time.Time
	// normalDue: when held normal-priority pushes get a wake of their own.
	normalDue time.Time
	// saved: the state the app last handed over (ui.MsgState); tomb: what
	// is kept after the low-memory killer ended it; startData: the state a
	// new process starts with; stale: the stack is the old process's pages,
	// only shown; revive: those pages by screen ID (tomb.go).
	saved     json.RawMessage
	tomb      *tombstone
	startData json.RawMessage
	stale     bool
	revive    map[string]*View
	// heldKeys: pressed before the app had a live page (starting, or its
	// old pages only showing); replayed once its pages settle (tomb.go).
	heldKeys []Key
	keysGen  int
	// relaunch: ended on the screen for a language change, it starts
	// again at once (language.go).
	relaunch bool
}

func (a *App) top() *View {
	if len(a.stack) == 0 {
		return nil
	}
	return a.stack[len(a.stack)-1]
}

type outbound struct {
	m     ui.Msg
	files []*os.File
}

// send queues a message without blocking the loop; order is preserved.
func (a *App) send(m ui.Msg) { a.sendFiles(m, nil) }

// sendFiles also hands open files to the app; the shell's copies are closed
// once sent (or dropped).
func (a *App) sendFiles(m ui.Msg, files []*os.File) {
	m.FDs = len(files)
	if a.out == nil || !a.running {
		closeFiles(files)
		return
	}
	select {
	case a.out <- outbound{m, files}:
	default:
		closeFiles(files)
	}
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		f.Close()
	}
}

type Core struct {
	cfg  Config
	log  *log.Logger
	apps []*App
	sys  *App
	fg   *App
	mru  []*App

	crashes []CrashRecord

	lowMem struct {
		checked, killed, idle     time.Time
		simulate, count, webCount int
	}

	overlays []overlay
	banner   *banner
	// screen: the panel's power and the notification light (screen.go);
	// usb: the cable to a computer (usb.go); gui: the renderer in use.
	screen screenState
	usb    usbState
	cpu    cpuStats
	sleep  sleepState
	// focusApp: the app updateFocus last spoke to.
	focusApp *App
	// powerKey: the board has its own power key (the end key is not it).
	powerKey bool
	gui      *GUI
	// OnInput hears of keyboards and mice plugged in or out (the frontend
	// reports them on the console).
	OnInput func(kind, name string, added bool)
	toast   *banner
	notifs  []*notif
	calls   map[string]*callInfo

	broker  *broker
	reply   *replyState // a quick reply being typed (sys.reply)
	subs    map[string]*media.Sub
	want    map[string]bool
	wantGUI map[string]bool // media surfaces the pixel renderer shows
	// surfs: streams in shared memory for a renderer with "shm" (pixels
	// go from mediad to the renderer); frameSurfs: those the last frame
	// shows, whose fds the renderer must have.
	surfs      map[string]*media.SurfaceSub
	frameSurfs map[string]bool
	// webprocs: each app's web process (webview.go); webviews: the pages of
	// web nodes in apps' screens; wantWeb: those the last frame showed.
	webprocs   map[string]*webProc
	webviews   map[string]*webView
	wantWeb    map[string]webWant
	webStarts  map[string][]time.Time
	webNextID  int
	webStarted int
	update     updater
	Frames     int
	docCache   map[string]*docResult
	ime        *imeClient
	imeOwner   string
	imeSaved   ime.Mode

	ev       chan func()
	pending  atomic.Bool
	quit     bool
	stopping atomic.Bool

	tel        *telStatus            // nil: no telephony service
	prompt     *webPromptState       // a page's prompt() on the system screen
	wallpapers map[string]*wallpaper // the standby pictures cut to the screen
	// thumbSurfs: photo thumbnails in shared memory for the renderer
	// (thumbs.go), those the last frame showed.
	thumbSurfs  map[string]*media.Surface
	players     map[string]*videoPlayer // video.open, by mediad's player id
	simSeen     string                  // SIM state the PIN screen last followed
	simLater    bool                    // the user put the PIN screen off
	cells       map[string]ui.TelCall   // cellular calls in progress
	contactList []ui.Contact
	alarms      *alarmStore
	clockSkew   time.Duration // selftest: pretend time passed
	audioUp     bool
	keylock     bool
	lock        lockState // the PIN, the fingerprints and what the lock hides (lock.go)
	locAt       time.Time
	jobs        []*job
	fdstore     bool
	jobBatches  int
	// idleWidget is the widget marked on the standby screen (1-based, 0 none).
	idleWidget int
	files      *fileStore
	traffic    *traffic
	prefs      appPrefs
	// lastPushAt is the last push delivery: the radio is up for a while.
	lastPushAt    time.Time
	powerOverride *Power
	clip          clipboard
	unlockArm     time.Time
	emergency     string          // digits typed on the locked keypad (emergency calls)
	silenced      map[string]bool // ringtones a volume key silenced (volume.go)
	vibrations    int
	lastPulse     time.Time
	lastKey       time.Time
	settings      sysSettings
	started       time.Time
	media         *mediaStore
	// screenDev is the pixel display (zero without a pixel renderer); grid
	// layouts that depend on its shape read it.
	screenDev  Device
	renderer   *RendererInfo // the pixel renderer's handshake, nil without one
	layout     *Layout       // the geometry it reported for its last frame
	profile    *Profile      // the composition layers (profile.go)
	cg         *cgroups      // per-app cgroups, nil when the shell has no delegated group
	pushUp     bool
	pushSealed map[string]int
	pushSeen   map[string]bool
	intents    map[string]*pendingIntent // requests to other apps (intent.go)
	use        *appUse                   // when installed apps were last opened (unused.go)
	unusedAt   time.Time
	wifi       wifiRadio // nil without Wi-Fi (radios.go)
	bt         btRadio
	radios     radioShell              // airplane mode, mobile data, Bluetooth file transfer (airplane.go, btobex.go)
	cgOff      string                  // why there are no per-app cgroups
	streams    map[string]audio.Stream // audiod streams, from its events
	sysWant    map[string]bool         // system sounds by key
	sysStream  map[string]string
	notifySeq  int
	booted     time.Time // boot animation until bootDuration later
	locale     localeState
	nfc        nfcShell // phone-nfcd and the card pack (nfc.go, cards.go)
	sms        smsShell // phone-smsd and emergency alerts (messaging.go)
}

type banner struct {
	title, body string
	until       time.Time
}

type notif struct {
	app *App
	n   ui.Notification
	at  time.Time
	// seen: the user has had it in front of them (screen.go).
	seen bool
}

func New(cfg Config) (*Core, error) {
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	c := &Core{
		cfg:        cfg,
		log:        log.New(cfg.Log, "shell: ", log.Ltime|log.Lmicroseconds),
		fdstore:    cfg.FDStore,
		calls:      map[string]*callInfo{},
		subs:       map[string]*media.Sub{},
		surfs:      map[string]*media.SurfaceSub{},
		webprocs:   map[string]*webProc{},
		webviews:   map[string]*webView{},
		wantWeb:    map[string]webWant{},
		thumbSurfs: map[string]*media.Surface{},
		players:    map[string]*videoPlayer{},
		webStarts:  map[string][]time.Time{},
		docCache:   map[string]*docResult{},
		ev:         make(chan func(), 512),
	}
	keypad := ime.QWERTY
	if cfg.Keypad == string(ime.T9Pad) {
		keypad = ime.T9Pad
	}
	_ = os.MkdirAll(filepath.Join(cfg.StateDir, "shell"), 0o700)
	c.ime = newIMEClient(filepath.Join(cfg.RunDir, svc.ImedSock), filepath.Join(cfg.StateDir, "shell", "ime-user.json"), keypad, c.log.Printf)
	c.loadProfile()
	if err := c.loadManifests(); err != nil {
		return nil, err
	}
	c.sys = &App{M: &Manifest{ID: "system", Name: tr.T("System")}, running: true, ready: true, system: true}
	c.media = openMedia(filepath.Join(cfg.StateDir, "media"), filepath.Join(filepath.Dir(cfg.AppsDir), "examples"))
	c.files = openFiles(filepath.Join(cfg.StateDir, "files"))
	c.sys.stack = []*View{newView(c.idleScreen())}
	b, err := newBroker(c)
	if err != nil {
		return nil, err
	}
	c.broker = b
	c.probeVideos()
	c.reportUpdate()
	c.booted = time.Now()
	c.alarms = loadAlarms(filepath.Join(cfg.StateDir, "shell", "alarms.json"))
	c.started, c.lastKey = time.Now(), time.Now()
	c.loadSettings()
	c.loadPrefs()
	c.ime.SetFuzzy(c.settings.IMEFuzzy)
	c.startLocale()
	loadBrand(filepath.Dir(cfg.AppsDir))
	c.loadJobs()
	if os.Getenv("PHONE_APP_SCOPES") == "1" {
		if g, err := setupScopes(); err == nil {
			c.cg = g
			c.log.Printf("cgroups: one systemd scope per app in %s (memory %d MB, %d processes)", appsSlice, appMemoryMax>>20, appPidsMax)
		} else {
			c.cgOff = err.Error()
			c.log.Printf("cgroups: no app scopes (%v); background apps freeze with SIGSTOP", err)
		}
	} else if g, err := setupCgroups(); err == nil {
		c.cg = g
		c.log.Printf("cgroups: one group per app under %s (memory %d MB, %d processes)", g.base, appMemoryMax>>20, appPidsMax)
	} else {
		c.cgOff = err.Error()
		c.log.Printf("cgroups: off (%v); background apps freeze with SIGSTOP", err)
	}
	c.setupTraffic()
	c.startRadios()
	c.startTelephony()
	c.startSMS()
	c.startAudio()
	c.startPush()
	c.startNFC()
	c.setupScreen(cfg.SKU)
	c.startLock()
	c.startUSB()
	go func() {
		for {
			time.Sleep(time.Duration(c.screen.tick.Load()))
			c.Post(func() { c.expire() })
		}
	}()
	return c, nil
}

// Events is the channel the frontend drains; every closure must run on the loop.
func (c *Core) Events() <-chan func() { return c.ev }

func (c *Core) Post(f func()) { c.ev <- f }

// postFrame coalesces media frame notifications into at most one pending redraw.
func (c *Core) postFrame() {
	if c.pending.CompareAndSwap(false, true) {
		select {
		case c.ev <- func() { c.pending.Store(false) }:
		default:
			c.pending.Store(false)
		}
	}
}

func (c *Core) Quit() bool { return c.quit }

// Trace writes a line into the shell log (selftest steps, interleaved with
// what the shell does).
func (c *Core) Trace(format string, args ...any) { c.log.Printf(format, args...) }

func (c *Core) loadManifests() error {
	paths, _ := filepath.Glob(filepath.Join(c.cfg.AppsDir, "*", "manifest.json"))
	sort.Strings(paths)
	for _, d := range c.cfg.ExtraAppsDirs {
		extra, _ := filepath.Glob(filepath.Join(d, "*", "manifest.json"))
		sort.Strings(extra)
		paths = append(paths, extra...)
	}
	paths = append(paths, c.devManifest()...)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m := &Manifest{}
		if err := json.Unmarshal(b, m); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		m.dir = filepath.Dir(p)
		missing := false
		for _, r := range m.Requires {
			if _, err := exec.LookPath(r); err != nil {
				missing = true
			}
		}
		if missing {
			c.log.Printf("skip %s: requires %v", m.ID, m.Requires)
			continue
		}
		if c.profile.hidden(m.ID) {
			c.log.Printf("skip %s: not in this product (profile)", m.ID)
			continue
		}
		c.apps = append(c.apps, &App{M: m})
	}
	c.shadowDev()
	if len(c.apps) == 0 {
		return fmt.Errorf("no app manifests under %s", c.cfg.AppsDir)
	}
	installed, _ := filepath.Glob(filepath.Join(c.packagesDir(), "*", "manifest.json"))
	sort.Strings(installed)
	for _, p := range installed {
		m := &Manifest{}
		if b, err := os.ReadFile(p); err != nil || json.Unmarshal(b, m) != nil {
			c.log.Printf("skip broken package %s", p)
			continue
		}
		if c.appByID(m.ID) != nil || filepath.Base(filepath.Dir(p)) != m.ID {
			c.log.Printf("skip package %s: id taken or misplaced", m.ID)
			continue
		}
		m.dir, m.installed = filepath.Dir(p), true
		c.apps = append(c.apps, &App{M: m})
	}
	for _, a := range c.apps {
		if o, ok := c.profile.AppOrder[a.M.ID]; ok {
			a.M.Order = o
		}
		if err := checkSettingDecls(a.M); err != nil {
			c.log.Printf("%s: settings ignored: %v", a.M.ID, err)
			a.M.Settings = nil
		}
	}
	sort.SliceStable(c.apps, func(i, j int) bool { return c.apps[i].M.Order < c.apps[j].M.Order })
	return nil
}

// AppByID reports whether an app is installed (manifest loaded).
func (c *Core) AppByID(id string) *App { return c.appByID(id) }

func (c *Core) appByID(id string) *App {
	for _, a := range c.apps {
		if a.M.ID == id {
			return a
		}
	}
	return nil
}

func (c *Core) resolveExec(m *Manifest) string {
	if m.Runtime != "" {
		return c.jsRuntime()
	}
	if m.installed {
		return filepath.Join(m.dir, filepath.Base(m.Exec))
	}
	if strings.Contains(m.Exec, "/") {
		return filepath.Join(m.dir, m.Exec)
	}
	for _, d := range []string{c.cfg.AppBinDir, m.dir} {
		p := filepath.Join(d, m.Exec)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath(m.Exec); err == nil {
		return p
	}
	return m.Exec
}

func (c *Core) spawn(a *App) error {
	if err := c.verifyInstalled(a); err != nil {
		return err
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	parent := os.NewFile(uintptr(fds[0]), "ui-parent")
	child := os.NewFile(uintptr(fds[1]), "ui-child")
	dataDir := filepath.Join(c.cfg.StateDir, "apps", a.M.ID)
	_ = os.MkdirAll(dataDir, 0o700)
	cmd, err := c.appCommand(a, dataDir)
	if err != nil {
		parent.Close()
		child.Close()
		return err
	}
	logf, _ := os.OpenFile(filepath.Join(c.cfg.RunDir, a.M.ID+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	cmd.Dir = a.M.dir
	cmd.ExtraFiles = []*os.File{child}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dataDir,
		"LANG=C.UTF-8",
		// The catalogs apps load follow the system's language (package i18n).
		"PHONE_LANG=" + tr.Lang(),
		"TZ=" + c.zoneName(),
		"PHONE_UI_FD=3",
		"PHONE_APP_ID=" + a.M.ID,
		"PHONE_DATA_DIR=" + dataDir,
		// Prototype stand-in for a root pinned inside the relying party's server.
		"PHONE_TRUST_ROOT=" + c.trustRoot(),
	}
	cmd.Env = append(cmd.Env, c.cfg.AppEnv...)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Apps die with the Shell even when it is killed, frozen ones included
	// (on the device systemd also removes the Shell's cgroup), unless their
	// connections sit in systemd's fd store for the restarted Shell.
	if !c.fdstore {
		cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	}
	var group, hold *os.File
	if c.cg != nil && c.cg.scopes {
		// The launcher waits on fd 4 until the app is in its scope.
		var r *os.File
		if r, hold, err = os.Pipe(); err == nil {
			cmd.ExtraFiles = append(cmd.ExtraFiles, r)
			defer r.Close()
			cmd.Env = append(cmd.Env, "PHONE_SANDBOX_HOLD=4", "GOMEMLIMIT="+goMemLimit)
		}
	} else if c.cg != nil {
		if group, err = c.cg.prepare(a.M.ID); err != nil {
			c.log.Printf("%s: cgroup: %v (starting it in the shell's group)", a.M.ID, err)
		} else {
			cmd.SysProcAttr.UseCgroupFD, cmd.SysProcAttr.CgroupFD = true, int(group.Fd())
			cmd.Env = append(cmd.Env, "GOMEMLIMIT="+goMemLimit)
		}
	}
	err = StartProcess(cmd)
	if group != nil {
		group.Close()
	}
	if hold != nil {
		if err == nil {
			if aerr := c.cg.attach(a.M.ID, cmd.Process.Pid); aerr != nil {
				c.log.Printf("%s: scope: %v (running in the shell's group)", a.M.ID, aerr)
			}
		}
		_, _ = hold.Write([]byte{1})
		hold.Close()
	}
	if err != nil {
		parent.Close()
		child.Close()
		return err
	}
	child.Close()
	if c.fdstore {
		c.storeConn(a, cmd.Process.Pid, parent)
	}
	a.proc, a.adopted, a.conn, a.running, a.ready, a.started = cmd.Process, false, wire.New(parent), true, false, time.Now()
	c.log.Printf("spawned %s pid=%d", a.M.ID, cmd.Process.Pid)
	c.serveApp(a, func() error {
		err := cmd.Wait()
		if logf != nil {
			logf.Close()
		}
		return err
	})
	a.startData = c.exhume(a)
	return nil
}

// serveApp runs an app connection: a writer for what the Shell sends and a
// reader that hands messages to the event loop until the app goes away;
// wait reaps it.
func (c *Core) serveApp(a *App, wait func() error) {
	// Files only travel from the system to apps.
	a.conn.RefuseFiles()
	a.stack = nil
	a.out = make(chan outbound, 256)
	conn, out := a.conn, a.out
	go func() {
		for o := range out {
			err := conn.SendFiles(o.m, o.files...)
			closeFiles(o.files)
			if err != nil {
				for o := range out {
					closeFiles(o.files)
				}
				return
			}
		}
	}()
	go func() {
		for {
			var m ui.Msg
			if err := conn.Recv(&m); err != nil {
				err = wait()
				c.Post(func() { c.appExited(a, err) })
				return
			}
			c.Post(func() { c.appMsg(a, m) })
		}
	}()
}

func (c *Core) appExited(a *App, err error) {
	c.log.Printf("app %s exited: %v", a.M.ID, err)
	c.unstoreConn(a)
	c.endIntents(a)
	a.adopted = false
	oom := false
	if c.cg != nil {
		if n := c.cg.oomKills(a.M.ID); n > 0 {
			oom = true
			c.log.Printf("app %s: killed for its memory limit (%d MB)", a.M.ID, appMemoryMax>>20)
			c.showToast(tr.T("“%s” went over its memory limit (%d MB) and was stopped by the system", a.M.Name, appMemoryMax>>20))
		}
		c.cg.release(a.M.ID)
	}
	if a.out != nil {
		close(a.out)
		a.out = nil
	}
	c.bury(a)
	a.running, a.ready, a.stack, a.conn = false, false, nil, nil
	a.startData, a.stale, a.revive, a.heldKeys = nil, false, nil, nil
	c.endWebProc(a)
	if reason := exitReason(err); reason != "" && !oom && !a.killed && !c.stopping.Load() {
		c.recordCrash(a, reason)
	}
	a.frozen, a.bgSince, a.ending, a.killed = false, time.Time{}, false, false
	a.ping.seq, a.ping.sent, a.waiting = 0, time.Time{}, 0
	for id, ci := range c.calls {
		if ci.app == a {
			c.broker.endCall(id)
		}
	}
	if a.tomb == nil {
		c.removeMRU(a)
	}
	if c.relaunched(a) {
		return
	}
	if c.fg == a {
		c.fg = nil
		if !oom {
			c.showToast(tr.T("“%s” quit", a.M.Name))
		}
	}
}

func (c *Core) removeMRU(a *App) {
	for i, x := range c.mru {
		if x == a {
			c.mru = append(c.mru[:i], c.mru[i+1:]...)
			return
		}
	}
}

// Launch brings an app to the foreground, starting it if needed. arg is a
// deep link: the start argument of a new process, an "open" event for a
// running one.
func (c *Core) Launch(a *App, arg string) {
	switch {
	case !a.running:
		a.arg = arg
		if err := c.spawn(a); err != nil {
			a.tomb = nil
			c.removeMRU(a)
			c.showToast(tr.T("Cannot start: %v", err))
			return
		}
	case arg != "" && a.ready:
		a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventOpen, Value: arg})
	case arg != "":
		a.arg = arg
	}
	c.setForeground(a)
}

func (c *Core) setForeground(a *App) {
	if c.fg == a {
		return
	}
	if a == nil {
		c.log.Printf("foreground: home")
	} else {
		c.log.Printf("foreground: %s", a.M.ID)
	}
	if c.cg != nil {
		if c.fg != nil {
			c.cg.weight(c.fg.M.ID, false)
		}
		if a != nil {
			c.cg.weight(a.M.ID, true)
		}
	}
	if c.fg != nil {
		c.fg.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateBackground})
		c.fg.bgSince = time.Now()
		c.stopRecording(c.fg)
	}
	c.fg, c.toast = a, nil
	c.opened(a)
	if a != nil {
		c.markSeen(a)
	}
	if a != nil {
		c.thaw(a)
		c.removeMRU(a)
		c.mru = append([]*App{a}, c.mru...)
		if a.ready {
			a.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateForeground})
		}
		c.flushPush(a)
	}
}

// GoHome returns to the idle screen (the red key on a Nokia).
func (c *Core) GoHome() {
	c.setForeground(nil)
	c.sys.stack = []*View{newView(c.idleScreen())}
}

// current returns the app whose screens are visible (system when fg is nil).
func (c *Core) current() *App {
	if c.fg != nil {
		return c.fg
	}
	return c.sys
}

func newView(s ui.Screen) *View {
	v := &View{S: s, inputs: map[string]string{}, appVals: map[string]string{}, ensure: true, stick: s.Bottom}
	v.prepareDocs()
	v.syncInputs()
	fs := v.focusables()
	switch {
	case s.Focus != "":
		v.focus = s.Focus
	case s.Bottom && len(fs) > 0:
		v.focus = s.Body[fs[len(fs)-1]].ID
	case len(fs) > 0:
		v.focus = s.Body[fs[0]].ID
	}
	v.focusIdx = v.indexOf(v.focus)
	return v
}

// prepareDocs turns every link in a Markdown node into a menu action, so
// links are reachable by keys (Lynx-style numbered links).
func (v *View) prepareDocs() {
	v.links = map[string][]docLink{}
	for i := range v.S.Body {
		n := &v.S.Body[i]
		if n.Kind != ui.KindDoc {
			continue
		}
		links := docLinks(n.Text)
		if len(links) == 0 {
			continue
		}
		if n.ID == "" {
			n.ID = fmt.Sprintf("_doc%d", i)
		}
		v.links[n.ID] = links
		acts := append([]ui.Action{}, n.Actions...)
		for k, l := range links {
			acts = append(acts, ui.Action{ID: fmt.Sprintf("link:%d", k+1), Label: cvTruncate(tr.T("Link %d: %s", k+1, l.text), 24)})
		}
		n.Actions = acts
	}
}

func (v *View) syncInputs() {
	for _, n := range v.S.Body {
		if n.Kind != ui.KindInput {
			continue
		}
		if _, seen := v.inputs[n.ID]; !seen || n.Value != v.appVals[n.ID] {
			v.inputs[n.ID] = n.Value
		}
		v.appVals[n.ID] = n.Value
	}
}

func (v *View) focusables() []int {
	var out []int
	for i := range v.S.Body {
		if v.S.Body[i].Focusable() && v.S.Body[i].ID != "" {
			out = append(out, i)
		}
	}
	return out
}

func (v *View) indexOf(id string) int {
	for i := range v.S.Body {
		if v.S.Body[i].ID == id && id != "" {
			return i
		}
	}
	return -1
}

func (v *View) focused() *ui.Node {
	if i := v.indexOf(v.focus); i >= 0 && v.S.Body[i].Focusable() {
		return &v.S.Body[i]
	}
	return nil
}

// replace installs a new version of the screen, keeping focus by node id;
// a focused node that was wholly on screen stays so, however the update
// changed the nodes' heights (a long one being read is left where it is).
func (v *View) replace(s ui.Screen) {
	fs := v.focusables()
	wasLast := len(fs) > 0 && v.indexOf(v.focus) == fs[len(fs)-1]
	if g, ok := v.geom[v.indexOf(v.focus)]; ok && v.bodyH > 0 && g[0] >= v.scroll && g[0]+g[1] <= v.scroll+v.bodyH {
		v.ensure = true
	}
	v.S = s
	v.prepareDocs()
	v.syncInputs()
	if v.focused() == nil {
		nf := v.focusables()
		v.focus = ""
		for _, i := range nf {
			if i >= v.focusIdx {
				v.focus = s.Body[i].ID
				break
			}
		}
		if v.focus == "" && len(nf) > 0 {
			v.focus = s.Body[nf[len(nf)-1]].ID
		}
		v.ensure = true
	}
	if s.Bottom && wasLast {
		nf := v.focusables()
		if len(nf) > 0 {
			v.focus = s.Body[nf[len(nf)-1]].ID
		}
		v.stick = true
	}
	v.focusIdx = v.indexOf(v.focus)
}

// What one app may put on the shell: an app message is already at most 4 MB
// (wire), but a screen of a hundred thousand nodes or a stack of thousands of
// screens would still take the shell's memory and every frame's time.
const (
	maxScreenNodes = 1000
	maxStackDepth  = 32
)

// reservedTitle: titles of the system's own dialogs (System permission,
// System security check), which start with "System" in the device's
// language. An app's screen titled so gets the app's name in front, so it
// cannot pass for the system asking for a PIN.
func reservedTitle(t string) bool {
	t = strings.ToLower(t)
	// The word in every language the system offers, anywhere in the title:
	// an app may copy a system dialog's title in any of them ("...del sistema").
	for _, l := range i18n.Languages() {
		if strings.Contains(t, strings.ToLower(tr.In(l.ID).T("System"))) {
			return true
		}
	}
	return false
}

// limitScreen cuts a screen to maxScreenNodes, saying so at the end, and
// marks a reserved title as the app's.
func (c *Core) limitScreen(a *App, s *ui.Screen) {
	if reservedTitle(s.Title) {
		s.Title = tr.T("%s: %s", a.M.Name, s.Title)
	}
	if len(s.Body) <= maxScreenNodes {
		return
	}
	if !a.overLimit {
		c.log.Printf("%s: screen %s has %d nodes, cut to %d", a.M.ID, s.ID, len(s.Body), maxScreenNodes)
		a.overLimit = true
	}
	s.Body = append(s.Body[:maxScreenNodes-1:maxScreenNodes-1], ui.Node{Kind: ui.KindText, Style: ui.StyleDim,
		Text: tr.T("(Too long; only the first %d items are shown)", maxScreenNodes-1)})
}

func (c *Core) appMsg(a *App, m ui.Msg) {
	if m.Screen != nil {
		c.limitScreen(a, m.Screen)
	}
	if m.T == ui.MsgPush || m.T == ui.MsgReplace || m.T == ui.MsgReset {
		c.replayKeysSoon(a)
	}
	switch m.T {
	case ui.MsgHello:
		if m.Proto != ui.Proto {
			c.log.Printf("%s speaks %q, want %q", a.M.ID, m.Proto, ui.Proto)
		}
		a.ready = true
		c.log.Printf("%s ready %s after start", a.M.ID, time.Since(a.started).Round(time.Millisecond))
		a.send(ui.Msg{T: ui.MsgStart, Value: a.arg, Data: a.startData})
		a.startData = nil
		if c.fg == a {
			a.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateForeground})
		} else {
			a.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateBackground})
		}
		c.flushPush(a)
	case ui.MsgPush:
		if m.Screen != nil {
			v := a.page(*m.Screen)
			if len(a.stack) >= maxStackDepth {
				// The top page is replaced instead: Back still leads where it did.
				c.log.Printf("%s: page stack at %d; push replaces the top page", a.M.ID, maxStackDepth)
				a.stack[len(a.stack)-1] = v
				break
			}
			a.stack = append(a.stack, v)
		}
	case ui.MsgReplace:
		if m.Screen != nil {
			v := a.page(*m.Screen)
			if len(a.stack) == 0 {
				a.stack = []*View{v}
			} else {
				a.stack[len(a.stack)-1] = v
			}
		}
	case ui.MsgReset:
		if m.Screen != nil {
			a.stack = []*View{a.page(*m.Screen)}
		}
	case ui.MsgState:
		c.saveState(a, m.Data)
		c.stateHandedOver(a)
	case ui.MsgUpdate:
		if m.Screen != nil && !a.stale {
			for _, v := range a.stack {
				if v.S.ID == m.Screen.ID {
					v.replace(*m.Screen)
				}
			}
		}
	case ui.MsgPop:
		if len(a.stack) > 1 && !a.stale {
			v := a.stack[len(a.stack)-1]
			a.stack = a.stack[:len(a.stack)-1]
			a.send(ui.Msg{T: ui.MsgPopped, ScreenID: v.S.ID})
		}
	case ui.MsgToast:
		if c.fg == a {
			c.showToast(m.Text)
		}
	case ui.MsgNotify:
		if m.Notif != nil {
			c.notify(a, *m.Notif)
		}
	case ui.MsgReq:
		c.broker.handle(a, m)
	case ui.MsgPong:
		c.pong(a, m.Value)
	default:
		c.log.Printf("%s: unknown message %q", a.M.ID, m.T)
	}
}

// notifsOff: the user switched the app's notifications off in Settings › App
// permissions.
func (c *Core) notifsOff(a *App) bool {
	return c.broker != nil && c.broker.perms[a.M.ID][ui.PermNotify] == "denied"
}

func (c *Core) notify(a *App, n ui.Notification) {
	if n.Kind == "call" {
		if !a.M.Has(ui.PermVoIP) {
			c.log.Printf("%s: call notification without voip permission", a.M.ID)
			return
		}
		a.voipDue = time.Time{}
		c.pushOverlay(&incomingOverlay{app: a, n: n, at: time.Now()})
		c.sound("voip-ring:"+a.M.ID, "ringtone", audio.Ring)
		return
	}
	if !a.M.Has(ui.PermNotify) {
		c.log.Printf("%s: notification without notifications permission", a.M.ID)
		return
	}
	if c.notifsOff(a) {
		c.log.Printf("%s: notification dropped: the user switched its notifications off", a.M.ID)
		return
	}
	c.addNotif(a, n)
}

// addNotif puts a notification into the centre (the system's own come here
// directly, in the name of the system app they lead to).
func (c *Core) addNotif(a *App, n ui.Notification) {
	for i, x := range c.notifs {
		if x.app == a && x.n.ID == n.ID {
			c.notifs = append(c.notifs[:i], c.notifs[i+1:]...)
			break
		}
	}
	c.notifs = append([]*notif{{app: a, n: n, at: time.Now(), seen: c.fg == a && a != nil && !c.screen.off}}, c.notifs...)
	c.trimNotifs(a)
	c.notified()
	if c.fg == nil && c.sys != nil {
		if top := c.sys.top(); top != nil && top.S.ID == "sys.notifs" {
			top.replace(c.systemScreen("sys.notifs"))
		}
	}
	if c.fg != a && c.alertBudget(a) {
		c.banner = c.notifBanner(a, n)
		c.ping()
	}
}

// An app's notifications may alert (banner and sound) notifyAlertsPerMinute
// times a minute; past that they still go to the notification centre,
// silently. The centre keeps maxNotifsPerApp of each app, the newest.
const (
	notifyAlertsPerMinute = 5
	maxNotifsPerApp       = 24
)

func (c *Core) alertBudget(a *App) bool {
	now := c.now()
	kept := a.alerts[:0]
	for _, t := range a.alerts {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	a.alerts = kept
	if len(kept) >= notifyAlertsPerMinute {
		c.log.Printf("%s: notification not alerted (%d alerts in the last minute)", a.M.ID, len(kept))
		return false
	}
	a.alerts = append(a.alerts, now)
	return true
}

func (c *Core) trimNotifs(a *App) {
	n, out := 0, c.notifs[:0]
	for _, x := range c.notifs {
		if x.app == a {
			if n++; n > maxNotifsPerApp {
				continue
			}
		}
		out = append(out, x)
	}
	c.notifs = out
}

func (c *Core) clearNotifs(a *App) {
	out := c.notifs[:0]
	for _, n := range c.notifs {
		if n.app != a {
			out = append(out, n)
		}
	}
	c.notifs = out
}

func (c *Core) showToast(s string) {
	c.toast = &banner{body: s, until: time.Now().Add(2500 * time.Millisecond)}
}

func (c *Core) expire() {
	now := time.Now()
	if c.banner != nil && now.After(c.banner.until) {
		c.banner = nil
	}
	if c.toast != nil && now.After(c.toast.until) {
		c.toast = nil
	}
	c.freezeIdle()
	c.checkMemory()
	c.checkANR()
	c.checkVoIP()
	c.endIntents(nil)
	c.checkNormalPush()
	c.vibrateRinging()
	c.autoLock()
	c.runJobs()
	c.checkUnused()
	c.reapWebProcs()
	lv, ln, before := c.liveInput()
	c.imeTick()
	c.liveChanged(lv, ln, before)
	c.fireAlarms()
	c.checkScreen()
	c.lockTick()
	c.checkUSB()
	c.checkCPU()
	c.checkSleep()
	c.checkNFC()
	c.updateFocus()
	c.refreshSystemTop()
}

// refreshSystemTop redraws the system screen in front (the clock, the
// notifications) while no app is.
func (c *Core) refreshSystemTop() {
	if c.fg != nil {
		return
	}
	if top := c.sys.top(); top != nil && systemRebuilt(top.S.ID) {
		top.replace(c.systemScreen(top.S.ID))
	}
}

// invoke runs an action of the current view, confirming dangerous ones first.
func (c *Core) invoke(a *App, v *View, node *ui.Node, act ui.Action) {
	run := func() {
		if a.system {
			c.systemAction(v, node, act)
			return
		}
		m := ui.Msg{T: ui.MsgAction, ScreenID: v.S.ID, Action: act.ID, Fields: map[string]string{}}
		if node != nil {
			m.Node = node.ID
			if node.Kind == ui.KindInput {
				m.Value = v.inputs[node.ID]
			}
			var k int
			if _, err := fmt.Sscanf(act.ID, "link:%d", &k); err == nil && k >= 1 && k <= len(v.links[node.ID]) {
				m.Value = v.links[node.ID][k-1].url
			}
		}
		for k, val := range v.inputs {
			m.Fields[k] = val
		}
		// A live input's "changed" is no submit: it keeps the text.
		for _, n := range v.S.Body {
			if n.Kind == ui.KindInput && n.Clear && node != nil && node.ID == n.ID && act.ID != "changed" {
				v.inputs[n.ID] = ""
			}
		}
		a.send(m)
		c.probe(a)
	}
	if act.Group == ui.GroupDanger || act.Confirm != "" {
		text := act.Confirm
		if text == "" {
			text = tr.T("Confirm %s?", act.Label)
		}
		c.pushOverlay(&confirmOverlay{text: text, onYes: run})
		return
	}
	run()
}

func (c *Core) back() {
	a := c.current()
	if top := a.top(); a.system && top != nil && top.S.ID == "sys.reply" {
		c.endReply("")
		return
	}
	if top := a.top(); a.system && top != nil && top.S.ID == "sys.prompt" {
		c.endPrompt(false, "")
		return
	}
	if len(a.stack) > 1 {
		v := a.stack[len(a.stack)-1]
		a.stack = a.stack[:len(a.stack)-1]
		if !a.system {
			a.send(ui.Msg{T: ui.MsgPopped, ScreenID: v.S.ID})
		}
		return
	}
	if !a.system {
		c.GoHome()
	}
}

// Key handles one key press. Overlays (system UI) always see keys first.
// Key handles one key press. Live inputs (search as you type) are told
// about every edit, whichever path changed them: typing, the input method,
// the symbol table, backspace.
func (c *Core) Key(k Key) {
	// A lock due while the screen was off comes before the screen lights.
	c.lockTick()
	woke := c.Wake("key")
	c.lastKey = c.now()
	if c.cardPickKey(k) {
		return
	}
	if woke {
		return
	}
	v, n, before := c.liveInput()
	c.key(k)
	c.liveChanged(v, n, before)
}

func (c *Core) liveInput() (*View, *ui.Node, string) {
	a := c.current()
	if a == nil || a.system {
		return nil, nil, ""
	}
	v := a.top()
	if v == nil {
		return nil, nil, ""
	}
	n := v.focused()
	if n == nil || n.Kind != ui.KindInput || !n.Live {
		return nil, nil, ""
	}
	return v, n, v.inputs[n.ID]
}

func (c *Core) liveChanged(v *View, n *ui.Node, before string) {
	if v == nil || v.inputs[n.ID] == before {
		return
	}
	if a := c.current(); a != nil && a.top() == v {
		c.invoke(a, v, n, ui.Action{ID: "changed"})
	}
}

func (c *Core) key(k Key) {
	// The volume keys work everywhere: over dialogs, on the locked keypad.
	if k.Name == KeyVolUp || k.Name == KeyVolDown {
		c.volumeKey(k.Name == KeyVolUp)
		return
	}
	// So do the media keys, as a headset button does on any phone.
	switch k.Name {
	case KeyPlayPause:
		c.mediaKey("playpause")
		return
	case KeyNextTrack, KeyPrevTrack:
		c.mediaKey(k.Name)
		return
	}
	// The end key backs out of menus and dialogs (cancelling them) before
	// it hangs up or goes to idle; incoming calls and alarms keep their own
	// handling.
	if k.Name == KeyF4 {
		for n := len(c.overlays); n > 0; n = len(c.overlays) {
			if c.endKeyOwn(c.overlays[n-1]) {
				break
			}
			c.overlays[n-1].key(c, Named(KeyEsc))
			if len(c.overlays) == n {
				break
			}
		}
	}
	if n := len(c.overlays); n > 0 {
		top := c.overlays[n-1]
		// On an incoming call the call key answers and the end key declines.
		switch top.(type) {
		case *incomingOverlay, *cellIncomingOverlay:
			switch k.Name {
			case KeyCall:
				k = Named(KeyEnter)
			case KeyF4:
				k = Named(KeyEsc)
			}
		}
		top.key(c, k)
		return
	}
	// Securely locked in a call, only the call screen is reachable.
	if c.secureLocked() && c.anyCall() {
		if !c.callApp(c.fg) {
			c.lockedKey(k)
			return
		}
		if !lockedCallKey(k) {
			return
		}
	}
	if c.keylock && !c.anyCall() {
		c.lockedKey(k)
		return
	}
	switch k.Name {
	case KeyF3:
		c.openSystem("sys.tasks")
		return
	case KeyF4:
		if !c.pageKey("hangup") {
			c.GoHome()
		}
		return
	case KeyCall:
		c.callKey()
		return
	}
	a := c.current()
	v := a.top()
	if v == nil || a.stale {
		switch {
		case k.Name == KeyEsc:
			c.GoHome()
		case !a.system && len(a.heldKeys) < maxHeldKeys:
			a.heldKeys = append(a.heldKeys, k)
		}
		return
	}
	node := v.focused()
	if node != nil && node.Kind == ui.KindWeb && c.webNodeKey(a, v, node, k) {
		return
	}
	if node != nil && node.Kind == ui.KindCanvas && node.Canvas != nil && node.Canvas.Keys && !a.system {
		if name := canvasKey(k); name != "" {
			a.send(ui.Msg{T: ui.MsgAction, ScreenID: v.S.ID, Node: node.ID, Action: ui.ActionKey, Value: name})
			c.probe(a)
			return
		}
	}
	inInput := node != nil && node.Kind == ui.KindInput
	if inInput && c.imeKey(k) {
		return
	}
	if k.Name == KeyIME {
		c.ime.Handle(ime.Key{Name: "ime"})
		return
	}
	switch k.Name {
	case KeyUp, KeyLeft, KeyDown, KeyRight:
		if a.system && v.S.ID == "sys.idle" {
			if !c.idleKey(k.Name) && k.Name == KeyUp {
				c.openSystem("sys.notifs")
			}
			return
		}
		if c.moveInGrid(v, k.Name) {
			return
		}
		if k.Name == KeyLeft || k.Name == KeyRight {
			c.sideKey(a, v, node, k.Name)
			return
		}
		if k.Name == KeyUp {
			c.moveFocus(v, -1)
		} else {
			c.moveFocus(v, 1)
		}
	case KeyEnter:
		if a.system && v.S.ID == "sys.idle" && c.idleKey(KeyEnter) {
			return
		}
		if node == nil {
			if act := pageAction(v, v.S.Center); act != nil {
				c.invoke(a, v, nil, *act)
			}
			return
		}
		if inInput {
			if node.Submit == "" {
				c.moveFocus(v, 1)
			} else if strings.TrimSpace(v.inputs[node.ID]) != "" {
				c.invoke(a, v, node, ui.Action{ID: node.Submit})
			}
			return
		}
		if p := node.PrimaryAction(); p != nil {
			c.invoke(a, v, node, *p)
		} else if len(node.Actions) > 0 {
			c.openMenu(a, v, false)
		}
	case KeyTab:
		c.openMenu(a, v, false)
	case KeyF2:
		if act := pageAction(v, v.S.Right); act != nil {
			c.invoke(a, v, node, *act)
		}
	case KeyEsc:
		if a.system && v.S.ID == "sys.idle" && c.idleKey(KeyEsc) {
			return
		}
		c.back()
	case KeyBackspace:
		if inInput {
			r := []rune(v.inputs[node.ID])
			if len(r) > 0 {
				v.inputs[node.ID] = string(r[:len(r)-1])
			}
		} else {
			c.back()
		}
	case KeyRunes:
		if inInput {
			v.inputs[node.ID] += string(k.Runes)
			return
		}
		if string(k.Runes) == "/" {
			c.openMenu(a, v, true)
			return
		}
		if a.system && v.S.ID == "sys.idle" && c.idleRunes(v, string(k.Runes)) {
			return
		}
		// Main menu and its folders: digits open what is in that grid
		// position, as on the Nokia menu grid.
		if r := k.Runes; a.system && menuGrid(v) && len(r) == 1 && r[0] >= '1' && r[0] <= '9' {
			i := int(r[0] - '1')
			for j := range v.S.Body {
				if t := &v.S.Body[j]; isTile(t) {
					if i == 0 {
						c.systemAction(v, t, ui.Action{ID: ui.ActionOpen})
						break
					}
					i--
				}
			}
			return
		}
		if act, n := shortcut(v, node, string(k.Runes)); act != nil {
			c.invoke(a, v, n, *act)
			return
		}
		for i := range v.S.Body {
			if v.S.Body[i].Kind == ui.KindInput {
				v.focus, v.focusIdx, v.ensure = v.S.Body[i].ID, i, true
				if !c.imeKey(k) {
					v.inputs[v.focus] += string(k.Runes)
				}
				return
			}
		}
	}
}

// pageKey runs the page action with that id (the end key's "hangup").
func (c *Core) pageKey(id string) bool {
	a := c.current()
	v := a.top()
	if v == nil {
		return false
	}
	if act := pageAction(v, id); act != nil {
		c.invoke(a, v, v.focused(), *act)
		return true
	}
	return false
}

// callKey: the focused element's "call" / "dial" action, a number field that
// dials, the page's "dial" / "call" action; otherwise (and on the idle
// screen) the recent calls, as the green key does on a Nokia.
func (c *Core) callKey() {
	a := c.current()
	v := a.top()
	if v != nil && !(a.system && v.S.ID == "sys.idle") {
		if n := v.focused(); n != nil {
			if n.Kind == ui.KindInput && (n.Submit == "dial" || n.Submit == "call") && strings.TrimSpace(v.inputs[n.ID]) != "" {
				c.invoke(a, v, n, ui.Action{ID: n.Submit})
				return
			}
			for _, act := range n.Actions {
				if act.ID == "call" || act.ID == "dial" {
					c.invoke(a, v, n, act)
					return
				}
			}
		}
		for _, id := range []string{"dial", "call"} {
			if c.pageKey(id) {
				return
			}
		}
	}
	if d := c.appByID(appDialer); d != nil {
		c.sys.stack = c.sys.stack[:1]
		c.Launch(d, "log")
	}
}

// endKeyOwn: overlays where the end key means something of their own.
func (c *Core) endKeyOwn(o overlay) bool {
	switch o.(type) {
	case *incomingOverlay, *cellIncomingOverlay, *alarmOverlay, *alertOverlay:
		return true
	}
	return false
}

// anyCall: a call in any state but ended; the locked keypad lets its keys
// through (hanging up a call still dialling, e.g. an emergency call).
func (c *Core) anyCall() bool {
	if len(c.calls) > 0 {
		return true
	}
	for _, call := range c.cells {
		if call.State != "ended" {
			return true
		}
	}
	return false
}

// emergencyNumbers can be dialled with the keypad locked (and, in
// production, without a SIM), as on every phone: 112 everywhere, the
// Chinese 110 / 119 / 120 / 122, and 911 / 999 for travellers.
var emergencyNumbers = map[string]bool{"112": true, "110": true, "119": true, "120": true, "122": true, "911": true, "999": true}

// lockedKey: with the keypad locked only the left soft key then * (within
// 3 s) unlocks, and an emergency number can be typed and called; incoming
// calls and alarms still get their keys (they are overlays). Lock-screen
// shortcuts come first; a secure lock (a PIN) has its own keys (lock.go).
func (c *Core) lockedKey(k Key) {
	if c.lockShortcutKey(k) {
		return
	}
	if c.lock.pin {
		c.secureLockedKey(k)
		return
	}
	if k.Name == KeyRunes && len(k.Runes) == 1 && k.Runes[0] >= '0' && k.Runes[0] <= '9' {
		c.emergency += string(k.Runes)
		if len(c.emergency) > 3 {
			c.emergency = c.emergency[len(c.emergency)-3:]
		}
		if emergencyNumbers[c.emergency] {
			c.showToast(tr.T("Emergency number %s · press Call to dial", c.emergency))
			return
		}
	}
	if k.Name == KeyCall && emergencyNumbers[c.emergency] {
		c.emergencyCall(c.emergency)
		c.emergency = ""
		return
	}
	if k.Name == KeyBackspace || k.Name == KeyEsc {
		c.emergency = ""
	}
	if k.Name == KeyF4 {
		// The end key still goes back to standby (after an emergency call).
		c.emergency = ""
		c.GoHome()
		return
	}
	switch {
	case k.Name == KeyTab:
		c.unlockArm = time.Now()
		c.showToast(tr.T("Now press *"))
	case k.Name == KeyRunes && string(k.Runes) == "*" && time.Since(c.unlockArm) < 3*time.Second:
		c.keylock = false
		c.showToast(tr.T("Keypad unlocked"))
	default:
		c.showToast(tr.T("Press “Unlock”, then *"))
	}
}

func pageAction(v *View, id string) *ui.Action {
	for i := range v.S.Actions {
		if v.S.Actions[i].ID == id {
			return &v.S.Actions[i]
		}
	}
	return nil
}

// sideKey is ← or → outside grids, inputs and web pages: an action the
// element or the page binds to it (a volume's Lower / Raise, a player's
// previous / next), else ← goes back and → opens the element, as OK does
// for items that lead somewhere.
func (c *Core) sideKey(a *App, v *View, node *ui.Node, key string) {
	name := map[string]string{KeyLeft: "left", KeyRight: "right"}[key]
	if act, n := shortcut(v, node, name); act != nil {
		c.invoke(a, v, n, *act)
		return
	}
	if key == KeyLeft {
		c.back()
		return
	}
	if node != nil {
		if p := node.PrimaryAction(); p != nil && p.ID == ui.ActionOpen {
			c.invoke(a, v, node, *p)
		}
	}
}

// keyLabel is how menus show an action's key.
func keyLabel(k string) string {
	switch k {
	case "left":
		return "←"
	case "right":
		return "→"
	}
	return k
}

func shortcut(v *View, node *ui.Node, key string) (*ui.Action, *ui.Node) {
	if node != nil {
		for i := range node.Actions {
			if node.Actions[i].Key == key {
				return &node.Actions[i], node
			}
		}
	}
	for i := range v.S.Actions {
		if v.S.Actions[i].Key == key {
			return &v.S.Actions[i], node
		}
	}
	return nil, nil
}

func (c *Core) moveFocus(v *View, dir int) {
	fs := v.focusables()
	cur := v.indexOf(v.focus)
	// A focused block taller than the screen (a long article) scrolls before
	// focus moves on, so none of its content is skipped: by the pixel
	// renderer's geometry when it is the screen, else by the text layout.
	if area, h, ok := c.pixelGeometry(v); ok {
		if f, inside := pixelScroll(v.pfrac, area, h, dir); inside {
			v.pixel, v.pfrac = true, f
			return
		}
		v.pixel, v.pfrac = true, map[bool]float64{true: 0, false: 1}[dir > 0]
	} else if g, ok := v.geom[cur]; ok && v.bodyH > 0 {
		top, h := g[0], g[1]
		if (dir > 0 && top+h > v.scroll+v.bodyH) || (dir < 0 && top < v.scroll) {
			v.scroll += 3 * dir
			v.stick = false
			return
		}
	}
	next := -1
	if dir > 0 {
		for _, i := range fs {
			if i > cur {
				next = i
				break
			}
		}
	} else {
		for j := len(fs) - 1; j >= 0; j-- {
			if fs[j] < cur {
				next = fs[j]
				break
			}
		}
	}
	if next < 0 {
		v.scroll += 3 * dir
		v.stick = false
		return
	}
	v.focus, v.focusIdx, v.ensure, v.stick = v.S.Body[next].ID, next, true, false
}

func (c *Core) pushOverlay(o overlay) {
	switch o.(type) {
	case *alarmOverlay:
		c.Wake("alarm")
	case *alertOverlay:
		c.Wake("alert")
	case *incomingOverlay, *cellIncomingOverlay:
		c.Wake("call")
	}
	c.log.Printf("overlay + %T", o)
	c.overlays = append(c.overlays, o)
	c.updateFocus()
}

func (c *Core) popOverlay(o overlay) {
	for i := len(c.overlays) - 1; i >= 0; i-- {
		if c.overlays[i] == o {
			c.log.Printf("overlay - %T", o)
			c.overlays = append(c.overlays[:i], c.overlays[i+1:]...)
			c.updateFocus()
			return
		}
	}
}

// Shutdown stops app processes and media subscriptions.
func (c *Core) Shutdown() {
	c.stopping.Store(true)
	c.harvestTraffic()
	for _, s := range c.subs {
		s.Close()
	}
	var left []*os.Process
	for _, a := range c.apps {
		if a.running && a.proc != nil {
			// A frozen app would only see the signal once thawed.
			c.thaw(a)
			_ = a.proc.Signal(syscall.SIGTERM)
			left = append(left, a.proc)
		}
	}
	for end := time.Now().Add(500 * time.Millisecond); len(left) > 0 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		alive := left[:0]
		for _, p := range left {
			if processAlive(p.Pid) {
				alive = append(alive, p)
			}
		}
		left = alive
	}
	for _, p := range left {
		_ = p.Kill()
	}
}
