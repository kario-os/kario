// Package ucm keeps a phone's sound card in the set-up its ALSA UCM
// profile describes for what sounds now: media, or a call on the
// earpiece or the speaker. phone-audiod uses it on phones whose card is
// routed by a UCM profile (the OnePlus 6T, 39-oneplus-6t.md); elsewhere
// the card needs no set-up and audiod has no Router.
package ucm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/ui"
)

// Setup is a use case verb, the devices enabled in it and the PCM that
// plays to them ("" for the sink's own).
type Setup struct {
	Verb    string   `json:"verb"`
	Devices []string `json:"devices"`
	PCM     string   `json:"pcm,omitempty"`
}

func (s Setup) same(o Setup) bool {
	return s.Verb == o.Verb && s.PCM == o.PCM && slices.Equal(s.Devices, o.Devices)
}

// Tool runs alsaucm on the card with the commands given ("set", "_verb",
// "HiFi", ...), all in one process.
type Tool interface {
	Run(cmds []string) error
}

// Alsaucm is alsa-utils' alsaucm.
type Alsaucm struct {
	Card string // hw:0
	Path string // "alsaucm", looked up in PATH, when empty
}

func (a Alsaucm) Run(cmds []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := append([]string{"-c", a.Card}, cmds...)
	out, err := exec.CommandContext(ctx, cmp.Or(a.Path, "alsaucm"), args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("alsaucm %q: %v: %s", args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Moves are the commands that take the card from one set-up to another.
// alsaucm keeps nothing between runs and UCM reads nothing back from the
// card, so a run first sets up from again (its controls to the values
// they already have) to tell UCM what to tear down. New devices are
// enabled before old ones are disabled: a device the profile lacks then
// leaves the old one sounding.
func Moves(from, to Setup) []string {
	var c []string
	if from.Verb != "" {
		c = append(c, "set", "_verb", from.Verb)
		for _, d := range from.Devices {
			c = append(c, "set", "_enadev", d)
		}
	}
	if from.Verb != to.Verb {
		c = append(c, "set", "_verb", to.Verb)
		for _, d := range to.Devices {
			c = append(c, "set", "_enadev", d)
		}
		return c
	}
	for _, d := range to.Devices {
		if !slices.Contains(from.Devices, d) {
			c = append(c, "set", "_enadev", d)
		}
	}
	for _, d := range from.Devices {
		if !slices.Contains(to.Devices, d) {
			c = append(c, "set", "_disdev", d)
		}
	}
	return c
}

// Router keeps the card in the set-up for what sounds. Without a call
// that is Media. While telephonyd has a call being set up, connected or
// held it is Earpiece, or Speaker once the call screen asks for it, and
// without the Mic device while the call is muted. When the last call
// ends the card goes back to Media, and the next call starts on the
// earpiece.
//
// The set-up and the speaker choice outlive audiod in State, a file in
// the run directory (gone at boot): a restarted Router takes the card as
// it is and changes it only once telephonyd has said what calls there
// are.
type Router struct {
	Tool     Tool
	Media    Setup
	Earpiece Setup  // Verb "": calls do not change the set-up
	Speaker  Setup  // Verb "": calls stay on the earpiece
	Mic      string // the device left out while the call is muted
	State    string

	mu      sync.Mutex
	cur     Setup // Verb "": not set up since boot
	calls   map[string]ui.TelCall
	known   bool
	inCall  bool
	speaker bool
	pcm     atomic.Value
}

type state struct {
	Setup   Setup `json:"setup"`
	InCall  bool  `json:"in_call"`
	Speaker bool  `json:"speaker"`
}

// Start takes the card over: as the last run left it, or, the first
// time since boot, through the profile's boot sequence (its volumes;
// nothing else on the image runs it) into Media.
func (r *Router) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.known = r.Earpiece.Verb == ""
	if raw, err := os.ReadFile(r.State); err == nil {
		var st state
		if json.Unmarshal(raw, &st) == nil && st.Setup.Verb != "" {
			r.cur, r.inCall, r.speaker = st.Setup, st.InCall, st.Speaker
			r.pcm.Store(st.Setup.PCM)
			return r.apply()
		}
	}
	var boot error
	if err := r.Tool.Run([]string{"set", "_boot", ""}); err != nil {
		boot = fmt.Errorf("boot sequence: %w", err)
	}
	err := r.move(r.Media)
	r.save()
	return errors.Join(boot, err)
}

// SetCalls is telephonyd's list of calls, on each connection to it.
func (r *Router) SetCalls(calls []ui.TelCall) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = map[string]ui.TelCall{}
	for _, c := range calls {
		if c.State != "ended" {
			r.calls[c.ID] = c
		}
	}
	r.known = true
	return r.apply()
}

// Call is a change of one call.
func (r *Router) Call(c ui.TelCall) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string]ui.TelCall{}
	}
	if c.State == "ended" {
		delete(r.calls, c.ID)
	} else {
		r.calls[c.ID] = c
	}
	return r.apply()
}

// SetRoute is audio.route: ui.RouteEarpiece or ui.RouteSpeaker.
func (r *Router) SetRoute(route string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.speaker = route == ui.RouteSpeaker
	return r.apply()
}

// PCM is what the mixer plays to in the card's set-up ("" the sink's
// own); safe to call from the output loop.
func (r *Router) PCM() string {
	s, _ := r.pcm.Load().(string)
	return s
}

// Setup is the set-up the card is in.
func (r *Router) Setup() Setup {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur
}

// talking: a call in these states carries sound (q6voiced opens the
// voice path for them); a held call keeps the call set-up without.
func talking(state string) bool {
	return state == "dialing" || state == "alerting" || state == "active"
}

// muted: every call that carries sound is muted.
func (r *Router) muted() bool {
	some := false
	for _, c := range r.calls {
		if talking(c.State) {
			if !c.Muted {
				return false
			}
			some = true
		}
	}
	return some
}

func (r *Router) want() Setup {
	if !r.inCall || r.Earpiece.Verb == "" {
		return r.Media
	}
	s := r.Earpiece
	if r.speaker && r.Speaker.Verb != "" {
		s = r.Speaker
	}
	if r.Mic != "" && r.muted() {
		s.Devices = slices.DeleteFunc(slices.Clone(s.Devices), func(d string) bool { return d == r.Mic })
	}
	return s
}

// apply moves the card to the set-up for the calls as they are; mu held.
func (r *Router) apply() error {
	defer r.save()
	if !r.known {
		return nil
	}
	in := false
	for _, c := range r.calls {
		in = in || talking(c.State) || c.State == "held"
	}
	if r.inCall && !in {
		r.speaker = false
	}
	r.inCall = in
	err := r.move(r.want())
	if err != nil && r.inCall && r.speaker {
		r.speaker = false
		err = errors.Join(err, r.move(r.want()))
	}
	return err
}

func (r *Router) move(to Setup) error {
	if to.same(r.cur) {
		return nil
	}
	if err := r.Tool.Run(Moves(r.cur, to)); err != nil {
		return err
	}
	r.cur = to
	r.pcm.Store(to.PCM)
	return nil
}

func (r *Router) save() {
	if r.State == "" {
		return
	}
	raw, _ := json.Marshal(state{Setup: r.cur, InCall: r.inCall, Speaker: r.speaker})
	tmp := r.State + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, r.State)
	}
}
