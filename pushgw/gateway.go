// Package pushgw is the push gateway's open core (docs/21-push.md): the role
// APNs, FCM or a vendor push service plays. App servers post messages for a
// token; every device keeps one long-lived connection over which they
// arrive. Messages are stored and forwarded: they wait, until their TTL, for
// the device to come online and acknowledge them.
//
// HTTP, JSON bodies:
//
//	POST /v1/push             {token, kind, title, body, data, thread, collapse_id, priority, ttl} -> {id, online}  (app servers)
//	POST /v1/device/register  {device, secret, attestation} -> {ok}
//	POST /v1/device/connect   {device, secret, heartbeat_ms} -> JSON lines: hello, msg, ping
//	POST /v1/device/heartbeat {device, secret, heartbeat_ms}   change the live connection's interval
//	POST /v1/device/token     {device, secret, app} -> {token}
//	POST /v1/device/ack       {device, secret, id}
//	POST /v1/device/revoke    {device, secret, app}
//
// With a trust root a device must register first: a keystored attestation,
// chained to the manufacturer root, of its push identity. Without it devices
// are trusted on first use (development). With a state file the signing
// key, devices, install generations and queued messages survive a restart,
// so tokens app servers hold keep working. Join makes it one instance of a
// cluster (cluster.go).
//
// cmd/phone-pushgw runs it as the simulated gateway devices are tested
// against. A gateway built on it (the official one) sets the hooks: who may
// push for an app, how fast, which instances a device belongs on; and adds
// its own routes to Routes().
package pushgw

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"gitee.com/Trisia/gotlcp/tlcp"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/ui"
)

const maxQueue = 100

type device struct {
	Secret string         `json:"secret"`
	Queue  []push.Message `json:"queue,omitempty"`
	// Installs counts uninstalls per app: tokens of an earlier install are void.
	Installs map[string]int `json:"installs,omitempty"`
	// Attested is the keystore device ID that registered this identity.
	Attested string `json:"attested,omitempty"`
	// At versions Secret and Attested across a cluster (last writer wins).
	At int64 `json:"at,omitempty"`
	// Acked are tombstones of acknowledged or replaced messages (ID ->
	// expiry), so a peer's late copy is not queued again.
	Acked map[string]int64 `json:"acked,omitempty"`
	conn  chan push.Message
	hb    chan time.Duration // interval changes for the live connection
	gen   int
}

type Gateway struct {
	mu        sync.Mutex
	key       []byte
	devices   map[string]*device
	seq       int
	heartbeat time.Duration
	statePath string
	rootPath  string
	root      *keystore.Roots
	cl        *cluster // nil: a lone gateway
	node      string   // in message IDs, unique across a cluster

	// Authorize decides whether a POST /v1/push may reach the app on the
	// device its valid token names: a *StatusError picks the status, any
	// other error is 403 (default: holding the token is enough).
	Authorize func(r *http.Request, device, app string) error
	// Limit is asked before a push is queued or forwarded: an error refuses
	// it, with 429 unless it is a *StatusError (default: no limit).
	Limit func(r *http.Request, device, app, kind string) error
	// Owner names the instances a device belongs on, in the order to try
	// them, nil when it belongs here (default). Its requests are redirected
	// there (307) and pushes for it forwarded, signed with the cluster key,
	// so Owner needs Join.
	Owner func(device string) []string
}

// StatusError is a hook's refusal: the request fails with Code, and with a
// Retry-After header when RetryAfter is set (whole seconds, rounded up).
type StatusError struct {
	Code       int
	Err        error
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	if e.Err == nil {
		return http.StatusText(e.Code)
	}
	return e.Err.Error()
}

func (e *StatusError) Unwrap() error { return e.Err }

// moved: the device belongs on another instance (Owner).
type moved struct{ url string }

func (m *moved) Error() string { return "the device belongs on " + m.url }

type savedState struct {
	Key     string             `json:"key"`
	Seq     int                `json:"seq"`
	Devices map[string]*device `json:"devices"`
}

// New opens a gateway that keeps its signing key, devices and queued
// messages in state ("" for memory only). With trustRoot (root.pub) devices
// must register with an attestation chained to it; heartbeat is the
// keep-alive interval of device connections that ask for none.
func New(state, trustRoot string, heartbeat time.Duration) (*Gateway, error) {
	g := &Gateway{devices: map[string]*device{}, heartbeat: heartbeat, statePath: state, rootPath: trustRoot, node: newNodeID()}
	if err := g.load(); err != nil {
		return nil, err
	}
	return g, nil
}

// load reads the saved state, or makes a new signing key.
func (g *Gateway) load() error {
	if g.statePath != "" {
		if raw, err := os.ReadFile(g.statePath); err == nil {
			var st savedState
			if err := json.Unmarshal(raw, &st); err != nil {
				return fmt.Errorf("%s: %w", g.statePath, err)
			}
			key, err := base64.StdEncoding.DecodeString(st.Key)
			if err != nil || len(key) != 32 {
				return fmt.Errorf("%s: bad key", g.statePath)
			}
			g.key, g.seq, g.devices = key, st.Seq, st.Devices
			if g.devices == nil {
				g.devices = map[string]*device{}
			}
			log.Printf("state from %s: %d devices", g.statePath, len(g.devices))
			return nil
		}
	}
	g.key = make([]byte, 32)
	if _, err := rand.Read(g.key); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.save()
}

// save writes the state; call with g.mu held.
func (g *Gateway) save() error {
	if g.statePath == "" {
		return nil
	}
	raw, _ := json.Marshal(savedState{Key: base64.StdEncoding.EncodeToString(g.key), Seq: g.seq, Devices: g.devices})
	tmp := g.statePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, g.statePath)
}

func (g *Gateway) saveLocked() {
	if err := g.save(); err != nil {
		log.Printf("saving state: %v", err)
	}
}

// trustRoot is the manufacturer root devices must chain to, nil when the
// gateway trusts devices on first use. Read lazily: on a device the
// keystore writes it at its first start.
func (g *Gateway) trustRoot() (*keystore.Roots, error) {
	if g.rootPath == "" {
		return nil, nil
	}
	if g.root == nil {
		root, err := keystore.LoadRootPub(g.rootPath)
		if err != nil {
			return nil, fmt.Errorf("trust root: %w", err)
		}
		g.root = root
	}
	return g.root, nil
}

func (g *Gateway) owner(device string) []string {
	if g.Owner == nil {
		return nil
	}
	if urls := g.Owner(device); len(urls) > 0 {
		return urls
	}
	return nil
}

type deviceReq struct {
	Device      string `json:"device"`
	Secret      string `json:"secret"`
	App         string `json:"app,omitempty"`
	ID          string `json:"id,omitempty"`
	HeartbeatMS int64  `json:"heartbeat_ms,omitempty"`
}

// interval is the device's requested heartbeat, within what the gateway
// allows, or the gateway's default.
func (g *Gateway) interval(ms int64) time.Duration {
	if ms <= 0 {
		return g.heartbeat
	}
	return min(max(time.Duration(ms)*time.Millisecond, push.HeartbeatMin), push.HeartbeatMax)
}

type line struct {
	T           string        `json:"t"` // hello | msg | ping
	Msg         *push.Message `json:"msg,omitempty"`
	HeartbeatMS int64         `json:"heartbeat_ms,omitempty"`
}

func (g *Gateway) auth(r *http.Request) (*deviceReq, *device, error) {
	var q deviceReq
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil || q.Device == "" || len(q.Secret) < 16 {
		return nil, nil, errors.New("bad device request")
	}
	// Before any state: a device first seen here must not be trusted on
	// first use by an instance it does not belong on.
	if urls := g.owner(q.Device); urls != nil {
		return nil, nil, &moved{urls[0] + r.URL.Path}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	d := g.devices[q.Device]
	if d == nil {
		if g.rootPath != "" {
			return nil, nil, errors.New("device not registered (attestation required)")
		}
		at := time.Now().UnixMilli()
		d = g.applyDevice(q.Device, q.Secret, "", at)
		g.replicate(op{Kind: "device", Device: q.Device, Secret: q.Secret, At: at})
		g.saveLocked()
		log.Printf("new device %s (trusted on first use)", q.Device)
	}
	if d.Secret != q.Secret {
		return nil, nil, errors.New("device authentication failed")
	}
	return &q, d, nil
}

// register admits a device that proves, with a keystored attestation chained
// to the manufacturer root, that its push identity lives on a genuine
// device. A registered identity can only be taken over by the same device.
func (g *Gateway) register(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Device      string          `json:"device"`
		Secret      string          `json:"secret"`
		Attestation *ui.Attestation `json:"attestation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil || q.Device == "" || len(q.Secret) < 16 || q.Attestation == nil {
		fail(w, http.StatusBadRequest, errors.New("register needs device, secret and attestation"))
		return
	}
	if urls := g.owner(q.Device); urls != nil {
		fail(w, http.StatusTemporaryRedirect, &moved{urls[0] + r.URL.Path})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	root, err := g.trustRoot()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	if root != nil {
		if err := keystore.VerifyAttestation(root, q.Attestation, push.RegisterApp, push.RegisterNonce(q.Device, q.Secret)); err != nil {
			fail(w, http.StatusForbidden, fmt.Errorf("attestation: %w", err))
			return
		}
	}
	d := g.devices[q.Device]
	switch {
	case d == nil || d.Secret == "":
		log.Printf("device %s registered (attested %s)", q.Device, q.Attestation.DeviceID)
	case d.Secret == q.Secret:
		reply(w, map[string]bool{"ok": true})
		return
	case d.Attested != "" && d.Attested == q.Attestation.DeviceID:
	default:
		fail(w, http.StatusForbidden, errors.New("device id registered by another device"))
		return
	}
	at := time.Now().UnixMilli()
	g.applyDevice(q.Device, q.Secret, q.Attestation.DeviceID, at)
	g.replicate(op{Kind: "device", Device: q.Device, Secret: q.Secret, Attested: q.Attestation.DeviceID, At: at})
	g.saveLocked()
	reply(w, map[string]bool{"ok": true})
}

func (d *device) prune(now int64) {
	out := d.Queue[:0]
	for _, m := range d.Queue {
		if m.Expires > now {
			out = append(out, m)
		}
	}
	d.Queue = out
	for id, exp := range d.Acked {
		if exp <= now {
			delete(d.Acked, id)
		}
	}
}

// collapse drops queued messages the new one replaces.
func (d *device) collapse(m push.Message) {
	if m.Collapse == "" {
		return
	}
	out := d.Queue[:0]
	for _, q := range d.Queue {
		if q.App != m.App || q.Collapse != m.Collapse {
			out = append(out, q)
		}
	}
	d.Queue = out
}

// fail answers with code, or with what a hook's *StatusError or a move to
// another instance says.
func fail(w http.ResponseWriter, code int, err error) {
	var se *StatusError
	var mv *moved
	switch {
	case errors.As(err, &mv):
		code = http.StatusTemporaryRedirect
		w.Header().Set("Location", mv.url)
	case errors.As(err, &se):
		code = se.Code
		if se.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.FormatInt(int64((se.RetryAfter+time.Second-1)/time.Second), 10))
		}
	}
	http.Error(w, err.Error(), code)
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// check is what any instance refuses in a push before queueing it.
func check(s push.Send) (int, error) {
	switch {
	case len(s.Data) > push.MaxData:
		return http.StatusRequestEntityTooLarge, fmt.Errorf("data over %d bytes", push.MaxData)
	case s.Title == "" && s.Data == "" && s.Kind != "voip":
		return http.StatusBadRequest, errors.New("empty push")
	case s.Kind != "" && s.Kind != "voip":
		return http.StatusBadRequest, fmt.Errorf("unknown kind %q", s.Kind)
	case len(s.Collapse) > push.MaxCollapse:
		return http.StatusBadRequest, fmt.Errorf("collapse_id over %d bytes", push.MaxCollapse)
	case s.Priority != "" && s.Priority != "high" && s.Priority != "normal":
		return http.StatusBadRequest, fmt.Errorf("unknown priority %q", s.Priority)
	}
	return 0, nil
}

func (g *Gateway) push(w http.ResponseWriter, r *http.Request) {
	var s push.Send
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	dev, app, gen, err := push.ParseTokenGen(g.key, s.Token)
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	if g.Authorize != nil {
		if err := g.Authorize(r, dev, app); err != nil {
			fail(w, http.StatusForbidden, err)
			return
		}
	}
	if code, err := check(s); err != nil {
		fail(w, code, err)
		return
	}
	if g.Limit != nil {
		if err := g.Limit(r, dev, app, s.Kind); err != nil {
			fail(w, http.StatusTooManyRequests, err)
			return
		}
	}
	if urls := g.owner(dev); urls != nil {
		g.forward(w, urls, s)
		return
	}
	g.queue(w, s, dev, app, gen)
}

// queue stores a checked push for a device that belongs here.
func (g *Gateway) queue(w http.ResponseWriter, s push.Send, dev, app string, gen int) {
	if s.Priority == "high" {
		s.Priority = ""
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = push.DefaultTTL
	}
	now := time.Now()
	g.mu.Lock()
	d := g.devices[dev]
	if d == nil {
		g.mu.Unlock()
		fail(w, http.StatusNotFound, errors.New("unknown device"))
		return
	}
	if gen != d.Installs[app] {
		g.mu.Unlock()
		// The app was uninstalled: the server should forget this token.
		fail(w, http.StatusGone, errors.New("push token revoked (app uninstalled)"))
		return
	}
	g.seq++
	m := push.Message{ID: fmt.Sprintf("m%d-%s%d", now.Unix(), g.node, g.seq), App: app, Kind: s.Kind, Title: s.Title, Body: s.Body,
		Data: s.Data, Thread: s.Thread, Collapse: s.Collapse, Priority: s.Priority, Sent: now.UnixMilli(), Expires: now.Add(time.Duration(ttl) * time.Second).UnixMilli()}
	ch, _ := g.applyMsg(dev, m)
	g.replicate(op{Kind: "msg", Device: dev, Msg: &m})
	g.saveLocked()
	g.mu.Unlock()
	if ch != nil {
		select {
		case ch <- m:
		default:
		}
	}
	reply(w, map[string]any{"id": m.ID, "online": ch != nil})
}

func (g *Gateway) connect(w http.ResponseWriter, r *http.Request) {
	q, d, err := g.auth(r)
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, errors.New("no streaming"))
		return
	}
	ch := make(chan push.Message, 64)
	hb := make(chan time.Duration, 1)
	interval := g.interval(q.HeartbeatMS)
	g.mu.Lock()
	d.gen++
	gen := d.gen
	d.conn, d.hb = ch, hb
	d.prune(time.Now().UnixMilli())
	backlog := append([]push.Message(nil), d.Queue...)
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		if d.gen == gen {
			d.conn, d.hb = nil, nil
		}
		g.mu.Unlock()
		log.Printf("device %s offline", q.Device)
	}()
	log.Printf("device %s online, %d queued", q.Device, len(backlog))
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	send := func(l line) bool {
		if enc.Encode(l) != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if !send(line{T: "hello", HeartbeatMS: interval.Milliseconds()}) {
		return
	}
	for i := range backlog {
		if !send(line{T: "msg", Msg: &backlog[i]}) {
			return
		}
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case m := <-ch:
			if !send(line{T: "msg", Msg: &m}) {
				return
			}
			// Traffic keeps the NAT mapping alive as well as a ping: the
			// next ping waits a full interval, so the radio wakes once.
			tick.Reset(interval)
		case iv := <-hb:
			interval = iv
			tick.Reset(iv)
			if !send(line{T: "hello", HeartbeatMS: iv.Milliseconds()}) {
				return
			}
		case <-tick.C:
			if !send(line{T: "ping"}) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// setHeartbeat changes the interval of the device's live connection: the
// device probes longer intervals as they prove to survive (push.Heartbeat).
func (g *Gateway) setHeartbeat(w http.ResponseWriter, r *http.Request) {
	q, d, err := g.auth(r)
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	iv := g.interval(q.HeartbeatMS)
	g.mu.Lock()
	hb := d.hb
	g.mu.Unlock()
	if hb == nil {
		fail(w, http.StatusConflict, errors.New("no live connection"))
		return
	}
	select {
	case hb <- iv:
	default:
	}
	reply(w, map[string]int64{"heartbeat_ms": iv.Milliseconds()})
}

func (g *Gateway) token(w http.ResponseWriter, r *http.Request) {
	q, _, err := g.auth(r)
	if err != nil || q.App == "" {
		fail(w, http.StatusForbidden, errors.Join(err, errors.New("token needs device and app")))
		return
	}
	g.mu.Lock()
	gen := g.devices[q.Device].Installs[q.App]
	g.mu.Unlock()
	reply(w, map[string]string{"token": push.TokenAt(g.key, q.Device, q.App, gen)})
}

// revoke voids an app's tokens on a device (the app was uninstalled).
func (g *Gateway) revoke(w http.ResponseWriter, r *http.Request) {
	q, d, err := g.auth(r)
	if err != nil || q.App == "" {
		fail(w, http.StatusForbidden, errors.Join(err, errors.New("revoke needs device and app")))
		return
	}
	g.mu.Lock()
	if d.Installs == nil {
		d.Installs = map[string]int{}
	}
	d.Installs[q.App]++
	g.replicate(op{Kind: "installs", Device: q.Device, App: q.App, N: d.Installs[q.App]})
	g.saveLocked()
	g.mu.Unlock()
	log.Printf("device %s: tokens of %s revoked", q.Device, q.App)
	reply(w, map[string]bool{"ok": true})
}

func (g *Gateway) ack(w http.ResponseWriter, r *http.Request) {
	q, _, err := g.auth(r)
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	g.mu.Lock()
	g.applyAck(q.Device, q.ID)
	g.replicate(op{Kind: "ack", Device: q.Device, ID: q.ID})
	g.saveLocked()
	g.mu.Unlock()
	reply(w, map[string]bool{"ok": true})
}

// Routes is the gateway's API; a gateway built on it adds its own routes.
// Call it after Join.
func (g *Gateway) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/push", g.push)
	mux.HandleFunc("POST /v1/device/register", g.register)
	mux.HandleFunc("POST /v1/device/heartbeat", g.setHeartbeat)
	mux.HandleFunc("POST /v1/device/connect", g.connect)
	mux.HandleFunc("POST /v1/device/token", g.token)
	mux.HandleFunc("POST /v1/device/ack", g.ack)
	mux.HandleFunc("POST /v1/device/revoke", g.revoke)
	if g.cl != nil {
		mux.HandleFunc("POST /v1/peer/ops", g.peerOps)
		mux.HandleFunc("GET /v1/peer/snapshot", g.peerSnapshot)
		mux.HandleFunc("POST /v1/peer/push", g.peerPush)
	}
	return mux
}

// Listen opens addr for a gateway, serving HTTPS when tlsDir is set: TLS
// 1.3, or TLCP with useTLCP, on the certificate kept there (made on first
// start; clients pin cert.pem). It returns the URL clients reach it at.
func Listen(addr, tlsDir string, useTLCP bool) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", err
	}
	if tlsDir == "" {
		return ln, "http://" + ln.Addr().String(), nil
	}
	host, _, _ := net.SplitHostPort(ln.Addr().String())
	if useTLCP {
		cfg, err := push.GatewayTLCP(tlsDir, host, "localhost")
		if err != nil {
			ln.Close()
			return nil, "", err
		}
		ln = tlcp.NewListener(ln, cfg)
	} else {
		cfg, err := push.GatewayTLS(tlsDir, host, "localhost")
		if err != nil {
			ln.Close()
			return nil, "", err
		}
		ln = tls.NewListener(ln, cfg)
	}
	return ln, "https://" + ln.Addr().String(), nil
}
