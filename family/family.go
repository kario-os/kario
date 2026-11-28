// Package family is kario-family/1, a parent's remote management of a
// phone in minor mode (docs/19-core-apps.md §1.2): the phone pairs with the
// Kario account server by a one-time code a parent enters on the server's
// web page, the server signs the rules the parent sets there (Policy), and
// the phone checks each against the server's key before minor mode takes
// it. What the phone sends back is what the parent's page shows and no
// more: today's screen time by app, the rules in force, and its location
// once when the parent asks. A child held back by a limit or downtime may
// ask the parent for more time (Ask), which the parent gives as a signed
// Grant; a parent who forgot the parental PIN confirms a signed Reset.
//
// family.go is what the server and the phone share: the signed policy,
// grant and reset, and the messages; client.go is the phone's side of the
// HTTP API; package familytest is a server in memory for tests and the
// selftest.
package family

import (
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kario-os/kario/keyset"
)

const (
	// PushApp is the push channel the server tells the phone on: the Shell
	// takes it itself, and no installable app can be named under system.
	PushApp = "system.family"
	// CodeLen digits make a pairing code, good for CodeTTL and once.
	CodeLen = 8
	CodeTTL = 10 * time.Minute
	// PolicyTTL is how long a signed policy may be taken after it was
	// issued; MaxTTL the longest a phone accepts.
	PolicyTTL = 14 * 24 * time.Hour
	MaxTTL    = 31 * 24 * time.Hour
	// LocateTTL: a location request older than this is not answered.
	LocateTTL = 2 * time.Minute
	// LocationKeep is the longest the server keeps a location.
	LocationKeep = 24 * time.Hour

	MaxContacts = 100
	MaxApps     = 300
	MaxLimit    = 24 * 60
	maxName     = 64
	maxNumber   = 32

	// A request for more time may be answered for AskTTL, and never after
	// the end of the phone's day; a phone sends at most AsksPerHour an
	// hour; its note is at most MaxNote characters. A parent gives at most
	// MaxGrant minutes at once.
	AskTTL      = time.Hour
	AsksPerHour = 5
	MaxNote     = 60
	MaxGrant    = 4 * 60
	// GrantTTL is how long a grant may be taken after it was issued (on
	// the day it is for); MaxGrantTTL the longest a phone accepts.
	GrantTTL    = time.Hour
	MaxGrantTTL = 24 * time.Hour
	// A PIN reset waits ResetAskTTL for the parent to confirm it; the
	// signed reset is good for ResetTTL (a phone accepts at most
	// MaxResetTTL), within which the new PIN must be set. A phone asks at
	// most ResetsPerHour an hour.
	ResetAskTTL   = time.Hour
	ResetTTL      = 10 * time.Minute
	MaxResetTTL   = 15 * time.Minute
	ResetsPerHour = 3
)

// AskMinutes are what a child may ask for.
var AskMinutes = []int{15, 30, 60}

// TokenPrefix starts the bearer token a phone gets when it pairs.
const TokenPrefix = "kfd_"

// What a push on PushApp says (PushData.T); the push never carries the
// rules or anything about the child.
const (
	PushClaimed = "claimed" // a parent entered the code: the phone asks to confirm
	PushPolicy  = "policy"  // a newer policy (V) waits
	PushLocate  = "locate"  // the parent asks where the phone is (ID)
	PushReport  = "report"  // the parent asks for today's screen time again
	PushUnbound = "unbound" // the parent ended the binding
	PushAnswer  = "answer"  // the parent answered a request for more time (ID)
	PushReset   = "reset"   // the parent confirmed a parental PIN reset (ID)
)

type PushData struct {
	T  string `json:"t"`
	V  int    `json:"v,omitempty"`
	ID string `json:"id,omitempty"`
}

// A binding's states (State.State).
const (
	StatePairing = "pairing" // the code is shown, no parent yet
	StateClaimed = "claimed" // a parent entered it; the phone has not allowed it yet
	StateBound   = "bound"
)

// Why a phone gives no position (Location.Error).
const (
	LocOff         = "off"         // the user switched location off
	LocUnavailable = "unavailable" // nothing can place the phone
	LocNoFix       = "no_fix"      // no fix came in time, and none is known
)

// Contact is a number calls and messages may go to and come from, with
// the name the parent gave it.
type Contact struct {
	Name   string `json:"name"`
	Number string `json:"number"`
}

// Downtime is the daily stretch in minutes after midnight (Start == End:
// none) in which only Apps (and those never held back) open.
type Downtime struct {
	On    bool     `json:"on"`
	Start int      `json:"start"`
	End   int      `json:"end"`
	Apps  []string `json:"apps"`
}

// Rules are minor mode's: the contacts allowed, the apps allowed, daily
// limits in minutes by app, downtime.
type Rules struct {
	Contacts []Contact      `json:"contacts"`
	Apps     []string       `json:"apps"`
	Limits   map[string]int `json:"limits"`
	Downtime Downtime       `json:"downtime"`
}

// Policy is what the server signs: the rules for one phone (Device), the
// Version-th the parent saved, issued and good until Expires (Unix
// seconds), and the parent's account the phone shows as managing it.
type Policy struct {
	Device  string `json:"device"`
	Version int    `json:"version"`
	Issued  int64  `json:"issued"`
	Expires int64  `json:"expires"`
	Parent  string `json:"parent"`
	Rules
}

// Signed is a policy as the server hands it out: the exact bytes it
// signed and the Ed25519 signature over Message of them, both base64, and
// the ID of the key that signed (keyset.ID; none from a server before key
// IDs).
type Signed struct {
	Policy string `json:"policy"`
	Sig    string `json:"sig"`
	Key    string `json:"key,omitempty"`
}

// Message is what is signed for a policy's bytes; a grant and a reset
// are signed under prefixes of their own (GrantMessage, ResetMessage), so
// no signature passes for another kind.
func Message(payload []byte) []byte {
	return append([]byte("kario-family/1\n"), payload...)
}

func GrantMessage(payload []byte) []byte {
	return append([]byte("kario-family-grant/1\n"), payload...)
}

func ResetMessage(payload []byte) []byte {
	return append([]byte("kario-family-reset/1\n"), payload...)
}

// seal is v's JSON and the signature over msg of it, both base64, and the
// signing key's ID. key is an ed25519.PrivateKey or a crypto.Signer
// holding one (an HSM's).
func seal(key crypto.Signer, msg func([]byte) []byte, v any) (payload, sig, id string, err error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", "", "", err
	}
	s, id, err := keyset.Sign(key, msg(raw))
	if err != nil {
		return "", "", "", fmt.Errorf("family: the policy key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(s), id, nil
}

// open is the bytes of payload when sig is over msg of them by the key of
// keys that id names (by any of them without an ID).
func open(keys keyset.Set, msg func([]byte) []byte, payload, sig, id string, v any) error {
	raw, err1 := base64.StdEncoding.DecodeString(payload)
	s, err2 := base64.StdEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil {
		return ErrSignature
	}
	if err := keys.Verify(id, msg(raw), s); err != nil {
		return fmt.Errorf("%w: %w", ErrSignature, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("family: the signed message does not parse: %w", err)
	}
	return nil
}

// within: issued..expires is a span of at most longest, not over at now.
func within(issued, expires int64, longest time.Duration, now time.Time) bool {
	return expires > now.Unix() && expires >= issued && expires-issued <= int64(longest/time.Second)
}

var appIDRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_-]+)+$`)

func ValidApp(id string) bool { return len(id) <= 128 && appIDRE.MatchString(id) }

func text(s string, limit int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= limit && !strings.ContainsFunc(s, unicode.IsControl)
}

// Normalize trims the rules' text and drops repeated entries (the first
// stays), so what is signed and compared is stable.
func (r Rules) Normalize() Rules {
	out := Rules{Contacts: []Contact{}, Apps: []string{}, Limits: map[string]int{},
		Downtime: Downtime{On: r.Downtime.On, Start: r.Downtime.Start, End: r.Downtime.End, Apps: []string{}}}
	seen := map[string]bool{}
	for _, c := range r.Contacts {
		c.Name, c.Number = strings.TrimSpace(c.Name), strings.TrimSpace(c.Number)
		if !seen["tel:"+c.Number] {
			seen["tel:"+c.Number] = true
			out.Contacts = append(out.Contacts, c)
		}
	}
	add := func(list []string, id string) []string {
		if id = strings.TrimSpace(id); slices.Contains(list, id) {
			return list
		}
		return append(list, id)
	}
	for _, id := range r.Apps {
		out.Apps = add(out.Apps, id)
	}
	for _, id := range r.Downtime.Apps {
		out.Downtime.Apps = add(out.Downtime.Apps, id)
	}
	for id, m := range r.Limits {
		out.Limits[strings.TrimSpace(id)] = m
	}
	return out
}

// Check is whether the rules are within what a phone takes.
func (r Rules) Check() error {
	switch {
	case len(r.Contacts) > MaxContacts:
		return fmt.Errorf("at most %d contacts", MaxContacts)
	case len(r.Apps) > MaxApps || len(r.Downtime.Apps) > MaxApps || len(r.Limits) > MaxApps:
		return fmt.Errorf("at most %d apps", MaxApps)
	case r.Downtime.Start < 0 || r.Downtime.Start >= 24*60 || r.Downtime.End < 0 || r.Downtime.End >= 24*60:
		return errors.New("downtime: minutes after midnight, 0 to 1439")
	}
	for _, c := range r.Contacts {
		if !text(c.Name, maxName) || !text(c.Number, maxNumber) || !strings.ContainsFunc(c.Number, unicode.IsDigit) {
			return fmt.Errorf("contact %q: a name of at most %d characters and a phone number", c.Name, maxName)
		}
	}
	for _, id := range append(slices.Clone(r.Apps), r.Downtime.Apps...) {
		if !ValidApp(id) {
			return fmt.Errorf("%q is not an app ID", id)
		}
	}
	for id, m := range r.Limits {
		if !ValidApp(id) || m < 1 || m > MaxLimit {
			return fmt.Errorf("limit of %q: 1 to %d minutes a day", id, MaxLimit)
		}
	}
	return nil
}

// Sign signs p with the server's key (an ed25519.PrivateKey, or a
// crypto.Signer holding one).
func Sign(key crypto.Signer, p Policy) (Signed, error) {
	p.Rules = p.Rules.Normalize()
	if err := p.Rules.Check(); err != nil {
		return Signed{}, err
	}
	payload, sig, id, err := seal(key, Message, p)
	return Signed{Policy: payload, Sig: sig, Key: id}, err
}

var (
	ErrSignature = errors.New("family: the signature does not match the account server's key")
	ErrDevice    = errors.New("family: it is for another phone")
	ErrReplay    = errors.New("family: the policy is not newer than the one in force")
	ErrExpired   = errors.New("family: it has expired")
	ErrDate      = errors.New("family: the grant is for another day")
)

// Verify checks a signed policy for the phone device, whose policy in
// force has version after, at now: the server's signature by one of keys,
// this phone, a newer version, not expired, rules a phone takes.
func Verify(keys keyset.Set, s Signed, device string, after int, now time.Time) (Policy, error) {
	var p Policy
	if err := open(keys, Message, s.Policy, s.Sig, s.Key, &p); err != nil {
		return Policy{}, err
	}
	switch {
	case p.Device != device:
		return Policy{}, ErrDevice
	case p.Version <= after:
		return Policy{}, ErrReplay
	case !within(p.Issued, p.Expires, MaxTTL, now):
		return Policy{}, ErrExpired
	}
	if err := p.Rules.Check(); err != nil {
		return Policy{}, fmt.Errorf("family: %w", err)
	}
	p.Rules = p.Rules.Normalize()
	return p, nil
}

// Pair is what a phone asks to pair with: its push token on PushApp ("" for
// none: it then looks every so often).
type Pair struct {
	PushToken string `json:"push_token,omitempty"`
}

// Paired is the server's answer: the phone's ID and bearer token (shown
// once), the code to show and how long it holds.
type Paired struct {
	Device    string `json:"device"`
	Token     string `json:"token"`
	Code      string `json:"code"`
	ExpiresIn int    `json:"expires_in"`
}

// State is a binding as the phone sees it: the parent's account once one
// claimed the code, the latest policy's version, a location request not
// answered yet, whether the parent asked for a fresh report, the phone's
// requests for more time the parent answered (today's), and the PIN reset
// the parent confirmed.
type State struct {
	State    string   `json:"state"`
	Parent   string   `json:"parent,omitempty"`
	Version  int      `json:"version,omitempty"`
	Locate   string   `json:"locate,omitempty"`
	Report   bool     `json:"report,omitempty"`
	Answered []string `json:"answered,omitempty"`
	Reset    string   `json:"reset,omitempty"`
}

// Confirm is the phone's answer to the parent who entered the code: Allow
// after the parental PIN, and the consent text's version it showed.
type Confirm struct {
	Allow   bool `json:"allow"`
	Consent int  `json:"consent"`
}

// ConsentVersion is the version of what the phone tells the child and the
// parent at binding (docs/19 §1.2): a change to it is a new version.
const ConsentVersion = 1

// AppUse is an app on the phone and its time on the screen today; Fixed:
// always allowed and never limited (the phone, Settings).
type AppUse struct {
	App   string `json:"app"`
	Name  string `json:"name"`
	MS    int64  `json:"ms"`
	Fixed bool   `json:"fixed,omitempty"`
}

// Report is what the parent's page shows of the phone: its apps and their
// time today (until the end of the phone's day, Until), the rules in force
// and the policy version they came from, and when they were last changed
// on the phone with the parental PIN (Local, 0: not since that policy).
type Report struct {
	At        int64    `json:"at"`
	Until     int64    `json:"until"`
	Version   int      `json:"version"`
	Local     int64    `json:"local,omitempty"`
	Apps      []AppUse `json:"apps"`
	Rules     Rules    `json:"rules"`
	PushToken string   `json:"push_token,omitempty"`
}

// Check bounds a report.
func (r Report) Check() error {
	if len(r.Apps) > MaxApps {
		return fmt.Errorf("at most %d apps", MaxApps)
	}
	for _, a := range r.Apps {
		if !ValidApp(a.App) || !text(a.Name, maxName) || a.MS < 0 || a.MS > 24*3600*1000 {
			return fmt.Errorf("app %q: an ID, a name and today's milliseconds", a.App)
		}
	}
	if r.Until < r.At || r.Until-r.At > int64(LocationKeep/time.Second) {
		return errors.New("until: the end of the phone's day, within a day")
	}
	return r.Rules.Check()
}

// Location answers one request: a position or why there is none.
type Location struct {
	Request   string  `json:"request"`
	Lat       float64 `json:"lat,omitempty"`
	Lon       float64 `json:"lon,omitempty"`
	AccuracyM float64 `json:"accuracy_m,omitempty"`
	At        int64   `json:"at,omitempty"`
	Source    string  `json:"source,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// Check bounds an answer.
func (l Location) Check() error {
	switch {
	case l.Request == "" || len(l.Request) > 64:
		return errors.New("location: which request it answers")
	case l.Error != "" && l.Error != LocOff && l.Error != LocUnavailable && l.Error != LocNoFix:
		return fmt.Errorf("location: unknown error %q", l.Error)
	case l.Error == "" && (l.Lat < -90 || l.Lat > 90 || l.Lon < -180 || l.Lon > 180 || l.AccuracyM < 0 || l.At <= 0):
		return errors.New("location: a position, its accuracy and its time")
	case len(l.Source) > 16:
		return errors.New("location: source")
	}
	return nil
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// ValidID: a request's or a reset's ID as the phone makes them (random).
func ValidID(id string) bool { return idRE.MatchString(id) }

func validDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// Why an app was held back when the child asked (Ask.Why).
const (
	WhyLimit    = "limit"
	WhyDowntime = "downtime"
)

// Ask is a child's request for more time with an app today, from the
// system's screen that held the app back: its ID (the phone's, random),
// the app and its name, the minutes (one of AskMinutes), a note for the
// parent, why the app was held back, and the phone's date and the end of
// that day (Unix).
type Ask struct {
	ID      string `json:"id"`
	App     string `json:"app"`
	Name    string `json:"name"`
	Minutes int    `json:"minutes"`
	Note    string `json:"note,omitempty"`
	Why     string `json:"why"`
	Date    string `json:"date"`
	Until   int64  `json:"until"`
}

// Check bounds a request received at now.
func (a Ask) Check(now time.Time) error {
	switch {
	case !ValidID(a.ID):
		return errors.New("ask: an ID of 8 to 64 letters, digits, - and _")
	case !ValidApp(a.App) || !text(a.Name, maxName):
		return errors.New("ask: an app ID and its name")
	case !slices.Contains(AskMinutes, a.Minutes):
		return fmt.Errorf("ask: minutes, one of %v", AskMinutes)
	case !text(a.Note, MaxNote):
		return fmt.Errorf("ask: a note of at most %d characters", MaxNote)
	case a.Why != WhyLimit && a.Why != WhyDowntime:
		return errors.New("ask: why, limit or downtime")
	case !validDate(a.Date) || a.Until <= now.Unix() || a.Until-now.Unix() > int64(LocationKeep/time.Second):
		return errors.New("ask: the phone's date and the end of its day, within a day")
	}
	return nil
}

// A request's states (AskState.State).
const (
	AskPending = "pending"
	AskGranted = "granted"
	AskRefused = "refused"
	AskExpired = "expired"
)

// AskState is a request as the server holds it: pending, granted (the
// minutes the parent gave, and the grant), refused, or expired unanswered.
type AskState struct {
	ID      string       `json:"id"`
	State   string       `json:"state"`
	Minutes int          `json:"minutes,omitempty"`
	Grant   *SignedGrant `json:"grant,omitempty"`
}

// Grant is what the server signs when a parent gives more time: Minutes
// more with App on Date (the phone's day the request was for), answering
// the phone's request Request, issued and good until Expires (Unix
// seconds).
type Grant struct {
	Device  string `json:"device"`
	Request string `json:"request"`
	App     string `json:"app"`
	Date    string `json:"date"`
	Minutes int    `json:"minutes"`
	Issued  int64  `json:"issued"`
	Expires int64  `json:"expires"`
}

// SignedGrant is a grant as the server hands it out, as Signed is a policy.
type SignedGrant struct {
	Grant string `json:"grant"`
	Sig   string `json:"sig"`
	Key   string `json:"key,omitempty"`
}

func (g Grant) check() error {
	if !ValidID(g.Request) || !ValidApp(g.App) || !validDate(g.Date) || g.Minutes < 1 || g.Minutes > MaxGrant {
		return fmt.Errorf("grant: a request, an app, a date and 1 to %d minutes", MaxGrant)
	}
	return nil
}

func SignGrant(key crypto.Signer, g Grant) (SignedGrant, error) {
	if err := g.check(); err != nil {
		return SignedGrant{}, err
	}
	payload, sig, id, err := seal(key, GrantMessage, g)
	return SignedGrant{Grant: payload, Sig: sig, Key: id}, err
}

// VerifyGrant checks a signed grant for the phone device on its day today,
// at now: the server's signature (as a grant), this phone, today, not
// expired. Which request it answers, and that it is taken once, the phone
// checks against the requests it sent.
func VerifyGrant(keys keyset.Set, s SignedGrant, device, today string, now time.Time) (Grant, error) {
	var g Grant
	if err := open(keys, GrantMessage, s.Grant, s.Sig, s.Key, &g); err != nil {
		return Grant{}, err
	}
	switch {
	case g.Device != device:
		return Grant{}, ErrDevice
	case g.Date != today:
		return Grant{}, ErrDate
	case !within(g.Issued, g.Expires, MaxGrantTTL, now):
		return Grant{}, ErrExpired
	}
	if err := g.check(); err != nil {
		return Grant{}, fmt.Errorf("family: %w", err)
	}
	return g, nil
}

// ResetAsk names a parental PIN reset the phone asks for (its ID, random)
// or reports done.
type ResetAsk struct {
	ID string `json:"id"`
}

// Reset is what the server signs when the parent, freshly signed in,
// confirms the phone's PIN reset ID: good from Issued until Expires (Unix
// seconds), once.
type Reset struct {
	Device  string `json:"device"`
	ID      string `json:"id"`
	Issued  int64  `json:"issued"`
	Expires int64  `json:"expires"`
}

type SignedReset struct {
	Reset string `json:"reset"`
	Sig   string `json:"sig"`
	Key   string `json:"key,omitempty"`
}

func SignReset(key crypto.Signer, r Reset) (SignedReset, error) {
	if !ValidID(r.ID) {
		return SignedReset{}, errors.New("reset: an ID")
	}
	payload, sig, id, err := seal(key, ResetMessage, r)
	return SignedReset{Reset: payload, Sig: sig, Key: id}, err
}

// VerifyReset checks a signed reset for the phone device at now: the
// server's signature (as a reset), this phone, not expired and good for
// at most MaxResetTTL. Which reset it confirms, and that it is used once,
// the phone checks against the one it asked for.
func VerifyReset(keys keyset.Set, s SignedReset, device string, now time.Time) (Reset, error) {
	var r Reset
	if err := open(keys, ResetMessage, s.Reset, s.Sig, s.Key, &r); err != nil {
		return Reset{}, err
	}
	switch {
	case r.Device != device:
		return Reset{}, ErrDevice
	case !within(r.Issued, r.Expires, MaxResetTTL, now):
		return Reset{}, ErrExpired
	case !ValidID(r.ID):
		return Reset{}, errors.New("family: the reset has no ID")
	}
	return r, nil
}
