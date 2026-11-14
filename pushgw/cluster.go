package pushgw

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/push"
)

// A cluster is gateways that share a secret (--cluster-key) and list each
// other (--peer-addr-file): any of them takes app servers' pushes and
// devices' connections, so one can restart or die and devices move to
// another without noticing. Every change to the state is an idempotent
// operation sent to every peer (full mesh, batched, retried): a device
// record is last-writer-wins by its version, a message is added once by ID
// (acknowledged and collapsed IDs leave tombstones so a late copy cannot
// bring them back), an install generation only grows. An instance merges a
// peer's snapshot when it starts and every half minute, which repairs what
// a dropped connection lost. The token signing key is derived from the
// cluster secret, so a token from one instance is valid at all. The
// instance holding a device's connection delivers its messages.

const (
	outboxSize  = 4096
	opsPerPost  = 64
	syncEvery   = 30 * time.Second
	maxClockGap = 2 * time.Minute
)

type op struct {
	Kind     string        `json:"kind"` // device | msg | ack | installs
	Device   string        `json:"device"`
	Secret   string        `json:"secret,omitempty"`
	Attested string        `json:"attested,omitempty"`
	At       int64         `json:"at,omitempty"`
	Msg      *push.Message `json:"msg,omitempty"`
	ID       string        `json:"id,omitempty"`
	App      string        `json:"app,omitempty"`
	N        int           `json:"n,omitempty"`
}

type cluster struct {
	key    []byte
	peers  []string // each peer's URL, or a file holding it (written by its --addr-file)
	client *http.Client
	outbox map[string]chan op
}

// Join makes g an instance of the cluster: it takes the token key derived
// from the secret, merges what the reachable peers have and starts sending
// them its changes. Peers are base URLs, or files a peer writes its URL to
// (read when needed). It returns how many snapshots it merged.
func (g *Gateway) Join(key []byte, client *http.Client, peers []string) int {
	g.cl = &cluster{key: key, client: client, peers: peers, outbox: map[string]chan op{}}
	for _, p := range peers {
		g.cl.outbox[p] = make(chan op, outboxSize)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("phone-push-token-key-v1"))
	g.key = mac.Sum(nil)
	n := g.syncFromPeers()
	for p, ch := range g.cl.outbox {
		go g.sendLoop(p, ch)
	}
	go g.syncLoop()
	log.Printf("cluster: node %s, %d peers, merged %d snapshots", g.node, len(peers), n)
	return n
}

// LoadClusterKey reads the shared secret (base64, 32 bytes).
func LoadClusterKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s: want 32 bytes, base64", path)
	}
	return key, nil
}

func newNodeID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *cluster) mac(method, path string, t int64, body []byte) string {
	h := hmac.New(sha256.New, c.key)
	fmt.Fprintf(h, "%s|%s|%d|", method, path, t)
	h.Write(body)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func (c *cluster) sign(req *http.Request, body []byte) {
	t := time.Now().Unix()
	req.Header.Set("X-Cluster-Time", strconv.FormatInt(t, 10))
	req.Header.Set("X-Cluster-MAC", c.mac(req.Method, req.URL.Path, t, body))
}

func (c *cluster) verify(r *http.Request, body []byte) error {
	t, err := strconv.ParseInt(r.Header.Get("X-Cluster-Time"), 10, 64)
	if err != nil || time.Since(time.Unix(t, 0)).Abs() > maxClockGap {
		return errors.New("peer request too old or undated")
	}
	if !hmac.Equal([]byte(r.Header.Get("X-Cluster-MAC")), []byte(c.mac(r.Method, r.URL.Path, t, body))) {
		return errors.New("peer request not signed with the cluster key")
	}
	return nil
}

func (c *cluster) peerURL(peer string) (string, error) {
	if strings.Contains(peer, "://") {
		return peer, nil
	}
	raw, err := os.ReadFile(peer)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// replicate queues an operation for every peer; a full outbox drops it
// (the periodic snapshot merge brings that peer up to date).
func (g *Gateway) replicate(o op) {
	if g.cl == nil {
		return
	}
	for peer, ch := range g.cl.outbox {
		select {
		case ch <- o:
		default:
			log.Printf("cluster: outbox for %s full; it catches up from a snapshot", peer)
		}
	}
}

func (g *Gateway) sendLoop(peer string, ch chan op) {
	for {
		batch := []op{<-ch}
	fill:
		for len(batch) < opsPerPost {
			select {
			case o := <-ch:
				batch = append(batch, o)
			default:
				break fill
			}
		}
		for attempt, wait := 0, 200*time.Millisecond; ; attempt++ {
			err := g.postOps(peer, batch)
			if err == nil {
				break
			}
			if attempt == 0 {
				log.Printf("cluster: %s: %v; retrying", peer, err)
			}
			time.Sleep(wait)
			wait = min(2*wait, 5*time.Second)
		}
	}
}

func (g *Gateway) postOps(peer string, ops []op) error {
	url, err := g.cl.peerURL(peer)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(ops)
	req, err := http.NewRequest(http.MethodPost, url+"/v1/peer/ops", bytes.NewReader(body))
	if err != nil {
		return err
	}
	g.cl.sign(req, body)
	resp, err := g.cl.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// peerOps applies a peer's operations; messages for a device connected
// here are delivered.
func (g *Gateway) peerOps(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err == nil {
		err = g.cl.verify(r, body)
	}
	var ops []op
	if err == nil {
		err = json.Unmarshal(body, &ops)
	}
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	g.mu.Lock()
	var out []delivery
	for _, o := range ops {
		out = append(out, g.apply(o)...)
	}
	g.saveLocked()
	g.mu.Unlock()
	deliver(out)
	reply(w, map[string]int{"applied": len(ops)})
}

// forward hands a push for a device that belongs elsewhere (Owner) to
// those instances, in order, and relays the first answer. Only an instance
// that cannot be reached is passed over: one that answered may have queued
// the push already.
func (g *Gateway) forward(w http.ResponseWriter, urls []string, s push.Send) {
	if g.cl == nil {
		fail(w, http.StatusInternalServerError, errors.New("forwarding needs a cluster key"))
		return
	}
	body, _ := json.Marshal(s)
	var err error
	for _, u := range urls {
		var req *http.Request
		if req, err = http.NewRequest(http.MethodPost, u+"/v1/peer/push", bytes.NewReader(body)); err != nil {
			continue
		}
		g.cl.sign(req, body)
		var resp *http.Response
		if resp, err = g.cl.client.Do(req); err != nil {
			continue
		}
		for _, h := range []string{"Content-Type", "X-Content-Type-Options", "Retry-After"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return
	}
	fail(w, http.StatusBadGateway, fmt.Errorf("the device's gateway instances cannot be reached: %w", err))
}

// peerPush queues a push another instance took from an app server for a
// device that belongs here; that instance has authorized and limited it.
func (g *Gateway) peerPush(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err == nil {
		err = g.cl.verify(r, body)
	}
	var s push.Send
	if err == nil {
		err = json.Unmarshal(body, &s)
	}
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	dev, app, gen, err := push.ParseTokenGen(g.key, s.Token)
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	if code, err := check(s); err != nil {
		fail(w, code, err)
		return
	}
	// The instances disagree on where the device belongs (their maps
	// differ): refuse rather than forward on, which could loop.
	if g.owner(dev) != nil {
		fail(w, http.StatusMisdirectedRequest, errors.New("the device does not belong on this instance"))
		return
	}
	g.queue(w, s, dev, app, gen)
}

type delivery struct {
	ch chan push.Message
	m  push.Message
}

func deliver(ds []delivery) {
	for _, d := range ds {
		select {
		case d.ch <- d.m:
		default:
		}
	}
}

// apply runs one operation on the state; call with g.mu held.
func (g *Gateway) apply(o op) []delivery {
	switch o.Kind {
	case "device":
		g.applyDevice(o.Device, o.Secret, o.Attested, o.At)
	case "msg":
		if o.Msg != nil {
			if ch, ok := g.applyMsg(o.Device, *o.Msg); ok && ch != nil {
				return []delivery{{ch, *o.Msg}}
			}
		}
	case "ack":
		g.applyAck(o.Device, o.ID)
	case "installs":
		g.applyInstalls(o.Device, o.App, o.N)
	}
	return nil
}

func (g *Gateway) deviceFor(id string) *device {
	d := g.devices[id]
	if d == nil {
		d = &device{}
		g.devices[id] = d
	}
	return d
}

func (g *Gateway) applyDevice(id, secret, attested string, at int64) *device {
	d := g.deviceFor(id)
	if at > d.At || d.Secret == "" {
		d.Secret, d.Attested, d.At = secret, attested, max(at, d.At)
	}
	return d
}

// applyMsg queues m unless it is known, tombstoned, expired or replaced by
// a newer message with its collapse key. It returns the device's live
// connection, if here.
func (g *Gateway) applyMsg(id string, m push.Message) (chan push.Message, bool) {
	d := g.deviceFor(id)
	now := time.Now().UnixMilli()
	d.prune(now)
	if m.Expires <= now || d.Acked[m.ID] != 0 {
		return nil, false
	}
	for _, q := range d.Queue {
		if q.ID == m.ID || (m.Collapse != "" && q.App == m.App && q.Collapse == m.Collapse && q.Sent > m.Sent) {
			return nil, false
		}
	}
	for _, q := range d.Queue {
		if m.Collapse != "" && q.App == m.App && q.Collapse == m.Collapse {
			d.tombstone(q)
		}
	}
	d.collapse(m)
	if len(d.Queue) >= maxQueue {
		d.Queue = d.Queue[1:]
	}
	d.Queue = append(d.Queue, m)
	sort.SliceStable(d.Queue, func(i, j int) bool { return d.Queue[i].Sent < d.Queue[j].Sent })
	return d.conn, true
}

func (g *Gateway) applyAck(id, msgID string) {
	d := g.deviceFor(id)
	for i, m := range d.Queue {
		if m.ID == msgID {
			d.tombstone(m)
			d.Queue = append(d.Queue[:i], d.Queue[i+1:]...)
			return
		}
	}
	// Not queued here (yet): remember it, so the copy cannot arrive later.
	d.tombstone(push.Message{ID: msgID, Expires: time.Now().Add(time.Duration(push.DefaultTTL) * time.Second).UnixMilli()})
}

func (g *Gateway) applyInstalls(id, app string, n int) {
	d := g.deviceFor(id)
	if d.Installs == nil {
		d.Installs = map[string]int{}
	}
	d.Installs[app] = max(d.Installs[app], n)
}

func (d *device) tombstone(m push.Message) {
	if d.Acked == nil {
		d.Acked = map[string]int64{}
	}
	d.Acked[m.ID] = m.Expires
}

// snapshot is the state a peer merges (the signing key stays out: every
// instance derives it from the cluster secret).
func (g *Gateway) peerSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := g.cl.verify(r, nil); err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	g.mu.Lock()
	raw, err := json.Marshal(savedState{Seq: g.seq, Devices: g.devices})
	g.mu.Unlock()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// syncFromPeers merges every reachable peer's snapshot.
func (g *Gateway) syncFromPeers() int {
	merged := 0
	for _, peer := range g.cl.peers {
		url, err := g.cl.peerURL(peer)
		if err != nil {
			continue
		}
		req, _ := http.NewRequest(http.MethodGet, url+"/v1/peer/snapshot", nil)
		g.cl.sign(req, nil)
		resp, err := g.cl.client.Do(req)
		if err != nil {
			continue
		}
		var st savedState
		err = json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		g.mu.Lock()
		var out []delivery
		for id, pd := range st.Devices {
			g.applyDevice(id, pd.Secret, pd.Attested, pd.At)
			for app, n := range pd.Installs {
				g.applyInstalls(id, app, n)
			}
			d := g.devices[id]
			for msgID, exp := range pd.Acked {
				for i, m := range d.Queue {
					if m.ID == msgID {
						d.Queue = append(d.Queue[:i], d.Queue[i+1:]...)
						break
					}
				}
				d.tombstone(push.Message{ID: msgID, Expires: exp})
			}
			for _, m := range pd.Queue {
				if ch, ok := g.applyMsg(id, m); ok && ch != nil {
					out = append(out, delivery{ch, m})
				}
			}
		}
		g.saveLocked()
		g.mu.Unlock()
		deliver(out)
		merged++
	}
	return merged
}

func (g *Gateway) syncLoop() {
	for range time.Tick(syncEvery) {
		g.syncFromPeers()
	}
}
