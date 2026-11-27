package shell

import (
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

func slowChoiceRig(t *testing.T) (*hostileRig, *App, func() string) {
	r := newHostileRig(t, map[string]string{"test.slowchoice": ""})
	a := r.launch("test.slowchoice")
	pages := func() string {
		var ids []string
		for _, v := range a.stack {
			ids = append(ids, v.S.ID+":"+v.S.Title)
		}
		return strings.Join(ids, " ")
	}
	if !r.until(5*time.Second, func() bool { return pages() == "main:main" }) {
		t.Fatalf("pages %q, want main", pages())
	}
	r.c.Key(Named(KeyEnter))
	if !r.until(5*time.Second, func() bool { return pages() == "main:main detail:detail" }) {
		t.Fatalf("pages %q after opening an item, want main and detail", pages())
	}
	return r, a, pages
}

// Back right after a choice the app answers late: Back waits for the
// answer, so it leaves the page the choice was made on, and the answer
// ("replace the top page") does not take the place of the page below. The
// same with a request of the app's out that brings up no dialog (an SMS
// waiting for the network).
func TestKeysWaitForTheAction(t *testing.T) {
	if !alone(t) {
		return
	}
	for _, tc := range []struct {
		name    string
		waiting int
	}{{"idle", 0}, {"request out", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			r, a, pages := slowChoiceRig(t)
			a.waiting += tc.waiting
			r.c.Key(Named(KeyEnter))
			r.c.Key(Named(KeyEsc))
			if !r.until(5*time.Second, func() bool { return !a.input.up && len(a.input.held) == 0 }) {
				t.Fatal("the keys after the choice are still held")
			}
			r.pump(time.Second)
			if got := pages(); got != "main:main" {
				t.Fatalf("pages %q after choosing and going back at once, want main", got)
			}
		})
	}
}

// A request that may bring up a consent dialog lets the keys through: they
// are the dialog's.
func TestKeysGoToADialogRequest(t *testing.T) {
	if !alone(t) {
		return
	}
	r, a, _ := slowChoiceRig(t)
	a.waiting++
	a.guard.dialogs++
	r.c.Key(Named(KeyEnter))
	r.c.Key(Named(KeyDown))
	if a.input.up || len(a.input.held) != 0 {
		t.Fatalf("a key was held while a dialog request was out: %+v", a.input)
	}
}

// A dialog that asks for a decision takes no key pressed before it showed
// and no answer in its first overlayArmDelay; the arrows and Esc it takes at
// once, but not an Esc pressed before an incoming call rang.
func TestDialogAnswersAreSeen(t *testing.T) {
	if !alone(t) {
		return
	}
	r, a, _ := slowChoiceRig(t)
	var got []bool
	o := &permOverlay{app: a, perms: []string{"camera"}, yes: true, decide: func(ok bool) { got = append(got, ok) }}
	before := time.Now()
	r.c.pushOverlay(o)
	r.c.Key(Key{Name: KeyEnter, At: before})
	r.c.Key(Named(KeyEnter))
	if len(got) != 0 {
		t.Fatalf("an Enter from before the dialog or in its first moment answered it: %v", got)
	}
	r.c.Key(Named(KeyRight))
	if o.yes {
		t.Fatal("the arrow key did not move the choice at once")
	}
	r.c.Key(Named(KeyLeft))
	if !r.c.overlayTakes(o, Key{Name: KeyEsc, At: before}) {
		t.Fatal("Esc from before the dialog cannot deny")
	}
	r.pump(overlayArmDelay)
	r.c.Key(Named(KeyEnter))
	if len(got) != 1 || !got[0] {
		t.Fatalf("Enter after the delay: %v, want one allow", got)
	}

	call := &incomingOverlay{}
	before = time.Now()
	r.c.pushOverlay(call)
	if r.c.overlayTakes(call, Key{Name: KeyEsc, At: before}) {
		t.Fatal("an Esc pressed before the call rang declines it")
	}
	r.c.popOverlay(call)
}

// Keys held behind an action and replayed do not answer a dialog that came
// up meanwhile: they were pressed before it showed.
func TestHeldKeysDoNotAnswerADialog(t *testing.T) {
	if !alone(t) {
		return
	}
	r, a, _ := slowChoiceRig(t)
	decided := false
	a.input = inputBarrier{up: true, checked: time.Now(), held: []Key{{Name: KeyEnter, At: time.Now()}}}
	r.pump(10 * time.Millisecond)
	o := &confirmOverlay{text: "Send an SMS?", onYes: func() { decided = true }}
	r.c.pushOverlay(o)
	r.pump(overlayArmDelay)
	r.c.releaseInput(a)
	if decided {
		t.Fatal("a held Enter, replayed, confirmed a dialog that showed after it was pressed")
	}
	r.c.popOverlay(o)
}

// An app that never answers holds the keys after an action for at most
// inputHoldMax: then they go on.
func TestHeldKeysGoOnWithoutAnAnswer(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newHostileRig(t, map[string]string{"test.slowchoice": ""})
	a := r.launch("test.slowchoice")
	if !r.until(5*time.Second, func() bool { return a.top() != nil && a.top().S.ID == "main" }) {
		t.Fatal("no main page")
	}
	a.input = inputBarrier{up: true, own: inputHoldMax, checked: time.Now()}
	r.c.Key(Named(KeyDown))
	if a.input.up || len(a.input.held) != 0 {
		t.Fatalf("a key after inputHoldMax was held: %+v", a.input)
	}
}

// The keys after an action wait for its answer through a time the Shell's
// own loop did not run (a durable write waiting for a busy disk, a process
// starved of the CPU) when the answer needs the Shell's to a request of the
// app's first, as the dialer's page for digits typed on the idle screen
// needs its lists: they used to go on to the page before once inputHoldMax
// of the wall clock had passed, and the number lost its first digit.
func TestHeldKeysWaitThroughTheLoopsStall(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newHostileRig(t, map[string]string{"test.asks": ""})
	a := r.launch("test.asks")
	if !r.until(5*time.Second, func() bool { return a.top() != nil && a.top().S.ID == "main" }) {
		t.Fatal("no main page")
	}
	r.c.Key(Named(KeyEnter))
	time.Sleep(2 * inputHoldMax)
	r.c.Key(Named(KeyDown))
	if !r.until(5*time.Second, func() bool { return !a.input.up && len(a.input.held) == 0 && a.top().S.ID == "detail" }) {
		t.Fatalf("no detail page, or keys still held: %+v", a.input)
	}
	if main, detail := a.stack[0], a.top(); main.focus != "n0" || detail.focus != "n1" {
		t.Fatalf("focus %q on main, %q on detail; want ↓ on the detail page", main.focus, detail.focus)
	}
}

// Nor do they go on while the Shell rests the app's messages (guard.go),
// which is not the app's time either; but not for ever.
func TestHeldKeysWaitThroughARest(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newHostileRig(t, map[string]string{"test.deaf": ""})
	a := r.launch("test.deaf")
	if !r.until(5*time.Second, func() bool { return a.top() != nil && a.top().S.ID == "main" }) {
		t.Fatal("no main page")
	}
	a.heldUntil.Store(time.Now().Add(time.Hour).UnixNano())
	r.c.Key(Named(KeyEnter))
	r.pump(2 * inputHoldMax)
	r.c.Key(Named(KeyDown))
	if !a.input.up || len(a.input.held) != 1 {
		t.Fatalf("a key went on while the Shell rested the app: %+v", a.input)
	}
	if !r.until(10*time.Second, func() bool { return !a.input.up }) {
		t.Fatalf("keys still held after %s of the loop's time: %+v", a.input.loop, a.input)
	}
}

// The script's fill types where the keys before it lead: ↓ held behind an
// action the app answers late goes first, so the text lands in the next
// field, not in the one ↓ was pressed on.
func TestFillAfterHeldKeys(t *testing.T) {
	lt, apps := stCore(t)
	c, a := lt.c, apps["com.phone.calc"]
	c.setForeground(a)
	a.stack = []*View{newView(ui.Screen{ID: "form", Body: []ui.Node{{Kind: ui.KindInput, ID: "me"}, {Kind: ui.KindInput, ID: "who"}}})}
	r := &Runner{C: c, Out: io.Discard}
	if err := r.step("fill", "me@kario.test"); err != nil {
		t.Fatal(err)
	}
	c.actionSent(a)
	seq := strconv.Itoa(a.ping.seq)
	time.AfterFunc(150*time.Millisecond, func() { c.Post(func() { c.pong(a, seq) }) })
	if err := r.step("key", "down"); err != nil {
		t.Fatal(err)
	}
	if len(a.input.held) != 1 {
		t.Fatalf("↓ was not held behind the action: %+v", a.input)
	}
	if err := r.step("fill", "lilei@kario.test"); err != nil {
		t.Fatal(err)
	}
	if v := a.top(); v.focus != "who" || v.inputs["me"] != "me@kario.test" || v.inputs["who"] != "lilei@kario.test" {
		t.Fatalf("focus %s, fields %v; want lilei in who", v.focus, v.inputs)
	}
}
