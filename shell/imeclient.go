package shell

import (
	"errors"
	"os"
	"time"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

// imeTimeout bounds every call to phone-imed: keys go through it, and a
// stuck input method must not freeze the Shell.
const imeTimeout = 2 * time.Second

// imeClient is the Shell's side of the input method. The engine runs in
// phone-imed when its socket exists, so a crash there costs the current
// composition, not the Shell; while imed is down (until it restarts) the
// built-in engine takes over, without the user dictionary, which stays
// imed's. Without an imed socket (unit tests, minimal images) the built-in
// engine is the engine and keeps the dictionary itself.
type imeClient struct {
	sock     string
	userDict string
	logf     func(string, ...any)

	keypad ime.Keypad
	mode   ime.Mode
	fuzzy  bool

	cl    *wire.Client
	retry time.Time
	local *ime.Engine
	st    ime.State
	// symbols is a pending request for the symbol table.
	symbols bool
}

func newIMEClient(sock, userDict string, keypad ime.Keypad, logf func(string, ...any)) *imeClient {
	c := &imeClient{keypad: keypad, mode: ime.Zh, logf: logf}
	if _, err := os.Stat(sock); err == nil {
		c.sock = sock
	} else {
		c.userDict = userDict
	}
	c.st = ime.State{Keypad: keypad, Mode: ime.Zh, Indicator: ime.New(keypad).Indicator()}
	return c
}

func (c *imeClient) engine() *ime.Engine {
	if c.local == nil {
		c.local = ime.New(c.keypad)
		if c.userDict != "" {
			c.local.SetUserDict(c.userDict)
		}
	}
	return c.local
}

// remote calls imed; false means use the built-in engine for this call. A
// composition started in the built-in engine finishes there.
func (c *imeClient) remote(method string, params, out any) bool {
	if c.sock == "" || c.local != nil && c.local.Composing() {
		return false
	}
	if c.cl == nil {
		if time.Now().Before(c.retry) {
			return false
		}
		cl, err := wire.Dial(c.sock)
		if err != nil {
			c.retry = time.Now().Add(2 * time.Second)
			return false
		}
		var st ime.State
		if err := cl.CallTimeout(svc.IMEOpen, map[string]any{"keypad": c.keypad, "mode": c.mode, "fuzzy": c.fuzzy}, &st, imeTimeout); err != nil {
			cl.Close()
			c.retry = time.Now().Add(2 * time.Second)
			return false
		}
		c.cl, c.st = cl, st
	}
	if err := c.cl.CallTimeout(method, params, out, imeTimeout); err != nil {
		c.logf("phone-imed: %v; using the built-in input method", err)
		c.cl.Close()
		c.cl, c.retry = nil, time.Now().Add(2*time.Second)
		return false
	}
	return true
}

func (c *imeClient) syncLocal() *ime.Engine {
	e := c.engine()
	if e.Keypad != c.keypad {
		e.SetKeypad(c.keypad)
	}
	if e.Mode != c.mode && !e.Composing() {
		e.SetMode(c.mode)
	}
	e.Fuzzy = c.fuzzy
	return e
}

func (c *imeClient) adopt(st ime.State) {
	c.st = st
	c.keypad, c.mode = st.Keypad, st.Mode
	if st.WantSymbols {
		c.symbols = true
	}
}

func (c *imeClient) fromLocal(e *ime.Engine) {
	c.adopt(e.State())
	e.WantSymbols = false
}

type imeKeyReply struct {
	Handled bool      `json:"handled"`
	Text    string    `json:"text"`
	State   ime.State `json:"state"`
}

func (c *imeClient) Handle(k ime.Key) (bool, string) {
	var r imeKeyReply
	if c.remote(svc.IMEKey, k, &r) {
		c.adopt(r.State)
		return r.Handled, r.Text
	}
	e := c.syncLocal()
	ok, text := e.Handle(k)
	c.fromLocal(e)
	return ok, text
}

func (c *imeClient) Tick() string {
	if !c.st.Tapping {
		return ""
	}
	var r imeKeyReply
	if c.remote(svc.IMETick, nil, &r) {
		c.adopt(r.State)
		return r.Text
	}
	e := c.syncLocal()
	text := e.Tick()
	c.fromLocal(e)
	return text
}

func (c *imeClient) Cancel() {
	var st ime.State
	if c.remote(svc.IMECancel, nil, &st) {
		c.adopt(st)
		return
	}
	e := c.syncLocal()
	e.Cancel()
	c.fromLocal(e)
}

func (c *imeClient) reopen() {
	if c.local != nil {
		c.local.Cancel()
	}
	var st ime.State
	if c.remote(svc.IMEOpen, map[string]any{"keypad": c.keypad, "mode": c.mode, "fuzzy": c.fuzzy}, &st) {
		c.adopt(st)
		return
	}
	c.fromLocal(c.syncLocal())
}

func (c *imeClient) SetKeypad(k ime.Keypad) {
	c.keypad, c.mode = k, ime.Zh
	c.reopen()
}

func (c *imeClient) SetMode(m ime.Mode) {
	c.mode = m
	c.reopen()
}

func (c *imeClient) SetFuzzy(on bool) {
	if c.fuzzy == on {
		return
	}
	c.fuzzy = on
	c.reopen()
}

// TakeSymbols reports (once) that a key asked for the symbol table.
func (c *imeClient) TakeSymbols() bool {
	s := c.symbols
	c.symbols = false
	return s
}

func (c *imeClient) Preload() {
	if c.sock == "" {
		ime.Preload()
	}
}

func (c *imeClient) Keypad() ime.Keypad { return c.keypad }
func (c *imeClient) Mode() ime.Mode     { return c.mode }
func (c *imeClient) Composing() bool    { return c.st.Composing }
func (c *imeClient) Active() bool       { return c.st.Active }
func (c *imeClient) Rows() []ime.Row    { return c.st.Rows }
func (c *imeClient) Preedit() string    { return c.st.Preedit }
func (c *imeClient) Indicator() string  { return c.st.Indicator }

// probe reconnects to phone-imed if it is due, without a key.
func (c *imeClient) probe() {
	if c.cl == nil {
		var st ime.State
		if c.remote(svc.IMEState, nil, &st) {
			c.adopt(st)
		}
	}
}

// Remote reports whether keys currently go to phone-imed.
func (c *imeClient) Remote() bool { return c.cl != nil && (c.local == nil || !c.local.Composing()) }

// IMERemote reports whether keys go to phone-imed; reconnect tries first
// if a reconnection is due.
func (c *Core) IMERemote(reconnect bool) bool {
	if reconnect {
		c.ime.probe()
	}
	return c.ime.Remote()
}

// IMEPid is phone-imed's process id.
func (c *Core) IMEPid() (int, error) {
	var st struct {
		Pid int `json:"pid"`
	}
	if !c.ime.remote(svc.IMEStats, nil, &st) || st.Pid == 0 {
		return 0, errors.New("phone-imed is not connected")
	}
	return st.Pid, nil
}
