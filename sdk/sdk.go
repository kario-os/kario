// Package sdk is the Go app SDK for phone.Ui. Apps describe screens and
// handle actions; they never draw, read raw keys or talk to services directly.
//
// Threading: handlers and Post callbacks run one at a time on the dispatch
// goroutine, so app state needs no locking. Call may be used from handlers;
// it blocks only the calling handler while responses keep flowing.
package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"strconv"
	"sync"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

type ActionEvent struct {
	Screen string
	Node   string
	Action string
	Value  string
	Fields map[string]string
}

type App struct {
	ID      string
	DataDir string
	conn    *wire.Conn

	OnStart     func(arg string)
	OnAction    func(ActionEvent)
	OnPopped    func(screenID string)
	OnLifecycle func(state string)
	OnNotif     func(id, action string)
	// OnNotifText gets a notification action that asked for text (a quick
	// reply, ui.Action.Input) with what the user typed.
	OnNotifText func(id, action, text string)
	OnEvent     func(name, value string)

	// OnResync runs after a new Shell took over the connection and the
	// page stack was sent again (apps with state outside pages refresh it).
	OnResync func()

	// OnSave runs whenever the app leaves the foreground and returns what
	// it needs to rebuild its pages (JSON, at most ui.MaxSavedState bytes)
	// should the system end it in the background; a new process reads it
	// with Restore. What the user typed into a page comes back by itself
	// when the rebuilt page has the same screen ID.
	OnSave func() any
	saved  json.RawMessage

	queue   chan func()
	mu      sync.Mutex
	next    int
	pending map[int]chan response
	// stack mirrors the pages the Shell holds, for a new Shell (MsgResync).
	stack []ui.Screen
	// purposes by origin (SetPurposes).
	purposes map[string]map[string]string
}

type response struct {
	m     ui.Msg
	files []*os.File
}

func New() (*App, error) {
	fd, err := strconv.Atoi(os.Getenv("PHONE_UI_FD"))
	if err != nil {
		return nil, errors.New("PHONE_UI_FD not set: apps are started by phone-shell")
	}
	f := os.NewFile(uintptr(fd), "phone-ui")
	log.SetPrefix(os.Getenv("PHONE_APP_ID") + ": ")
	return &App{
		ID: os.Getenv("PHONE_APP_ID"), DataDir: os.Getenv("PHONE_DATA_DIR"),
		conn: wire.New(f), queue: make(chan func(), 1024), pending: map[int]chan response{},
	}, nil
}

// Run says hello and dispatches until the shell closes the connection.
func (a *App) Run() error {
	if err := a.conn.Send(ui.Msg{T: ui.MsgHello, App: a.ID, Proto: ui.Proto}); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		for {
			var m ui.Msg
			if err := a.conn.Recv(&m); err != nil {
				done <- err
				close(a.queue)
				return
			}
			if m.T == ui.MsgRes {
				var files []*os.File
				if m.FDs > 0 {
					files = a.conn.TakeFiles(m.FDs)
				}
				a.mu.Lock()
				ch := a.pending[m.Req]
				delete(a.pending, m.Req)
				a.mu.Unlock()
				if ch != nil {
					ch <- response{m, files}
				} else {
					for _, f := range files {
						f.Close()
					}
				}
				continue
			}
			a.queue <- func() { a.dispatch(m) }
		}
	}()
	for f := range a.queue {
		f()
	}
	return <-done
}

func (a *App) dispatch(m ui.Msg) {
	switch m.T {
	case ui.MsgStart:
		a.saved = m.Data
		if a.OnStart != nil {
			a.OnStart(m.Value)
		}
	case ui.MsgAction:
		if a.OnAction != nil {
			a.OnAction(ActionEvent{Screen: m.ScreenID, Node: m.Node, Action: m.Action, Value: m.Value, Fields: m.Fields})
		}
	case ui.MsgPopped:
		if n := len(a.stack); n > 1 && a.stack[n-1].ID == m.ScreenID {
			a.stack = a.stack[:n-1]
		}
		if a.OnPopped != nil {
			a.OnPopped(m.ScreenID)
		}
	case ui.MsgLifecycle:
		if m.State == ui.StateBackground && a.OnSave != nil {
			a.saveState(a.OnSave())
		}
		if a.OnLifecycle != nil {
			a.OnLifecycle(m.State)
		}
	case ui.MsgNotifAction:
		if m.Text != "" && a.OnNotifText != nil {
			a.OnNotifText(m.Value, m.Action, m.Text)
		} else if a.OnNotif != nil {
			a.OnNotif(m.Value, m.Action)
		}
	case ui.MsgEvent:
		if a.OnEvent != nil {
			a.OnEvent(m.Event, m.Value)
		}
	case ui.MsgPing:
		a.send(ui.Msg{T: ui.MsgPong, Value: m.Value})
	case ui.MsgResync:
		a.resync()
	}
}

// resync answers a new Shell: calls the old one never answered fail, and the
// mirrored page stack is sent again.
func (a *App) resync() {
	a.mu.Lock()
	for id, ch := range a.pending {
		ch <- response{m: ui.Msg{T: ui.MsgRes, Req: id, Err: "the shell restarted"}}
		delete(a.pending, id)
	}
	a.mu.Unlock()
	stack := append([]ui.Screen(nil), a.stack...)
	for i, s := range stack {
		t := ui.MsgPush
		if i == 0 {
			t = ui.MsgReset
		}
		a.send(ui.Msg{T: t, Screen: &s})
	}
	if a.OnResync != nil {
		a.OnResync()
	}
}

// SetPurposes says why the content the app hosts as origin (a
// mini-program) asks for each runtime permission; requests made on its
// behalf carry them to the system's permission dialog.
func (a *App) SetPurposes(origin string, purposes map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.purposes == nil {
		a.purposes = map[string]map[string]string{}
	}
	a.purposes[origin] = purposes
}

// mirror records what a page message does to the Shell's stack.
func (a *App) mirror(m ui.Msg) {
	if m.Screen == nil {
		return
	}
	s := *m.Screen
	switch m.T {
	case ui.MsgPush:
		a.stack = append(a.stack, s)
	case ui.MsgReplace:
		if len(a.stack) == 0 {
			a.stack = []ui.Screen{s}
		} else {
			a.stack[len(a.stack)-1] = s
		}
	case ui.MsgReset:
		a.stack = []ui.Screen{s}
	case ui.MsgUpdate:
		for i := range a.stack {
			if a.stack[i].ID == s.ID {
				a.stack[i] = s
			}
		}
	}
}

func (a *App) saveState(v any) {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > ui.MaxSavedState {
		log.Printf("saved state not sent: %d bytes, %v", len(raw), err)
		return
	}
	a.send(ui.Msg{T: ui.MsgState, Data: raw})
}

// Restore reads the state OnSave returned in the process the system ended,
// from OnStart on; false when this process starts afresh.
func (a *App) Restore(v any) bool {
	return len(a.saved) > 0 && json.Unmarshal(a.saved, v) == nil
}

// Post runs f on the dispatch goroutine (use from timers and goroutines).
func (a *App) Post(f func()) {
	defer func() { _ = recover() }()
	a.queue <- f
}

func (a *App) send(m ui.Msg) {
	if err := a.conn.Send(m); err != nil {
		log.Printf("send %s: %v", m.T, err)
	}
}

func (a *App) page(m ui.Msg) {
	a.mirror(m)
	a.send(m)
}

func (a *App) Push(s ui.Screen)    { a.page(ui.Msg{T: ui.MsgPush, Screen: &s}) }
func (a *App) Replace(s ui.Screen) { a.page(ui.Msg{T: ui.MsgReplace, Screen: &s}) }
func (a *App) Update(s ui.Screen)  { a.page(ui.Msg{T: ui.MsgUpdate, Screen: &s}) }
func (a *App) Reset(s ui.Screen)   { a.page(ui.Msg{T: ui.MsgReset, Screen: &s}) }
func (a *App) Pop()                { a.send(ui.Msg{T: ui.MsgPop}) }
func (a *App) Toast(text string)   { a.send(ui.Msg{T: ui.MsgToast, Text: text}) }
func (a *App) Notify(n ui.Notification) {
	a.send(ui.Msg{T: ui.MsgNotify, Notif: &n})
}

// Call requests a platform capability; the shell checks permissions and may
// show system UI (consent, trusted confirmation, scanner) before replying.
func (a *App) Call(capName string, args any, out any) error {
	return a.CallAs("", capName, args, out)
}

// CallAs is Call on behalf of content this app hosts (e.g. a mini-program);
// system dialogs show origin next to the app's own name.
func (a *App) CallAs(origin, capName string, args any, out any) error {
	data, files, err := a.roundTrip(origin, capName, args)
	for _, f := range files {
		f.Close()
	}
	return decode(data, err, out)
}

func (a *App) roundTrip(origin, capName string, args any) (json.RawMessage, []*os.File, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan response, 1)
	a.mu.Lock()
	a.next++
	id := a.next
	a.pending[id] = ch
	purposes := a.purposes[origin]
	a.mu.Unlock()
	a.send(ui.Msg{T: ui.MsgReq, Req: id, Cap: capName, Args: raw, Origin: origin, Purposes: purposes})
	r := <-ch
	if !r.m.OK {
		return nil, r.files, fmt.Errorf("%s: %s", capName, r.m.Err)
	}
	return r.m.Data, r.files, nil
}

// decode replaces *out with the result. Unmarshalling into what out held
// would merge: a field the reply leaves out (omitempty) would keep the old
// value, and slice elements are reused, so a call log entry without a name
// would show the name of the entry that sat there before.
func decode(data json.RawMessage, err error, out any) error {
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		if v := reflect.ValueOf(out); v.Kind() == reflect.Pointer && !v.IsNil() {
			v.Elem().SetZero()
		}
		return json.Unmarshal(data, out)
	}
	return nil
}

// Go runs a capability call off the dispatch goroutine and delivers the result
// back on it, so a handler can start a long system interaction (scanner,
// trusted confirmation) without blocking other events. out is filled on the
// dispatch goroutine just before done runs, like everything else the app
// touches, so concurrent calls may share it. done may be nil when the result
// does not matter.
func (a *App) Go(capName string, args any, out any, done func(error)) {
	a.GoAs("", capName, args, out, done)
}

func (a *App) GoAs(origin, capName string, args any, out any, done func(error)) {
	go func() {
		data, files, err := a.roundTrip(origin, capName, args)
		for _, f := range files {
			f.Close()
		}
		a.Post(func() {
			if err := decode(data, err, out); done != nil {
				done(err)
			}
		})
	}()
}

// Helpers for building screens.

func Text(s string) ui.Node { return ui.Node{Kind: ui.KindText, Text: s} }

func Styled(style, s string) ui.Node { return ui.Node{Kind: ui.KindText, Text: s, Style: style} }

func Section(s string) ui.Node { return ui.Node{Kind: ui.KindSection, Text: s} }

func Progress(label string, fraction float64, note string) ui.Node {
	return ui.Node{Kind: ui.KindProgress, Text: label, Fraction: fraction, Trailing: note}
}

func Button(id, label, action string) ui.Node {
	return ui.Node{Kind: ui.KindButton, ID: id, Text: label, Actions: []ui.Action{{ID: action, Label: label, Primary: true}}}
}

func Open(label string) ui.Action { return ui.Action{ID: ui.ActionOpen, Label: label, Primary: true} }

// CallFile is Call for capabilities that hand over one file (media.pick):
// use reads it, then it is closed. Like Call it blocks; call it from a
// goroutine of the app's own when the system shows UI (the picker).
func (a *App) CallFile(capName string, args, out any, use func(*os.File) (int64, error)) (int64, error) {
	data, files, err := a.roundTrip("", capName, args)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	if err := decode(data, err, out); err != nil {
		return 0, err
	}
	if len(files) != 1 {
		return 0, errors.New(capName + ": no file in the reply")
	}
	return use(files[0])
}
