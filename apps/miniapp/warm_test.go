package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// keepJS has two pages; the second counts presses and the times it was
// shown and hidden, and once hidden tries a toast and the scanner.
const keepJS = `
Page("index", {
  render: function () { return [ui.text("index")]; },
  onAction: function (e) { if (e.action === "next") phone.navigateTo("second"); },
});
Page("second", {
  data: { n: 0, hid: 0, shown: 0, err: "" },
  onShow: function () { this.setData({ shown: this.data.shown + 1 }); },
  onHide: function () {
    var self = this;
    self.data.hid++;
    setTimeout(function () {
      phone.toast("late toast");
      phone.scanCode({ fail: function (e) { self.setData({ err: e.errMsg }); } });
    }, 50);
  },
  render: function () {
    var d = this.data;
    return [ui.text("n " + d.n + " hid " + d.hid + " shown " + d.shown + " err " + d.err)];
  },
  onAction: function (e) { if (e.action === "inc") this.setData({ n: this.data.n + 1 }); },
});`

// back plays the user's back key: the Shell drops its top screen and says
// so.
func (s *shell) back() {
	s.t.Helper()
	s.mu.Lock()
	id := s.stack[len(s.stack)-1].ID
	s.stack = s.stack[:len(s.stack)-1]
	s.mu.Unlock()
	s.send(ui.Msg{T: ui.MsgPopped, ScreenID: id})
}

func (s *shell) warm(id string) bool {
	warm := false
	s.on(func(h *host) {
		if p := h.progByID(id); p != nil {
			warm = p.warm && p.run != nil
		}
	})
	return warm
}

// A closed program stays warm: its runtime runs on with its pages off the
// screen, showing nothing (a toast dropped, the scanner refused); opened
// again it is on the page it was on, with its state, the page told it was
// hidden and shown. Back on its last page closes it the same way.
func TestWarmReopen(t *testing.T) {
	t.Setenv("PHONE_LANG", "en")
	s := startHost(t, jsProgram(t, "keep", keepJS, "scan"))
	index := s.open("keep")
	s.act(index.ID, "", "next", "")
	second := s.pushed("mp:keep:second:")
	s.act(second.ID, "", "inc", "")
	s.page("n 1 hid 0 shown 1")
	pid := s.runtimePid("keep")

	s.act(second.ID, "", "mp.exit", "")
	s.waitDepth(1)
	if !s.warm("keep") || s.runtimePid("keep") != pid {
		t.Fatal("not kept warm")
	}
	time.Sleep(300 * time.Millisecond)
	if slices.ContainsFunc(s.toasts(), func(t string) bool { return strings.Contains(t, "late toast") }) {
		t.Error("a closed program's toast was shown")
	}
	s.mu.Lock()
	for _, m := range s.seen {
		if m.T == ui.MsgReq && m.Cap == ui.CapScanQR {
			t.Error("a closed program opened the scanner")
		}
	}
	s.mu.Unlock()

	s.open("keep")
	s.page("n 1 hid 1 shown 2 err " + errHidden)
	if s.depth() != 3 || s.top().ID != second.ID || s.runtimePid("keep") != pid || s.warm("keep") {
		t.Fatalf("reopened: %d screens, top %s, pid %d (was %d)", s.depth(), s.top().ID, s.runtimePid("keep"), pid)
	}
	s.act(second.ID, "", "inc", "")
	s.page("n 2 hid 1 shown 2")

	s.back()
	s.waitDepth(2)
	s.back()
	s.waitDepth(1)
	deadline := time.Now().Add(wait)
	for !s.warm("keep") {
		if time.Now().After(deadline) {
			t.Fatal("back on the last page did not keep it warm")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if again := s.open("keep"); again.ID != index.ID || s.runtimePid("keep") != pid {
		t.Fatalf("reopened on %s, pid %d", again.ID, s.runtimePid("keep"))
	}
}

// A warm program ends warmFor after it closed, and the one closed longest
// ago ends when more than warmMax are warm or they hold more than
// warmMemory; one whose runtime dies while warm goes without a word, and
// opens afresh.
func TestWarmLimits(t *testing.T) {
	t.Setenv("PHONE_LANG", "en")
	a, b := jsProgram(t, "a", keepJS), jsProgram(t, "b", keepJS)
	s := startHost(t, a, b)
	exit := func(id string) int {
		t.Helper()
		page := s.open(id)
		pid := s.runtimePid(id)
		s.act(page.ID, "", "mp.exit", "")
		s.waitDepth(1)
		return pid
	}

	s.on(func(h *host) { h.warmFor = 200 * time.Millisecond })
	pid := exit("a")
	gone(t, pid)
	if s.warm("a") || s.runtimePid("a") != 0 {
		t.Fatal("still warm past warmFor")
	}
	s.on(func(h *host) {
		if len(h.pages) != 0 {
			t.Errorf("pages left: %v", h.pages)
		}
	})

	s.on(func(h *host) { h.warmFor, h.warmMax = warmFor, 1 })
	pa := exit("a")
	pb := exit("b")
	gone(t, pa)
	if s.warm("a") || !s.warm("b") || s.runtimePid("b") != pb {
		t.Fatalf("over warmMax: a warm %v, b warm %v", s.warm("a"), s.warm("b"))
	}

	s.on(func(h *host) { h.warmMax, h.warmMemory = warmMax, 1 })
	pa = exit("a")
	gone(t, pa)
	gone(t, pb)
	if s.warm("a") || s.warm("b") {
		t.Fatal("kept over warmMemory")
	}

	s.on(func(h *host) { h.warmMemory = warmMemory })
	pa = exit("a")
	toasts := len(s.toasts())
	_ = syscall.Kill(pa, syscall.SIGKILL)
	gone(t, pa)
	deadline := time.Now().Add(wait)
	for s.warm("a") {
		if time.Now().After(deadline) {
			t.Fatal("a dead runtime is still warm")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.toasts(); len(got) != toasts {
		t.Errorf("the end of a warm program said %q", got[toasts:])
	}
	if page := s.open("a"); !strings.HasPrefix(page.ID, "mp:a:index:") || s.runtimePid("a") == pa {
		t.Fatalf("after its runtime died: %s, pid %d", page.ID, s.runtimePid("a"))
	}
}

// Adding mini-programs loads the list again: a warm program not added
// again stays warm, where its timer and stopAll reach it; one added again
// ends, and opens next with its new files.
func TestAddKeepsWarmPrograms(t *testing.T) {
	t.Setenv("PHONE_LANG", "en")
	s := startHost(t)
	var h *host
	s.on(func(x *host) { h = x })
	data := h.app.DataDir
	store, err := pkg.DevStore(filepath.Join(t.TempDir(), "store"), filepath.Join(data, "trust"))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := pkg.DevDeveloper(filepath.Join(t.TempDir(), "developers"), "test", store)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pkg.Build(counterDir, dev)
	if err != nil {
		t.Fatal(err)
	}
	downloads := filepath.Join(data, "downloads")
	mpk := filepath.Join(downloads, "counter.mpk")
	if err := os.MkdirAll(downloads, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mpk, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	var id string
	s.on(func(h *host) {
		h.addDownloaded()
		for _, p := range h.progs {
			if p.dir != "" {
				id = p.meta.ID
			}
		}
	})
	if id == "" {
		t.Fatal("not installed")
	}
	page := s.open(id)
	pid := s.runtimePid(id)
	s.act(page.ID, "", "mp.exit", "")
	s.waitDepth(1)
	if !s.warm(id) {
		t.Fatal("not kept warm")
	}

	if err := os.Remove(mpk); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloads, "other.mpk"), []byte("not a package"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.on(func(h *host) { h.addDownloaded() })
	if !s.warm(id) || s.runtimePid(id) != pid {
		t.Fatalf("after adding another: warm %v, pid %d (was %d)", s.warm(id), s.runtimePid(id), pid)
	}

	if err := os.WriteFile(mpk, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	s.on(func(h *host) { h.addDownloaded() })
	gone(t, pid)
	if s.warm(id) || s.runtimePid(id) != 0 {
		t.Fatalf("after adding it again: warm %v, pid %d", s.warm(id), s.runtimePid(id))
	}
}
