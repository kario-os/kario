package shell

import (
	"strings"
	"testing"
	"time"
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
	a.input = inputBarrier{up: true, since: r.c.now(), held: []Key{{Name: KeyEnter, At: time.Now()}}}
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
	a.input = inputBarrier{up: true, since: r.c.now().Add(-inputHoldMax)}
	r.c.Key(Named(KeyDown))
	if a.input.up || len(a.input.held) != 0 {
		t.Fatalf("a key after inputHoldMax was held: %+v", a.input)
	}
}
