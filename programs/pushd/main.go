// phone-pushd keeps the device's one connection to the push gateway and hands
// messages to the shell, which shows notifications and wakes apps. Apps never
// hold connections to their own servers in the background: one connection for
// the whole device is what lets every app stay frozen.
//
//	phone-pushd --run-dir DIR --state-dir DIR (--gateway URL | --gateway-file PATH) [--keystore SOCK] [--gateway-cert PEM]
//
// Before connecting it registers its push identity with the gateway, proving
// with a keystored attestation (device certificate chained to the
// manufacturer root) that the identity lives on a genuine device. The
// connection is TLS to a pinned gateway certificate (--gateway-cert), and its
// heartbeat interval adapts to the network (push.Heartbeat), kept in the
// state directory across restarts.
//
// RPC on pushd.sock (mode 0600, the shell only):
//
//	register {app} -> {token}    token for an app server to address this app here
//	pending        -> [message]  received but not yet acknowledged
//	ack {id}                     the shell handled it; the gateway may forget it
//	state          -> {gateway, device, connected}
//
// Subscribers receive {"event":"push","msg":...} for every new message.
package pushd

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

type identity struct {
	Device string `json:"device"`
	Secret string `json:"secret"`
}

type daemon struct {
	gwFlag, gwFile string
	keystore       string
	certFile       string
	stateDir       string
	id             identity
	hub            *wire.Hub
	http           *http.Client
	stream         *http.Client // the long-lived connection: no overall timeout
	hb             push.Heartbeat

	mu        sync.Mutex
	gw        string
	connected bool
	lastRide  time.Time // the last keep-alive reset riding on other traffic
	pending   []push.Message
	tokens    map[string]string
}

func loadIdentity(stateDir string) (identity, error) {
	p := filepath.Join(stateDir, "push-identity.json")
	var id identity
	if raw, err := os.ReadFile(p); err == nil && json.Unmarshal(raw, &id) == nil && id.Device != "" {
		return id, nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return id, err
	}
	id = identity{Device: "dev-" + hex.EncodeToString(b[:8]), Secret: hex.EncodeToString(b[8:])}
	raw, _ := json.Marshal(id)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return id, err
	}
	return id, os.WriteFile(p, raw, 0o600)
}

// postAny posts to the gateway the device is connected to, then to the
// other instances until one answers.
func (d *daemon) postAny(path string, body, out any) error {
	gws, err := push.Gateways(d.gwFlag, d.gwFile)
	if err != nil {
		return err
	}
	d.mu.Lock()
	cur := d.gw
	d.mu.Unlock()
	order := []string{cur}
	for _, g := range gws {
		if g != cur {
			order = append(order, g)
		}
	}
	for _, gw := range order {
		if gw == "" {
			continue
		}
		if err = d.post(gw, path, body, out); err == nil {
			return nil
		}
	}
	return err
}

func (d *daemon) post(gw, path string, body any, out any) error {
	raw, _ := json.Marshal(body)
	resp, err := d.http.Post(gw+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("gateway %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type gwLine struct {
	T           string        `json:"t"`
	Msg         *push.Message `json:"msg"`
	HeartbeatMS int64         `json:"heartbeat_ms"`
}

// register proves the push identity to the gateway.
func (d *daemon) register(gw string) error {
	ks, err := wire.Dial(d.keystore)
	if err != nil {
		return fmt.Errorf("keystore: %w", err)
	}
	defer ks.Close()
	var att ui.Attestation
	if err := ks.Call("attest", map[string]string{"app": push.RegisterApp, "nonce": push.RegisterNonce(d.id.Device, d.id.Secret)}, &att); err != nil {
		return fmt.Errorf("attest: %w", err)
	}
	return d.post(gw, "/v1/device/register", map[string]any{"device": d.id.Device, "secret": d.id.Secret, "attestation": &att}, nil)
}

func (d *daemon) heartbeatPath() string { return filepath.Join(d.stateDir, "push-heartbeat.json") }

func (d *daemon) loadHeartbeat() {
	d.hb = push.NewHeartbeat()
	if raw, err := os.ReadFile(d.heartbeatPath()); err == nil {
		_ = json.Unmarshal(raw, &d.hb)
	}
	if d.hb.Cur < push.HeartbeatMin {
		d.hb = push.NewHeartbeat()
	}
}

func (d *daemon) saveHeartbeat() {
	raw, _ := json.Marshal(d.hb)
	_ = os.WriteFile(d.heartbeatPath(), raw, 0o600)
}

// session runs one gateway connection until it drops or goes silent for
// three heartbeats. A connection that outlives push.HeartbeatProbe pings
// asks for a longer interval; one that dies while idle shortens it.
func (d *daemon) session(gw string) error {
	if err := d.register(gw); err != nil {
		log.Printf("register: %v (connecting anyway)", err)
	}
	d.mu.Lock()
	iv := d.hb.Cur
	d.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"device": d.id.Device, "secret": d.id.Secret, "heartbeat_ms": iv.Milliseconds()})
	resp, err := d.stream.Post(gw+"/v1/device/connect", "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("connect: %s", resp.Status)
	}
	silence := 3 * iv
	watchdog := time.AfterFunc(silence, func() { resp.Body.Close() })
	defer watchdog.Stop()
	d.setConnected(gw, true)
	defer d.setConnected(gw, false)
	pings := 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var l gwLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		switch l.T {
		case "hello":
			if l.HeartbeatMS > 0 {
				silence = 3 * time.Duration(l.HeartbeatMS) * time.Millisecond
			}
		case "msg":
			if l.Msg != nil {
				d.received(*l.Msg)
			}
		case "ping":
			if pings++; pings == push.HeartbeatProbe {
				pings = 0
				d.longer(gw)
			}
		}
		watchdog.Reset(silence)
	}
	// Idle long enough that a NAT mapping could have expired: the interval
	// was too long for this network.
	d.mu.Lock()
	if pings > 0 || d.hb.Good == 0 {
		d.hb.Dropped()
		d.saveHeartbeat()
		log.Printf("gateway connection lost while idle; heartbeat now %s", d.hb.Cur)
	}
	d.mu.Unlock()
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("gateway closed the connection")
}

// longer: the connection held at the current interval; ask for the next.
func (d *daemon) longer(gw string) {
	d.mu.Lock()
	before := d.hb.Cur
	d.hb.Survived()
	d.saveHeartbeat()
	next := d.hb.Cur
	d.mu.Unlock()
	if next == before {
		return
	}
	go func(iv time.Duration) {
		body := map[string]any{"device": d.id.Device, "secret": d.id.Secret, "heartbeat_ms": iv.Milliseconds()}
		if err := d.post(gw, "/v1/device/heartbeat", body, nil); err != nil {
			log.Printf("heartbeat %s: %v", iv, err)
			return
		}
		log.Printf("heartbeat now %s", iv)
	}(next)
}

func (d *daemon) setConnected(gw string, on bool) {
	d.mu.Lock()
	d.gw, d.connected = gw, on
	d.mu.Unlock()
	log.Printf("gateway %s connected=%v", gw, on)
}

func (d *daemon) received(m push.Message) {
	d.mu.Lock()
	for _, p := range d.pending {
		if p.ID == m.ID {
			d.mu.Unlock()
			return
		}
	}
	d.pending = append(d.pending, m)
	d.mu.Unlock()
	d.hub.Broadcast(map[string]any{"event": svc.PushEvent, "msg": m})
}

// run keeps one connection up. When it fails or drops, the next gateway of
// the cluster is tried at once: instances share the device's state, so the
// device just reconnects; only when all of them have failed in a row does it
// back off.
func (d *daemon) run() {
	backoff, next, failed := time.Second, 0, 0
	for {
		gws, err := push.Gateways(d.gwFlag, d.gwFile)
		if err == nil {
			gw := gws[next%len(gws)]
			start := time.Now()
			err = d.session(gw)
			if time.Since(start) > 30*time.Second {
				backoff, failed = time.Second, 0
			}
			next++
			if failed++; failed < len(gws) {
				log.Printf("gateway %s: %v; trying the next", gw, err)
				continue
			}
		}
		failed = 0
		log.Printf("gateway: %v; retry in %s", err, backoff)
		time.Sleep(backoff)
		backoff = min(2*backoff, 30*time.Second)
	}
}

func (d *daemon) handle(method string, params json.RawMessage) (any, error) {
	var p struct {
		App string `json:"app"`
		ID  string `json:"id"`
	}
	_ = json.Unmarshal(params, &p)
	switch method {
	case svc.PushRegister:
		if p.App == "" {
			return nil, errors.New("register needs app")
		}
		d.mu.Lock()
		tok := d.tokens[p.App]
		d.mu.Unlock()
		if tok != "" {
			return map[string]string{"token": tok}, nil
		}
		var out struct {
			Token string `json:"token"`
		}
		if err := d.postAny("/v1/device/token", map[string]string{"device": d.id.Device, "secret": d.id.Secret, "app": p.App}, &out); err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.tokens[p.App] = out.Token
		d.mu.Unlock()
		return out, nil
	case svc.PushUnregister:
		// The app was uninstalled: forget its token here and void it at the
		// gateway, so its server gets 410 Gone instead of reaching a
		// reinstalled app with the same id.
		d.mu.Lock()
		delete(d.tokens, p.App)
		d.mu.Unlock()
		if p.App != "" {
			go func() {
				if err := d.postAny("/v1/device/revoke", map[string]string{"device": d.id.Device, "secret": d.id.Secret, "app": p.App}, nil); err != nil {
					log.Printf("revoke %s: %v", p.App, err)
				}
			}()
		}
		return map[string]bool{"ok": true}, nil
	case svc.PushRadioActive:
		// Other traffic has the radio up: let the gateway push its next ping
		// a full interval out (at most once per half interval).
		d.mu.Lock()
		gw, on, iv, last := d.gw, d.connected, d.hb.Cur, d.lastRide
		ride := on && time.Since(last) > iv/2
		if ride {
			d.lastRide = time.Now()
		}
		d.mu.Unlock()
		if ride {
			go func() {
				body := map[string]any{"device": d.id.Device, "secret": d.id.Secret, "heartbeat_ms": iv.Milliseconds()}
				if err := d.post(gw, "/v1/device/heartbeat", body, nil); err != nil {
					log.Printf("radio active: %v", err)
				}
			}()
		}
		return map[string]bool{"rode": ride}, nil
	case svc.PushPending:
		d.mu.Lock()
		defer d.mu.Unlock()
		return append([]push.Message{}, d.pending...), nil
	case svc.PushAck:
		d.mu.Lock()
		for i, m := range d.pending {
			if m.ID == p.ID {
				d.pending = append(d.pending[:i], d.pending[i+1:]...)
				break
			}
		}
		d.mu.Unlock()
		// Unacknowledged at the gateway means redelivered on reconnect,
		// which the shell deduplicates by id.
		go func() {
			if err := d.postAny("/v1/device/ack", map[string]string{"device": d.id.Device, "secret": d.id.Secret, "id": p.ID}, nil); err != nil {
				log.Printf("ack %s: %v", p.ID, err)
			}
		}()
		return map[string]bool{"ok": true}, nil
	case svc.PushState:
		gws, _ := push.Gateways(d.gwFlag, d.gwFile)
		d.mu.Lock()
		defer d.mu.Unlock()
		return map[string]any{"gateway": d.gw, "gateways": gws, "device": d.id.Device, "connected": d.connected,
			"gateway_cert": d.certFile, "heartbeat_ms": d.hb.Cur.Milliseconds()}, nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	runDir := flag.String("run-dir", "/run/phone", "directory for pushd.sock")
	stateDir := flag.String("state-dir", "/var/lib/phone", "device push identity")
	gw := flag.String("gateway", "", "push gateway URLs, comma-separated (instances of one cluster)")
	gwFile := flag.String("gateway-file", "", "read the gateway URLs from these files, comma-separated (written by phone-pushgw --addr-file)")
	ksSock := flag.String("keystore", "", "keystored socket for the device attestation (default <run-dir>/keystored.sock)")
	certFile := flag.String("gateway-cert", "", "pin the gateway's TLS certificate (PEM); without it the gateway is plain HTTP")
	flag.Parse()
	if *ksSock == "" {
		*ksSock = filepath.Join(*runDir, svc.KeystoredSock)
	}
	if (*gw == "") == (*gwFile == "") {
		log.Fatal("give exactly one of --gateway and --gateway-file")
	}
	id, err := loadIdentity(*stateDir)
	if err != nil {
		log.Fatal(err)
	}
	httpc, err := push.Client(*certFile, 10*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	stream, err := push.Client(*certFile, 0)
	if err != nil {
		log.Fatal(err)
	}
	d := &daemon{gwFlag: *gw, gwFile: *gwFile, keystore: *ksSock, certFile: *certFile, stateDir: *stateDir, id: id,
		tokens: map[string]string{}, http: httpc, stream: stream}
	d.loadHeartbeat()
	sock := filepath.Join(*runDir, svc.PushdSock)
	if d.hub, err = wire.ServeHub(sock, d.handle); err != nil {
		log.Fatal(err)
	}
	log.Printf("phone-pushd: device %s on %s", id.Device, sock)
	d.run()
}
