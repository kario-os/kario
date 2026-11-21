package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Minor mode (Settings › Minor mode, 19-core-apps.md; the parental PIN,
// 31-lock-and-biometrics.md §10): a phone for a child. A parental PIN in
// phone-keystored guards it: setting one turns minor mode on, turning it
// off removes it. While it is on, calls and messages go only to and from
// the contacts a parent allowed (the others are blocked as the block list
// blocks them; emergency numbers always go through); only the apps a
// parent allowed are on the menu and in the palette, and only they run;
// installing from the store, developer mode, unlocking the device and the
// date and time take the parental PIN, as does every change to minor mode
// and to screen time (screentime.go). The shell enforces it; the PIN goes
// from the system's own screen to phone-keystored and nowhere else. With
// the PIN, a parent's account may manage the same rules remotely
// (family.go).

// parentOpenFor: how long a parent's PIN entered in Settings lets them
// change one thing after another, while Settings stays in front.
const parentOpenFor = 10 * time.Minute

// minorDefaultApps are allowed when minor mode comes on, besides the
// phone and Settings: no browser, store, payments, chat, mini-programs or
// games until a parent allows them.
var minorDefaultApps = []string{appMessages, appContacts, appCards, appCalendar, "com.phone.clock", "com.phone.calc",
	"com.phone.camera", "com.phone.gallery", "com.phone.music", "com.phone.weather"}

// Allowed numbers are kept with the name they had when a parent chose
// them: a contact edited later (another number put under the name) does
// not change who may call.
type minorContact struct {
	Name   string `json:"name"`
	Number string `json:"number"`
}

// minorPolicy is state/shell/minor.json. On is phone-keystored's word (a
// parental PIN is set) as last heard: a start begins in minor mode before
// it answers.
type minorPolicy struct {
	On       bool           `json:"on"`
	Apps     []string       `json:"apps"`
	Contacts []minorContact `json:"contacts"`
}

type minorShell struct {
	p *minorPolicy
	// asked: phone-keystored was asked whether a parental PIN is set.
	asked bool
	// openUntil: a parent's PIN entered in Settings counts until then
	// (parentOpen).
	openUntil time.Time
	// remote: a parent's account managing minor mode (family.go).
	remote familyShell
}

func (c *Core) minorPath() string { return filepath.Join(c.cfg.StateDir, "shell", "minor.json") }

func (c *Core) minor() *minorPolicy {
	if c.parental.p == nil {
		c.parental.p = &minorPolicy{}
		if raw, err := os.ReadFile(c.minorPath()); err == nil {
			_ = json.Unmarshal(raw, c.parental.p)
		}
	}
	return c.parental.p
}

func (c *Core) saveMinor() {
	raw, _ := json.MarshalIndent(c.minor(), "", "  ")
	_ = os.MkdirAll(filepath.Dir(c.minorPath()), 0o700)
	_ = os.WriteFile(c.minorPath(), raw, 0o600)
}

func (c *Core) minorOn() bool { return c.minor().On }

// minorStart asks phone-keystored, once, whether a parental PIN is set;
// its word corrects the one kept either way.
func (c *Core) minorStart() {
	if c.parental.asked {
		return
	}
	c.parental.asked = true
	var st keystore.ParentStatus
	c.broker.call(svc.Keystored, keystore.MethodParentStatus, nil, &st, func(err error) {
		if err != nil {
			c.log.Printf("minor mode: %v", err)
			return
		}
		if st.PIN != c.minorOn() {
			c.log.Printf("minor mode: the key service says a parental PIN is set: %v", st.PIN)
			c.setMinor(st.PIN)
		}
	})
}

// setMinor turns minor mode on, with the apps it allows at first and no
// contacts, or off, its choices gone and screen time's limits and downtime
// with them: off, the phone holds nothing back.
func (c *Core) setMinor(on bool) {
	p := &minorPolicy{}
	if on {
		p.On = true
		for _, id := range minorDefaultApps {
			if c.appByID(id) != nil {
				p.Apps = append(p.Apps, id)
			}
		}
		c.parental.p = p
		c.saveMinor()
	} else {
		c.familyLeave()
		c.parental.p, c.parental.openUntil = p, time.Time{}
		_ = os.Remove(c.minorPath())
		c.screenTimeClear()
	}
	c.log.Printf("minor mode %v", on)
	c.minorApply()
}

// minorApply puts the policy in force: the apps it hides stop and their
// notifications go; smsd hears the numbers it allows.
func (c *Core) minorApply() {
	for _, a := range c.apps {
		if !c.minorHidden(a) {
			continue
		}
		c.clearNotifs(a)
		c.kill(a)
		a.tomb = nil
		c.removeMRU(a)
	}
	c.smsOnly()
	c.refreshSystemScreens()
}

// minorHidden: minor mode keeps the app off the menu and from running.
func (c *Core) minorHidden(a *App) bool {
	p := c.minor()
	return p.On && !neverHeld(a) && !slices.Contains(p.Apps, a.M.ID)
}

// minorRefusal keeps an app minor mode hides from starting, in the
// background too (a push, a job).
func (c *Core) minorRefusal(a *App) error {
	if c.minorHidden(a) {
		return errors.New(tr.T("“%s” is not allowed in minor mode.", a.M.Name))
	}
	return nil
}

// minorRefuses: minor mode keeps calls and messages to and from the
// number out.
func (c *Core) minorRefuses(number string) bool {
	p := c.minor()
	if !p.On || c.emergencyNumber(strings.TrimSpace(number)) {
		return false
	}
	key := ui.NumberKey(number)
	return key == "" || !slices.ContainsFunc(p.Contacts, func(x minorContact) bool { return ui.NumberKey(x.Number) == key })
}

// minorRecipients refuses a message to a number minor mode does not allow
// (the recipients are separated by commas or semicolons); one sent again
// by its ID went to its recipients before.
func (c *Core) minorRecipients(to string) error {
	for _, n := range strings.FieldsFunc(to, func(r rune) bool { return r == ',' || r == ';' }) {
		if c.minorRefuses(n) {
			return errors.New(tr.T("Minor mode: only the contacts a parent allowed can be texted"))
		}
	}
	return nil
}

// smsOnly tells phone-smsd the numbers minor mode allows, the emergency
// numbers among them.
func (c *Core) smsOnly() {
	if !c.sms.up {
		return
	}
	p := c.minor()
	args := map[string]any{"on": p.On}
	if p.On {
		numbers := slices.Sorted(maps.Keys(emergencyNumbers))
		if c.tel != nil {
			numbers = append(numbers, c.tel.Emergency...)
		}
		for _, x := range p.Contacts {
			numbers = append(numbers, x.Number)
		}
		args["numbers"] = numbers
	}
	go func() {
		if err := c.smsRPC(svc.SMSOnly, args, nil); err != nil {
			c.Post(func() { c.log.Printf("minor mode: messages service: %v", err) })
		}
	}()
}

// parentOpen: a parent's PIN entered lately in Settings, which has stayed
// in front since.
func (c *Core) parentOpen() bool {
	return c.fg != nil && c.fg.M.ID == appSettings && time.Now().Before(c.parental.openUntil)
}

// parentGate runs a change minor mode guards: at once while it is off or
// a parent's PIN counts, else after the parental PIN; fail hears why not.
func (c *Core) parentGate(why string, run func(), fail func(error)) {
	if !c.minorOn() || c.parentOpen() {
		run()
		return
	}
	c.askParentPIN("verify", why, func(err error) {
		if err != nil {
			fail(err)
			return
		}
		if c.fg != nil && c.fg.M.ID == appSettings {
			c.parental.openUntil = time.Now().Add(parentOpenFor)
		}
		run()
	})
}

// minorInstall: in minor mode an install from the store takes the parental
// PIN first, and the app it installs is allowed.
func (c *Core) minorInstall(id string, reply func(any, error), install func(reply func(any, error))) {
	if !c.minorOn() {
		install(reply)
		return
	}
	c.askParentPIN("verify", tr.T("A parent's PIN is needed to install apps."), func(err error) {
		if err != nil {
			reply(nil, err)
			return
		}
		install(func(res any, err error) {
			if p := c.minor(); err == nil && p.On && !slices.Contains(p.Apps, id) {
				p.Apps = append(p.Apps, id)
				c.saveMinor()
				c.familyLocalChange()
				c.refreshSystemScreens()
			}
			reply(res, err)
		})
	})
}

// minorGuarded are the system settings minor mode guards: what would let
// a child around it.
var minorGuarded = map[string]bool{ui.SettingDeveloperMode: true, ui.SettingDeviceUnlocked: true,
	ui.SettingTime: true, ui.SettingAutoTime: true, ui.SettingTimeZone: true}

// minorSetting is settings.set for Settings › Minor mode, and for the
// settings it guards; false for another key.
func (c *Core) minorSetting(args ui.SettingArgs, done func(error)) bool {
	p := c.minor()
	switch {
	case args.Key == ui.SettingMinorMode && args.Value != 0 && p.On, args.Key == ui.SettingMinorMode && args.Value == 0 && !p.On:
		done(nil)
	case args.Key == ui.SettingMinorMode && args.Value != 0:
		c.askParentPIN("set", "", func(err error) {
			if err == nil {
				c.setMinor(true)
				c.parental.openUntil = time.Now().Add(parentOpenFor)
			}
			done(err)
		})
	case args.Key == ui.SettingMinorMode:
		c.askParentPIN("clear", "", func(err error) {
			if err == nil {
				c.setMinor(false)
			}
			done(err)
		})
	case args.Key == ui.SettingParentPIN:
		if !p.On {
			done(errors.New(tr.T("Minor mode is off")))
			return true
		}
		c.askParentPIN("change", "", done)
	case args.Key == ui.SettingParentReset:
		switch {
		case !p.On:
			done(errors.New(tr.T("Minor mode is off")))
		case args.Value == 0:
			c.familyResetCancel()
			done(nil)
		default:
			c.familyResetStep(done)
		}
	case args.Key == ui.SettingMinorApp || args.Key == ui.SettingMinorContact:
		if !p.On {
			done(errors.New(tr.T("Minor mode is off")))
			return true
		}
		c.parentGate(tr.T("A parent's PIN is needed to change minor mode."), func() {
			err := c.changeMinor(args)
			if err == nil {
				c.saveMinor()
				c.minorApply()
				c.familyLocalChange()
			}
			done(err)
		}, done)
	case args.Key == ui.SettingParentLink || args.Key == ui.SettingParentConfirm:
		f := c.family()
		switch {
		case !p.On:
			done(errors.New(tr.T("Minor mode is off")))
		case args.Key == ui.SettingParentConfirm:
			c.parentGate(tr.T("A parent's PIN is needed to let an account manage this phone."), func() { c.familyConfirm(args.Value != 0, done) }, done)
		case args.Value != 0:
			c.parentGate(tr.T("A parent's PIN is needed to link a parent's account."), func() { c.familyPair(done) }, done)
		case f.file.Bound == nil:
			c.familyLeave()
			done(nil)
		default:
			c.parentGate(tr.T("A parent's PIN is needed to unlink the parent's account."), func() {
				c.familyLeave()
				done(nil)
			}, done)
		}
	case minorGuarded[args.Key] && p.On:
		c.parentGate(tr.T("A parent's PIN is needed for this in minor mode."), func() { done(c.settingsSet(args.Key, args.Value)) }, done)
	default:
		return false
	}
	return true
}

func (c *Core) changeMinor(args ui.SettingArgs) error {
	p := c.minor()
	if args.Key == ui.SettingMinorApp {
		a := c.appByID(args.Text)
		if a == nil || neverHeld(a) {
			return fmt.Errorf("%q is always allowed or no app", args.Text)
		}
		p.Apps = slices.DeleteFunc(p.Apps, func(id string) bool { return id == a.M.ID })
		if args.Value != 0 {
			p.Apps = append(p.Apps, a.M.ID)
		}
		c.log.Printf("minor mode: %s allowed %v", a.M.ID, args.Value != 0)
		return nil
	}
	n := strings.TrimSpace(args.Text)
	key := ui.NumberKey(n)
	if key == "" {
		return errors.New(tr.T("Not a phone number"))
	}
	p.Contacts = slices.DeleteFunc(p.Contacts, func(x minorContact) bool { return ui.NumberKey(x.Number) == key })
	if args.Value != 0 {
		p.Contacts = append(p.Contacts, minorContact{Name: c.contactName(n), Number: n})
	}
	c.log.Printf("minor mode: a number allowed %v", args.Value != 0)
	return nil
}

// minorView is settings.get's "minor": the apps with whether each is
// allowed, the numbers allowed, and the contacts' numbers that are not.
func (c *Core) minorView() map[string]any {
	p := c.minor()
	type app struct {
		App     string `json:"app"`
		Name    string `json:"name"`
		Allowed bool   `json:"allowed"`
		Fixed   bool   `json:"fixed,omitempty"`
	}
	apps := []app{}
	for _, a := range c.apps {
		apps = append(apps, app{App: a.M.ID, Name: a.M.Name, Allowed: !c.minorHidden(a), Fixed: neverHeld(a)})
	}
	allowed := []minorContact{}
	for _, x := range p.Contacts {
		if name := c.contactName(x.Number); name != "" {
			x.Name = name
		}
		allowed = append(allowed, x)
	}
	others := []minorContact{}
	for _, x := range c.contacts() {
		for _, n := range ui.NumbersOf(x) {
			if c.minorRefuses(n.Number) || !p.On {
				others = append(others, minorContact{Name: x.Name, Number: n.Number})
			}
		}
	}
	sort.SliceStable(others, func(i, j int) bool { return others[i].Name < others[j].Name })
	return map[string]any{"on": p.On, "apps": apps, "contacts": allowed, "others": others, "open": c.parentOpen(), "remote": c.familyView()}
}

// parentPadOverlay is the system's parental PIN screen: setting it (minor
// mode on), entering it for a change (verify), changing it, removing it
// (minor mode off), or a new one after the parent's signed reset (reset,
// family_ask.go). Settings hears only how it went.
type parentPadOverlay struct {
	op        string // set | verify | change | clear | reset
	why       string
	step      int // 0 the PIN in force, 1 the new one, 2 the new one again
	old, new  string
	digits    string
	busy      bool
	note      string
	bad       bool
	waitUntil time.Time
	done      func(error)
}

func (c *Core) askParentPIN(op, why string, done func(error)) {
	o := &parentPadOverlay{op: op, why: why, done: done}
	if o.newOnly() {
		o.step = 1
	}
	c.pushOverlay(o)
}

// newOnly: the screen asks only for a new PIN, not the one in force.
func (o *parentPadOverlay) newOnly() bool { return o.op == "set" || o.op == "reset" }

func (o *parentPadOverlay) key(c *Core, k Key) {
	if o.busy {
		return
	}
	switch k.Name {
	case KeyEsc:
		o.digits, o.old, o.new = "", "", ""
		c.popOverlay(o)
		o.done(errors.New(ui.ErrCancelled))
	case KeyBackspace:
		if o.digits != "" {
			o.digits = o.digits[:len(o.digits)-1]
		}
	case KeyRunes:
		o.digits = pinDigit(o.digits, c.fnRunes(k.Runes))
	case KeyEnter:
		o.submit(c)
	}
}

func (o *parentPadOverlay) submit(c *Core) {
	if time.Until(o.waitUntil) > 0 {
		return
	}
	if len(o.digits) < keystore.MinPIN {
		o.digits = ""
		o.note, o.bad = tr.T("The PIN is %d to %d digits", keystore.MinPIN, keystore.MaxPIN), true
		return
	}
	d := o.digits
	o.digits, o.note, o.bad = "", "", false
	switch {
	case o.step == 0 && o.op == "change":
		o.old, o.step = d, 1
		return
	case o.step == 1:
		o.new, o.step = d, 2
		return
	case o.step == 2 && d != o.new:
		o.new, o.step = "", 1
		o.note, o.bad = tr.T("The two PINs differ; enter the new PIN again"), true
		return
	}
	method, args := keystore.MethodParentVerify, map[string]any{"pin": d}
	switch o.op {
	case "set", "change":
		method, args = keystore.MethodParentSetPIN, map[string]any{"old": o.old, "pin": o.new}
	case "clear":
		method, args = keystore.MethodParentClearPIN, map[string]any{"old": d}
	case "reset":
		if err := c.familyResetValid(); err != nil {
			o.new = ""
			c.popOverlay(o)
			o.done(err)
			return
		}
		method, args = keystore.MethodParentResetPIN, map[string]any{"pin": o.new}
	}
	o.old, o.new, o.busy = "", "", true
	var r keystore.PINResult
	c.broker.call(svc.Keystored, method, args, &r, func(err error) {
		o.busy = false
		switch {
		case err != nil:
			o.step = map[bool]int{true: 1, false: 0}[o.newOnly()]
			o.note, o.bad = tr.T("Could not check the PIN: %v", err), true
		case r.OK:
			c.popOverlay(o)
			switch o.op {
			case "set":
				c.showToast(tr.T("Minor mode is on"))
			case "change":
				c.showToast(tr.T("Parental PIN changed"))
			case "clear":
				c.showToast(tr.T("Minor mode is off"))
			case "reset":
				c.showToast(tr.T("Parental PIN reset"))
			}
			o.done(nil)
		default:
			o.step = map[bool]int{true: 1, false: 0}[o.newOnly()]
			o.note, o.waitUntil = pinWrong(r)
			o.bad = true
		}
	})
}

func (o *parentPadOverlay) title() string {
	return map[string]string{"set": tr.T("Set a parental PIN"), "verify": tr.T("Parental PIN"),
		"change": tr.T("Change the parental PIN"), "clear": tr.T("Turn off minor mode"), "reset": tr.T("Reset the parental PIN")}[o.op]
}

func (o *parentPadOverlay) lines() (lines []string, note string, bad bool) {
	switch o.op {
	case "set":
		lines = append(lines, tr.T("Minor mode: calls and messages only with the contacts you allow, only the apps you allow, screen time limits. This PIN guards it; choose one the child does not know, not the lock-screen PIN."))
	case "clear":
		lines = append(lines, tr.T("Minor mode ends and the parental PIN is removed."))
	case "verify", "reset":
		lines = append(lines, o.why)
	}
	switch o.step {
	case 1:
		lines = append(lines, tr.T("New PIN (%d to %d digits)", keystore.MinPIN, keystore.MaxPIN))
	case 2:
		lines = append(lines, tr.T("The new PIN once more"))
	default:
		lines = append(lines, tr.T("The parental PIN"))
	}
	note, bad = pinNote(o.note, o.bad, o.waitUntil)
	if o.busy {
		note, bad = tr.T("Verifying…"), false
	}
	return
}

func (o *parentPadOverlay) frame(c *Core) *FrameOverlay {
	lines, note, bad := o.lines()
	return &FrameOverlay{Kind: "auth", Title: o.title(), Lines: lines, PIN: [2]int{len(o.digits), pinSlots(len(o.digits))},
		Note: note, NoteBad: bad,
		Footer: []string{tr.T("Digit keys to enter · OK confirm · Back cancel"), tr.T("The system draws this screen; the PIN goes only to the key service.")}}
}

func (o *parentPadOverlay) render(c *Core, can *cv.Canvas) {
	lines, note, bad := o.lines()
	drawLockPin(c, can, o.title(), lines, len(o.digits), pinSlots(len(o.digits)), note, bad,
		[]string{tr.T("Digit keys to enter · Enter confirm · Esc cancel"), tr.T("The system draws this screen; the PIN goes only to the key service.")})
}
