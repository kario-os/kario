package usbshare

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/wire"
)

// The Library across two processes (27-usb-typec.md §3.2): the Shell keeps
// the library and serves a Helper on its end of a socket pair; the helper
// process (phone-usbshared), in a sandbox of its own, runs the Server over
// a Remote, so the code that parses what a computer sends never runs in
// the Shell. The helper opens no path: a file to download comes as a
// read-only descriptor the Shell opened, and an upload goes into the write
// end of a pipe whose read end the Shell's store reads. Messages are wire
// RPC lines, and either side may ask: the helper for the library, the
// Shell for a probe of the helper's sandbox (selftest).

const (
	msgHello   = "hello"
	msgFolders = "folders"
	msgList    = "list"
	msgListDir = "listdir"
	msgMakeDir = "mkdir"
	msgOpen    = "open"
	msgSave    = "save"
	msgEnd     = "end"
	msgLog     = "log"
	msgProbe   = "probe"
)

// Bounds on what one helper can make the Shell hold: requests being
// answered, uploads open, log lines a second and their length, a name.
const (
	maxRequests   = 256
	maxUploads    = 32
	maxLogsPerSec = 100
	maxLogLine    = 512
	maxNameBytes  = 4096
	maxNameDepth  = 16
	// endWait is how long the Shell waits, once an upload's pipe is at
	// its end, for the helper's word that it is complete, or the other
	// way round.
	endWait = 30 * time.Second
	// callWait bounds the helper's requests but the end of an upload,
	// which waits for the store.
	callWait = 30 * time.Second
)

// Hello is what the helper reports as it starts: the sandbox layers in
// force (sandbox.Env and its own), its uid and effective capabilities.
type Hello struct {
	Sandbox []string `json:"sandbox"`
	UID     int      `json:"uid"`
	Caps    uint64   `json:"caps"`
}

type args struct {
	Folder   string `json:"folder,omitempty"`
	Name     string `json:"name,omitempty"`
	ID       int    `json:"id,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Complete bool   `json:"complete,omitempty"`
	Line     string `json:"line,omitempty"`
}

type welcome struct {
	Password string `json:"password"`
	Lang     string `json:"lang"`
}

type folderList struct {
	Folders []Folder `json:"folders"`
	Lang    string   `json:"lang"`
}

type dirList struct {
	Items []Item `json:"items"`
	OK    bool   `json:"ok"`
}

var errClosed = errors.New("usbshare: the other side is gone")

type reply struct {
	rpc   wire.RPC
	files []*os.File
}

// peer is one end of the socket pair: requests both ways, answers matched
// by id; a request without an id is a notice and gets none.
type peer struct {
	c      *wire.Conn
	handle func(method string, params json.RawMessage) (any, []*os.File, error)
	limit  int // requests answered at once (0: any)

	mu      sync.Mutex
	next    int
	pending map[int]chan reply
	busy    int
	err     error
	done    chan struct{}
}

func newPeer(conn *net.UnixConn, handle func(string, json.RawMessage) (any, []*os.File, error)) *peer {
	return &peer{c: wire.New(conn), handle: handle, pending: map[int]chan reply{}, done: make(chan struct{})}
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		f.Close()
	}
}

func (p *peer) run() {
	defer func() {
		p.mu.Lock()
		p.err = errClosed
		for id, ch := range p.pending {
			ch <- reply{rpc: wire.RPC{ID: id, Err: errClosed.Error()}}
			delete(p.pending, id)
		}
		p.mu.Unlock()
		p.c.Close()
		close(p.done)
	}()
	for {
		var m wire.RPC
		if err := p.c.Recv(&m); err != nil {
			return
		}
		var files []*os.File
		if m.FDs > 0 {
			files = p.c.TakeFiles(m.FDs)
		}
		if m.Method != "" {
			closeFiles(files)
			p.mu.Lock()
			full := p.limit > 0 && p.busy >= p.limit
			if !full {
				p.busy++
			}
			p.mu.Unlock()
			if full {
				if m.ID != 0 {
					_ = p.c.Send(wire.RPC{ID: m.ID, Err: "busy"})
				}
				continue
			}
			go p.serve(m)
			continue
		}
		p.mu.Lock()
		ch := p.pending[m.ID]
		delete(p.pending, m.ID)
		p.mu.Unlock()
		if ch == nil {
			closeFiles(files)
			continue
		}
		ch <- reply{rpc: m, files: files}
	}
}

func (p *peer) serve(m wire.RPC) {
	defer func() {
		p.mu.Lock()
		p.busy--
		p.mu.Unlock()
	}()
	res, files, err := p.handle(m.Method, m.Params)
	defer closeFiles(files)
	if m.ID == 0 {
		return
	}
	resp := wire.RPC{ID: m.ID}
	if err != nil {
		resp.Err = err.Error()
		_ = p.c.Send(resp)
		return
	}
	resp.OK = true
	resp.Result, _ = json.Marshal(res)
	resp.FDs = len(files)
	_ = p.c.SendFiles(resp, files...)
}

// call asks the other side; wait 0 waits as long as it takes (or until
// the connection goes). The caller owns the files of the answer.
func (p *peer) call(method string, params, out any, wait time.Duration) ([]*os.File, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	ch := make(chan reply, 1)
	p.mu.Lock()
	if p.err != nil {
		p.mu.Unlock()
		return nil, p.err
	}
	p.next++
	id := p.next
	p.pending[id] = ch
	p.mu.Unlock()
	forget := func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}
	if err := p.c.Send(wire.RPC{ID: id, Method: method, Params: raw}); err != nil {
		forget()
		return nil, err
	}
	var r reply
	if wait > 0 {
		select {
		case r = <-ch:
		case <-time.After(wait):
			forget()
			return nil, fmt.Errorf("usbshare: no answer to %s", method)
		}
	} else {
		r = <-ch
	}
	if !r.rpc.OK {
		closeFiles(r.files)
		if r.rpc.Err == "" {
			r.rpc.Err = "refused"
		}
		return nil, errors.New(r.rpc.Err)
	}
	if out != nil && len(r.rpc.Result) > 0 {
		if err := json.Unmarshal(r.rpc.Result, out); err != nil {
			closeFiles(r.files)
			return nil, err
		}
	}
	return r.files, nil
}

func (p *peer) notify(method string, params any) {
	raw, _ := json.Marshal(params)
	_ = p.c.Send(wire.RPC{Method: method, Params: raw})
}

// --- The Shell's end ---

// HelperConfig is what the Shell lends one helper process.
type HelperConfig struct {
	Lib      Library
	Password string
	// Lang is the language the helper's pages are in, asked at every
	// listing of the folders (the user may change it while sharing).
	Lang func() string
	// Hello gets what the helper reported as it started.
	Hello func(Hello)
	// Log gets each line the helper logs, cleaned of control characters
	// and cut short.
	Log func(line string)
	// Added: a copy from the computer is in the library.
	Added func(folder, name string)
}

// Helper is the Shell's end of one helper process. Everything the helper
// sends is taken as untrusted: folders only from the library's own list,
// names only as names inside their folder, each folder's size limit
// counted again on the Shell's side, a copy kept only when the helper says
// it is complete and its size is what came through the pipe; descriptors
// the helper sends are closed unread.
type Helper struct {
	cfg HelperConfig
	p   *peer

	mu     sync.Mutex
	hello  *Hello
	ups    map[int]*upload
	nextUp int
	logN   int
	logAt  time.Time
}

// ServeHelper answers the helper on conn until it closes (Done).
func ServeHelper(conn *net.UnixConn, cfg HelperConfig) *Helper {
	if cfg.Lang == nil {
		cfg.Lang = i18n.Lang
	}
	h := &Helper{cfg: cfg, ups: map[int]*upload{}}
	h.p = newPeer(conn, h.handle)
	h.p.limit = maxRequests
	h.p.c.RefuseFiles()
	go h.p.run()
	go func() {
		<-h.p.done
		h.mu.Lock()
		for id, u := range h.ups {
			u.r.Close()
			delete(h.ups, id)
		}
		h.mu.Unlock()
	}()
	return h
}

// Done is closed when the helper's connection is gone.
func (h *Helper) Done() <-chan struct{} { return h.p.done }

// Close ends the connection: a helper ends with it.
func (h *Helper) Close() { _ = h.p.c.Close() }

// Reported is what the helper said as it started (false: not yet).
func (h *Helper) Reported() (Hello, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hello == nil {
		return Hello{}, false
	}
	return *h.hello, true
}

// Probe asks the helper to try what its sandbox must refuse: opening and
// making files by path, sockets, running a program, debugging one. The
// answer is each try's errno name ("ok" if it worked).
func (h *Helper) Probe() (map[string]string, error) {
	var out map[string]string
	files, err := h.p.call(msgProbe, nil, &out, 5*time.Second)
	closeFiles(files)
	return out, err
}

var (
	errNoFolder = errors.New("no such folder")
	errNoCopy   = errors.New("no such copy")
	// errIncomplete: the helper did not say the copy was complete, or
	// its size is not what came through the pipe.
	errIncomplete = errors.New("the copy did not complete")
)

func errBadName() error { return errors.New(tr.T("Not a name the phone takes for a file or folder")) }

func (h *Helper) folder(id string) (Folder, bool) {
	for _, f := range h.cfg.Lib.Folders() {
		if f.ID == id {
			return f, true
		}
	}
	return Folder{}, false
}

// checkName takes a name from the helper only as one inside its folder:
// valid UTF-8 without control characters, no empty, "." or ".." element,
// and folders (with "/", or a Windows "\") only in a nested folder.
func checkName(f Folder, name string) (string, error) {
	if name == "" || len(name) > maxNameBytes || !utf8.ValidString(name) {
		return "", errBadName()
	}
	parts := strings.Split(strings.ReplaceAll(name, `\`, "/"), "/")
	if len(parts) > 1 && !f.Nested || len(parts) > maxNameDepth {
		return "", errBadName()
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || len(p) > 255 {
			return "", errBadName()
		}
		for _, r := range p {
			if unicode.IsControl(r) {
				return "", errBadName()
			}
		}
	}
	return name, nil
}

func (h *Helper) handle(method string, raw json.RawMessage) (any, []*os.File, error) {
	if method == msgHello {
		return h.greet(raw)
	}
	if _, ok := h.Reported(); !ok {
		return nil, nil, errors.New("hello first")
	}
	var a args
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, nil, err
		}
	}
	switch method {
	case msgFolders:
		return folderList{Folders: h.cfg.Lib.Folders(), Lang: h.cfg.Lang()}, nil, nil
	case msgLog:
		h.log(a.Line)
		return nil, nil, nil
	case msgEnd:
		return h.end(a)
	}
	f, ok := h.folder(a.Folder)
	if !ok {
		return nil, nil, errNoFolder
	}
	switch method {
	case msgList:
		return h.cfg.Lib.List(f.ID), nil, nil
	case msgListDir:
		d, ok := h.cfg.Lib.(Dirs)
		name, err := checkName(f, a.Name)
		if !ok || !f.Nested || err != nil {
			return dirList{}, nil, nil
		}
		items, ok := d.ListDir(f.ID, name)
		return dirList{Items: items, OK: ok}, nil, nil
	case msgMakeDir:
		d, ok := h.cfg.Lib.(Dirs)
		if !ok || !f.Nested || !f.Writable {
			return nil, nil, errors.New(tr.T("This folder is read-only"))
		}
		name, err := checkName(f, a.Name)
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, d.MakeDir(f.ID, name)
	case msgOpen:
		name, err := checkName(f, a.Name)
		if err != nil {
			return nil, nil, os.ErrNotExist
		}
		return h.open(f, name)
	case msgSave:
		return h.save(f, a.Name)
	}
	return nil, nil, fmt.Errorf("unknown request %q", method)
}

func (h *Helper) greet(raw json.RawMessage) (any, []*os.File, error) {
	var hi Hello
	if err := json.Unmarshal(raw, &hi); err != nil {
		return nil, nil, err
	}
	h.mu.Lock()
	again := h.hello != nil
	if !again {
		h.hello = &hi
	}
	h.mu.Unlock()
	if again {
		return nil, nil, errors.New("hello again")
	}
	if h.cfg.Hello != nil {
		h.cfg.Hello(hi)
	}
	return welcome{Password: h.cfg.Password, Lang: h.cfg.Lang()}, nil, nil
}

func (h *Helper) log(line string) {
	h.mu.Lock()
	if now := time.Now(); now.Sub(h.logAt) >= time.Second {
		h.logAt, h.logN = now, 0
	}
	h.logN++
	n := h.logN
	h.mu.Unlock()
	if h.cfg.Log == nil || n > maxLogsPerSec {
		return
	}
	if len(line) > maxLogLine {
		line = line[:maxLogLine]
	}
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(line, "?"))
	if !strings.HasPrefix(line, "usb sharing: ") {
		line = "usb sharing: the helper: " + line
	}
	h.cfg.Log(line)
}

// open hands over a file the library opened: a regular file, read-only.
func (h *Helper) open(f Folder, name string) (any, []*os.File, error) {
	rc, it, err := h.cfg.Lib.Open(f.ID, name)
	if err != nil {
		return nil, nil, err
	}
	file, ok := rc.(*os.File)
	if !ok {
		rc.Close()
		return nil, nil, errors.New("not a file")
	}
	if err := readOnlyFile(file); err != nil {
		file.Close()
		return nil, nil, err
	}
	return it, []*os.File{file}, nil
}

func readOnlyFile(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	sc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var flags int
	var ferr error
	if err := sc.Control(func(fd uintptr) { flags, ferr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil {
		return err
	}
	if ferr != nil {
		return ferr
	}
	if flags&unix.O_ACCMODE != unix.O_RDONLY {
		return errors.New("not opened read-only")
	}
	return nil
}

type saved struct {
	name string
	err  error
}

// save starts a copy into the library: the helper gets a pipe's write end,
// the library reads the other end (upload) while the helper copies.
func (h *Helper) save(f Folder, name string) (any, []*os.File, error) {
	if !f.Writable {
		return nil, nil, errors.New(tr.T("This folder is read-only"))
	}
	name, err := checkName(f, name)
	if err != nil {
		return nil, nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	u := &upload{r: r, limit: f.limit(), verdict: make(chan args, 1), gone: h.p.done, result: make(chan saved, 1)}
	h.mu.Lock()
	if len(h.ups) >= maxUploads {
		h.mu.Unlock()
		r.Close()
		w.Close()
		return nil, nil, errors.New("too many copies at once")
	}
	h.nextUp++
	id := h.nextUp
	h.ups[id] = u
	h.mu.Unlock()
	go func() {
		got, err := h.cfg.Lib.Save(f.ID, name, u)
		r.Close()
		if err == nil && h.cfg.Added != nil {
			h.cfg.Added(f.ID, got)
		}
		u.result <- saved{got, err}
	}()
	return args{ID: id}, []*os.File{w}, nil
}

// end is the helper's word on a copy: complete (with its size) or not.
func (h *Helper) end(a args) (any, []*os.File, error) {
	h.mu.Lock()
	u := h.ups[a.ID]
	delete(h.ups, a.ID)
	h.mu.Unlock()
	if u == nil {
		return nil, nil, errNoCopy
	}
	u.verdict <- a
	if a.Complete {
		_ = u.r.SetReadDeadline(time.Now().Add(endWait))
	} else {
		u.r.Close()
	}
	res := <-u.result
	if res.err != nil {
		return nil, nil, res.err
	}
	return args{Name: res.name}, nil, nil
}

// upload is what the library reads a copy from: the pipe, at most the
// folder's limit, and an end only when the helper says the copy is
// complete with the size that came through. A Library's Save reads it to
// its end, which is where that is checked.
type upload struct {
	r       *os.File
	n       int64
	limit   int64
	verdict chan args
	gone    <-chan struct{}
	result  chan saved
	err     error
}

func (u *upload) Read(p []byte) (int, error) {
	if u.err != nil {
		return 0, u.err
	}
	if room := u.limit - u.n; int64(len(p)) > room {
		p = p[:room]
	}
	var n int
	var err error
	if len(p) > 0 {
		n, err = u.r.Read(p)
		u.n += int64(n)
	}
	if err == nil && u.n == u.limit {
		// Nothing more may come: the copy has to end here, before these
		// bytes are given out (a reader limited to the same size would
		// not ask again).
		var one [1]byte
		var m int
		for m == 0 && err == nil {
			m, err = u.r.Read(one[:])
		}
		if m > 0 {
			u.err = ErrTooLarge
			return 0, u.err
		}
	}
	if err == io.EOF {
		err = u.finish()
	}
	if err != nil {
		u.err = err
	}
	return n, err
}

func (u *upload) finish() error {
	var v args
	select {
	case v = <-u.verdict:
	case <-u.gone:
		return errIncomplete
	case <-time.After(endWait):
		return errIncomplete
	}
	if !v.Complete || v.Size != u.n {
		return errIncomplete
	}
	return io.EOF
}

// --- The helper's end ---

// Remote is the library as the helper's Server sees it: each call is a
// request to the Shell.
type Remote struct {
	p     *peer
	probe func() map[string]string
}

var (
	_ Library = (*Remote)(nil)
	_ Dirs    = (*Remote)(nil)
)

// Connect greets the Shell on conn with what the sandbox holds; the answer
// carries the connection's password. probe answers the Shell's probe.
func Connect(conn *net.UnixConn, hi Hello, probe func() map[string]string) (*Remote, string, error) {
	r := &Remote{probe: probe}
	r.p = newPeer(conn, r.handle)
	go r.p.run()
	var w welcome
	files, err := r.p.call(msgHello, hi, &w, callWait)
	closeFiles(files)
	if err != nil {
		r.p.c.Close()
		return nil, "", err
	}
	setLang(w.Lang)
	return r, w.Password, nil
}

func (r *Remote) handle(method string, _ json.RawMessage) (any, []*os.File, error) {
	if method == msgProbe && r.probe != nil {
		return r.probe(), nil, nil
	}
	return nil, nil, fmt.Errorf("unknown request %q", method)
}

// Done is closed when the Shell's end is gone: the helper's work is over.
func (r *Remote) Done() <-chan struct{} { return r.p.done }

// Logf is the Server's Log: the line goes to the Shell's log.
func (r *Remote) Logf(format string, a ...any) {
	r.p.notify(msgLog, args{Line: fmt.Sprintf(format, a...)})
}

func setLang(l string) {
	if l != "" && l != i18n.Lang() {
		i18n.SetLang(l)
	}
}

// shellErr is an error the Shell answered; a copy over its limit is
// ErrTooLarge again.
func shellErr(err error) error {
	if err.Error() == ErrTooLarge.Error() {
		return ErrTooLarge
	}
	return err
}

func (r *Remote) ask(method string, a, out any) error {
	files, err := r.p.call(method, a, out, callWait)
	closeFiles(files)
	if err != nil {
		return shellErr(err)
	}
	return nil
}

func (r *Remote) Folders() []Folder {
	var fl folderList
	if r.ask(msgFolders, nil, &fl) != nil {
		return nil
	}
	setLang(fl.Lang)
	return fl.Folders
}

func (r *Remote) List(folder string) []Item {
	var out []Item
	_ = r.ask(msgList, args{Folder: folder}, &out)
	return out
}

func (r *Remote) ListDir(folder, dir string) ([]Item, bool) {
	var d dirList
	if r.ask(msgListDir, args{Folder: folder, Name: dir}, &d) != nil {
		return nil, false
	}
	return d.Items, d.OK
}

func (r *Remote) MakeDir(folder, dir string) error {
	return r.ask(msgMakeDir, args{Folder: folder, Name: dir}, nil)
}

func (r *Remote) Open(folder, name string) (io.ReadSeekCloser, Item, error) {
	var it Item
	files, err := r.p.call(msgOpen, args{Folder: folder, Name: name}, &it, callWait)
	if err != nil {
		return nil, Item{}, err
	}
	if len(files) != 1 {
		closeFiles(files)
		return nil, Item{}, os.ErrNotExist
	}
	return files[0], it, nil
}

// Save copies body into the pipe the Shell hands over, then tells it
// whether the copy is complete: only then does the Shell keep it.
func (r *Remote) Save(folder, name string, body io.Reader) (string, error) {
	var st args
	files, err := r.p.call(msgSave, args{Folder: folder, Name: name}, &st, callWait)
	if err != nil {
		return "", shellErr(err)
	}
	if len(files) != 1 {
		closeFiles(files)
		return "", errors.New("usbshare: no pipe to copy into")
	}
	w := files[0]
	n, cerr := io.Copy(w, body)
	w.Close()
	var got args
	_, serr := r.p.call(msgEnd, args{ID: st.ID, Size: n, Complete: cerr == nil}, &got, 0)
	switch {
	case errors.Is(cerr, ErrTooLarge):
		return "", ErrTooLarge
	case serr != nil:
		return "", shellErr(serr)
	case cerr != nil:
		return "", cerr
	}
	return got.Name, nil
}
