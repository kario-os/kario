package shell

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"errors"
	"math"
	"regexp"

	_ "embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/cts"
	"github.com/kario-os/kario/obex"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

//go:embed selftest.script
var SelftestScript string

// CTSScript is appended to the selftest by phone-shell --cts.
//
//go:embed cts.script
var CTSScript string

// Script is a list of steps, one per line:
//
//	== <case name>          start a named case (reported PASS/FAIL)
//	key <name> [count]      press a key (enter esc tab up down left right backspace f2 f3 f4, power: the power key)
//	type <text>             type text
//	fill <text>             set the focused input to text, past the input method; {chat} is the
//	                        simulated chat server's URL (its test pages)
//	long <char>|<key>       hold a key (9-key: digit, # = 123, * = symbols; end, power: the power menu)
//	tel ring <n> | tel sms <from> <text> | tel lock <pin> | tel ussd <text>   simulated baseband (telephonyd)
//	tel network <sim-mcc> <network-mcc> [<nitz-minutes>]   the SIM's country, the network's, the zone it sends
//	tel cb <channel> <serial> <text>   a cell broadcast (an emergency alert; 4370 the highest level)
//	tel pending <n> [ms]    wait until telephonyd holds n events phone-smsd has not acknowledged
//	tel held <from> <text>  a message telephonyd stores but does not announce, as if it died right after
//	tel kill                kill phone-telephonyd; wait until its supervisor's new process answers
//	require-sms             skip the case without phone-smsd
//	sms-kill                kill phone-smsd (its supervisor starts it again)
//	alerts <n> [ms]         wait until n emergency alerts have been shown
//	locale <lang> <zone>    wait until the shell's language and time zone are these
//	gui-env <name> <value>  wait until the pixel renderer runs with name=value in its environment (none: nothing to check)
//	timeshift <seconds>     move the shell clock forward (alarms)
//	require-tel             skip the case without telephonyd
//	audio <category> | audio paused <category> | audio idle   wait for phone-audiod
//	require-audio           skip the case without phone-audiod
//	push [voip] [normal] [sealed] [other] <app-id> <title> | <body> | <data>   send through the gateway as the app
//	                        server; normal: priority normal; sealed: data end-to-end encrypted to the app's key;
//	                        other: to a gateway instance the device is not connected to
//	require-push            skip the case without phone-pushd
//	require-nfc             skip the case without phone-nfcd (and its simulated controller)
//	nfc card <uid> [encrypted|cpu] | nfc card none   a physical card held to the phone, for reader mode
//	nfc reader <name> door <uid>[,<uid>...] | gate <aid> <fare> | pay <aid> <amount>   a simulated reader
//	                        (a door accepting those UIDs, a transit gate, a payment terminal; amounts in cents)
//	nfc tap <reader> ok|fail [<card name>]   hold the phone to the reader; it must accept or refuse (the card it saw)
//	nfc clock <HH:MM> [workday|weekend]   move the shell's clock forward to that time of day (on such a day)
//	nfc asleep | nfc woken  wait until the simulated sleep is on; the last sleep ended by a reader's field
//	nfc picker on|off       the end key's double press opens the quick card view (scripts start with it off)
//	require-pushgw-cluster  skip the case without two gateway instances phone-shell can kill
//	pushgw-kill             kill the gateway instance the device is connected to; wait until it moves
//	require-netfw           skip the case without the per-app firewall (root)
//	require-webview         skip the case without the web view (phone-webview and a pixel renderer)
//	net-send <app-id> <sent|blocked>   send UDP to TEST-NET-1 as the app's uid; the firewall must agree
//	appkill <app-id>        end the app process (the system ends it: no crash, no tombstone)
//	lowmem-kill <app-id>    end a background app as the low-memory killer does (it keeps a tombstone)
//	saved <app-id> [ms]     wait until the app has handed over its state (lifecycle background)
//	revived <app-id> [ms]   wait until a tombstoned app's new process has sent its rebuilt pages
//	revoked <app-id> [ms]   wait until the device has the store's takedown of the app
//	require-radios          skip the case without Wi-Fi and Bluetooth (iwd and BlueZ, or the simulated ones)
//	require-eap-ap          skip the case where no enterprise (802.1X) access point can be put up (the simulated Wi-Fi, or hostapd on a radio iwd leaves alone: the emulator's third one)
//	wifi-eap-ap <ssid> <user> <password> <domain>   an 802.1X access point whose server takes that user and has a certificate for domain
//	wifi-ap <ssid> <passphrase>   an access point comes in range (on the emulator: the second simulated radio)
//	bt-peer <name>          a device to pair with appears nearby (on the emulator: the second virtual controller)
//	radio wifi|bt|cell on|off [ms]   wait until Wi-Fi, Bluetooth or the modem's radio is on or off
//	data-cut <iface>|none [ms]   wait until the firewall closes just that interface for mobile data (require-netfw)
//	bt-push <name> <text>   the paired device sends the phone a file (the user answers on screen)
//	bt-pull                 the paired device (a car kit) reads the phone book and the missed calls
//	bt-peer-result ok|refused [text] [ms]   wait until the device's push or pull ended so (a pull read text)
//	bt-peer-got <name> <text> [ms]   wait until the paired device has received the file with text in it
//	key-if <key> <text> [ms]   press the key (or type the one character) if the text shows within the time, else go on
//	screen-auto on|off      the screen timeout applies (scripts start with it off: their keys would wake the screen)
//	screen on|off [ms]      wait until the screen (and the renderer's panel) is on or off
//	screen-key <key>        press a key as the keypad does: with the screen off it only wakes it
//	light <pattern> [ms]    wait until the notification light shows the pattern (off, blink 200/4800 ms)
//	require-light           skip the case without a notification light (a board without one)
//	no-light on|off         the board has no notification light (on) or its own again (off)
//	alarm-in <seconds> <label>  the clock app's alarm that far ahead on the shell's clock
//	system-sleep on|off     the phone may suspend (scripts start with it off; the developer machine gets a simulated sleep that moves the clock to the wake time)
//	slept <times> [ms]      wait until the phone has slept this often
//	tap <text>              a pointer's click on the body node titled text, as the renderer reports it (require-gui)
//	cpu-charge <app-id> <ms>   count CPU time for the app as if it had used it
//	bgcpu <app-id> <ms> [timeout]   wait until the app's background CPU of the last day reaches ms
//	light-blinks <n> [ms]   wait until the userspace LED has come on n times a blink period after it last did (the emulator)
//	require-uleds           skip the case unless the light is a userspace LED (the emulator)
//	require-usb             skip the case without a USB device controller (or the simulated one)
//	usb-cable on|off        plug the cable to a computer in or out (the emulator: dummy_hcd; else simulated)
//	usb-get <path> <text>   the computer fetches path with the password the phone shows; it must contain text
//	usb-put <path> <text>   the computer copies text into path (a file in a writable folder)
//	usb-refused             the computer no longer reaches the phone
//	usb-port <port> open|closed   the computer can (or cannot) connect to that port of the phone
//	require-usb-host        skip the case without the USB host side of the phone's own cable (dummy_hcd: the emulator)
//	usb-mtp <step> ...      the computer over file transfer (MTP), usbmtp.go: storages, ls, props, get, partial, put, refused, event, windows, libmtp, kernel, gone
//	store-rollout <app-id> <version> <percent>   the developer moves a staged version
//	appstop <app-id>        end it if it runs and wait until it is gone (the next open starts it afresh)
//	push-sealed <app-id>    the app got end-to-end encrypted push data
//	perm-state <key> <perm> <state> [ms]   wait until a grant reads state (ask granted denied);
//	                        key is an app id or "<app-id>/<mini-program>"
//	require-cgroup          skip the case without per-app cgroups (developer machine)
//	require-gui             skip the case without a pixel renderer
//	gui-crash               kill the pixel renderer; the watchdog must start a new one and resend the frame
//	cgroup <app-id> <file> <value> [ms]   wait until the app's cgroup file reads value
//	appenv <app-id> <name> <value>   the running app's environment has name=value
//	mem-low <n>             the next n memory checks read as low (the low-memory killer runs)
//	pages-ended <n>         at least n background apps' pages were ended for low memory
//	vibrated [ms]           wait until the vibration motor is asked to run (again)
//	require-v4l2            skip the case unless phone-mediad captures from a V4L2 camera
//	camera-frames <n> [ms]  wait until the V4L2 camera has captured at least n frames
//	setting <key> <value>   change a system setting (developer_mode 1)
//	pkg-tamper <app-id> <file>   append a byte to a file of an installed package
//	pkg-build <example> <app-id> <file> [tamper]   sign share/phone/examples/<example> with the
//	                        development store into <file> in the app's data dir (tamper: then flip a byte)
//	mp-build <example> <host-app-id> <file> [tamper]   the same for a mini-program, signed by a development
//	                        mini-program store the host is made to trust
//	store-publish <example> <version>   publish a sample app through the store backend (as its developer,
//	                        with the staff verifying the developer and approving a review)
//	store-takedown <app-id> <reason>   the store staff take an app down
//	at <screen-id> [ms]     wait for a screen by id (sys.idle, sys.menu, <app-id>/<screen>)
//	wait <text> [ms]        wait until the screen shows text (default 5000ms)
//	gone <text> [ms]        wait until the text disappears
//	wait-re <regexp> [ms]   wait until the screen matches (text that moves on: a play position)
//	frames <n> [ms]         wait until at least n more media frames reached the screen
//	painted [ms]            wait until the web view shows a painted page (not a single colour)
//	web-procs <n>           assert how many web processes were started since boot
//	masked                  assert the screen contains secure (masked) content
//	sleep <ms>
//	snap <name>             write the plain screen to the snapshot dir
type Runner struct {
	// Only: run the cases whose name matches, skip the others (--only).
	Only    *regexp.Regexp
	C       *Core
	SnapDir string
	Out     io.Writer
	// Scale multiplies every timeout (e.g. 4 under QEMU TCG emulation).
	Scale int
	// GUI, when set, receives frames during the run and writes a PNG at
	// every snap step. With Watch it is Watch.G, replaced when the renderer
	// dies.
	GUI    *GUI
	Watch  *RendererWatch
	pushed time.Time
	// vibrations seen by the last "vibrated" step
	vibrations int
	// Cases are the results of the last Run.
	Cases []CaseResult
	// Restart execs a successor Shell that resumes the script from the file
	// it is given (phone-shell sets it; without it shell-restart cases skip).
	Restart func(resumeFile string) error
	// KillService ends one of the services phone-shell runs (its supervisor
	// starts it again); without it pushgw-kill cases skip.
	KillService func(name string) error
	// Resume continues a script a previous Shell ran up to a shell-restart.
	Resume *Resume
	// ResumeFile is where shell-restart and shell-crash leave the script's
	// state (default: the run directory).
	ResumeFile string
	pids       map[string]int
}

func (r *Runner) pump(d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case f := <-r.C.Events():
			f()
		case <-r.guiDone():
			r.restartGUI()
		case <-deadline:
			r.pushGUI()
			return
		}
	}
}

func (r *Runner) guiDone() <-chan struct{} {
	if r.Watch == nil {
		return nil
	}
	return r.Watch.G.Done()
}

func (r *Runner) restartGUI() {
	if err := r.Watch.Restart(); err != nil {
		fmt.Fprintln(r.Out, "PHONE-GUI-EXIT:", err)
		r.Watch, r.GUI = nil, nil
		return
	}
	r.GUI = r.Watch.G
	r.pushed = time.Time{}
	r.pushGUI()
}

func (r *Runner) pushGUI() {
	if r.GUI == nil || time.Since(r.pushed) < 100*time.Millisecond {
		return
	}
	r.pushed = time.Now()
	r.C.Render()
	_ = r.GUI.Push()
}

// computer is the computer at the other end of the USB cable doing f,
// while the shell's loop keeps running (sharing needs it).
func (r *Runner) computer(addr string, f func(do httpDo, _ dialPort, base string) error) error {
	done := make(chan error, 1)
	go func() { done <- r.C.usbComputer(addr, f) }()
	for {
		select {
		case err := <-done:
			return err
		default:
			r.pump(20 * time.Millisecond)
		}
	}
}

func (r *Runner) screen() string {
	return r.C.Render().Plain()
}

func (r *Runner) waitFor(cond func(string) bool, ms int) (string, bool) {
	end := time.Now().Add(time.Duration(ms*max(1, r.Scale)) * time.Millisecond)
	for {
		s := r.screen()
		if cond(s) {
			return s, true
		}
		if time.Now().After(end) {
			return s, false
		}
		r.pump(30 * time.Millisecond)
	}
}

var keyNames = map[string]string{
	"enter": KeyEnter, "esc": KeyEsc, "tab": KeyTab, "up": KeyUp, "down": KeyDown,
	"left": KeyLeft, "right": KeyRight, "backspace": KeyBackspace, "f2": KeyF2, "f3": KeyF3, "f4": KeyF4, "end": KeyF4, "call": KeyCall,
	"ime": KeyIME, "sym": KeySym, "volup": KeyVolUp, "voldown": KeyVolDown,
	"playpause": KeyPlayPause, "next": KeyNextTrack, "previous": KeyPrevTrack, "power": KeyPower,
}

// KeyByName is the key a renderer or a script names (enter, left, f4, call…).
func KeyByName(name string) (Key, bool) {
	n, ok := keyNames[name]
	return Named(n), ok
}

// notSelected is why --only skips a case.
const notSelected = "not selected"

// CaseResult is one case of a run, for the compatibility report.
type CaseResult = cts.CaseResult

// WriteJUnit writes a run's cases as a JUnit XML report (CI test views).
func WriteJUnit(path, suite string, cases []CaseResult) error {
	type failure struct {
		Message string `xml:"message,attr"`
		Text    string `xml:",chardata"`
	}
	type skipped struct {
		Message string `xml:"message,attr,omitempty"`
	}
	type testcase struct {
		Name      string   `xml:"name,attr"`
		Classname string   `xml:"classname,attr"`
		Time      float64  `xml:"time,attr"`
		Failure   *failure `xml:"failure,omitempty"`
		Skipped   *skipped `xml:"skipped,omitempty"`
	}
	type testsuite struct {
		XMLName  xml.Name   `xml:"testsuite"`
		Name     string     `xml:"name,attr"`
		Tests    int        `xml:"tests,attr"`
		Failures int        `xml:"failures,attr"`
		Skipped  int        `xml:"skipped,attr"`
		Time     float64    `xml:"time,attr"`
		Cases    []testcase `xml:"testcase"`
	}
	ts := testsuite{Name: suite, Tests: len(cases)}
	for _, c := range cases {
		tc := testcase{Name: c.Name, Classname: suite, Time: c.Seconds}
		switch c.Status {
		case "FAIL":
			tc.Failure = &failure{Message: c.Message, Text: c.Message}
			ts.Failures++
		case "SKIP":
			tc.Skipped = &skipped{Message: c.Message}
			ts.Skipped++
		}
		ts.Time += c.Seconds
		ts.Cases = append(ts.Cases, tc)
	}
	raw, err := xml.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(xml.Header), append(raw, '\n')...), 0o644)
}

// Run executes the script and reports whether every case passed; the cases
// are kept in r.Cases.
// Resume is where a script stood when the Shell restarted in place.
type Resume struct {
	Line     int                `json:"line"`
	Case     string             `json:"case"`
	Cases    []CaseResult       `json:"cases"`
	CapStats map[string]CapStat `json:"cap_stats"`
	Pids     map[string]int     `json:"pids,omitempty"` // app-pid save
}

// LoadResume reads what a previous Shell left for its successor.
func LoadResume(path string) (*Resume, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var res Resume
	return &res, json.Unmarshal(raw, &res)
}

func (r *Runner) Run(script string) bool {
	type result struct {
		name string
		ok   bool
		dur  time.Duration
	}
	var results []result
	r.Cases = nil
	cur, t0, failed, skipping := "(setup)", time.Now(), false, false
	why := ""
	from := 0
	if r.Resume != nil {
		from, cur, r.Cases, r.pids = r.Resume.Line, r.Resume.Case, r.Resume.Cases, r.Resume.Pids
		r.C.AddCapStats(r.Resume.CapStats)
		for _, c := range r.Cases {
			if c.Status != "SKIP" {
				results = append(results, result{c.Name, c.Status == "PASS", 0})
			}
		}
	}
	finish := func(ok bool) {
		status := "PASS"
		switch {
		case skipping:
			status = "SKIP"
		case !ok:
			status = "FAIL"
		}
		if !skipping {
			results = append(results, result{cur, ok, time.Since(t0)})
		}
		r.Cases = append(r.Cases, CaseResult{Name: cur, Status: status, Seconds: math.Round(time.Since(t0).Seconds()*10) / 10, Message: why})
		fmt.Fprintf(r.Out, "%s  %s  (%.1fs)\n", status, cur, time.Since(t0).Seconds())
		if skipping && why != "" && why != notSelected {
			fmt.Fprintf(r.Out, "  %s\n", why)
		}
	}
	if r.Resume == nil {
		r.C.RestartBoot()
	}
	// The screen stays on unless a case turns its timeout on (screen-auto):
	// a script's keys would otherwise be spent waking it.
	r.C.SetScreenAuto(false)
	r.C.SetSleep(false)
	r.C.SetEndKeyPicker(false)
	r.pump(200 * time.Millisecond)
	sc := bufio.NewScanner(strings.NewReader(script))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if lineNo <= from {
			continue
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "== ") {
			if cur != "(setup)" {
				finish(true)
			}
			cur, t0, skipping, why = strings.TrimPrefix(line, "== "), time.Now(), false, ""
			// --only: the cases asked for; the others are skipped (a case
			// may need what an earlier one left behind).
			if r.Only != nil && !r.Only.MatchString(cur) {
				skipping, why = true, notSelected
			}
			continue
		}
		if skipping {
			continue
		}
		cmd, arg, _ := strings.Cut(line, " ")
		if strings.HasPrefix(cmd, "require-") {
			why = "needs " + strings.TrimPrefix(cmd, "require-") + " " + arg
		}
		if cmd == "require-app" {
			skipping = skipping || r.C.AppByID(arg) == nil
			continue
		}
		if cmd == "require-nfc" {
			r.waitFor(func(string) bool { return r.C.HasNFC() }, 3000)
			skipping = skipping || !r.C.HasNFC()
			continue
		}
		if cmd == "require-push" {
			r.waitFor(func(string) bool { return r.C.HasPush() }, 3000)
			skipping = skipping || !r.C.HasPush()
			continue
		}
		if cmd == "require-audio" {
			r.waitFor(func(string) bool { return r.C.HasAudio() }, 3000)
			skipping = skipping || !r.C.HasAudio()
			continue
		}
		if cmd == "require-tel" {
			r.waitFor(func(string) bool { return r.C.HasTelephony() }, 3000)
			skipping = skipping || !r.C.HasTelephony()
			continue
		}
		if cmd == "require-sms" {
			r.waitFor(func(string) bool { return r.C.HasSMS() }, 3000)
			skipping = skipping || !r.C.HasSMS()
			continue
		}
		if cmd == "require-video" {
			// mediad has read the sample video (it has a decoder).
			r.waitFor(func(string) bool { return r.C.CanPlayVideo() }, 5000)
			skipping = skipping || !r.C.CanPlayVideo()
			continue
		}
		if cmd == "require-sandbox" {
			skipping = skipping || !r.C.Sandboxed()
			continue
		}
		if cmd == "require-cgroup" {
			if r.C.cg == nil && !skipping {
				why = "no per-app cgroups: " + r.C.cgOff
			}
			skipping = skipping || r.C.cg == nil
			continue
		}
		if cmd == "require-pushgw-cluster" {
			gws, _ := r.C.PushGateways()
			skipping = skipping || len(gws) < 2 || r.KillService == nil
			continue
		}
		if cmd == "require-webview" {
			skipping = skipping || !r.C.HasWebView()
			continue
		}
		if cmd == "require-netfw" {
			skipping = skipping || r.C.traffic == nil
			continue
		}
		if cmd == "require-v4l2" {
			skipping = skipping || !strings.HasPrefix(r.C.MediaInfo()["camera"].(string), "v4l2:")
			continue
		}
		if cmd == "require-gui" {
			skipping = skipping || r.Watch == nil
			continue
		}
		if cmd == "require-store" {
			skipping = skipping || r.C.appEnvValue(ui.EnvStoreServer) == ""
			continue
		}
		if cmd == "require-usb" {
			skipping = skipping || !r.C.HasUSB()
			continue
		}
		if cmd == "require-usb-host" {
			skipping = skipping || !strings.HasPrefix(r.C.usb.udc, "dummy_udc")
			continue
		}
		if cmd == "require-light" {
			skipping = skipping || r.C.Light() == ""
			continue
		}
		if cmd == "require-uleds" {
			skipping = skipping || r.C.screen.uled == nil
			continue
		}
		if cmd == "require-radios" {
			skipping = skipping || r.C.wifi == nil || r.C.bt == nil
			continue
		}
		if cmd == "require-eap-ap" {
			can := false
			switch w := r.C.wifi.(type) {
			case *simWifi:
				can = true
			case *iwdRadio:
				can = w.canEAP()
			}
			skipping = skipping || !can
			continue
		}
		if cmd == "require-restart" {
			skipping = skipping || r.Restart == nil
			continue
		}
		if cmd == "require-fdstore" {
			skipping = skipping || !r.C.fdstore || r.ResumeFile == ""
			continue
		}
		if cmd == "shell-restart" || cmd == "shell-crash" {
			// The next Shell continues after this line, inside this case:
			// an in-place restart, or (shell-crash) the one systemd starts
			// with the connections from its fd store.
			path := r.ResumeFile
			if path == "" {
				path = filepath.Join(r.C.cfg.RunDir, "selftest-resume.json")
			}
			raw, _ := json.Marshal(Resume{Line: lineNo, Case: cur, Cases: r.Cases, CapStats: r.C.CapStats(), Pids: r.pids})
			err := os.WriteFile(path, raw, 0o600)
			if err == nil && cmd == "shell-crash" {
				fmt.Fprintf(r.Out, "  line %d: shell-crash: exiting without cleanup\n", lineNo)
				os.Exit(3)
			}
			if err == nil {
				if r.Restart == nil {
					err = errors.New("no in-place restart here")
				} else {
					err = r.Restart(path)
				}
			}
			finish(false)
			fmt.Fprintf(r.Out, "  line %d: %s: %v\n", lineNo, line, err)
			failed = true
			break
		}
		r.C.Trace("script %d: %s", lineNo, line)
		switch cmd {
		case "key", "key-if", "type", "fill", "long", "snap":
			// A script's input comes from someone looking at the screen.
			r.C.Wake("script")
		}
		if err := r.step(cmd, arg); err != nil {
			why = fmt.Sprintf("line %d: %s: %v", lineNo, line, err)
			finish(false)
			fmt.Fprintf(r.Out, "  line %d: %s: %v\n  screen:\n%s", lineNo, line, err, indent(r.screen()))
			fmt.Fprintf(r.Out, "  processes:\n")
			for _, l := range r.C.ProcReport() {
				fmt.Fprintf(r.Out, "    %s\n", l)
			}
			failed = true
			break
		}
	}
	if !failed && cur != "(setup)" {
		finish(true)
	}
	pass := 0
	for _, x := range results {
		if x.ok {
			pass++
		}
	}
	verdict := "PASS"
	if failed {
		verdict = "FAIL"
	}
	fmt.Fprintf(r.Out, "PHONE-SELFTEST-RESULT: %s (%d/%d cases)\n", verdict, pass, len(results))
	return !failed
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("  | " + l + "\n")
	}
	return b.String()
}

// lastNum splits a trailing integer argument off, e.g. "<contact name> 3000".
func lastNum(arg string, def int) (string, int) {
	if i := strings.LastIndex(arg, " "); i > 0 {
		if n, err := strconv.Atoi(arg[i+1:]); err == nil {
			return arg[:i], n
		}
	}
	return arg, def
}

func (r *Runner) step(cmd, arg string) error {
	switch cmd {
	case "key-if":
		// key-if <key> <text> [ms]: press the key if the text shows within
		// the time (a dialog only some devices raise), else go on.
		name, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
		text, ms := lastNum(rest, 3000)
		k, ok := keyNames[name]
		if !ok && utf8.RuneCountInString(name) != 1 {
			return fmt.Errorf("unknown key %q", name)
		}
		if _, shown := r.waitFor(func(s string) bool { return strings.Contains(s, strings.TrimSpace(text)) }, ms); shown {
			if ok {
				r.C.Press(Named(k))
			} else {
				r.C.Key(Runes(name))
			}
			r.pump(40 * time.Millisecond)
		}
	case "screen-auto":
		r.C.SetScreenAuto(strings.TrimSpace(arg) == "on")
	case "cpu-charge":
		id, ms := lastNum(strings.TrimSpace(arg), 0)
		r.C.ChargeCPU(strings.TrimSpace(id), time.Duration(ms)*time.Millisecond)
	case "bgcpu":
		// bgcpu <app-id> <ms> [timeout ms]: wait until the app's background CPU of the day reaches ms.
		f := strings.Fields(arg)
		if len(f) < 2 {
			return errors.New("bgcpu <app-id> <ms> [timeout ms]")
		}
		want, _ := strconv.Atoi(f[1])
		ms := 10000
		if len(f) > 2 {
			ms, _ = strconv.Atoi(f[2])
		}
		got := func() time.Duration {
			for _, u := range r.C.CPUUsage() {
				if u.ID == f[0] {
					return u.Background
				}
			}
			return 0
		}
		if _, ok := r.waitFor(func(string) bool { return got() >= time.Duration(want)*time.Millisecond }, ms); !ok {
			return fmt.Errorf("%s used %s in the background, not %d ms", f[0], got(), want)
		}
	case "no-light":
		r.C.SetLightPresent(strings.TrimSpace(arg) != "on")
	case "alarm-in":
		secs, label, _ := strings.Cut(strings.TrimSpace(arg), " ")
		n, err := strconv.Atoi(secs)
		if err != nil {
			return fmt.Errorf("alarm-in <seconds> <label>")
		}
		if err := r.C.AlarmIn(time.Duration(n)*time.Second, label); err != nil {
			return err
		}
	case "system-sleep":
		r.C.SetSleep(strings.TrimSpace(arg) == "on")
	case "slept":
		n, ms := lastNum(strings.TrimSpace(arg), 5000)
		want, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return fmt.Errorf("slept <times> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { got, _ := r.C.Sleeps(); return got >= want }, ms); !ok {
			got, why := r.C.Sleeps()
			return fmt.Errorf("slept %d times, not %d (awake: %s)", got, want, why)
		}
	case "tap":
		r.pushed = time.Time{}
		r.pushGUI()
		if err := r.C.tapText(strings.TrimSpace(arg)); err != nil {
			return err
		}
	case "screen":
		state, ms := lastNum(strings.TrimSpace(arg), 5000)
		off := strings.TrimSpace(state) == "off"
		if _, ok := r.waitFor(func(string) bool {
			return r.C.ScreenOff() == off && (r.C.gui == nil || !r.C.gui.info.Has("power") || r.C.screen.panel == !off)
		}, ms); !ok {
			return fmt.Errorf("the screen is not %s (off %v, panel on %v)", state, r.C.ScreenOff(), r.C.screen.panel)
		}
	case "screen-key":
		name := strings.TrimSpace(arg)
		k, ok := keyNames[name]
		if !ok {
			return fmt.Errorf("unknown key %q", name)
		}
		r.C.Press(Named(k))
		r.pump(40 * time.Millisecond)
	case "finger":
		// finger <name>: touch the simulated fingerprint sensor with that finger.
		if err := r.C.TouchFinger(strings.TrimSpace(arg)); err != nil {
			return err
		}
		r.pump(100 * time.Millisecond)
	case "locked":
		// locked on|off [ms]: wait for the lock (any kind) to be on or off.
		state, ms := lastNum(strings.TrimSpace(arg), 5000)
		want := strings.TrimSpace(state) == "on"
		if _, ok := r.waitFor(func(string) bool { return r.C.Locked() == want }, ms); !ok {
			return fmt.Errorf("the phone is not locked %s", state)
		}
	case "light":
		want, ms := lastNum(strings.TrimSpace(arg), 5000)
		want = strings.TrimSpace(want)
		if _, ok := r.waitFor(func(string) bool { return r.C.Light() == want }, ms); !ok {
			return fmt.Errorf("the notification light shows %q", r.C.Light())
		}
	case "light-blinks":
		ns, ms := lastNum(strings.TrimSpace(arg), 15000)
		n, _ := strconv.Atoi(strings.TrimSpace(ns))
		if _, ok := r.waitFor(func(string) bool { return r.C.LightBlinks() >= int64(n) }, ms); !ok {
			return fmt.Errorf("the light blinked %d times with the slow blink's period, not %d", r.C.LightBlinks(), n)
		}
	case "usb-cable":
		return r.C.PlugUSB(strings.TrimSpace(arg) == "on")
	case "usb-get", "usb-put":
		path, text, _ := strings.Cut(strings.TrimSpace(arg), " ")
		addr, pass := r.C.USBSharing()
		return r.computer(addr, func(do httpDo, _ dialPort, base string) error {
			method, body, want := "GET", "", http.StatusOK
			if cmd == "usb-put" {
				method, body, want = "PUT", text, http.StatusCreated
			}
			req, err := http.NewRequest(method, base+path, strings.NewReader(body))
			if err != nil {
				return err
			}
			req.SetBasicAuth("kario", pass)
			resp, err := do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			got, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if resp.StatusCode != want {
				return fmt.Errorf("%s %s: %s", method, path, resp.Status)
			}
			if cmd == "usb-get" && !strings.Contains(string(got), text) {
				return fmt.Errorf("GET %s has no %q: %.300s", path, text, got)
			}
			return nil
		})
	case "usb-port":
		// usb-port <port> open|closed: the computer can (or cannot) connect to the phone's port.
		f := strings.Fields(arg)
		if len(f) != 2 {
			return errors.New("usb-port <port> open|closed")
		}
		port, _ := strconv.Atoi(f[0])
		addr, _ := r.C.USBSharing()
		return r.computer(addr, func(_ httpDo, dial dialPort, _ string) error {
			conn, err := dial(port)
			if conn != nil {
				conn.Close()
			}
			switch {
			case f[1] == "open" && err != nil:
				return fmt.Errorf("port %d is closed to the computer: %v", port, err)
			case f[1] == "closed" && err == nil:
				return fmt.Errorf("port %d is open to the computer", port)
			}
			return nil
		})
	case "usb-refused":
		if addr, _ := r.C.USBSharing(); addr != "" {
			return fmt.Errorf("still sharing on %s", addr)
		}
		err := r.computer("", func(do httpDo, _ dialPort, base string) error {
			req, _ := http.NewRequest("GET", base+"/", nil)
			resp, err := do(req)
			if err == nil {
				resp.Body.Close()
				return fmt.Errorf("the computer still reaches the phone: %s", resp.Status)
			}
			return nil
		})
		if err != nil && strings.Contains(err.Error(), "still reaches") {
			return err
		}
	case "usb-helper":
		return r.usbHelperCmd(arg)
	case "usb-put-big":
		return r.usbPutBig(arg)
	case "usb-mtp":
		return r.usbMTPCmd(arg)
	case "key":
		name, n := lastNum(arg, 1)
		k, ok := keyNames[name]
		if !ok && name != "space" {
			return fmt.Errorf("unknown key %q", name)
		}
		for i := 0; i < n; i++ {
			if name == "space" {
				r.C.Key(Runes(" "))
			} else {
				r.C.Press(Named(k))
			}
			r.pump(40 * time.Millisecond)
		}
	case "tel":
		// tel ring <number> | tel sms <from> <text> | tel lock <pin>: drive the simulated baseband.
		f := strings.SplitN(arg, " ", 3)
		var err error
		switch {
		case len(f) >= 2 && f[0] == "ring":
			err = r.C.TelHook(svc.TelSimRing, map[string]string{"number": f[1]})
		case len(f) == 3 && f[0] == "sms":
			err = r.C.TelHook(svc.TelSimSMS, map[string]string{"from": f[1], "text": f[2]})
		case len(f) == 3 && f[0] == "held":
			err = r.C.TelHook(svc.TelSimSMS, map[string]any{"from": f[1], "text": f[2], "held": true})
		case len(f) == 1 && f[0] == "kill":
			err = r.C.TelKill(func(cond func() bool) bool {
				_, ok := r.waitFor(func(string) bool { return cond() }, 15000)
				return ok
			})
		case len(f) == 3 && f[0] == "cb":
			cb := strings.SplitN(arg, " ", 4)
			ch, e1 := strconv.Atoi(f[1])
			serial, e2 := strconv.Atoi(strings.Fields(f[2])[0])
			if e1 != nil || e2 != nil || len(cb) < 4 {
				return fmt.Errorf("usage: tel cb <channel> <serial> <text>")
			}
			err = r.C.TelHook(svc.TelSimCB, map[string]any{"channel": ch, "serial": serial, "text": cb[3]})
		case len(f) >= 2 && f[0] == "pending":
			rest, ms := lastNum(strings.TrimPrefix(arg, "pending "), 5000)
			want, perr := strconv.Atoi(strings.TrimSpace(rest))
			if perr != nil {
				return fmt.Errorf("usage: tel pending <n> [ms]")
			}
			n := -1
			if _, ok := r.waitFor(func(string) bool {
				var pending []json.RawMessage
				if r.C.telRPC(svc.TelSMSPending, nil, &pending) == nil {
					n = len(pending)
				}
				return n == want
			}, ms); !ok {
				return fmt.Errorf("telephonyd holds %d unacknowledged events, want %d", n, want)
			}
		case len(f) == 2 && f[0] == "lock":
			err = r.C.TelHook(svc.TelSimLock, map[string]string{"pin": f[1]})
		case len(f) >= 2 && f[0] == "ussd":
			// The network asks a question by USSD (a reply ends it).
			err = r.C.TelHook(svc.TelSimUSSD, map[string]any{"text": strings.TrimPrefix(arg, "ussd "), "request": true})
		case f[0] == "network":
			err = r.C.TelHook(svc.TelSimNetwork, simNetworkArgs(strings.Fields(arg)[1:]))
		default:
			err = fmt.Errorf("usage: tel ring <number> | tel sms <from> <text> | tel held <from> <text> | tel kill | tel cb <channel> <serial> <text> | tel pending <n> [ms] | tel lock <pin> | tel ussd <text> | tel network <sim-mcc> <network-mcc> [<nitz-minutes>]")
		}
		if err != nil {
			return err
		}
		r.pump(100 * time.Millisecond)
	case "audio":
		// audio <category> | audio paused <category> | audio idle: wait for
		// what phone-audiod reports.
		rest, ms := lastNum(arg, 8000)
		f := strings.Fields(rest)
		want := func(streams []audio.Stream) bool {
			switch {
			case len(f) == 1 && f[0] == "idle":
				for _, s := range streams {
					if s.State == ui.PlayPlaying && s.Category != audio.Notify {
						return false
					}
				}
				return true
			case len(f) == 2 && f[0] == ui.PlayPaused:
				for _, s := range streams {
					if s.Category == f[1] && s.State == ui.PlayPaused {
						return true
					}
				}
			case len(f) == 1:
				for _, s := range streams {
					if s.Category == f[0] && s.State == ui.PlayPlaying {
						return true
					}
				}
			}
			return false
		}
		var last []audio.Stream
		if _, ok := r.waitFor(func(string) bool {
			st, err := r.C.AudioState()
			last = st
			return err == nil && want(st)
		}, ms); !ok {
			return fmt.Errorf("audio %s: streams are %+v", rest, last)
		}
	case "push":
		// push [voip] [normal] [sealed] <app-id> <title> | <body> | <data>: as the app's server.
		var o TestPush
		for {
			word, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
			switch word {
			case "voip":
				o.Kind = "voip"
			case "normal":
				o.Priority = "normal"
			case "sealed":
				o.Seal = true
			case "other":
				o.Other = true
			default:
				goto parsed
			}
			arg = rest
		}
	parsed:
		id, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
		var fields []string
		for _, f := range strings.SplitN(rest, "|", 3) {
			fields = append(fields, strings.TrimSpace(f))
		}
		if err := r.C.SendTestPush(id, o, fields...); err != nil {
			return fmt.Errorf("push: %w", err)
		}
	case "timeshift":
		sec, _ := strconv.Atoi(arg)
		r.C.SetClockSkew(time.Duration(sec) * time.Second)
		r.pump(600 * time.Millisecond)
	case "long":
		if k, ok := keyNames[arg]; ok {
			r.C.Press(Key{Name: k, Long: true})
		} else {
			r.C.Key(LongRunes(arg))
		}
		r.pump(40 * time.Millisecond)
	case "keypad":
		r.C.SetKeypad(arg)
	case "imemode":
		r.C.SetIMEMode(arg)
	case "gui-env":
		f := strings.Fields(arg)
		if len(f) != 2 {
			return fmt.Errorf("usage: gui-env <name> <value>")
		}
		if r.Watch == nil {
			return nil
		}
		has := func(string) bool {
			raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", r.Watch.G.Pid()))
			return strings.Contains("\x00"+string(raw), "\x00"+f[0]+"="+f[1]+"\x00")
		}
		if _, ok := r.waitFor(has, 5000); !ok {
			return fmt.Errorf("the renderer (pid %d) does not have %s=%s", r.Watch.G.Pid(), f[0], f[1])
		}
	case "locale":
		f := strings.Fields(arg)
		if len(f) != 2 {
			return fmt.Errorf("usage: locale <lang> <zone>")
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.Language() == f[0] && r.C.ZoneName() == f[1] }, 5000); !ok {
			return fmt.Errorf("the language is %s and the time zone %s", r.C.Language(), r.C.ZoneName())
		}
	case "setting":
		f := strings.Fields(arg)
		if len(f) != 2 {
			return fmt.Errorf("usage: setting <key> <value>")
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			return err
		}
		return r.C.settingsSet(f[0], v)
	case "pkg-tamper":
		f := strings.Fields(arg)
		a := r.C.AppByID(f[0])
		if len(f) != 2 || a == nil || !a.M.installed {
			return fmt.Errorf("usage: pkg-tamper <installed app-id> <file>")
		}
		fh, err := os.OpenFile(filepath.Join(a.M.dir, f[1]), os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return err
		}
		_, err = fh.Write([]byte{0})
		fh.Close()
		return err
	case "camera-frames":
		rest, ms := lastNum(arg, 5000)
		n, _ := strconv.Atoi(strings.TrimSpace(rest))
		if _, ok := r.waitFor(func(string) bool { f, _ := r.C.MediaInfo()["frames"].(float64); return int(f) >= n }, ms); !ok {
			return fmt.Errorf("the camera captured fewer than %d frames", n)
		}
	case "vibrated":
		_, ms := lastNum(arg, 5000)
		base := r.vibrations
		if _, ok := r.waitFor(func(string) bool { return r.C.Vibrations() > base }, ms); !ok {
			return fmt.Errorf("no vibration")
		}
		r.vibrations = r.C.Vibrations()
	case "mem-low":
		n, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil {
			return fmt.Errorf("usage: mem-low <n>")
		}
		r.C.SimulateLowMemory(n)
		for range n {
			r.C.checkMemory()
			r.pump(40 * time.Millisecond)
		}
	case "appenv":
		f := strings.Fields(arg)
		if len(f) != 3 {
			return fmt.Errorf("usage: appenv <app-id> <name> <value>")
		}
		a := r.C.AppByID(f[0])
		if a == nil || a.proc == nil {
			return fmt.Errorf("%s is not running", f[0])
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", a.proc.Pid))
		if err != nil {
			return err
		}
		want := f[1] + "=" + f[2]
		for _, kv := range strings.Split(string(raw), "\x00") {
			if kv == want {
				return nil
			}
		}
		return fmt.Errorf("%s does not have %s", f[0], want)
	case "gui-crash":
		if r.Watch == nil {
			return fmt.Errorf("no pixel renderer")
		}
		old := r.Watch.G
		pid := old.Pid()
		if err := old.Kill(); err != nil {
			return err
		}
		select {
		case <-old.Done():
		case <-time.After(time.Duration(5*max(1, r.Scale)) * time.Second):
			return fmt.Errorf("renderer %d did not exit", pid)
		}
		r.restartGUI()
		if r.Watch == nil || r.Watch.G.Pid() == pid {
			return fmt.Errorf("the watchdog did not start a new renderer")
		}
		if r.GUI.last == "" {
			return fmt.Errorf("the new renderer did not get the current frame")
		}
		fmt.Fprintf(r.Out, "PHONE-GUI-RESTARTED %s (pid %d → %d)\n", r.GUI.Info(), pid, r.GUI.Pid())
	case "type":
		for _, ch := range arg {
			r.C.Key(Runes(string(ch)))
			r.pump(10 * time.Millisecond)
		}
		r.pump(40 * time.Millisecond)
	case "fill":
		v := r.C.current().top()
		if v == nil || v.focused() == nil || v.focused().Kind != ui.KindInput {
			return fmt.Errorf("fill: no input has focus")
		}
		v.inputs[v.focus] = strings.ReplaceAll(arg, "{chat}", strings.TrimSuffix(r.C.appEnvValue(ui.EnvChatServer), "/"))
		r.C.postFrame()
		r.pump(40 * time.Millisecond)
	case "wait-re":
		// wait-re <regexp> [ms]: for text that moves on (a play position).
		pat, ms := lastNum(arg, 8000)
		re, err := regexp.Compile(strings.TrimSpace(pat))
		if err != nil {
			return err
		}
		if _, ok := r.waitFor(func(s string) bool { return re.MatchString(s) }, ms); !ok {
			return fmt.Errorf("nothing matching %q appeared", pat)
		}
	case "wait", "gone":
		text, ms := lastNum(arg, 8000)
		want := cmd == "wait"
		if _, ok := r.waitFor(func(s string) bool { return strings.Contains(s, text) == want }, ms); !ok {
			if want {
				return fmt.Errorf("text %q did not appear", text)
			}
			return fmt.Errorf("text %q did not go away", text)
		}
	case "frames":
		_, ms := lastNum(arg, 5000)
		n, _ := strconv.Atoi(strings.Fields(arg)[0])
		base := r.C.FramesSeen()
		if _, ok := r.waitFor(func(string) bool { return r.C.FramesSeen()-base >= n }, ms); !ok {
			return fmt.Errorf("only %d media frames reached the screen", r.C.FramesSeen()-base)
		}
	case "painted":
		_, ms := lastNum(arg, 8000)
		if _, ok := r.waitFor(func(string) bool { ok, _ := r.C.WebPainted(); return ok }, ms); !ok {
			_, why := r.C.WebPainted()
			return fmt.Errorf("the page is not painted: %s", why)
		}
	case "pages-ended":
		n, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil {
			return fmt.Errorf("usage: pages-ended <n>")
		}
		if got := r.C.LowMemoryPageStops(); got < n {
			return fmt.Errorf("%d background apps' pages were ended for low memory, want at least %d", got, n)
		}
	case "web-procs":
		n, _ := strconv.Atoi(strings.TrimSpace(arg))
		if got := r.C.WebProcsStarted(); got != n {
			return fmt.Errorf("%d web processes were started, want %d", got, n)
		}
	case "masked":
		if !strings.Contains(r.screen(), "▒") {
			return fmt.Errorf("secure content is not masked in the snapshot")
		}
	case "sleep":
		ms, _ := strconv.Atoi(arg)
		r.pump(time.Duration(ms) * time.Millisecond)
	case "freeze-after":
		ms, _ := strconv.Atoi(arg)
		r.C.SetFreezeAfter(time.Duration(ms) * time.Millisecond)
	case "at":
		// at <screen-id>: wait until that screen is on top ("sys.idle", or
		// "<app-id>/<screen-id>").
		rest, ms := lastNum(arg, 8000)
		id := strings.TrimSpace(rest)
		at := func() bool { s := r.C.ScreenID(); return s == id || strings.HasSuffix(s, "/"+id) }
		if _, ok := r.waitFor(func(string) bool { return at() }, ms); !ok {
			return fmt.Errorf("at %s: the screen is %s", id, r.C.ScreenID())
		}
	case "nfc":
		return r.nfcStep(arg)
	case "push-sealed":
		// push-sealed <app-id>: the app has received end-to-end encrypted
		// push data (from its server).
		rest, ms := lastNum(arg, 8000)
		id := strings.TrimSpace(rest)
		if _, ok := r.waitFor(func(string) bool { return r.C.SealedPushes(id) > 0 }, ms); !ok {
			return fmt.Errorf("%s received no sealed push data", id)
		}
	case "pushgw-kill":
		// The gateway instance the device is connected to dies; the device
		// must move to another.
		gws, cur := r.C.PushGateways()
		name := ""
		for i, gw := range gws {
			if gw == cur {
				name = []string{"phone-pushgw", "phone-pushgw-b"}[min(i, 1)]
			}
		}
		if name == "" {
			return fmt.Errorf("pushd is not connected to a known gateway (%q of %v)", cur, gws)
		}
		if err := r.KillService(name); err != nil {
			return err
		}
		if _, ok := r.waitFor(func(string) bool { _, now := r.C.PushGateways(); return now != "" && now != cur }, 8000); !ok {
			return fmt.Errorf("the device did not move off %s", cur)
		}
	case "net-send":
		f := strings.Fields(arg)
		if len(f) != 2 || (f[1] != "sent" && f[1] != "blocked") {
			return fmt.Errorf("usage: net-send <app-id> <sent|blocked>")
		}
		err := r.C.sendAs(f[0])
		switch {
		case f[1] == "sent" && err != nil:
			return fmt.Errorf("%s could not send: %v", f[0], err)
		case f[1] == "blocked" && err == nil:
			return fmt.Errorf("%s sent past the firewall", f[0])
		case f[1] == "blocked" && !errors.Is(err, syscall.EPERM):
			return fmt.Errorf("%s: %v, want the firewall's EPERM", f[0], err)
		}
	case "perm-state":
		f := strings.Fields(arg)
		if len(f) != 3 {
			return fmt.Errorf("usage: perm-state <key> <perm> <state>")
		}
		var got string
		if _, ok := r.waitFor(func(string) bool {
			got = firstNonEmpty(r.C.broker.perms[f[0]][f[1]], "ask")
			return got == f[2]
		}, 5000); !ok {
			return fmt.Errorf("%s %s is %s, want %s", f[0], f[1], got, f[2])
		}
	case "cgroup":
		f, ms := strings.Fields(arg), 8000
		if len(f) == 4 {
			n, err := strconv.Atoi(f[3])
			if err != nil {
				return fmt.Errorf("usage: cgroup <app-id> <file> <value> [ms]")
			}
			f, ms = f[:3], n
		}
		if len(f) != 3 {
			return fmt.Errorf("usage: cgroup <app-id> <file> <value> [ms]")
		}
		if r.C.cg == nil {
			return fmt.Errorf("no per-app cgroups")
		}
		var got string
		if _, ok := r.waitFor(func(string) bool { got = r.C.cg.value(f[0], f[1]); return got == f[2] }, ms); !ok {
			return fmt.Errorf("cgroup %s %s is %q, want %q", f[0], f[1], got, f[2])
		}
	case "mp-build":
		f := strings.Fields(arg)
		if len(f) < 3 {
			return fmt.Errorf("usage: mp-build <example> <host-app-id> <file> [tamper]")
		}
		if err := r.C.buildMiniProgram(f[0], f[1], f[2], len(f) > 3 && f[3] == "tamper"); err != nil {
			return err
		}
	case "pkg-build":
		f := strings.Fields(arg)
		if len(f) < 3 {
			return fmt.Errorf("usage: pkg-build <example> <app-id> <file> [tamper]")
		}
		if err := r.C.buildExample(f[0], f[1], f[2], len(f) > 3 && f[3] == "tamper"); err != nil {
			return err
		}
	case "ime-remote":
		// ime-remote on|off [ms]: keys go (or no longer go) to phone-imed.
		f := strings.Fields(arg)
		ms := 5000
		if len(f) > 1 {
			ms, _ = strconv.Atoi(f[1])
		}
		want := len(f) > 0 && f[0] == "on"
		if _, ok := r.waitFor(func(string) bool { return r.C.IMERemote(want) == want }, ms); !ok {
			return fmt.Errorf("input method remote=%v, want %v", !want, want)
		}
	case "power":
		// power <level> [charging [fast]]
		f := strings.Fields(arg)
		level, _ := strconv.Atoi(f[0])
		r.C.SetPower(Power{Level: level, Charging: len(f) > 1 && f[1] == "charging", Fast: len(f) > 2 && f[2] == "fast"})
		r.pump(200 * time.Millisecond)
	case "job":
		// job <app> <id> runs <n> | job <app> <id> with <other id>
		f := strings.Fields(arg)
		if len(f) != 4 {
			return fmt.Errorf("job <app> <id> runs <n> | with <id>")
		}
		ok := func(string) bool {
			runs, batch := r.C.JobRuns(f[0], f[1])
			if f[2] == "with" {
				_, other := r.C.JobRuns(f[0], f[3])
				return batch != 0 && batch == other
			}
			want, _ := strconv.Atoi(f[3])
			return runs == want
		}
		if _, done := r.waitFor(ok, 3000); !done {
			runs, batch := r.C.JobRuns(f[0], f[1])
			return fmt.Errorf("job %s: %d runs, last in wake %d", f[1], runs, batch)
		}
	case "store-publish":
		// store-publish <example> <version> [rollout <percent>]: a developer
		// publishes through the store backend, to everyone or staged.
		f := strings.Fields(arg)
		rollout := 100
		if len(f) == 4 && f[2] == "rollout" {
			rollout, _ = strconv.Atoi(f[3])
		} else if len(f) != 2 {
			return fmt.Errorf("store-publish <example> <version> [rollout <percent>]")
		}
		if err := r.C.PublishExample(f[0], f[1], rollout); err != nil {
			return err
		}
	case "store-rollout":
		// store-rollout <app-id> <version> <percent>: its developer moves a
		// staged version.
		f := strings.Fields(arg)
		if len(f) != 3 {
			return fmt.Errorf("store-rollout <app-id> <version> <percent>")
		}
		percent, _ := strconv.Atoi(f[2])
		if err := r.C.RolloutExample(f[0], f[1], percent); err != nil {
			return err
		}
	case "store-takedown":
		// store-takedown <app-id> <reason>: the store staff take an app down.
		id, reason, _ := strings.Cut(strings.TrimSpace(arg), " ")
		if err := r.C.TakedownExample(id, reason); err != nil {
			return err
		}
	case "music-import":
		// Copies the sample music files into the media library, as a USB
		// transfer would.
		src := filepath.Join(filepath.Dir(r.C.cfg.AppsDir), "examples", "music")
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(r.C.musicDir(), 0o700); err != nil {
			return err
		}
		for _, e := range ents {
			raw, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(r.C.musicDir(), e.Name()), raw, 0o600); err != nil {
				return err
			}
		}
	case "app-pid":
		// app-pid <id> save | same: the process survives a Shell restart.
		id, op, _ := strings.Cut(arg, " ")
		pid := r.C.AppPid(id)
		if pid == 0 {
			return fmt.Errorf("%s is not running", id)
		}
		if op == "save" {
			if r.pids == nil {
				r.pids = map[string]int{}
			}
			r.pids[id] = pid
			return nil
		}
		if was := r.pids[id]; was != pid {
			return fmt.Errorf("%s runs as pid %d, was %d: it was restarted", id, pid, was)
		}
	case "adopted":
		if !r.C.Adopted(strings.TrimSpace(arg)) {
			return fmt.Errorf("%s was not taken over from the previous Shell", arg)
		}
	case "mem-report":
		fmt.Fprintln(r.Out, MemReport())
	case "ime-kill":
		pid, err := r.C.IMEPid()
		if err != nil {
			return err
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			return err
		}
	case "sms-kill":
		if err := r.C.SMSKill(); err != nil {
			return err
		}
	case "alerts":
		rest, ms := lastNum(strings.TrimSpace(arg), 5000)
		want, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return fmt.Errorf("usage: alerts <n> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.AlertsShown() == want }, ms); !ok {
			return fmt.Errorf("%d emergency alerts shown, want %d", r.C.AlertsShown(), want)
		}
	case "appstop":
		id := strings.TrimSpace(arg)
		if r.C.ProcState(id) != ui.PlayStopped {
			if err := r.C.KillApp(id); err != nil {
				return err
			}
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.ProcState(id) == ui.PlayStopped }, 5000); !ok {
			return fmt.Errorf("%s did not stop", id)
		}
		r.C.ForgetTombstone(id)
	case "appkill":
		if err := r.C.KillApp(strings.TrimSpace(arg)); err != nil {
			return err
		}
	case "wifi-ap":
		f := strings.Fields(arg)
		if len(f) != 2 {
			return fmt.Errorf("wifi-ap <ssid> <passphrase>")
		}
		switch w := r.C.wifi.(type) {
		case *simWifi:
			w.addNetwork(f[0], f[1])
		case *iwdRadio:
			if err := w.startAP(f[0], f[1]); err != nil {
				return err
			}
		default:
			return fmt.Errorf("no Wi-Fi")
		}
	case "wifi-eap-ap":
		f := strings.Fields(arg)
		if len(f) != 4 {
			return fmt.Errorf("wifi-eap-ap <ssid> <user> <password> <domain>")
		}
		switch w := r.C.wifi.(type) {
		case *simWifi:
			w.addEnterprise(f[0], f[1], f[2], f[3])
		case *iwdRadio:
			if err := w.startEAP(eapTestDir, f[0], f[1], f[2], f[3]); err != nil {
				return err
			}
		default:
			return fmt.Errorf("no Wi-Fi")
		}
	case "radio":
		rest, ms := lastNum(arg, 8000)
		f := strings.Fields(rest)
		if len(f) != 2 || (f[1] != "on" && f[1] != "off") {
			return fmt.Errorf("usage: radio wifi|bt|cell on|off [ms]")
		}
		want := f[1] == "on"
		on := func() (bool, error) {
			switch {
			case f[0] == "wifi" && r.C.wifi != nil:
				st, err := r.C.wifi.state()
				return st.Enabled, err
			case f[0] == "bt" && r.C.bt != nil:
				st, err := r.C.bt.state()
				return st.Enabled, err
			case f[0] == "cell" && r.C.tel != nil:
				return r.C.tel.Radio, nil
			}
			return false, fmt.Errorf("no %s", f[0])
		}
		var now bool
		var err error
		if _, ok := r.waitFor(func(string) bool { now, err = on(); return err == nil && now == want }, ms); !ok {
			return fmt.Errorf("%s is on: %v (%v), want %v", f[0], now, err, want)
		}
	case "data-cut":
		rest, ms := lastNum(arg, 5000)
		want := strings.TrimSpace(rest)
		if r.C.traffic == nil {
			return fmt.Errorf("no firewall")
		}
		var got []string
		if _, ok := r.waitFor(func(string) bool {
			got, _ = r.C.traffic.fw.Cut(cutMobile)
			return (want == "none" && len(got) == 0) || (len(got) == 1 && got[0] == want)
		}, ms); !ok {
			return fmt.Errorf("the firewall closes %v, want %s", got, want)
		}
	case "bt-push", "bt-pull":
		name, text, _ := strings.Cut(strings.TrimSpace(arg), " ")
		if cmd == "bt-push" && (name == "" || text == "") {
			return fmt.Errorf("usage: bt-push <name> <text>")
		}
		return r.C.peerRun(cmd == "bt-pull", name, text)
	case "bt-peer-result":
		rest, ms := lastNum(arg, 15000)
		want, text, _ := strings.Cut(strings.TrimSpace(rest), " ")
		var res *peerResult
		if _, ok := r.waitFor(func(string) bool { res = r.C.peerDone(); return res != nil }, ms); !ok {
			return fmt.Errorf("the device's transfer has not ended")
		}
		var refused obex.ResponseError
		switch {
		case want == "ok" && res.err != nil:
			return fmt.Errorf("the device's transfer failed: %v", res.err)
		case want == "refused" && !errors.As(res.err, &refused):
			return fmt.Errorf("the device's transfer was not refused: %v", res.err)
		case text != "" && !strings.Contains(res.book, text):
			return fmt.Errorf("the phone book lacks %q:\n%s", text, res.book)
		}
	case "bt-peer-got":
		rest, ms := lastNum(arg, 10000)
		name, text, _ := strings.Cut(strings.TrimSpace(rest), " ")
		var got []byte
		if _, ok := r.waitFor(func(string) bool { got = r.C.peerGot(name); return got != nil }, ms); !ok || !strings.Contains(string(got), text) {
			return fmt.Errorf("the device got %q as %s, want %q in it", got, name, text)
		}
	case "bt-peer":
		name := strings.TrimSpace(arg)
		switch b := r.C.bt.(type) {
		case *simBT:
			b.addPeer(name, false)
		case *bluezRadio:
			if err := b.setPeer(name); err != nil {
				return err
			}
		default:
			return fmt.Errorf("no Bluetooth")
		}
	case "lowmem-kill":
		if err := r.C.EndInBackground(strings.TrimSpace(arg)); err != nil {
			return err
		}
	case "saved", "revived", "revoked":
		id, ms := lastNum(arg, 8000)
		id = strings.TrimSpace(id)
		check := map[string]func(string) bool{"saved": r.C.HasSavedState, "revived": r.C.Revived, "revoked": r.C.Revoked}[cmd]
		if _, ok := r.waitFor(func(string) bool { return check(id) }, ms); !ok {
			return fmt.Errorf("%s: not %s", id, cmd)
		}
	case "appstate":
		rest, ms := lastNum(arg, 8000)
		f := strings.Fields(rest)
		if len(f) != 2 {
			return fmt.Errorf("usage: appstate <app-id> <frozen|running|stopped> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.ProcState(f[0]) == f[1] }, ms); !ok {
			return fmt.Errorf("%s is %s, want %s", f[0], r.C.ProcState(f[0]), f[1])
		}
	case "snap":
		if r.GUI != nil {
			r.C.Render()
			if err := r.GUI.Snap(arg); err != nil {
				return err
			}
		}
		if r.SnapDir != "" {
			_ = os.MkdirAll(r.SnapDir, 0o755)
			can := r.C.Render()
			if err := os.WriteFile(filepath.Join(r.SnapDir, arg+".txt"), []byte(can.Plain()), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(r.SnapDir, arg+".ans"), []byte(can.ANSIMasked()), 0o644)
		}
	default:
		return fmt.Errorf("unknown step %q", cmd)
	}
	return nil
}
