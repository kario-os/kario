package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/jsrt/nativetest"
	"github.com/kario-os/kario/ui"
)

// The order program end to end: the scan and the payment reach the Shell
// as the host's requests with the program as origin and its own purposes;
// their results come back to its callbacks. The origin is the name its
// app.json is written in whatever the language; the payment's prompt
// names it in the device's.
func TestOrderScanAndPay(t *testing.T) {
	t.Setenv("PHONE_LANG", "en")
	order := testProgram(t, "order", subFS(t, "programs/order"))
	s := startHost(t, order)
	index := s.open("order")
	s.act(index.ID, "scan", "scan", "")
	scan := s.req(ui.CapScanQR)
	if scan.Origin != "\u6668\u5149\u5496\u5561\u70b9\u9910" || scan.Origin != order.meta.Name ||
		scan.Purposes["camera"] != "Scan the QR code on the table to order" {
		t.Fatalf("scan request: origin %q, purposes %v", scan.Origin, scan.Purposes)
	}
	s.reply(scan.Req, ui.ScanResult{Text: "mp|12|Morning Light"}, "")
	menu := s.pushed("mp:order:menu:")
	if !strings.Contains(screenText(menu), "Morning Light") {
		t.Fatalf("menu: %s", screenText(menu))
	}
	s.act(menu.ID, "latte", "add", "")
	s.page("Total ¥18.00")
	s.act(menu.ID, "pay", "pay", "")
	pay := s.req(ui.CapAuthConfirm)
	var args ui.AuthConfirmArgs
	_ = json.Unmarshal(pay.Args, &args)
	if pay.Origin != order.meta.Name || !strings.Contains(args.Prompt, `Mini-program "Morning Light Coffee" pays`) ||
		!strings.Contains(args.Prompt, "¥18.00") || !strings.Contains(args.Payload, `"program":"order"`) {
		t.Fatalf("payment: origin %q, %+v", pay.Origin, args)
	}
	s.reply(pay.Req, ui.KeyProof{}, "")
	done := s.pushed("mp:order:done:")
	if !strings.Contains(screenText(done), "Payment successful") {
		t.Fatalf("done: %s", screenText(done))
	}
	s.act(done.ID, "finish", "finish", "")
	s.waitDepth(2)
	if top := s.top(); top.ID != index.ID || s.depth() != 2 {
		t.Fatalf("after finishing: top %s, %d screens", top.ID, s.depth())
	}

	s.act(index.ID, "scan", "scan", "")
	s.reply(s.req(ui.CapScanQR).Req, nil, ui.ErrCancelled)
	s.toast("Scan failed: " + ui.CapScanQR + ": " + ui.ErrCancelled)
}

// What a program without permissions asks for, and what it tries past the
// bridge with kario-jsrt's own interface, stops at the host.
func TestBridgeRefuses(t *testing.T) {
	rogue := jsProgram(t, "rogue", `
Page("index", {
  render: function () { return [ui.text("rogue")]; },
  onAction: function (e) {
    if (e.action === "scan") {
      phone.scanCode({ fail: function (r) { phone.toast("scan: " + r.errMsg); } });
    } else if (e.action === "pay") {
      phone.requestPayment({ merchant: "m", amount: 100, fail: function (r) { phone.toast("pay: " + r.errMsg); } });
    } else if (e.action === "store") {
      try { phone.setStorage("k", 1); } catch (x) { phone.toast("store: " + x.message); }
    } else if (e.action === "raw") {
      import("kario").then(function (k) {
        k.call("`+ui.CapScanQR+`").catch(function (x) { k.toast("raw: " + x.message); });
        k.push({ id: "home", title: "fake", body: [] });
        k.push({ id: "mp:notes:index:1", title: "fake", body: [] });
        k.reset({ id: "mp:rogue:x:1", title: "fake", body: [] });
        k.notify({ id: "n", title: "fake" });
        k.toast("raw done");
      });
    } else if (e.action === "back") {
      phone.navigateBack(99);
    }
  },
});`)
	s := startHost(t, rogue)
	index := s.open("rogue")
	s.act(index.ID, "", "scan", "")
	s.toast("scan: permission not declared by mini-program: scan")
	s.act(index.ID, "", "pay", "")
	s.toast("pay: permission not declared by mini-program: payment")
	s.act(index.ID, "", "store", "")
	s.toast("store: permission not declared by mini-program: storage")
	s.act(index.ID, "", "raw", "")
	s.toastsSay("raw: not an interface of mini-programs: "+ui.CapScanQR, "raw done")
	if n := s.count(ui.MsgReq); n != 0 {
		t.Fatalf("%d requests reached the Shell", n)
	}
	if s.count(ui.MsgReset) != 1 || s.count(ui.MsgNotify) != 0 || s.depth() != 2 || s.top().ID != index.ID {
		t.Fatalf("the program changed the host's pages: %d screens, top %s", s.depth(), s.top().ID)
	}
	s.act(index.ID, "", "back", "")
	s.waitDepth(1)
	time.Sleep(100 * time.Millisecond)
	if s.depth() != 1 || s.top().ID != "home" || s.count(ui.MsgPop) != 1 {
		t.Fatalf("navigateBack(99): %d screens, %d pops", s.depth(), s.count(ui.MsgPop))
	}
}

// Errors: one in a callback is a toast and the program goes on; one at
// the top level, or a missing index page, is a start that failed.
func TestProgramErrors(t *testing.T) {
	t.Setenv("PHONE_LANG", "en")
	thrower := jsProgram(t, "thrower", `
Page("index", {
  render: function () { return [ui.text("ok")]; },
  onAction: function (e) {
    if (e.action === "throw") throw new RangeError("boom");
    phone.toast("still here");
  },
});`)
	broken := jsProgram(t, "broken", `Page("index", { render: function () { return [ui.text("never")] } }); undefinedFunction();`)
	noIndex := jsProgram(t, "noindex", `Page("other", { render: function () { return []; } });`)
	syntax := jsProgram(t, "syntax", `Page("index", {`)
	s := startHost(t, thrower, broken, noIndex, syntax)
	index := s.open("thrower")
	s.act(index.ID, "", "throw", "")
	s.toast("Mini-program error: RangeError: boom")
	s.act(index.ID, "", "again", "")
	s.toast("still here")
	for id, want := range map[string]string{
		"broken": "broken: ReferenceError", "noindex": "noindex has no index page", "syntax": "syntax: SyntaxError",
	} {
		s.send(ui.Msg{T: ui.MsgAction, ScreenID: "home", Node: "p:" + id, Action: ui.ActionOpen})
		s.toast("Cannot start the mini-program: " + want)
		time.Sleep(50 * time.Millisecond)
		if pid := s.runtimePid(id); pid != 0 {
			t.Errorf("%s: its runtime still runs", id)
		}
	}
}

// An infinite loop is interrupted at the budget and the program goes on;
// one at the top level fails the start. A runtime stuck where the budget
// does not reach (here: none set) is ended when it leaves a ping unanswered.
func TestInfiniteLoop(t *testing.T) {
	t.Setenv("PHONE_LANG", "en")
	spin := jsProgram(t, "spin", `
Page("index", {
  render: function () { return [ui.text("spin")]; },
  onAction: function (e) {
    if (e.action === "spin") { while (true) {} }
    phone.toast("alive");
  },
});`)
	top := jsProgram(t, "top", `for (;;) {}`)
	s := startHost(t, spin, top)
	index := s.open("spin")
	t0 := time.Now()
	s.act(index.ID, "", "spin", "")
	s.toast("was interrupted")
	if took := time.Since(t0); took > 2*time.Second {
		t.Errorf("interrupted after %v", took)
	}
	s.act(index.ID, "", "ping", "")
	s.toast("alive")
	s.send(ui.Msg{T: ui.MsgAction, ScreenID: "home", Node: "p:top", Action: ui.ActionOpen})
	s.toast("Cannot start the mini-program")

	// Not kept warm: it starts again without a budget.
	s.on(func(h *host) { h.warmMax = 0 })
	s.act(index.ID, "", "mp.exit", "")
	s.waitDepth(1)
	s.on(func(h *host) { h.budget, h.hang = 0, 500*time.Millisecond })
	index = s.open("spin")
	pid := s.runtimePid("spin")
	s.act(index.ID, "", "spin", "")
	s.toast("Mini-program stopped: it is not responding")
	gone(t, pid)
	s.waitDepth(1)
}

// Floods: setData in a loop reaches the Shell as a few pages with the last
// state; toasts beyond a burst are dropped; pages go at most maxPages deep;
// a page over maxPage is a warning; a message over maxLine ends the runtime.
func TestFloods(t *testing.T) {
	t.Setenv("PHONE_LANG", "en")
	flood := jsProgram(t, "flood", `
Page("index", {
  data: { n: 0 },
  render: function () {
    if (this.data.big) return [ui.text("x".repeat(this.data.big))];
    return [ui.text("n=" + this.data.n)];
  },
  onAction: function (e) {
    if (e.action === "set") { for (var i = 1; i <= 2000; i++) this.setData({ n: i }); }
    if (e.action === "toast") { for (var j = 0; j < 1000; j++) phone.toast("t" + j); }
    if (e.action === "deep") { for (var k = 0; k < 30; k++) phone.navigateTo("index"); }
    if (e.action === "big") this.setData({ big: Number(e.value) });
  },
});`)
	s := startHost(t, flood)
	index := s.open("flood")
	s.act(index.ID, "", "set", "")
	s.page("n=2000")
	time.Sleep(100 * time.Millisecond)
	if n := s.count(ui.MsgUpdate); n > 20 {
		t.Errorf("%d updates for 2000 setData calls", n)
	}
	s.act(index.ID, "", "toast", "")
	s.toast("t0")
	time.Sleep(300 * time.Millisecond)
	if n := len(s.toasts()); n > toastBurst+2 {
		t.Errorf("%d toasts reached the Shell", n)
	}
	s.act(index.ID, "", "deep", "")
	time.Sleep(500 * time.Millisecond)
	if d := s.depth(); d != maxPages+1 {
		t.Errorf("%d screens, want home and %d pages", d, maxPages)
	}
	s.on(func(h *host) { h.exit(flood) })
	s.waitDepth(1)

	index = s.open("flood")
	s.act(index.ID, "", "big", strconv.Itoa(maxPage+1000))
	s.page("is not shown")
	s.act(index.ID, "", "big", strconv.Itoa(maxLine+1000))
	s.toast("Mini-program stopped: it sent a message over")
	s.waitDepth(1)
}

// A runtime that crashes, or runs out of memory, ends alone: the host
// says so, closes its pages and starts it again next time.
func TestCrashAndMemory(t *testing.T) {
	t.Setenv("PHONE_LANG", "en")
	hog := jsProgram(t, "hog", `
Page("index", {
  render: function () { return [ui.text("hog")]; },
  onAction: function (e) {
    var keep = [];
    for (;;) keep.push(new Array(1 << 20).fill(e.action));
  },
});`)
	s := startHost(t, hog)
	index := s.open("hog")
	pid := s.runtimePid("hog")
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	s.toast("Mini-program stopped: signal: killed")
	s.waitDepth(1)
	index = s.open("hog")
	pid = s.runtimePid("hog")
	raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/limits", pid))
	if !strings.Contains(string(raw), strconv.Itoa(runtimeMemory)) {
		t.Fatalf("limits of the runtime:\n%s", raw)
	}
	s.act(index.ID, "", "fill", "")
	msg := s.toast("Mini-program")
	if !strings.Contains(msg, "out of memory") && !strings.Contains(msg, "stopped") {
		t.Fatalf("toast %q", msg)
	}
	s.on(func(*host) {})
}

// Under phone-sandbox (make build's) a runtime is confined further by the
// sandbox's layers, with its store still working; with Landlock it can
// read nothing of the host's data but its own module and store.
func TestNestedSandbox(t *testing.T) {
	sb, _ := filepath.Abs(filepath.Join("..", "..", "build", "usr", "lib", "phone", "bin", "phone-sandbox"))
	if _, err := os.Stat(sb); err != nil {
		t.Skipf("no phone-sandbox at %s (make build)", sb)
	}
	prog := jsProgram(t, "boxed", `
Page("index", {
  onLoad: function () { phone.setStorage("n", (phone.getStorage("n") || 0) + 1); },
  render: function () { return [ui.text("n=" + phone.getStorage("n"))]; },
});`, "storage")
	s := startHost(t, prog)
	s.on(func(h *host) { h.sandbox = sb })
	page := s.open("boxed")
	if !strings.Contains(screenText(page), "n=1") {
		t.Fatalf("page %s; toasts %v", screenText(page), s.toasts())
	}
	var dir string
	s.on(func(h *host) { dir = h.storeDir(prog) })
	stored(t, dir, `"n":"1"`)
	pid := s.runtimePid("boxed")
	env, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	status, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	var applied string
	for _, kv := range strings.Split(string(env), "\x00") {
		if v, ok := strings.CutPrefix(kv, "PHONE_SANDBOX="); ok {
			applied = v
		}
	}
	if !strings.Contains(applied, "seccomp") || !strings.Contains(string(status), "NoNewPrivs:\t1") {
		t.Fatalf("the runtime is not confined: PHONE_SANDBOX=%q\n%s", applied, status)
	}
	exe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if filepath.Base(exe) != jsrt.Program {
		t.Fatalf("the runtime is %s", exe)
	}
	t.Logf("layers: %s", applied)
}

// Programs cannot reach each other's data: each runtime has its own
// store (none without "storage"), no module of the engine's that opens
// files, and only its own connection, not the host's to the Shell.
func TestIsolation(t *testing.T) {
	writer := jsProgram(t, "writer", `
Page("index", {
  onLoad: function () { phone.setStorage("secret", "w-secret"); },
  render: function () { return [ui.text("writer")]; },
});`, "storage")
	reader := jsProgram(t, "reader", `
var found = [];
Page("index", {
  onLoad: function () {
    found.push("get=" + JSON.stringify(phone.getStorage("secret")));
    var self = this;
    import("kario").then(function (k) {
      found.push("keys=" + JSON.stringify(k.storage.keys()));
      return import("os").catch(function (x) { found.push("os: " + x.message); });
    }).then(function () {
      return import("std").catch(function (x) { found.push("std: " + x.message); });
    }).then(function () { self.setData({ done: true }); });
  },
  render: function () { return [ui.text(this.data.done ? "done " + found.join("; ") : "wait")]; },
});`, "storage")
	none := jsProgram(t, "none", `
Page("index", {
  render: function () {
    var keys;
    try { keys = JSON.stringify(require); } catch (x) { keys = "no require"; }
    return [ui.text(String(phone.getStorage("secret")) + " " + keys)];
  },
});`)
	s := startHost(t, writer, reader, none)
	w := s.open("writer")
	var wdir, rdir, ndir string
	s.on(func(h *host) { wdir, rdir, ndir = h.storeDir(writer), h.storeDir(reader), h.storeDir(none) })
	stored(t, wdir, "w-secret")
	s.act(w.ID, "", "mp.exit", "")
	s.waitDepth(1)

	s.open("reader")
	page := s.page("done ")
	text := screenText(page)
	for _, want := range []string{"get=null", "keys=[]", "os: ", "std: "} {
		if !strings.Contains(text, want) {
			t.Errorf("reader: %q lacks %q", text, want)
		}
	}
	if strings.Contains(text, "w-secret") {
		t.Fatalf("the reader read the writer's data: %s", text)
	}
	pid := s.runtimePid("reader")
	fds, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	var names []string
	for _, f := range fds {
		names = append(names, f.Name())
	}
	if strings.Join(names, ",") != "0,1,2,3" {
		t.Errorf("the runtime's descriptors: %v (want its standard ones and its connection)", names)
	}
	s.act(page.ID, "", "mp.exit", "")
	s.waitDepth(1)

	page = s.open("none")
	if text := screenText(page); !strings.Contains(text, "null no require") {
		t.Errorf("none: %s", text)
	}
	if _, err := os.Stat(filepath.Join(ndir, jsrt.StorageFile)); err == nil {
		t.Error("a program without storage has a store")
	}
	if _, err := os.Stat(filepath.Join(rdir, jsrt.StorageFile)); err == nil {
		t.Error("the reader has a store it never wrote")
	}
}

// A program's last save survives its end right after its exit (its warm
// time short here): the runtime writes its store on a thread of its own
// (here each fsync takes a second), and the host ends it with SIGTERM,
// which kario-jsrt holds back until the write is done, not with SIGKILL.
func TestStoredBeforeStop(t *testing.T) {
	saver := jsProgram(t, "saver", `
Page("index", {
  render: function () { return [ui.text("saver")]; },
  onAction: function () { phone.setStorage("k", "last"); phone.toast("saved"); },
});`, "storage")
	s := startHost(t, saver)
	slow := filepath.Join(t.TempDir(), jsrt.Program)
	script := fmt.Sprintf("#!/bin/sh\nexec /usr/bin/env LD_PRELOAD=%s SLOWFS_DELAY_MS=1000 %s \"$@\"\n", nativetest.SlowFS(t), jsrtBinary(t))
	if err := os.WriteFile(slow, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s.on(func(h *host) { h.jsrt, h.warmFor = slow, 100*time.Millisecond })
	page := s.open("saver")
	s.act(page.ID, "", "save", "")
	s.toast("saved")
	pid := s.runtimePid("saver")
	s.act(page.ID, "", "mp.exit", "")
	s.waitDepth(1)
	gone(t, pid)
	var dir string
	s.on(func(h *host) { dir = h.storeDir(saver) })
	if raw, err := os.ReadFile(filepath.Join(dir, jsrt.StorageFile)); err != nil || !strings.Contains(string(raw), "last") {
		t.Fatalf("the store after the exit: %s (%v)", raw, err)
	}
}
