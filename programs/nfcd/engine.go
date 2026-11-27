package nfcd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/hal/nfc"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The engine decides what the phone answers a reader as. With the screen
// on and a controller with observe mode it sees each reader's polling
// loop first, whose vendor frame may say what kind of reader it is, and
// decides for that reader. With the screen off (the phone may be
// suspended) or without observe mode the controller answers on its own as
// the engine set it between fields: the door card that goes first now by
// its UID, and ISO-DEP for the app cards, whose commands the host answers
// once the field has woken it.
//
// Door cards go by the time of use: the one tapped most around this time
// of day, on this kind of day (workday or day off, the region's holiday
// calendar deciding where the shell has one), goes first; without
// such taps the one this reader accepted last, where its polling loop
// tells it apart (observe mode only), then the user's order. A card the
// reader refuses makes the next one first at the next tap; every tap a
// door accepts is recorded with its time.
//
// What may answer: payment cards of the payment app only, unlocked and
// with the screen on; cards the user lets answer locked (door cards and
// the express transit card unless the user says otherwise) always; other
// door cards unlocked with the screen on; an app's other cards with the
// screen on, and unlocked when it asks for that. A card the user chose in
// the quick view goes first for a while.

// How long a refusal keeps a card from going first (and the next card
// after it waits for the next tap), and the longest a card chosen in the
// quick view goes first.
const (
	failMemory  = 2 * time.Minute
	maxExplicit = 2 * time.Minute
)

// The time of use: a tap counts by a quartic kernel over the time of day,
// zero beyond tapReach minutes (a standard deviation of about an hour),
// on its own kind of day only, and half as much every tapHalfLife; taps
// older than tapKeep are dropped, at most maxTaps kept. The card pack's
// usual hours show each tap as usualSpan minutes either side of it, the
// usualSpans heaviest spans per kind of day.
const (
	tapReach    = 150
	tapHalfLife = 14 * 24 * time.Hour
	tapKeep     = 60 * 24 * time.Hour
	maxTaps     = 400
	usualSpan   = 60
	usualSpans  = 3
	maxReaders  = 32
)

// readKeep: how long a card read in reader mode waits to be added.
const readKeep = 5 * time.Minute

const (
	kindDoor    = ui.NFCDoor
	kindPayment = ui.NFCPayment
	kindOther   = ui.NFCOther
)

type card struct {
	ID, Name, Kind, App, AppName, Card string
	AIDs                               [][]byte
	UID                                []byte
	RequiresUnlock, CanExpress         bool
}

type door struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// tap is a door card a reader accepted, at the shell's time in its zone
// then (the time of day the user saw).
type tap struct {
	Card string    `json:"card"`
	At   time.Time `json:"at"`
}

// reader is the door card a reader, told apart by its polling loop,
// accepted last.
type reader struct {
	Sig  string    `json:"sig"`
	Card string    `json:"card"`
	At   time.Time `json:"at"`
}

// saved is what survives a restart: the user's choices, the door cards,
// their taps and the readers that took them.
type saved struct {
	Enabled        bool     `json:"enabled"`
	Order          []string `json:"order,omitempty"`
	Express        string   `json:"express,omitempty"`
	DefaultPayment string   `json:"default_payment,omitempty"`
	Foreground     bool     `json:"foreground,omitempty"`
	// Locked is the user's word on answering with the phone locked and the
	// screen off, for the cards where it differs from the default.
	Locked   map[string]bool `json:"locked,omitempty"`
	Doors    []door          `json:"doors,omitempty"`
	NextDoor int             `json:"next_door"`
	Taps     []tap           `json:"taps,omitempty"`
	Readers  []reader        `json:"readers,omitempty"`
}

// field is one reader's field, from on to off.
type field struct {
	session string
	// observed: the engine decides after the first polling loop (loop);
	// else the controller answered as it was set between fields.
	observed bool
	loop     []nfc.PollingFrame
	hint     byte
	sig      string
	decided  bool
	// card is the card the reader gets first; door the door card whose
	// UID it gets (the same, or a UID beside ISO-DEP for app cards).
	card, door  *card
	ident       *nfc.Identity
	activations int
	halted      bool
	selected    *card
	aid         []byte
	exchanges   int
	lastSW      uint16
	waiting     bool
	blocked     *card
	told        bool
	// left: the app of selected was told the reader is done with it.
	left bool
}

// readCard is a door card read in reader mode, until it is added.
type readCard struct {
	uid   string
	until time.Time
}

type engine struct {
	mu   sync.Mutex
	ctl  nfc.Controller
	path string
	st   saved
	pol  svc.NFCPolicyArgs
	// zoned: the shell has said its zone (pol.ZoneOffset).
	zoned bool
	// daysOff and workdays are the policy's holiday calendar, by date.
	daysOff, workdays map[string]bool
	// reader is reader mode in use: the card pack's read or an app's
	// session (reader.go).
	reader *readerSession
	apps   []*card
	// explicit: the card chosen in the quick view, until explicitUntil.
	explicit      string
	explicitUntil time.Time
	// pending: the card the next tap presents after a refusal.
	pending      string
	pendingUntil time.Time
	failed       map[string]time.Time
	read         *readCard
	f            *field
	seq          int
	// What the controller answers as between fields (and on its own in a
	// field that is not observed), and the key it was last set by.
	idleCard, idleDoor *card
	idleID             *nfc.Identity
	idleKey            string
	// done counts finished fields; last is the card the last one
	// presented (the simulated reader's report).
	done int
	last *card
	emit func(svc.NFCEvent)
	// now is the system clock; clock adds the shell's skew and zone.
	now func() time.Time
}

func newEngine(ctl nfc.Controller, stateDir string, emit func(svc.NFCEvent)) *engine {
	e := &engine{ctl: ctl, path: filepath.Join(stateDir, "nfc", "state.json"), emit: emit, now: time.Now,
		failed: map[string]time.Time{}, st: saved{Enabled: true}}
	if raw, err := os.ReadFile(e.path); err == nil {
		_ = json.Unmarshal(raw, &e.st)
	}
	e.pol.ScreenOn = true
	return e
}

func (e *engine) save() {
	raw, _ := json.MarshalIndent(e.st, "", "  ")
	_ = os.MkdirAll(filepath.Dir(e.path), 0o700)
	tmp := e.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, e.path)
	}
}

// clock is the shell's time, in the shell's zone once it has said it.
func (e *engine) clock() time.Time {
	t := e.now().Add(time.Duration(e.pol.ClockSkewMS) * time.Millisecond)
	if e.zoned {
		t = t.In(time.FixedZone("", e.pol.ZoneOffset))
	}
	return t
}

func (e *engine) run() {
	e.mu.Lock()
	e.idle()
	e.mu.Unlock()
	go func() {
		// What the controller answers as on its own follows the time of use.
		for range time.Tick(time.Minute) {
			e.mu.Lock()
			if e.f == nil {
				e.idle()
			}
			e.mu.Unlock()
		}
	}()
	for ev := range e.ctl.Events() {
		e.mu.Lock()
		e.event(ev)
		e.mu.Unlock()
	}
}

// observing: the engine decides per reader. Only with the screen on: with
// it off the phone may be suspended, and the controller must answer by
// itself.
func (e *engine) observing() bool { return e.ctl.Info().Observe && e.pol.ScreenOn }

func (e *engine) event(ev nfc.Event) {
	switch ev.Type {
	case nfc.EventField:
		if ev.On {
			e.begin()
			return
		}
		if e.f != nil {
			e.leave(e.f, ui.NFCDeactivatedLinkLoss)
		}
		e.finish()
		e.f = nil
		e.done++
		e.idle()
		e.emit(svc.NFCEvent{Event: svc.NFCEventField})
	case nfc.EventFrame:
		if e.f == nil {
			e.begin()
		}
		f := e.f
		if !f.observed || f.decided {
			return
		}
		f.loop = append(f.loop, ev.Frame)
		if h := nfc.ReaderHint(ev.Frame); h != 0 {
			f.hint = h
		}
		if ev.Frame.Type == nfc.FrameOff || len(f.loop) >= 16 {
			e.decide()
		}
	case nfc.EventActivated:
		if e.f != nil {
			e.f.activations++
		}
	case nfc.EventDeactivated:
		if e.f != nil && ev.Sleep {
			e.f.halted = true
			e.leave(e.f, ui.NFCDeactivatedLinkLoss)
		}
	case nfc.EventAPDU:
		e.apdu(ev.APDU)
	}
}

func (e *engine) begin() {
	e.seq++
	f := &field{session: fmt.Sprintf("s%d", e.seq), observed: e.observing()}
	e.f = f
	e.emit(svc.NFCEvent{Event: svc.NFCEventField, On: true})
	if !f.observed {
		f.decided = true
		f.card, f.door, f.ident = e.idleCard, e.idleDoor, e.idleID
	}
}

// idle is what the phone answers as between fields: nothing while
// observing (the next loop decides), else what the next reader is most
// likely to want, set again only when it changes.
func (e *engine) idle() {
	var c, d *card
	var id *nfc.Identity
	if !e.observing() {
		cands, _ := e.choose(0, "")
		if len(cands) > 0 {
			c = cands[0]
		}
		d, id = e.identity(c, 0, cands)
	}
	e.idleCard, e.idleDoor, e.idleID = c, d, id
	key := "none"
	if id != nil {
		key = fmt.Sprintf("%X %v %X", id.UID, id.HCE, id.AIDs)
	}
	if key == e.idleKey {
		return
	}
	e.idleKey = key
	_ = e.ctl.Present(id)
}

func (e *engine) decide() {
	f := e.f
	f.decided = true
	f.sig = signature(f.loop)
	cands, blocked := e.choose(f.hint, f.sig)
	f.card, f.door, f.ident = nil, nil, nil
	e.idleKey = ""
	if len(cands) == 0 {
		f.blocked = blocked
		if blocked != nil && !f.told {
			f.told = true
			e.emit(svc.NFCEvent{Event: svc.NFCEventResult, Card: blocked.ID, Name: blocked.Name, Kind: blocked.Kind, Reason: e.usable(blocked)})
		}
		_ = e.ctl.Present(nil)
		return
	}
	f.card = cands[0]
	f.door, f.ident = e.identity(f.card, f.hint, cands)
	_ = e.ctl.Present(f.ident)
}

// redecide: what may answer changed while a reader is polling and no card
// has answered yet (the phone was unlocked at the terminal).
func (e *engine) redecide() {
	if f := e.f; f != nil && f.observed && f.decided && f.activations == 0 {
		e.decide()
	} else if f == nil {
		e.idle()
	}
}

// signature tells a reader by its polling loop, by the frames of its own
// only (vendor frames): a plain loop is the same for every reader of its
// kind. "" when there are none.
func signature(loop []nfc.PollingFrame) string {
	h := sha256.New()
	own := false
	for _, f := range loop {
		if f.Type == nfc.FrameCustom {
			own = true
			h.Write(f.Data)
		}
	}
	if !own {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// ordered is every card in the user's order; cards the order does not
// name yet come after, door cards first.
func (e *engine) ordered() []*card {
	all := []*card{}
	for _, d := range e.st.Doors {
		uid, _ := hex.DecodeString(d.UID)
		all = append(all, &card{ID: d.ID, Name: d.Name, Kind: kindDoor, UID: uid})
	}
	all = append(all, e.apps...)
	pos := map[string]int{}
	for i, id := range e.st.Order {
		if _, dup := pos[id]; !dup {
			pos[id] = i
		}
	}
	at := func(c *card) int {
		if i, ok := pos[c.ID]; ok {
			return i
		}
		return len(e.st.Order)
	}
	sort.SliceStable(all, func(i, j int) bool { return at(all[i]) < at(all[j]) })
	return all
}

func (e *engine) find(id string) *card {
	for _, c := range e.ordered() {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// paymentApp is the app whose payment cards answer: the one in the
// foreground when the user allows it, else the default.
func (e *engine) paymentApp() string {
	if e.st.Foreground && e.pol.Foreground != "" && e.hasPayment(e.pol.Foreground) {
		return e.pol.Foreground
	}
	return e.defaultPayment()
}

// defaultPayment is the user's choice, or the only payment app there is
// (as Android makes it the default).
func (e *engine) defaultPayment() string {
	if e.hasPayment(e.st.DefaultPayment) {
		return e.st.DefaultPayment
	}
	only := ""
	for _, c := range e.apps {
		if c.Kind == kindPayment {
			if only != "" && only != c.App {
				return ""
			}
			only = c.App
		}
	}
	return only
}

func (e *engine) hasPayment(app string) bool {
	for _, c := range e.apps {
		if c.Kind == kindPayment && c.App == app {
			return true
		}
	}
	return false
}

// lockedFixed: the card never answers locked, whatever the user says
// (payment cards, and cards their app keeps for an unlocked phone).
func lockedFixed(c *card) bool { return c.Kind == kindPayment || c.RequiresUnlock }

// lockedOK: the card answers with the phone locked and the screen off:
// the user's word, else door cards and the express transit card do.
func (e *engine) lockedOK(c *card) bool {
	if lockedFixed(c) {
		return false
	}
	if on, ok := e.st.Locked[c.ID]; ok {
		return on
	}
	return c.Kind == kindDoor || c.CanExpress && c.ID == e.st.Express
}

// usable is why a card may not answer now ("" when it may): "off" (NFC
// is off), "default" (not the payment app's), "locked", "screen".
func (e *engine) usable(c *card) string {
	p := e.pol
	switch {
	case !e.st.Enabled:
		return "off"
	case c.Kind == kindPayment && c.App != e.paymentApp():
		return "default"
	case e.lockedOK(c):
		return ""
	case p.Locked && (c.Kind != kindOther || c.RequiresUnlock):
		return "locked"
	case !p.ScreenOn:
		return "screen"
	}
	return ""
}

func (e *engine) eligible() []*card {
	var out []*card
	for _, c := range e.ordered() {
		if e.usable(c) == "" {
			out = append(out, c)
		}
	}
	return out
}

func hintKind(c *card, hint byte) bool {
	switch hint {
	case nfc.HintAccess:
		return c.Kind == kindDoor
	case nfc.HintTransit:
		return c.Kind == kindOther && c.CanExpress
	case nfc.HintPayment:
		return c.Kind == kindPayment
	}
	return false
}

// ranked: the cards that may answer, the kind the reader asks for first;
// door cards keep the places the order gives them but take them by the
// time of use (byTime). Cards refused lately are out.
func (e *engine) ranked(hint byte, sig string) []*card {
	var out []*card
	var doors []int
	for _, c := range e.eligible() {
		if t, ok := e.failed[c.ID]; ok && e.now().Sub(t) < failMemory {
			continue
		}
		if c.Kind == kindDoor {
			doors = append(doors, len(out))
		}
		out = append(out, c)
	}
	byTime := make([]*card, len(doors))
	for k, i := range doors {
		byTime[k] = out[i]
	}
	e.byTime(byTime, sig)
	for k, i := range doors {
		out[i] = byTime[k]
	}
	if hint != 0 {
		sort.SliceStable(out, func(i, j int) bool { return hintKind(out[i], hint) && !hintKind(out[j], hint) })
	}
	return out
}

// byTime orders door cards by how likely each is now (doorScores); among
// cards as likely (no taps around now), the one the reader with this
// signature accepted last goes first, then the order.
func (e *engine) byTime(doors []*card, sig string) {
	if len(doors) < 2 {
		return
	}
	score := e.doorScores(e.clock())
	hinted := e.readerCard(sig)
	sort.SliceStable(doors, func(i, j int) bool {
		a, b := score[doors[i].ID], score[doors[j].ID]
		if a != b {
			return a > b
		}
		return doors[i].ID == hinted && doors[j].ID != hinted
	})
}

func weekend(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }

// dayOff: t's date (in its zone) is a weekend day or a public holiday of
// the calendar, and not a weekend day the calendar makes a workday.
func (e *engine) dayOff(t time.Time) bool {
	d := t.Format(time.DateOnly)
	switch {
	case e.daysOff[d]:
		return true
	case e.workdays[d]:
		return false
	}
	return weekend(t)
}

func minuteOfDay(t time.Time) float64 {
	return float64(t.Hour()*60+t.Minute()) + float64(t.Second())/60
}

// apart is how many minutes two times of day are apart, across midnight
// too.
func apart(a, b float64) float64 {
	d := math.Abs(a - b)
	return min(d, 24*60-d)
}

// kernel is how much a tap d minutes of the day away counts: 1 at the
// same time, falling smoothly to 0 at tapReach.
func kernel(d float64) float64 {
	if d >= tapReach {
		return 0
	}
	x := d / tapReach
	return (1 - x*x) * (1 - x*x)
}

func weight(age time.Duration) float64 {
	return math.Exp2(-max(age, 0).Hours() / tapHalfLife.Hours())
}

// doorScores: how likely each door card is at now, from its taps on the
// same kind of day (workday or day off), nearer times of day and newer
// taps counting more.
func (e *engine) doorScores(now time.Time) map[string]float64 {
	out := map[string]float64{}
	off := e.dayOff(now)
	for _, t := range e.st.Taps {
		age := now.Sub(t.At)
		if e.dayOff(t.At) != off || age >= tapKeep {
			continue
		}
		if k := kernel(apart(minuteOfDay(t.At), minuteOfDay(now))); k > 0 {
			out[t.Card] += k * weight(age)
		}
	}
	return out
}

// readerCard is the door card the reader with this signature accepted
// last ("" for none, or none lately).
func (e *engine) readerCard(sig string) string {
	if sig == "" {
		return ""
	}
	for _, r := range e.st.Readers {
		if r.Sig == sig && e.clock().Sub(r.At) < tapKeep {
			return r.Card
		}
	}
	return ""
}

// doorFirst is the door card a door gets first now, by the time of use
// and the order.
func (e *engine) doorFirst() string {
	var doors []*card
	for _, c := range e.ordered() {
		if c.Kind == kindDoor {
			doors = append(doors, c)
		}
	}
	if len(doors) == 0 {
		return ""
	}
	e.byTime(doors, "")
	return doors[0].ID
}

// usual is when a door card is usually tapped: per kind of day, each
// tap's span merged with those it overlaps, the heaviest usualSpans in
// the order of the day, ends rounded to the half hour.
func (e *engine) usual(id string, now time.Time) []ui.NFCHours {
	type span struct {
		from, to int
		w        float64
	}
	var out []ui.NFCHours
	for _, we := range []bool{false, true} {
		var spans []span
		for _, t := range e.st.Taps {
			age := now.Sub(t.At)
			if t.Card != id || e.dayOff(t.At) != we || age >= tapKeep {
				continue
			}
			m := t.At.Hour()*60 + t.At.Minute()
			spans = append(spans, span{max(m-usualSpan, 0), min(m+usualSpan, 24*60), weight(age)})
		}
		sort.Slice(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
		var merged []span
		for _, s := range spans {
			if n := len(merged); n > 0 && s.from <= merged[n-1].to {
				merged[n-1].to = max(merged[n-1].to, s.to)
				merged[n-1].w += s.w
				continue
			}
			merged = append(merged, s)
		}
		sort.SliceStable(merged, func(i, j int) bool { return merged[i].w > merged[j].w })
		merged = merged[:min(len(merged), usualSpans)]
		sort.Slice(merged, func(i, j int) bool { return merged[i].from < merged[j].from })
		for _, s := range merged {
			out = append(out, ui.NFCHours{Weekend: we, From: (s.from + 15) / 30 * 30, To: (s.to + 15) / 30 * 30})
		}
	}
	return out
}

// record keeps a tap a door accepted, and which card the reader took.
func (e *engine) record(id, sig string) {
	now := e.clock().Truncate(time.Second)
	keep := make([]tap, 0, len(e.st.Taps)+1)
	for _, t := range e.st.Taps {
		if now.Sub(t.At) < tapKeep {
			keep = append(keep, t)
		}
	}
	keep = append(keep, tap{Card: id, At: now})
	if len(keep) > maxTaps {
		keep = keep[len(keep)-maxTaps:]
	}
	e.st.Taps = keep
	if sig != "" {
		readers := []reader{{Sig: sig, Card: id, At: now}}
		for _, r := range e.st.Readers {
			if r.Sig != sig && now.Sub(r.At) < tapKeep && len(readers) < maxReaders {
				readers = append(readers, r)
			}
		}
		e.st.Readers = readers
	}
	e.save()
}

// choose is the cards in the order a reader gets them: the user's pick,
// the one to try after a refusal, then the ranked. A reader that says what
// it wants gets nothing when only the lock or the screen keeps that kind
// of card silent (blocked: the card), rather than another kind.
func (e *engine) choose(hint byte, sig string) (cands []*card, blocked *card) {
	now := e.now()
	add := func(id string, until time.Time) {
		if id == "" || !now.Before(until) {
			return
		}
		if c := e.find(id); c != nil && e.usable(c) == "" && !listed(cands, c.ID) {
			cands = append(cands, c)
		}
	}
	add(e.explicit, e.explicitUntil)
	add(e.pending, e.pendingUntil)
	r := e.ranked(hint, sig)
	if hint != 0 && len(cands) == 0 {
		want := false
		for _, c := range r {
			want = want || hintKind(c, hint)
		}
		if !want {
			for _, c := range e.ordered() {
				if why := e.usable(c); hintKind(c, hint) && (why == "locked" || why == "screen") {
					return nil, c
				}
			}
		}
	}
	for _, c := range r {
		if !listed(cands, c.ID) {
			cands = append(cands, c)
		}
	}
	return cands, nil
}

func listed(cs []*card, id string) bool {
	for _, c := range cs {
		if c.ID == id {
			return true
		}
	}
	return false
}

// identity is what the phone answers as with c first (nil: none of the
// cards may answer): a door card's UID, or ISO-DEP with c's AIDs ahead of
// every other app card that may answer (an AID two cards share goes to
// the first). A reader of no known kind gets both, as from a
// dual-interface card: the first door card's UID, and ISO-DEP whenever an
// app has cards, so that a reader selecting one that may not answer now
// is told so and the user why. door is the card whose UID it is.
func (e *engine) identity(c *card, hint byte, cands []*card) (door *card, id *nfc.Identity) {
	switch {
	case c != nil && c.Kind == kindDoor:
		door = c
	case hint == 0:
		for _, x := range cands {
			if x.Kind == kindDoor {
				door = x
				break
			}
		}
	}
	hce := c != nil && c.Kind != kindDoor || hint == 0 && len(e.apps) > 0 && e.st.Enabled
	if door == nil && !hce {
		return nil, nil
	}
	id = &nfc.Identity{HCE: hce}
	if door != nil {
		id.UID = door.UID
	}
	if !hce {
		return door, id
	}
	seen := map[string]bool{}
	add := func(x *card) {
		for _, a := range x.AIDs {
			if k := hex.EncodeToString(a); !seen[k] {
				seen[k] = true
				id.AIDs = append(id.AIDs, a)
			}
		}
	}
	if c != nil && c.Kind != kindDoor {
		add(c)
	}
	for _, x := range e.eligible() {
		if x.Kind != kindDoor {
			add(x)
		}
	}
	return door, id
}

// owner is the card a SELECT reaches: the presented card's, else the
// first card that may answer with that AID.
func (e *engine) owner(aid []byte) *card {
	has := func(c *card) bool {
		for _, a := range c.AIDs {
			if bytes.Equal(a, aid) {
				return true
			}
		}
		return false
	}
	if c := e.f.card; c != nil && c.Kind != kindDoor && has(c) {
		return c
	}
	for _, c := range e.eligible() {
		if c.Kind != kindDoor && has(c) {
			return c
		}
	}
	for _, c := range e.ordered() {
		if c.Kind != kindDoor && has(c) {
			e.f.blocked = c
			break
		}
	}
	return nil
}

func (e *engine) apdu(cmd []byte) {
	f := e.f
	if f == nil || f.ident == nil || !f.ident.HCE {
		_ = e.ctl.Respond(nfc.SWUnknown)
		return
	}
	if aid := nfc.SelectAID(cmd); aid != nil {
		owner := e.owner(aid)
		if f.selected != nil && (owner == nil || owner.ID != f.selected.ID) {
			e.leave(f, ui.NFCDeactivatedDeselected)
			f.left = false
		}
		if owner == nil {
			f.selected, f.lastSW = nil, 0x6A82
			f.exchanges++
			_ = e.ctl.Respond(nfc.SWNotFound)
			return
		}
		f.selected, f.aid = owner, append([]byte(nil), aid...)
	}
	if f.selected == nil {
		f.lastSW = 0x6F00
		f.exchanges++
		_ = e.ctl.Respond(nfc.SWUnknown)
		return
	}
	f.waiting = true
	e.emit(svc.NFCEvent{Event: svc.NFCEventAPDU, Session: f.session, App: f.selected.App, Card: f.selected.Card,
		AID: strings.ToUpper(hex.EncodeToString(f.aid)), APDU: strings.ToUpper(hex.EncodeToString(cmd))})
}

// leave tells the app whose card the reader's commands went to that the
// reader is done with it, once per selection.
func (e *engine) leave(f *field, reason string) {
	if f.selected == nil || f.left {
		return
	}
	f.left = true
	e.emit(svc.NFCEvent{Event: svc.NFCEventDeactivated, Session: f.session, App: f.selected.App, Card: f.selected.Card, Reason: reason})
}

func (e *engine) reply(session, app, apdu string) error {
	f := e.f
	if f == nil || f.session != session || !f.waiting {
		return errors.New("no command of this session is waiting (the reader may have gone)")
	}
	if f.selected == nil || f.selected.App != app {
		return errors.New("the command is not for this app")
	}
	resp, err := hex.DecodeString(apdu)
	if err != nil || len(resp) < 2 || len(resp) > 4096 {
		return errors.New("the answer is 2 to 4096 bytes in hex, ending with the status word")
	}
	f.waiting = false
	f.exchanges++
	f.lastSW = nfc.SW(resp)
	return e.ctl.Respond(resp)
}

// finish judges the field that ended. A reader that spoke no ISO-DEP read
// the door card's UID: it accepted it when it halted it and did not poll
// it again. Else an app's card was served when the last command was
// answered without an error.
func (e *engine) finish() {
	f := e.f
	e.last = nil
	if f == nil || !f.decided {
		return
	}
	e.last = f.card
	if f.selected == nil && f.blocked != nil {
		if !f.told {
			e.emit(svc.NFCEvent{Event: svc.NFCEventResult, Card: f.blocked.ID, Name: f.blocked.Name, Kind: f.blocked.Kind, Reason: e.usable(f.blocked)})
		}
		return
	}
	if f.exchanges == 0 && f.door != nil {
		e.last = f.door
		if f.activations == 0 {
			return
		}
		ok := f.activations == 1 && f.halted
		reason := ""
		if !ok {
			reason = "refused"
		}
		e.outcome(f, f.door, ok, reason)
		return
	}
	served := f.selected
	if served == nil {
		served = f.card
	}
	if served == nil {
		return
	}
	e.last = served
	reason := ""
	switch {
	case f.waiting:
		reason = "timeout"
	case f.exchanges == 0:
		reason = "no command"
	case !nfc.SWDone(f.lastSW):
		reason = fmt.Sprintf("status %04X", f.lastSW)
	}
	e.outcome(f, served, reason == "", reason)
}

func (e *engine) outcome(f *field, c *card, ok bool, reason string) {
	if ok {
		e.pending, e.failed, e.explicit = "", map[string]time.Time{}, ""
		if c.Kind == kindDoor {
			e.record(c.ID, f.sig)
		}
		e.emit(svc.NFCEvent{Event: svc.NFCEventResult, OK: true, Card: c.ID, Name: c.Name, Kind: c.Kind})
		return
	}
	now := e.now()
	e.failed[c.ID] = now
	if c.Kind != kindDoor && f.card != nil && f.card.Kind != kindDoor {
		e.failed[f.card.ID] = now
	}
	var next *card
	for _, x := range e.ranked(f.hint, f.sig) {
		switch {
		case c.Kind == kindDoor && x.Kind != kindDoor:
			// The app cards had their ISO-DEP already: a door wants another UID.
			continue
		case c.Kind != kindDoor && x.Kind != kindDoor && !shares(x, f.aid):
			// Every app card that may answer was there already: another
			// only helps when it shares the AID the reader selected.
			continue
		}
		next = x
		break
	}
	ev := svc.NFCEvent{Event: svc.NFCEventResult, Card: c.ID, Name: c.Name, Kind: c.Kind, Reason: reason}
	e.pending = ""
	if next != nil {
		e.pending, e.pendingUntil = next.ID, now.Add(failMemory)
		ev.Next, ev.NextName = next.ID, next.Name
		if e.explicit != "" && now.Before(e.explicitUntil) {
			e.explicit, e.explicitUntil = next.ID, now.Add(time.Minute)
		}
	}
	e.emit(ev)
}

func dates(ds []string) map[string]bool {
	out := map[string]bool{}
	for _, d := range ds {
		out[d] = true
	}
	return out
}

func shares(c *card, aid []byte) bool {
	for _, a := range c.AIDs {
		if aid != nil && bytes.Equal(a, aid) {
			return true
		}
	}
	return false
}

func (e *engine) setPolicy(p svc.NFCPolicyArgs) {
	e.pol, e.zoned = p, true
	e.daysOff, e.workdays = dates(p.DaysOff), dates(p.Workdays)
	e.apps = nil
	for _, a := range p.Cards {
		c := &card{ID: a.App + "/" + a.Card, Name: a.Name, Kind: a.Category, App: a.App, AppName: a.AppName, Card: a.Card,
			RequiresUnlock: a.RequiresUnlock, CanExpress: a.Express && a.Category == kindOther}
		if c.Kind != kindPayment && c.Kind != kindOther {
			continue
		}
		for _, s := range a.AIDs {
			if b, err := ui.ParseAID(s); err == nil {
				c.AIDs = append(c.AIDs, b)
			}
		}
		e.apps = append(e.apps, c)
	}
	e.redecide()
}

func (e *engine) present(id string, d time.Duration) error {
	if id == "" {
		e.explicit = ""
		e.redecide()
		return nil
	}
	c := e.find(id)
	if c == nil {
		return errors.New("no such card")
	}
	if why := e.usable(c); why != "" {
		return errors.New(why)
	}
	e.explicit, e.explicitUntil = c.ID, e.now().Add(min(max(d, time.Second), maxExplicit))
	e.redecide()
	return nil
}

func (e *engine) state() ui.NFCState {
	info := e.ctl.Info()
	s := ui.NFCState{Controller: info.Name, Simulated: info.Simulated, Observe: info.Observe, Enabled: e.st.Enabled,
		DefaultPayment: e.defaultPayment(), Express: e.st.Express, Foreground: e.st.Foreground, Cards: []ui.NFCCardInfo{},
		PaymentApps: []ui.NFCApp{}, DoorFirst: e.doorFirst()}
	if c := e.find(e.st.Express); c == nil || !c.CanExpress {
		s.Express = ""
	}
	now := e.clock()
	for _, c := range e.ordered() {
		ci := ui.NFCCardInfo{ID: c.ID, Name: c.Name, Kind: c.Kind, App: c.App, AppName: c.AppName, CanExpress: c.CanExpress,
			Locked: e.lockedOK(c), LockedFixed: lockedFixed(c)}
		if c.Kind == kindDoor && len(c.UID) > 0 {
			ci.UID = fmt.Sprintf("%X", c.UID[len(c.UID)-1:])
			ci.Usual = e.usual(c.ID, now)
		}
		s.Cards = append(s.Cards, ci)
		if c.Kind == kindPayment && (len(s.PaymentApps) == 0 || s.PaymentApps[len(s.PaymentApps)-1].ID != c.App) {
			s.PaymentApps = append(s.PaymentApps, ui.NFCApp{ID: c.App, Name: c.AppName})
		}
	}
	return s
}

func (e *engine) set(a ui.NFCSetArgs) error {
	switch a.Key {
	case ui.NFCSetCancelRead:
		e.cancelRead()
		return nil
	case ui.NFCSetAddDoor:
		return e.addRead(a.Name)
	case ui.NFCSetRename:
		return e.renameDoor(a.Card, a.Name)
	case ui.NFCSetDelete:
		return e.deleteDoor(a.Card)
	case ui.NFCSetEnabled:
		e.st.Enabled = a.On
	case ui.NFCSetForeground:
		e.st.Foreground = a.On
	case ui.NFCSetPayment:
		if a.App != "" && !e.hasPayment(a.App) {
			return errors.New("that app has no payment card")
		}
		e.st.DefaultPayment = a.App
	case ui.NFCSetExpress:
		if c := e.find(a.Card); a.Card != "" && (c == nil || !c.CanExpress) {
			return errors.New("that card cannot be an express transit card")
		}
		e.st.Express = a.Card
	case ui.NFCSetLocked:
		c := e.find(a.Card)
		if c == nil || lockedFixed(c) {
			return errors.New("that card cannot answer while the phone is locked")
		}
		if e.st.Locked == nil {
			e.st.Locked = map[string]bool{}
		}
		e.st.Locked[c.ID] = a.On
	case ui.NFCSetMove:
		all := e.ordered()
		i := -1
		for k, c := range all {
			if c.ID == a.Card {
				i = k
			}
		}
		j := i + a.By
		if i < 0 || j < 0 || j >= len(all) || (a.By != 1 && a.By != -1) {
			return errors.New("the card cannot move there")
		}
		all[i], all[j] = all[j], all[i]
		e.st.Order = e.st.Order[:0]
		for _, c := range all {
			e.st.Order = append(e.st.Order, c.ID)
		}
	default:
		return fmt.Errorf("unknown key %q", a.Key)
	}
	e.save()
	e.redecide()
	return nil
}

// readDoor is the card pack's reader mode, the controller polling outside
// the engine's lock until a card answers, d is up or cancel_read: a door
// card that can be copied waits for add_door.
func (e *engine) readDoor(d time.Duration) ui.NFCRead {
	e.mu.Lock()
	r, err := e.readerBegin("")
	e.mu.Unlock()
	if err != nil {
		return ui.NFCRead{Result: ui.NFCReadNone}
	}
	ctx, cancel := context.WithTimeout(r.ctx, d)
	t, err := e.ctl.Read(ctx)
	cancel()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.readerEnd(r, "done")
	e.read = nil
	uid := strings.ToUpper(hex.EncodeToString(t.UID))
	switch {
	case err != nil || !uidSize(len(t.UID)):
		return ui.NFCRead{Result: ui.NFCReadNone}
	case t.ISODep:
		return ui.NFCRead{Result: ui.NFCReadSmart}
	case t.Encrypted:
		return ui.NFCRead{Result: ui.NFCReadEncrypted}
	}
	for _, d := range e.st.Doors {
		if d.UID == uid {
			return ui.NFCRead{Result: ui.NFCReadDuplicate, Name: d.Name}
		}
	}
	e.read = &readCard{uid: uid, until: e.now().Add(readKeep)}
	return ui.NFCRead{Result: ui.NFCReadOK, UID: uid[len(uid)-2:]}
}

// uidSize: an ISO 14443-3A UID (NFCID1) is 4, 7 or 10 bytes.
func uidSize(n int) bool { return n == 4 || n == 7 || n == 10 }

func (e *engine) addRead(name string) error {
	r := e.read
	if r == nil || e.now().After(r.until) {
		return errors.New("no card was read (read it first)")
	}
	if _, err := e.addDoor(name, r.uid); err != nil {
		return err
	}
	e.read = nil
	return nil
}

func cardName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n == 0 || n > 24 || strings.ContainsAny(s, "\n\t") {
		return "", errors.New("a name of 1 to 24 characters")
	}
	return s, nil
}

func (e *engine) addDoor(name, uid string) (ui.NFCCardInfo, error) {
	name, err := cardName(name)
	if err != nil {
		return ui.NFCCardInfo{}, err
	}
	b, err := hex.DecodeString(uid)
	if err != nil || !uidSize(len(b)) {
		return ui.NFCCardInfo{}, errors.New("a UID of 4, 7 or 10 bytes")
	}
	for _, d := range e.st.Doors {
		if strings.EqualFold(d.UID, uid) {
			return ui.NFCCardInfo{}, fmt.Errorf("already in the card pack as %q", d.Name)
		}
	}
	e.st.NextDoor++
	d := door{ID: fmt.Sprintf("door:%d", e.st.NextDoor), Name: name, UID: strings.ToUpper(uid)}
	e.st.Doors = append(e.st.Doors, d)
	e.save()
	e.redecide()
	return ui.NFCCardInfo{ID: d.ID, Name: d.Name, Kind: kindDoor, UID: d.UID[len(d.UID)-2:]}, nil
}

func (e *engine) renameDoor(id, name string) error {
	name, err := cardName(name)
	if err != nil {
		return err
	}
	for i := range e.st.Doors {
		if e.st.Doors[i].ID == id {
			e.st.Doors[i].Name = name
			e.save()
			return nil
		}
	}
	return errors.New("no such door card")
}

func (e *engine) deleteDoor(id string) error {
	for i, d := range e.st.Doors {
		if d.ID != id {
			continue
		}
		e.st.Doors = append(e.st.Doors[:i], e.st.Doors[i+1:]...)
		taps := e.st.Taps[:0]
		for _, t := range e.st.Taps {
			if t.Card != id {
				taps = append(taps, t)
			}
		}
		e.st.Taps = taps
		readers := e.st.Readers[:0]
		for _, r := range e.st.Readers {
			if r.Card != id {
				readers = append(readers, r)
			}
		}
		e.st.Readers = readers
		delete(e.st.Locked, id)
		if e.explicit == id {
			e.explicit = ""
		}
		if e.pending == id {
			e.pending = ""
		}
		e.save()
		e.redecide()
		return nil
	}
	return errors.New("no such door card")
}
