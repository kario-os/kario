package main

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// fakeShell plays the Shell for the app, with phone-smsd behind it: while
// down, every SMS and block list call fails, as the broker's do while
// phone-smsd restarts. pages are the pages the app sent, in order.
type fakeShell struct {
	t       *testing.T
	shell   *wire.Conn
	mu      sync.Mutex
	down    bool
	threads []ui.SMSThread
	msgs    []ui.SMS
	alerts  []ui.SMSAlert
	pages   []ui.Screen
}

// startMessages starts the app against f and returns the page it starts
// with.
func startMessages(t *testing.T, f *fakeShell) ui.Screen {
	t.Helper()
	f.t = t
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.shell = wire.New(os.NewFile(uintptr(fds[1]), "shell"))
	t.Cleanup(func() { f.shell.Close() })
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
	a, err := sdk.New()
	if err != nil {
		t.Fatal(err)
	}
	newMessages(a)
	go a.Run()
	go f.serve()
	return f.do(ui.Msg{T: ui.MsgStart}, "list")
}

func (f *fakeShell) serve() {
	for {
		var m ui.Msg
		if f.shell.Recv(&m) != nil {
			return
		}
		switch {
		case m.T == ui.MsgReq:
			f.answer(m)
		case m.Screen != nil:
			f.mu.Lock()
			f.pages = append(f.pages, *m.Screen)
			f.mu.Unlock()
		}
	}
}

func (f *fakeShell) answer(m ui.Msg) {
	f.mu.Lock()
	var v any = map[string]bool{"ok": true}
	switch m.Cap {
	case ui.CapSMSThreads:
		var args ui.SMSListArgs
		_ = json.Unmarshal(m.Args, &args)
		v = []ui.SMSThread{}
		if args.Folder == "" {
			v = f.threads
		}
	case ui.CapSMSList:
		v = f.msgs
	case ui.CapSMSAlerts:
		v = f.alerts
	case ui.CapSMSBlocked:
		v = []string{}
	case ui.CapBlockList:
		v = ui.BlockList{}
	}
	res := ui.Msg{T: ui.MsgRes, Req: m.Req, OK: true}
	if f.down && (strings.HasPrefix(m.Cap, "sms.") || strings.HasPrefix(m.Cap, "block.")) {
		res = ui.Msg{T: ui.MsgRes, Req: m.Req, Err: "smsd unavailable: dial unix smsd.sock: connect: connection refused"}
	} else {
		res.Data, _ = json.Marshal(v)
	}
	f.mu.Unlock()
	_ = f.shell.Send(res)
}

func (f *fakeShell) send(m ui.Msg) {
	if err := f.shell.Send(m); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeShell) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// do sends m and returns the next page id the app sends for it.
func (f *fakeShell) do(m ui.Msg, id string) ui.Screen {
	f.t.Helper()
	f.mu.Lock()
	n := len(f.pages)
	f.mu.Unlock()
	f.send(m)
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		f.mu.Lock()
		for _, s := range f.pages[n:] {
			if s.ID == id {
				f.mu.Unlock()
				return s
			}
		}
		f.mu.Unlock()
	}
	f.t.Fatalf("no page %q after %s %s %s", id, m.T, m.Event, m.Action)
	return ui.Screen{}
}

// smsEvent is the system's "sms" event: a message came, or the Shell
// subscribed to a new phone-smsd.
var smsEvent = ui.Msg{T: ui.MsgEvent, Event: ui.EventSMS, Value: "{}"}

func action(screen, node, act, value string) ui.Msg {
	return ui.Msg{T: ui.MsgAction, ScreenID: screen, Node: node, Action: act, Value: value}
}

func ids(s ui.Screen) []string {
	var out []string
	for _, n := range s.Body {
		if n.ID != "" {
			out = append(out, n.ID)
		}
	}
	return out
}

func says(s ui.Screen, text string) bool {
	return slices.ContainsFunc(s.Body, func(n ui.Node) bool { return n.Text == text })
}

var (
	awayText    = tr.T("Messages are not available right now. They will show again by themselves.")
	noneText    = tr.T("No messages. Press the right soft key to write one.")
	peer        = "13800000016"
	firstThread = ui.SMSThread{Thread: "+86" + peer, Peer: peer, Last: ui.SMS{ID: "a", Dir: "in", Peer: peer, Text: "see you", At: 1}}
)

// While phone-smsd restarts, a list that could not be read again is not an
// empty inbox: the last one stays; the "sms" event the Shell sends once it
// is subscribed to the new phone-smsd reads it again.
func TestListStaysWhileSmsdRestarts(t *testing.T) {
	f := &fakeShell{threads: []ui.SMSThread{firstThread}}
	if s := startMessages(t, f); !slices.Contains(ids(s), "t:"+peer) {
		t.Fatalf("list %v", ids(s))
	}
	f.setDown(true)
	if s := f.do(smsEvent, "list"); !slices.Contains(ids(s), "t:"+peer) || says(s, noneText) {
		t.Fatalf("phone-smsd away: the list %v, %+v", ids(s), s.Body)
	}
	f.mu.Lock()
	f.down = false
	f.threads = append([]ui.SMSThread{{Thread: "+8613800000017", Peer: "13800000017", Last: ui.SMS{ID: "b", Dir: "in", Text: "new", At: 2}}}, f.threads...)
	f.mu.Unlock()
	if s := f.do(smsEvent, "list"); !slices.Equal(ids(s), []string{"t:13800000017", "t:" + peer}) || says(s, awayText) {
		t.Fatalf("phone-smsd back: the list %v, %+v", ids(s), s.Body)
	}
}

// Started while phone-smsd restarts (or before it at boot), the app says
// the messages are not there yet instead of "No messages", and shows them
// once the Shell's "sms" event comes.
func TestStartedWhileSmsdRestarts(t *testing.T) {
	f := &fakeShell{down: true, threads: []ui.SMSThread{firstThread}}
	if s := startMessages(t, f); !says(s, awayText) || says(s, noneText) {
		t.Fatalf("started with phone-smsd away: %+v", s.Body)
	}
	f.setDown(false)
	if s := f.do(smsEvent, "list"); !slices.Equal(ids(s), []string{"t:" + peer}) || says(s, awayText) {
		t.Fatalf("phone-smsd back: the list %v, %+v", ids(s), s.Body)
	}
}

// The open thread keeps its messages while phone-smsd restarts; one opened
// meanwhile says they are not there yet instead of showing none.
func TestThreadStaysWhileSmsdRestarts(t *testing.T) {
	f := &fakeShell{threads: []ui.SMSThread{firstThread}, msgs: []ui.SMS{firstThread.Last}}
	startMessages(t, f)
	if s := f.do(action("list", "t:"+peer, ui.ActionOpen, ""), "thread"); !slices.Contains(ids(s), "m:a") {
		t.Fatalf("thread %v", ids(s))
	}
	f.setDown(true)
	if s := f.do(smsEvent, "thread"); !slices.Contains(ids(s), "m:a") || says(s, awayText) {
		t.Fatalf("phone-smsd away: the thread %v, %+v", ids(s), s.Body)
	}
	if s := f.do(ui.Msg{T: ui.MsgPopped, ScreenID: "thread"}, "list"); !slices.Contains(ids(s), "t:"+peer) {
		t.Fatalf("back on the list %v", ids(s))
	}
	s := f.do(action("list", "t:"+peer, ui.ActionOpen, ""), "thread")
	if slices.Contains(ids(s), "m:a") || !says(s, awayText) || !slices.Contains(ids(s), "text") {
		t.Fatalf("opened with phone-smsd away: %v, %+v", ids(s), s.Body)
	}
	f.setDown(false)
	if s := f.do(smsEvent, "thread"); !slices.Contains(ids(s), "m:a") || says(s, awayText) {
		t.Fatalf("phone-smsd back: the thread %v, %+v", ids(s), s.Body)
	}
}

// Search, Blocked and Emergency alerts say the messages are not there yet
// while phone-smsd restarts, not that there are none; the alerts come
// with the Shell's "sms" event.
func TestPagesSayWhileSmsdRestarts(t *testing.T) {
	f := &fakeShell{down: true, threads: []ui.SMSThread{firstThread}, alerts: []ui.SMSAlert{{ID: "al1", Level: ui.AlertExtreme, Text: "Flood warning", At: 1}}}
	startMessages(t, f)
	f.do(action("list", "", "search", ""), "search")
	if s := f.do(action("search", "q", "changed", "see"), "search"); !says(s, awayText) ||
		slices.ContainsFunc(s.Body, func(n ui.Node) bool { return n.Text == tr.T("Found (%d)", 0) }) {
		t.Fatalf("search: %+v", s.Body)
	}
	f.send(ui.Msg{T: ui.MsgPopped, ScreenID: "search"})
	if s := f.do(action("list", "", "blocked", ""), "blocked"); !says(s, awayText) || says(s, tr.T("None")) {
		t.Fatalf("blocked: %+v", s.Body)
	}
	f.send(ui.Msg{T: ui.MsgPopped, ScreenID: "blocked"})
	if s := f.do(action("list", "", "alerts", ""), "alerts"); !says(s, awayText) || says(s, tr.T("No emergency alerts received")) {
		t.Fatalf("alerts: %+v", s.Body)
	}
	f.setDown(false)
	if s := f.do(smsEvent, "alerts"); !slices.Equal(ids(s), []string{"a:al1"}) || says(s, awayText) {
		t.Fatalf("phone-smsd back: the alerts %v, %+v", ids(s), s.Body)
	}
	if s := f.do(action("alerts", "a:al1", "alert", ""), "alert"); !says(s, "Flood warning") {
		t.Fatalf("alert: %+v", s.Body)
	}
}
