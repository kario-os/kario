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
	"slices"
	"strconv"
	"sync"
	"time"

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
	// Platform is the system's ui.PlatformLevel: a capability of a higher
	// level (ui.CapLevels) answers "unknown capability" here.
	Platform int
	conn     *wire.Conn

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

	queue chan func()
	// closed: the Shell has gone; Run finishes what is queued and returns.
	closed  chan struct{}
	mu      sync.Mutex
	next    int
	pending map[int]chan response
	// stack mirrors the pages the Shell holds, for a new Shell (MsgResync).
	stack []ui.Screen
	// purposes by origin (SetPurposes).
	purposes map[string]map[string]string

	// forms: the other majors the app speaks (ui.EnvTestForms). With any,
	// messages wait in held until start names the major (form: nil for
	// the SDK's own), under mu.
	forms   []ui.ProtoForm
	form    *ui.ProtoForm
	started bool
	held    []ui.Msg
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
		ID: os.Getenv("PHONE_APP_ID"), DataDir: os.Getenv("PHONE_DATA_DIR"), Platform: platformLevel(os.Getenv(ui.EnvPlatform)),
		conn: wire.New(f), queue: make(chan func(), 1024), closed: make(chan struct{}), pending: map[int]chan response{},
		forms: otherForms(os.Getenv(ui.EnvTestForms)),
	}, nil
}

// otherForms are the majors besides ui.ProtoMajor in env's forms.
func otherForms(env string) []ui.ProtoForm {
	var forms, out []ui.ProtoForm
	if env == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(env), &forms); err != nil {
		log.Printf("%s: %v", ui.EnvTestForms, err)
		return nil
	}
	for _, f := range forms {
		if f.Major >= 0 && f.Major != ui.ProtoMajor {
			out = append(out, f)
		}
	}
	return out
}

// hello is the app's hello: proto the oldest major it speaks (what a
// system that does not negotiate reads), protos all of them, newest first,
// when there is more than one (22-developer-api.md §2.1).
func (a *App) hello() ui.Msg {
	majors := []int{ui.ProtoMajor}
	for _, f := range a.forms {
		majors = append(majors, f.Major)
	}
	slices.Sort(majors)
	majors = slices.Compact(majors)
	m := ui.Msg{T: ui.MsgHello, App: a.ID, Proto: ui.ProtoName(majors[0])}
	if len(majors) == 1 {
		m.Proto = ui.Proto
		return m
	}
	for i := len(majors) - 1; i >= 0; i-- {
		m.Protos = append(m.Protos, ui.ProtoName(majors[i]))
	}
	return m
}

// choose takes the major start names (none: a system that does not
// negotiate, so hello's proto) and sends what waited for it.
func (a *App) choose(proto string) {
	hello := a.hello()
	if proto == "" {
		proto = hello.Proto
	}
	a.mu.Lock()
	major, _ := ui.ParseProto(proto)
	a.form = nil
	for i := range a.forms {
		if a.forms[i].Major == major {
			a.form = &a.forms[i]
		}
	}
	a.started = true
	held := a.held
	a.held = nil
	a.mu.Unlock()
	for _, m := range held {
		a.send(m)
	}
}

// recv reads the Shell's next message in the SDK's own form.
func (a *App) recv(m *ui.Msg) error {
	if len(a.forms) == 0 {
		return a.conn.Recv(m)
	}
	for {
		var fields map[string]json.RawMessage
		if err := a.conn.Recv(&fields); err != nil {
			return err
		}
		a.mu.Lock()
		started, form := a.started, a.form
		a.mu.Unlock()
		var t, proto string
		_ = json.Unmarshal(fields["t"], &t)
		if !started && t == ui.MsgStart {
			_ = json.Unmarshal(fields["proto"], &proto)
			a.choose(proto)
			a.mu.Lock()
			form = a.form
			a.mu.Unlock()
		}
		if form != nil {
			if err := form.FromMajor(fields); err != nil {
				log.Printf("from the Shell: %v", err)
				continue
			}
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		*m = ui.Msg{}
		return json.Unmarshal(raw, m)
	}
}

// platformLevel is the level the system says (PHONE_PLATFORM); one that
// says none is level 1.
func platformLevel(v string) int {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	return 1
}

// Run says hello and dispatches until the shell closes the connection.
func (a *App) Run() error {
	if err := a.conn.Send(a.hello()); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		for {
			var m ui.Msg
			if err := a.recv(&m); err != nil {
				done <- err
				close(a.closed)
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
	for {
		select {
		case f := <-a.queue:
			f()
		case <-a.closed:
			for {
				select {
				case f := <-a.queue:
					f()
				default:
					return <-done
				}
			}
		}
	}
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
		if m.Event == ui.EventTimeZone {
			setZone(m.Value)
		}
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
	select {
	case a.queue <- f:
	case <-a.closed:
	}
}

func (a *App) send(m ui.Msg) {
	var out any = m
	if len(a.forms) > 0 {
		a.mu.Lock()
		if !a.started {
			a.held = append(a.held, m)
			a.mu.Unlock()
			return
		}
		form := a.form
		a.mu.Unlock()
		if form != nil {
			raw, _ := json.Marshal(m)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			form.ToMajor(fields)
			out = fields
		}
	}
	if err := a.conn.Send(out); err != nil {
		log.Printf("send %s: %v", m.T, err)
	}
}

func (a *App) page(m ui.Msg) {
	// The Shell drops an update of a page it does not hold; not sending one
	// keeps a refresh of many pages from using up the rate the Shell reads
	// an app at, which would hold up the app's answers to pings.
	if m.T == ui.MsgUpdate && m.Screen != nil && !a.holds(m.Screen.ID) {
		return
	}
	a.mirror(m)
	a.send(m)
}

func (a *App) holds(id string) bool {
	for _, s := range a.stack {
		if s.ID == id {
			return true
		}
	}
	return false
}

// Top is the ID of the page on top of the app's stack ("" before the
// first): a page leaves it when the Shell says it was popped (OnPopped),
// not when Pop is sent.
func (a *App) Top() string {
	if n := len(a.stack); n > 0 {
		return a.stack[n-1].ID
	}
	return ""
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
	ch, err := a.start(origin, capName, args)
	if err != nil {
		return nil, nil, err
	}
	return wait(capName, ch)
}

// start sends a capability request and returns where its answer comes.
func (a *App) start(origin, capName string, args any) (chan response, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	ch := make(chan response, 1)
	a.mu.Lock()
	a.next++
	id := a.next
	a.pending[id] = ch
	purposes := a.purposes[origin]
	a.mu.Unlock()
	a.send(ui.Msg{T: ui.MsgReq, Req: id, Cap: capName, Args: raw, Origin: origin, Purposes: purposes})
	return ch, nil
}

func wait(capName string, ch chan response) (json.RawMessage, []*os.File, error) {
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
	// Sent here, in the caller's order: two presses' requests reach the
	// Shell as they were made, whichever answer comes first.
	ch, err := a.start(origin, capName, args)
	go func() {
		var data json.RawMessage
		var files []*os.File
		if err == nil {
			data, files, err = wait(capName, ch)
		}
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

// setZone puts a new time zone in force for the app's clock, as TZ did at
// its start.
func setZone(name string) {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return
	}
	_ = os.Setenv("TZ", name)
	time.Local = loc
}
