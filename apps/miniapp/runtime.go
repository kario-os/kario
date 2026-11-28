package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/sandbox"
	"github.com/kario-os/kario/statefile"
	"github.com/kario-os/kario/ui"
)

// Each mini-program runs on kario-jsrt, the runtime of TypeScript apps, in
// a process of its own (WeChat gives each its own JS context), started when
// it opens and ended when its last page closes. The host is that process's
// Shell: the runtime module (runtime.js) turns Page, setData and phone.*
// into pages it pushes and calls it makes on its connection, and the host
// checks each against the program before anything reaches the real Shell.
// What the program cannot get past, whatever its code does:
//
//   - its connection is the only descriptor it has: the host's connection
//     to the Shell is close-on-exec, so no runtime can speak as the host;
//   - its calls are the bridge's few (bridge*), checked against the
//     permissions it declared and sent to the Shell with its own name as
//     origin; its pages carry its own prefix, at most maxPages of them, and
//     it pops only its own;
//   - storage is kario-jsrt's, in its own directory (none without the
//     "storage" permission); under phone-sandbox (when the host can run it)
//     Landlock keeps the process to that directory and its own module, so
//     even a compromised engine cannot read another program's data;
//   - a turn of its JavaScript is interrupted past budget, its memory is
//     bounded (runtimeMemory), and a runtime that stops reading, floods
//     its connection or leaves a ping unanswered is ended.

//go:embed runtime.js
var runtimeJS string

const (
	// runtimeMemory bounds a runtime's data (RLIMIT_DATA); the engine's
	// allocations fail past it, not the host's.
	runtimeMemory = 64 << 20
	// maxPages is how deep a program's pages go (WeChat's limit).
	maxPages = 10
	// maxPage bounds one page as the host sends it to the Shell, well
	// under wire.MaxLine.
	maxPage = 1 << 20
	// maxLine is the longest message a runtime may send.
	maxLine = 2 << 20
	// maxScreenID: "mp:<program>:<page>:<n>".
	maxScreenID = 256
	// maxCalls: bridge calls of one runtime not answered yet.
	maxCalls = 8
	// outQueue: messages to a runtime that does not read them; a full
	// queue ends it.
	outQueue = 64
	// badLimit: messages the host cannot use before it ends the runtime.
	badLimit = 16
	// Toasts (its own and its errors): a burst, then one per toastEvery.
	toastBurst = 5
	toastEvery = 500 * time.Millisecond
	// updateEvery: setData's pages reach the Shell at most this often.
	updateEvery = 20 * time.Millisecond
	// Its log lines, on the host's stderr: a burst per logWindow.
	logBurst  = 20
	logWindow = 10 * time.Second
	// startLimit: from the start of the process to its hello.
	startLimit = 10 * time.Second
	// A ping follows activity by pingAfter; hangLimit without its pong
	// ends the runtime (a turn stuck where the budget does not reach).
	pingAfter = time.Second
	hangLimit = 3 * time.Second
	hangGrace = 200 * time.Millisecond
	// stopGrace: a runtime ended with SIGTERM finishes the storage write
	// under way (kario-jsrt writes it on a thread of its own), then it is
	// killed all the same.
	stopGrace = 5 * time.Second
)

// The calls runtime.js makes; every other name is refused.
const (
	bridgeScan  = "scanCode"
	bridgePay   = "requestPayment"
	bridgeError = "error"
)

// What an error call reports (runtime.js's tell): a start that failed, a
// page that does not exist; anything else is an error in a callback.
const (
	errStart = "start"
	errPage  = "page"
)

// runtimePrograms finds kario-jsrt and phone-sandbox beside the host.
func runtimePrograms() (string, string) {
	exe, err := os.Executable()
	if err != nil {
		return jsrt.Program, ""
	}
	dir := filepath.Dir(exe)
	sb := filepath.Join(dir, "phone-sandbox")
	if _, err := os.Stat(sb); err != nil {
		sb = ""
	}
	return filepath.Join(dir, jsrt.Program), sb
}

// closeOnExec keeps every descriptor the host has from the runtimes it
// starts (they get theirs as ExtraFiles): the connection to the Shell
// first of all, which speaks for the host.
func closeOnExec() {
	if fd, err := strconv.Atoi(os.Getenv("PHONE_UI_FD")); err == nil {
		syscall.CloseOnExec(fd)
	}
	entries, _ := os.ReadDir("/proc/self/fd")
	for _, e := range entries {
		if fd, err := strconv.Atoi(e.Name()); err == nil && fd > 2 {
			syscall.CloseOnExec(fd)
		}
	}
}

// child is one program's runtime. Its fields belong to the host's dispatch
// goroutine but for mu's.
type child struct {
	h     *host
	prog  *program
	cmd   *exec.Cmd
	sock  *os.File
	out   chan []byte
	gone  bool
	hello bool
	calls int
	bad   int
	// The ping in flight, and whether one is due.
	ping    string
	pingDue bool
	pings   int
	// timers to stop with the runtime.
	timers []*time.Timer
	// toasts it may still show now; one more per toastEvery.
	toasts  float64
	toastAt time.Time

	// reopen: the pages of a program opened again (warm.go) that its
	// runtime is to send again.
	reopen map[string]bool

	mu       sync.Mutex
	updates  map[string]json.RawMessage
	flushing bool
	flushT   *time.Timer
}

// after runs f on the dispatch goroutine in d, unless the runtime is
// stopped first.
func (c *child) after(d time.Duration, f func()) {
	c.timers = append(c.timers, time.AfterFunc(d, func() { c.h.app.Post(f) }))
}

// module is the runtime module of p: runtime.js after p's data, which is
// one JSON string (JSON.parse keeps "__proto__" a plain key).
func (h *host) module(p *program) ([]byte, error) {
	data, err := json.Marshal(map[string]any{
		"id": p.meta.ID, "name": p.meta.Name, "source": p.source, "msgs": nonNil(p.msgs),
		"storage": p.allows("storage"), "quota": storageQuota, "prelude": prelude,
	})
	if err != nil {
		return nil, err
	}
	lit, err := json.Marshal(string(data))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("const PROGRAM = JSON.parse(")
	b.Write(lit)
	b.WriteString(");\n")
	b.WriteString(runtimeJS)
	return b.Bytes(), nil
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// start opens p's index page, starting its runtime first; a program kept
// warm opens where it was.
func (h *host) start(p *program) error {
	if p.run != nil {
		if p.warm {
			h.show(p)
		}
		return nil
	}
	c, err := h.spawn(p)
	if err != nil {
		return err
	}
	p.run = c
	return nil
}

func (h *host) spawn(p *program) (*child, error) {
	if h.jsrt == "" {
		return nil, errors.New("no JavaScript runtime")
	}
	runDir := filepath.Join(h.app.DataDir, "mp-run", p.meta.ID)
	data := h.storeDir(p)
	for _, d := range []string{runDir, data} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	migrateStorage(data)
	mod, err := h.module(p)
	if err != nil {
		return nil, err
	}
	main := filepath.Join(runDir, jsrt.DefaultMain)
	if err := writeFile(main, mod); err != nil {
		return nil, err
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent, theirs := os.NewFile(uintptr(fds[0]), "mp-parent"), os.NewFile(uintptr(fds[1]), "mp-child")
	defer theirs.Close()
	env := []string{"PHONE_UI_FD=3", "PHONE_APP_ID=" + h.app.ID + "/" + p.meta.ID, "PHONE_LANG=" + i18n.Lang()}
	if h.budget > 0 {
		env = append(env, jsrt.BudgetEnv+"="+strconv.FormatInt(h.budget.Milliseconds(), 10))
	}
	if p.allows("storage") {
		env = append(env, "PHONE_DATA_DIR="+data)
	}
	argv := []string{h.jsrt, "--no-bytecode", main}
	c := &child{h: h, prog: p, sock: parent, out: make(chan []byte, outQueue), updates: map[string]json.RawMessage{},
		toasts: toastBurst, toastAt: time.Now()}
	var started error
	if h.sandbox != "" {
		policy, _ := json.Marshal(sandbox.Policy{DataDir: data, ReadOnly: []string{"/etc", runDir},
			Code: []string{"/usr", "/lib", "/lib64", "/bin", "/sbin", filepath.Dir(h.jsrt)}})
		c.cmd = c.command(append([]string{h.sandbox, string(policy)}, argv...), env, theirs)
		if started = c.cmd.Start(); started != nil {
			log.Printf("%s: %s: %v; running it in the host's sandbox only", p.meta.ID, h.sandbox, started)
		}
	}
	if h.sandbox == "" || started != nil {
		c.cmd = c.command(argv, env, theirs)
		if err := c.cmd.Start(); err != nil {
			parent.Close()
			return nil, err
		}
	}
	h.wg.Add(2)
	go func() {
		defer h.wg.Done()
		c.read()
	}()
	go c.write()
	go func() {
		defer h.wg.Done()
		err := c.cmd.Wait()
		h.app.Post(func() { c.ended(err) })
	}()
	c.after(startLimit, func() {
		if !c.gone && !c.hello {
			c.fail(tr.T("Cannot start the mini-program: %s", tr.T("its runtime did not start")))
		}
	})
	return c, nil
}

func (c *child) command(argv, env []string, conn *os.File) *exec.Cmd {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.ExtraFiles = []*os.File{conn}
	cmd.Stderr = &logLines{c: c}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return cmd
}

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// migrateStorage moves the store of a program that last ran in the host
// itself (storage.json: key -> value) into kario-jsrt's (key -> JSON text).
func migrateStorage(dir string) {
	old := filepath.Join(dir, "storage.json")
	raw, err := os.ReadFile(old)
	if err != nil {
		return
	}
	var vals map[string]json.RawMessage
	if json.Unmarshal(raw, &vals) == nil {
		texts := map[string]string{}
		for k, v := range vals {
			texts[k] = string(v)
		}
		out, _ := json.Marshal(texts)
		if err := statefile.Write(filepath.Join(dir, jsrt.StorageFile), out, 0o600); err != nil {
			return
		}
	}
	_ = os.Remove(old)
}

// send queues m for the runtime and pings after it.
func (c *child) send(m ui.Msg) {
	if c.gone {
		return
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	select {
	case c.out <- append(raw, '\n'):
	default:
		c.fail(tr.T("Mini-program stopped: %s", tr.T("it stopped reading")))
		return
	}
	if m.T != ui.MsgPing && !c.pingDue && c.ping == "" {
		c.pingDue = true
		c.after(pingAfter, c.sendPing)
	}
}

func (c *child) sendPing() {
	c.pingDue = false
	if c.gone || c.ping != "" {
		return
	}
	c.pings++
	c.ping = strconv.Itoa(c.pings)
	sent := c.ping
	c.send(ui.Msg{T: ui.MsgPing, Value: sent})
	// The second look: a host the Shell froze in the background fires its
	// timers on thawing, maybe before it reads the pong waiting for it.
	c.after(c.h.hang, func() {
		if !c.gone && c.ping == sent {
			c.after(hangGrace, func() {
				if !c.gone && c.ping == sent {
					c.fail(tr.T("Mini-program stopped: %s", tr.T("it is not responding")))
				}
			})
		}
	})
}

func (c *child) write() {
	for b := range c.out {
		if _, err := c.sock.Write(b); err != nil {
			return
		}
	}
}

// stop ends the runtime: it has no page left, or the host is done with it.
// SIGTERM lets it finish the storage write under way first.
func (c *child) stop() {
	if c.gone {
		return
	}
	c.gone = true
	if p := c.prog; p.run == c {
		p.run, p.warm, p.shelf = nil, false, nil
	}
	for _, t := range c.timers {
		t.Stop()
	}
	c.mu.Lock()
	if c.flushT != nil {
		c.flushT.Stop()
	}
	c.flushing = true
	c.mu.Unlock()
	close(c.out)
	p := c.cmd.Process
	_ = p.Signal(syscall.SIGTERM)
	time.AfterFunc(stopGrace, func() { _ = p.Kill() })
	c.sock.Close()
}

// fail ends the runtime, says why (in the log only when it was kept warm,
// off the screen) and closes its pages.
func (c *child) fail(why string) {
	if c.gone {
		return
	}
	log.Printf("%s: %s", c.prog.meta.ID, why)
	warm := c.prog.warm
	c.stop()
	if !warm {
		c.h.app.Toast(why)
	}
	c.closePages()
}

// closePages removes the pages of a runtime that is gone: those on top of
// the stack close, the rest are forgotten.
func (c *child) closePages() {
	h := c.h
	h.exit(c.prog)
	for id, pg := range h.pages {
		if pg.prog == c.prog && !slices.Contains(h.stack, id) {
			delete(h.pages, id)
		}
	}
}

func (c *child) ended(err error) {
	if c.gone {
		return
	}
	why := tr.T("it exited")
	if err != nil {
		why = err.Error()
	}
	c.fail(tr.T("Mini-program stopped: %s", why))
}

// msg is a message from a runtime: phone.Ui's, as kario-jsrt writes them,
// kept raw where the host checks them itself.
type msg struct {
	T      string          `json:"t"`
	Screen json.RawMessage `json:"screen"`
	Text   string          `json:"text"`
	Req    int             `json:"req"`
	Cap    string          `json:"cap"`
	Args   json.RawMessage `json:"args"`
	Value  string          `json:"value"`
}

func (c *child) read() {
	r := bufio.NewReaderSize(c.sock, 64<<10)
	for {
		line, err := readLine(r)
		if errors.Is(err, errTooLong) {
			c.h.app.Post(func() {
				c.fail(tr.T("Mini-program stopped: %s", tr.T("it sent a message over %d KB", maxLine>>10)))
			})
		}
		if err != nil {
			return
		}
		var m msg
		if err := json.Unmarshal(line, &m); err != nil {
			c.h.app.Post(func() { c.badMsg("not JSON") })
			continue
		}
		if m.T == ui.MsgUpdate {
			c.queueUpdate(m.Screen)
			continue
		}
		c.h.app.Post(func() { c.handle(m) })
	}
}

var errTooLong = errors.New("line too long")

func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > maxLine {
			return nil, errTooLong
		}
		line = append(line, part...)
		if err == nil {
			return bytes.TrimSuffix(line, []byte("\n")), nil
		}
		if err != bufio.ErrBufferFull {
			return nil, err
		}
	}
}

// queueUpdate keeps a page's latest content: a program that calls setData
// in a loop sends the Shell a page per updateEvery, not one per call.
func (c *child) queueUpdate(raw json.RawMessage) {
	var id struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &id)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates[id.ID] = raw
	if !c.flushing {
		c.flushing = true
		c.flushT = time.AfterFunc(updateEvery, func() { c.h.app.Post(c.flush) })
	}
}

func (c *child) flush() {
	c.mu.Lock()
	updates := c.updates
	c.updates = map[string]json.RawMessage{}
	c.flushing = c.gone
	c.mu.Unlock()
	for id, raw := range updates {
		if c.gone {
			return
		}
		pg := c.h.pages[id]
		if pg == nil || pg.prog != c.prog {
			continue
		}
		if s, ok := c.screen(raw); ok {
			c.h.app.Update(s)
		}
	}
}

func (c *child) badMsg(why string) {
	c.bad++
	log.Printf("%s: %s", c.prog.meta.ID, why)
	if c.bad >= badLimit {
		c.fail(tr.T("Mini-program stopped: %s", tr.T("it sent %d messages the host cannot use", badLimit)))
	}
}

func (c *child) handle(m msg) {
	if c.gone {
		return
	}
	switch m.T {
	case ui.MsgHello:
		if c.hello {
			c.badMsg("hello again")
			return
		}
		c.hello = true
		// The engine runs now, the program's code not yet: it runs when
		// start arrives, under the limit.
		lim := &unix.Rlimit{Cur: runtimeMemory, Max: runtimeMemory}
		if err := unix.Prlimit(c.cmd.Process.Pid, unix.RLIMIT_DATA, lim, nil); err != nil {
			log.Printf("%s: memory limit: %v", c.prog.meta.ID, err)
		}
		c.send(ui.Msg{T: ui.MsgStart, Value: "index"})
	case ui.MsgPong:
		if m.Value == c.ping {
			c.ping = ""
		}
	case ui.MsgReset, ui.MsgPush:
		// Its pages again, the first as reset, once it was opened again
		// (resync); a reset is nothing else.
		if !c.reshow(m.Screen) {
			if m.T == ui.MsgReset {
				c.badMsg("refused " + strconv.Quote(m.T))
			} else {
				c.pushPage(m.Screen)
			}
		}
	case ui.MsgPop:
		c.popPage()
	case ui.MsgToast:
		c.toast(m.Text)
	case ui.MsgReq:
		c.request(m)
	default:
		c.badMsg("refused " + strconv.Quote(m.T))
	}
}

func (c *child) toast(text string) {
	if c.prog.warm {
		log.Printf("%s: closed, not shown: %s", c.prog.meta.ID, text)
		return
	}
	now := time.Now()
	c.toasts = min(toastBurst, c.toasts+float64(now.Sub(c.toastAt))/float64(toastEvery))
	c.toastAt = now
	if c.toasts < 1 {
		return
	}
	c.toasts--
	c.h.app.Toast(text)
}

// screen turns a page the runtime sent into the host's: its ID is one of
// the program's, its body phone.Ui nodes (a warning when it is not), its
// actions the program's and the host's own.
func (c *child) screen(raw json.RawMessage) (ui.Screen, bool) {
	s, ok := parsePage(c.prog, raw)
	if !ok {
		c.badMsg("a page that is not the program's")
	}
	return s, ok
}

func parsePage(p *program, raw json.RawMessage) (ui.Screen, bool) {
	var in struct {
		ID      string          `json:"id"`
		Title   string          `json:"title"`
		Body    json.RawMessage `json:"body"`
		Actions json.RawMessage `json:"actions"`
	}
	if json.Unmarshal(raw, &in) != nil || !strings.HasPrefix(in.ID, "mp:"+p.meta.ID+":") || len(in.ID) > maxScreenID {
		return ui.Screen{}, false
	}
	s := ui.Screen{ID: in.ID, Title: in.Title}
	if s.Title == "" {
		s.Title, _ = p.meta.text()
	}
	if len(in.Body) > 0 && string(in.Body) != "null" {
		if err := json.Unmarshal(in.Body, &s.Body); err != nil {
			s.Body = []ui.Node{{Kind: ui.KindText, Style: ui.StyleWarn, Text: tr.T("render() did not return phone.Ui nodes: %s", err.Error())}}
		}
	}
	if len(in.Actions) > 0 {
		if json.Unmarshal(in.Actions, &s.Actions) != nil {
			s.Actions = nil
		}
	}
	s.Actions = append(s.Actions,
		ui.Action{ID: "mp.info", Label: tr.T("About this mini-program"), Key: "?"},
		ui.Action{ID: "mp.exit", Label: tr.T("Exit the mini-program"), Key: "Q"})
	if out, err := json.Marshal(s); err != nil || len(out) > maxPage {
		s.Body = []ui.Node{{Kind: ui.KindText, Style: ui.StyleWarn, Text: tr.T("The page is over %d KB and is not shown", maxPage>>10)}}
	}
	return s, true
}

func (c *child) pushPage(raw json.RawMessage) {
	s, ok := c.screen(raw)
	if !ok {
		return
	}
	h := c.h
	if h.pages[s.ID] != nil {
		c.badMsg("a page pushed twice")
		return
	}
	if c.prog.warm {
		c.shelve(s)
		return
	}
	if h.pageCount(c.prog) >= maxPages {
		c.send(ui.Msg{T: ui.MsgPopped, ScreenID: s.ID})
		c.toast(tr.T("A mini-program opens at most %d pages", maxPages))
		return
	}
	h.pages[s.ID] = &page{prog: c.prog, screen: s.ID}
	h.push(s)
}

// reshow puts a page of a program opened again back on the stack, if raw
// is one it is to send again.
func (c *child) reshow(raw json.RawMessage) bool {
	var id struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &id) != nil || !c.reopen[id.ID] {
		return false
	}
	delete(c.reopen, id.ID)
	if s, ok := c.screen(raw); ok {
		c.h.push(s)
	}
	return true
}

// popPage closes the top page when it is the program's.
func (c *child) popPage() {
	h := c.h
	if c.prog.warm {
		c.unshelve()
		return
	}
	if n := len(h.stack); n > 0 {
		if top := h.pages[h.stack[n-1]]; top != nil && top.prog == c.prog {
			h.pop()
		}
	}
}

func (c *child) reply(req int, data any, err error) {
	c.calls--
	m := ui.Msg{T: ui.MsgRes, Req: req}
	if err != nil {
		m.Err = err.Error()
	} else {
		m.OK = true
		m.Data, _ = json.Marshal(data)
	}
	c.send(m)
}

func (c *child) request(m msg) {
	c.calls++
	if c.calls > maxCalls {
		c.reply(m.Req, nil, errors.New(ui.ErrBusy))
		return
	}
	switch m.Cap {
	case bridgeError:
		var e struct{ Kind, Message string }
		_ = json.Unmarshal(m.Args, &e)
		c.reply(m.Req, nil, nil)
		c.report(e.Kind, e.Message)
	case bridgeScan, bridgePay:
		if c.prog.warm {
			c.reply(m.Req, nil, errors.New(errHidden))
			return
		}
		c.bridge(m)
	default:
		c.reply(m.Req, nil, fmt.Errorf("not an interface of mini-programs: %s", m.Cap))
	}
}

// bridge runs a call that shows the user something: the scanner, the
// payment's confirmation.
func (c *child) bridge(m msg) {
	switch m.Cap {
	case bridgeScan:
		c.h.scan(c, m.Req)
	case bridgePay:
		var a struct {
			Merchant string
			Amount   int64
		}
		if json.Unmarshal(m.Args, &a) != nil {
			c.reply(m.Req, nil, errors.New("invalid amount"))
			return
		}
		c.h.pay(c, m.Req, a.Merchant, a.Amount)
	}
}

// report shows what went wrong in the program; a start that failed ends
// the runtime.
func (c *child) report(kind, message string) {
	log.Printf("%s: %s", c.prog.meta.ID, message)
	switch kind {
	case errStart:
		c.fail(tr.T("Cannot start the mini-program: %s", firstLine(message)))
	case errPage:
		c.toast(tr.T("No such page: %s", firstLine(message)))
	default:
		c.toast(tr.T("Mini-program error: %s", firstLine(message)))
	}
}

func (h *host) scan(c *child, req int) {
	p := c.prog
	if !p.allows("scan") {
		c.reply(req, nil, errors.New("permission not declared by mini-program: scan"))
		return
	}
	var res ui.ScanResult
	h.app.GoAs(p.meta.Name, ui.CapScanQR, nil, &res, func(err error) {
		if err != nil {
			c.reply(req, nil, err)
			return
		}
		c.reply(req, map[string]any{"result": res.Text}, nil)
	})
}

// pay runs the host's payment: the system trusted UI signs a prompt that
// names the mini-program, and the host's server verifies before success.
func (h *host) pay(c *child, req int, merchant string, amount int64) {
	p := c.prog
	if !p.allows("payment") {
		c.reply(req, nil, errors.New("permission not declared by mini-program: payment"))
		return
	}
	if amount <= 0 {
		c.reply(req, nil, errors.New("invalid amount"))
		return
	}
	h.seq++
	order := fmt.Sprintf("MP%d%03d", time.Now().Unix(), h.seq)
	name, _ := p.meta.text()
	prompt := tr.T("Mini-program \"%s\" pays \"%s\" %s", name, merchant, yuan(amount))
	raw, _ := json.Marshal(map[string]any{"program": p.meta.ID, "merchant": merchant, "amount": amount, "order": order})
	payload := string(raw)
	var proof ui.KeyProof
	h.app.GoAs(p.meta.Name, ui.CapAuthConfirm, ui.AuthConfirmArgs{Key: "mp-pay", Prompt: prompt, Payload: payload}, &proof, func(err error) {
		if err == nil && h.root != nil {
			// The key is the mini-program's own ("host/origin"), so the
			// merchant's server learns which mini-program the user paid in.
			err = keystore.VerifyConfirm(h.root, &proof, h.app.ID+"/"+p.meta.Name, "mp-pay", prompt, payload)
		}
		if err != nil {
			c.reply(req, nil, err)
			return
		}
		c.reply(req, map[string]any{"orderId": order, "verified": true}, nil)
	})
}

func (h *host) stopAll() {
	for _, p := range h.progs {
		if p.run != nil {
			p.run.stop()
		}
	}
}

// waitRuntimes waits, at most stopGrace, for the runtimes stopped to end:
// the host's own end would kill them (Pdeathsig) before their last write.
func (h *host) waitRuntimes() {
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopGrace):
	}
}

// logLines puts a runtime's stderr (its console, the engine's reports) in
// the host's log, a burst per logWindow; an interrupted turn is the
// program's error.
type logLines struct {
	c     *child
	buf   []byte
	n     int
	since time.Time
}

const maxLogLine = 1 << 10

func (l *logLines) Write(b []byte) (int, error) {
	l.buf = append(l.buf, b...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			if len(l.buf) > maxLogLine {
				l.line(string(l.buf[:maxLogLine]))
				l.buf = l.buf[:0]
			}
			return len(b), nil
		}
		l.line(string(l.buf[:min(i, maxLogLine)]))
		l.buf = l.buf[i+1:]
	}
}

func (l *logLines) line(s string) {
	c := l.c
	why := ""
	switch {
	case strings.HasPrefix(s, jsrt.Interrupted):
		why = tr.T("a callback ran over %d ms and was interrupted", c.h.budget.Milliseconds())
	case strings.HasPrefix(s, jsrt.Uncaught) && !strings.HasSuffix(s, jsrt.InterruptedError):
		// Errors runtime.js cannot catch (QuickJS's InternalError: out of
		// memory, the engine's own failures).
		_, why, _ = strings.Cut(strings.TrimPrefix(s, jsrt.Uncaught), ": ")
	}
	if why != "" {
		c.h.app.Post(func() {
			switch {
			case c.gone:
			case c.h.pageCount(c.prog) == 0:
				c.fail(tr.T("Cannot start the mini-program: %s", why))
			default:
				c.toast(tr.T("Mini-program error: %s", why))
			}
		})
	}
	if now := time.Now(); now.Sub(l.since) > logWindow {
		l.n, l.since = 0, now
	}
	if l.n++; l.n <= logBurst {
		log.Printf("%s: %s", c.prog.meta.ID, s)
	}
}
