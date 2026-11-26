package switchrecv

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Limits and defaults of kario-switch/1 as this receiver keeps them.
const (
	Port        = 47800
	MaxBody     = 4 << 20 // a request's body, the envelope
	MaxBatch    = 500
	Chunk       = 1 << 20
	IdleTimeout = 30 * time.Minute
	// DefaultMaxFile bounds one photo or video; DefaultReserve is the free
	// space a session leaves on the disk.
	DefaultMaxFile = 4 << 30
	DefaultReserve = 256 << 20
	// maxFailed bounds the done reply's list; the counts go on.
	maxFailed = 1000
)

// Session states: waiting for the app's hello, receiving, and the three
// ways a session ends.
const (
	StateWaiting   = "waiting"
	StateReceiving = "receiving"
	StateDone      = "done"
	StateIdle      = "idle"
	StateClosed    = "closed"
)

// Config is one session's.
type Config struct {
	// Name is the device name the app shows; Host (an IPv4 literal) and
	// Port where the receiver listens; SSID and PSK the hotspot's, both
	// or neither; Wifi, never with them, the Wi-Fi network Host is on,
	// which the old phone joins itself.
	Name      string
	Host      string
	Port      int
	SSID, PSK string
	Wifi      string
	// Accept are the kinds the user ticked (Kinds' names).
	Accept []string
	Sink   Sink
	// Dir is where files are staged while they arrive (the session's own:
	// what is left there when it ends is deleted).
	Dir string
	// MaxFile bounds one file (DefaultMaxFile); MaxTotal all of them,
	// otherwise the free space of Dir's disk less Reserve (DefaultReserve).
	MaxFile, MaxTotal, Reserve int64
	// Idle ends the session after that long without a request that opens
	// (IdleTimeout).
	Idle time.Duration
	// Rand makes the key pair, the token and the session id (crypto/rand).
	Rand io.Reader
	// OnChange is called, from any goroutine and without locks held, after
	// the session's progress or state changed.
	OnChange func()
}

// Receiver serves the sessions open on it, keyed by their token: a request
// carries no token, so hello finds its session by the key that opens it,
// later requests by the app key that does.
type Receiver struct {
	logf func(format string, args ...any)
	// serial: one request at a time (the app sends them one after another).
	serial   sync.Mutex
	mu       sync.Mutex
	sessions map[string]*Session
	// last: the app key of the last request that opened, which a request
	// too large to read is answered under.
	last *appKey
}

// NewReceiver makes a receiver; logf (nil: none) gets what happens, never
// keys or content.
func NewReceiver(logf func(format string, args ...any)) *Receiver {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Receiver{logf: logf, sessions: map[string]*Session{}}
}

// appKey is one app key a session has seen: the app makes a new pair each
// time it starts, so a session can have several. Each keeps its own
// counters, so an old hello sent again is a replay and changes nothing.
type appKey struct {
	s        *Session
	pub      string
	up, down cipher.AEAD
	seen     bool
	last     uint64 // the highest upstream counter that opened
	next     uint64 // the next downstream counter
	ready    bool   // its hello went through
}

// Session is one pairing: its key pair and token, and what came in.
type Session struct {
	r     *Receiver
	cfg   Config
	priv  *ecdh.PrivateKey
	token []byte
	id    string

	mu      sync.Mutex
	state   string
	keys    map[string]*appKey
	current *appKey
	device  Device
	expect  map[string]int
	count   map[string]*KindProgress
	seen    map[string]map[[32]byte]bool
	failed  []Failure
	nfailed int
	files   map[string]*fileState
	// reserved: the bytes of the files offered and kept, against capacity.
	reserved, capacity int64
	received           int64
	timer              *time.Timer
	done               chan struct{}
}

// Device is what the app's hello says about the old phone.
type Device struct {
	App       string `json:"app"`
	Version   string `json:"version"`
	Brand     string `json:"brand"`
	Model     string `json:"model"`
	OS        string `json:"os"`
	OSVersion string `json:"os_version"`
}

// KindProgress is one accepted kind's: what hello said there is (0
// unknown: counts may come later or not at all), and what came of it.
type KindProgress struct {
	Kind     string `json:"kind"`
	Expected int    `json:"expected"`
	Stored   int    `json:"stored"`
	Skipped  int    `json:"skipped"`
	Failed   int    `json:"failed"`
	// Last: the app sent the kind's last batch.
	Last bool `json:"last,omitempty"`
}

// Failure is an item or file that did not come in; ID is the file's id,
// or the item's own when the app gave one.
type Failure struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Progress is a session's state for the screen.
type Progress struct {
	State  string
	Device Device
	Kinds  []KindProgress
	Failed int
	// Bytes of files received so far.
	Bytes int64
}

// Open starts a session: a new key pair and token, waiting for the app.
func (r *Receiver) Open(cfg Config) (*Session, error) {
	if ip := net.ParseIP(cfg.Host); ip == nil || ip.To4() == nil || strings.Contains(cfg.Host, ":") {
		return nil, fmt.Errorf("switch: host %q is not an IPv4 address", cfg.Host)
	}
	if (cfg.SSID == "") != (cfg.PSK == "") {
		return nil, errors.New("switch: the hotspot's name and passphrase go together")
	}
	if cfg.SSID != "" && cfg.Wifi != "" {
		return nil, errors.New("switch: a Wi-Fi network and a hotspot do not go together")
	}
	if cfg.Sink == nil || cfg.Dir == "" {
		return nil, errors.New("switch: no sink or staging directory")
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Reader
	}
	if cfg.Idle <= 0 {
		cfg.Idle = IdleTimeout
	}
	if cfg.MaxFile <= 0 {
		cfg.MaxFile = DefaultMaxFile
	}
	if cfg.Reserve <= 0 {
		cfg.Reserve = DefaultReserve
	}
	var accept []string
	for _, k := range Kinds {
		if slices.Contains(cfg.Accept, k) {
			accept = append(accept, k)
		}
	}
	cfg.Accept = accept
	var seed [keySize + tokenSize + 8]byte
	if _, err := io.ReadFull(cfg.Rand, seed[:]); err != nil {
		return nil, err
	}
	priv, err := ecdh.X25519().NewPrivateKey(seed[:keySize])
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, err
	}
	s := &Session{r: r, cfg: cfg, priv: priv, token: append([]byte(nil), seed[keySize:keySize+tokenSize]...),
		id: hex.EncodeToString(seed[keySize+tokenSize:]), state: StateWaiting, keys: map[string]*appKey{},
		expect: map[string]int{}, count: map[string]*KindProgress{}, seen: map[string]map[[32]byte]bool{},
		files: map[string]*fileState{}, done: make(chan struct{})}
	for _, k := range accept {
		s.count[k] = &KindProgress{Kind: k}
		s.seen[k] = map[[32]byte]bool{}
	}
	s.capacity = cfg.MaxTotal
	if s.capacity <= 0 {
		s.capacity = max(0, freeBytes(cfg.Dir)-cfg.Reserve)
	}
	s.timer = time.AfterFunc(cfg.Idle, func() { s.end(StateIdle) })
	r.mu.Lock()
	r.sessions[string(s.token)] = s
	r.mu.Unlock()
	r.logf("switch: session open on %s:%d for %s", cfg.Host, cfg.Port, strings.Join(accept, ","))
	return s, nil
}

func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// Pairing is what the session's QR code carries.
func (s *Session) Pairing() Pairing {
	return Pairing{Host: s.cfg.Host, Port: s.cfg.Port, SSID: s.cfg.SSID, PSK: s.cfg.PSK, Wifi: s.cfg.Wifi,
		Token: append([]byte(nil), s.token...), Key: s.priv.PublicKey().Bytes(), Name: s.cfg.Name}
}

// Accept is the kinds the session takes, in Kinds' order.
func (s *Session) Accept() []string { return slices.Clone(s.cfg.Accept) }

// Done is closed when the session has ended.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close ends the session (the user stopped it); the token is void.
func (s *Session) Close() { s.end(StateClosed) }

// Progress is a copy of the session's state.
func (s *Session) Progress() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := Progress{State: s.state, Device: s.device, Failed: s.nfailed, Bytes: s.received}
	for _, k := range s.cfg.Accept {
		c := *s.count[k]
		c.Expected = s.expect[k]
		p.Kinds = append(p.Kinds, c)
	}
	return p
}

func (s *Session) ended() bool {
	return s.state == StateDone || s.state == StateIdle || s.state == StateClosed
}

// end closes the session once: it leaves the receiver, so nothing opens
// under its keys again, and its staged files go.
func (s *Session) end(state string) {
	s.mu.Lock()
	if s.ended() {
		s.mu.Unlock()
		return
	}
	s.state = state
	s.timer.Stop()
	for _, f := range s.files {
		if !f.stored {
			_ = os.Remove(f.path)
		}
	}
	s.keys, s.current = map[string]*appKey{}, nil
	s.mu.Unlock()
	r := s.r
	r.mu.Lock()
	delete(r.sessions, string(s.token))
	if r.last != nil && r.last.s == s {
		r.last = nil
	}
	r.mu.Unlock()
	_ = os.Remove(s.cfg.Dir)
	r.logf("switch: session %s", state)
	close(s.done)
	s.changed()
}

func (s *Session) changed() {
	if s.cfg.OnChange != nil {
		s.cfg.OnChange()
	}
}

// Server is an http.Server for the receiver with the limits of a phone on
// a local network: slow clients are cut off, headers are small.
func (r *Receiver) Server() *http.Server {
	return &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute,
		WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10,
		ErrorLog: log.New(io.Discard, "", 0)}
}

// errorReply is an error's plaintext; Offset only for bad_offset.
type errorReply struct {
	Error  string `json:"error"`
	Code   string `json:"code"`
	Offset *int64 `json:"offset,omitempty"`
}

func fail(status int, code, msg string) (int, any) {
	return status, errorReply{Error: msg, Code: code}
}

// ServeHTTP answers one request. The AAD is the method and the request
// target as sent, its query included.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.serial.Lock()
	defer r.serial.Unlock()
	aad := req.Method + " " + req.RequestURI
	if req.ContentLength > MaxBody {
		r.tooLarge(w, aad)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, MaxBody+1))
	if err != nil {
		return
	}
	if len(body) > MaxBody {
		r.tooLarge(w, aad)
		return
	}
	hello := req.Method == http.MethodPost && req.URL.Path == "/v1/hello"
	var k *appKey
	var counter uint64
	var plain []byte
	if hello {
		k, counter, plain = r.openHello(req.Header.Get("X-Kario-Key"), body, aad)
	} else {
		k, counter, plain = r.openAny(body, aad)
	}
	if k == nil {
		unauthorized(w)
		return
	}
	s := k.s
	s.mu.Lock()
	if s.ended() {
		s.mu.Unlock()
		unauthorized(w)
		return
	}
	if k.seen && counter <= k.last {
		s.mu.Unlock()
		// Not a request: the idle timer does not move.
		st, v := fail(http.StatusConflict, "replay", "envelope counter not above the last one")
		r.reply(w, k, aad, st, v)
		return
	}
	k.seen, k.last = true, counter
	s.timer.Reset(s.cfg.Idle)
	s.mu.Unlock()
	r.mu.Lock()
	r.last = k
	r.mu.Unlock()
	st, v := s.route(k, req, plain)
	r.reply(w, k, aad, st, v)
	if req.URL.Path == "/v1/done" && st == http.StatusOK {
		// The end closes the server (the Shell's switchEnded), this
		// connection with it: the answer leaves first.
		_ = http.NewResponseController(w).Flush()
		s.end(StateDone)
		return
	}
	s.changed()
}

// openHello opens a hello: under the app key in its header, known to a
// session already or new to it.
func (r *Receiver) openHello(header string, body []byte, aad string) (*appKey, uint64, []byte) {
	pub, err := b64.Strict().DecodeString(header)
	if err != nil || len(pub) != keySize {
		return nil, 0, nil
	}
	for _, s := range r.open() {
		s.mu.Lock()
		k := s.keys[string(pub)]
		if k == nil && !s.ended() {
			if ks, err := deriveKeys(s.priv, pub, s.token); err == nil {
				k = &appKey{s: s, pub: string(pub), up: newAEAD(ks.up), down: newAEAD(ks.down)}
			}
		}
		s.mu.Unlock()
		if k == nil {
			continue
		}
		c, plain, err := open(k.up, dirUp, body, aad)
		if err != nil {
			continue
		}
		s.mu.Lock()
		if old := s.keys[k.pub]; old != nil {
			k = old
		} else if !s.ended() {
			s.keys[k.pub] = k
		}
		s.mu.Unlock()
		return k, c, plain
	}
	return nil, 0, nil
}

// openAny opens a request under any app key of an open session, the last
// one used first.
func (r *Receiver) openAny(body []byte, aad string) (*appKey, uint64, []byte) {
	r.mu.Lock()
	last := r.last
	r.mu.Unlock()
	var all []*appKey
	if last != nil {
		all = append(all, last)
	}
	for _, s := range r.open() {
		s.mu.Lock()
		for _, k := range s.keys {
			if k != last {
				all = append(all, k)
			}
		}
		s.mu.Unlock()
	}
	for _, k := range all {
		if c, plain, err := open(k.up, dirUp, body, aad); err == nil {
			return k, c, plain
		}
	}
	return nil, 0, nil
}

func (r *Receiver) open() []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out
}

// unauthorized is the answer to what does not open: plain JSON, as the
// other side has no keys to read anything else.
func unauthorized(w http.ResponseWriter) {
	plainJSON(w, http.StatusUnauthorized, errorReply{Error: "the envelope does not open", Code: "bad_key"})
}

func plainJSON(w http.ResponseWriter, status int, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// tooLarge answers a body over MaxBody, which is not read: under the app
// key of the last request that opened (the app sends one request after
// another), in plain JSON before any did.
func (r *Receiver) tooLarge(w http.ResponseWriter, aad string) {
	st, v := fail(http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("the body is over %d bytes", MaxBody))
	r.mu.Lock()
	k := r.last
	r.mu.Unlock()
	if k == nil {
		plainJSON(w, st, v)
		return
	}
	r.reply(w, k, aad, st, v)
}

// reply seals the answer under the app key's downstream key and the
// request's AAD; every reply, an error's too, takes the next counter.
func (r *Receiver) reply(w http.ResponseWriter, k *appKey, aad string, status int, v any) {
	plain, _ := json.Marshal(v)
	k.s.mu.Lock()
	c := k.next
	k.next++
	k.s.mu.Unlock()
	env := seal(k.down, dirDown, c, plain, aad)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(env)))
	w.WriteHeader(status)
	_, _ = w.Write(env)
}

// route answers a request that opened.
func (s *Session) route(k *appKey, req *http.Request, plain []byte) (int, any) {
	path, post, put := req.URL.Path, req.Method == http.MethodPost, req.Method == http.MethodPut
	if path == "/v1/hello" && post {
		return s.hello(k, plain)
	}
	s.mu.Lock()
	ready := k.ready
	s.mu.Unlock()
	if !ready {
		return fail(http.StatusConflict, "no_hello", "hello first")
	}
	switch {
	case post && strings.HasPrefix(path, "/v1/items/"):
		return s.items(strings.TrimPrefix(path, "/v1/items/"), plain)
	case post && path == "/v1/files":
		return s.offer(plain)
	case put && strings.HasPrefix(path, "/v1/files/"):
		return s.chunk(strings.TrimPrefix(path, "/v1/files/"), req.URL.Query(), plain)
	case post && path == "/v1/done":
		return s.finish(plain)
	}
	return fail(http.StatusNotFound, "not_found", "no such request")
}

type helloReply struct {
	Session  string   `json:"session"`
	Accept   []string `json:"accept"`
	MaxBatch int      `json:"max_batch"`
	Chunk    int      `json:"chunk"`
	MaxBody  int      `json:"max_body"`
}

func (s *Session) hello(k *appKey, plain []byte) (int, any) {
	var h struct {
		App     string `json:"app"`
		Version string `json:"version"`
		Device  struct {
			Brand     string `json:"brand"`
			Model     string `json:"model"`
			OS        string `json:"os"`
			OSVersion string `json:"os_version"`
		} `json:"device"`
		Counts map[string]int64 `json:"counts"`
	}
	if err := json.Unmarshal(plain, &h); err != nil {
		return fail(http.StatusBadRequest, "bad_json", "hello is not the JSON it should be")
	}
	if h.App != "kario-switch" {
		return fail(http.StatusBadRequest, "bad_hello", "not the Kario Switch app")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := h.Device
	s.device = Device{App: short(h.App), Version: short(h.Version), Brand: short(d.Brand), Model: short(d.Model), OS: short(d.OS), OSVersion: short(d.OSVersion)}
	// Counts may come partial (the app asks for permissions after
	// accept): a later hello fills in what an earlier one lacked.
	for kind, n := range h.Counts {
		if _, ok := s.count[kind]; ok && n >= 0 {
			s.expect[kind] = int(min(n, 1<<30))
		}
	}
	k.ready, s.current, s.state = true, k, StateReceiving
	s.r.logf("switch: hello from %s %s (%s %s)", s.device.Brand, s.device.Model, s.device.OS, s.device.OSVersion)
	return http.StatusOK, helloReply{Session: s.id, Accept: append([]string{}, s.cfg.Accept...), MaxBatch: MaxBatch, Chunk: Chunk, MaxBody: MaxBody}
}

// short is a hello field as the screen shows it: one line, 64 characters.
func short(v string) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(v, ""))
	if r := []rune(strings.TrimSpace(v)); len(r) > 64 {
		return string(r[:64])
	}
	return strings.TrimSpace(v)
}

type itemsReply struct {
	Stored  int `json:"stored"`
	Skipped int `json:"skipped"`
}

func (s *Session) items(kind string, plain []byte) (int, any) {
	if !itemKinds[kind] {
		return fail(http.StatusNotFound, "not_found", "no such kind")
	}
	if !slices.Contains(s.cfg.Accept, kind) {
		return fail(http.StatusForbidden, "not_accepted", "this phone does not take "+kind)
	}
	var b struct {
		Items []json.RawMessage `json:"items"`
		Last  bool              `json:"last"`
	}
	if err := json.Unmarshal(plain, &b); err != nil || b.Items == nil {
		return fail(http.StatusBadRequest, "bad_json", "a batch is {\"items\": [...], \"last\": ...}")
	}
	if len(b.Items) > MaxBatch {
		return fail(http.StatusBadRequest, "too_many", fmt.Sprintf("at most %d items a batch", MaxBatch))
	}
	var fresh []item
	var at []int
	// bad keeps the batch's order: what did not parse and what the sink
	// refused, by the item's place.
	bad := map[int]Failure{}
	skipped := 0
	s.mu.Lock()
	inBatch := map[[32]byte]bool{}
	for i, raw := range b.Items {
		it, err := parseItem(kind, raw)
		switch {
		case err != nil:
			bad[i] = Failure{Kind: kind, ID: it.id, Reason: err.Error()}
		case s.seen[kind][it.hash] || inBatch[it.hash]:
			skipped++
		default:
			inBatch[it.hash] = true
			fresh, at = append(fresh, it), append(at, i)
		}
	}
	s.mu.Unlock()
	results, err := s.store(kind, fresh)
	if err != nil {
		s.r.logf("switch: %s: %v", kind, err)
		return fail(http.StatusServiceUnavailable, "unavailable", "the phone cannot store these now; send them again later")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.count[kind]
	stored := 0
	for i, res := range results {
		it := fresh[i]
		switch res.Outcome {
		case Stored:
			stored++
			s.seen[kind][it.hash] = true
			s.clearFailure(kind, it.id)
		case Duplicate:
			skipped++
			s.seen[kind][it.hash] = true
		default:
			bad[at[i]] = Failure{Kind: kind, ID: it.id, Reason: firstNonEmpty(res.Reason, "rejected")}
		}
	}
	for _, i := range slices.Sorted(maps.Keys(bad)) {
		s.addFailure(bad[i])
	}
	c.Stored, c.Skipped, c.Last = c.Stored+stored, c.Skipped+skipped, c.Last || b.Last
	s.r.logf("switch: %s batch of %d: %d stored, %d skipped, %d failed", kind, len(b.Items), stored, skipped, len(bad))
	return http.StatusOK, itemsReply{Stored: stored, Skipped: skipped}
}

// store hands a batch to the sink, without the session's lock: the sink
// may wait for the Shell, which may be asking for the progress.
func (s *Session) store(kind string, items []item) ([]Result, error) {
	if len(items) == 0 {
		return nil, nil
	}
	sink := s.cfg.Sink
	var res []Result
	var err error
	switch kind {
	case KindContacts:
		res, err = sink.Contacts(values[string](items))
	case KindCallLog:
		res, err = sink.CallLog(values[CallEntry](items))
	case KindSMS:
		res, err = sink.SMS(values[Message](items))
	case KindCalendar:
		res, err = sink.Calendar(values[string](items))
	}
	if err == nil && len(res) != len(items) {
		err = fmt.Errorf("the sink gave %d results for %d items", len(res), len(items))
	}
	return res, err
}

func values[T any](items []item) []T {
	out := make([]T, len(items))
	for i, it := range items {
		out[i] = it.value.(T)
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// addFailure records one (mu); a kind's count goes on past maxFailed.
func (s *Session) addFailure(f Failure) {
	if c := s.count[pluralKind(f.Kind)]; c != nil {
		c.Failed++
	}
	s.nfailed++
	if f.ID != "" {
		s.clearFailure(f.Kind, f.ID)
	}
	if len(s.failed) < maxFailed {
		s.failed = append(s.failed, f)
	}
}

// clearFailure forgets an earlier failure of what now came in (mu).
func (s *Session) clearFailure(kind, id string) {
	if id == "" {
		return
	}
	for i, f := range s.failed {
		if f.Kind == kind && f.ID == id {
			s.failed = slices.Delete(s.failed, i, i+1)
			s.nfailed--
			if c := s.count[pluralKind(kind)]; c != nil {
				c.Failed--
			}
			return
		}
	}
}

func pluralKind(k string) string {
	switch k {
	case FilePhoto:
		return KindPhotos
	case FileVideo:
		return KindVideos
	}
	return k
}

// doneReply lists what the session stored; its fields keep this order.
type doneReply struct {
	Contacts int       `json:"contacts"`
	CallLog  int       `json:"call_log"`
	SMS      int       `json:"sms"`
	Calendar int       `json:"calendar"`
	Photos   int       `json:"photos"`
	Videos   int       `json:"videos"`
	Failed   []Failure `json:"failed"`
}

func (s *Session) finish(plain []byte) (int, any) {
	var v map[string]json.RawMessage
	if err := json.Unmarshal(plain, &v); err != nil {
		return fail(http.StatusBadRequest, "bad_json", "done takes {}")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := func(kind string) int {
		if c := s.count[kind]; c != nil {
			return c.Stored
		}
		return 0
	}
	return http.StatusOK, doneReply{Contacts: n(KindContacts), CallLog: n(KindCallLog), SMS: n(KindSMS), Calendar: n(KindCalendar),
		Photos: n(KindPhotos), Videos: n(KindVideos), Failed: append([]Failure{}, s.failed...)}
}
