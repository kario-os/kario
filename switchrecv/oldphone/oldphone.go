// Package oldphone is the old phone's side of kario-switch/1, written from
// docs/36-device-switch.md apart from the receiver (package switchrecv) and
// sharing none of its code, so the receiver's tests and the Shell's
// selftest check one against the other. It does what the "Kario Switch"
// app does: it reads the QR code's text, says hello, sends items in
// batches and files in chunks, resuming where the receiver says, and ends
// with done.
package oldphone

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Pairing is the QR code's content. Wifi: the Wi-Fi network the receiver
// is on, which the old phone has to be on too (no hotspot to join).
type Pairing struct {
	Host       string
	Port       int
	SSID, PSK  string
	Wifi       string
	Token, Key []byte
	Name       string
}

var b64 = base64.RawURLEncoding.Strict()

// ParsePairing reads kario-switch://pair?v=1&host=...: version 1, an IPv4
// host, the hotspot's name and passphrase together or not at all, or else
// perhaps the Wi-Fi network's name (at most 32 bytes), a 32-byte token and
// key.
func ParsePairing(s string) (Pairing, error) {
	rest, ok := strings.CutPrefix(s, "kario-switch://pair?")
	if !ok {
		return Pairing{}, errors.New("not a kario-switch pairing")
	}
	q, err := url.ParseQuery(rest)
	if err != nil {
		return Pairing{}, err
	}
	p := Pairing{Host: q.Get("host"), SSID: q.Get("ssid"), PSK: q.Get("psk"), Wifi: q.Get("wifi"), Name: q.Get("name")}
	p.Port, err = strconv.Atoi(q.Get("port"))
	if err != nil || p.Port <= 0 || p.Port > 65535 {
		return Pairing{}, errors.New("bad port")
	}
	if p.Token, err = b64.DecodeString(q.Get("token")); err != nil || len(p.Token) != 32 {
		return Pairing{}, errors.New("bad token")
	}
	if p.Key, err = b64.DecodeString(q.Get("key")); err != nil || len(p.Key) != 32 {
		return Pairing{}, errors.New("bad key")
	}
	switch ip := net.ParseIP(p.Host); {
	case q.Get("v") != "1":
		return Pairing{}, errors.New("not version 1")
	case ip == nil || ip.To4() == nil || strings.Contains(p.Host, ":"):
		return Pairing{}, errors.New("host is not an IPv4 address")
	case (p.SSID == "") != (q.Get("psk") == ""):
		return Pairing{}, errors.New("ssid and psk come together")
	case p.Wifi != "" && p.SSID != "":
		return Pairing{}, errors.New("wifi does not come with ssid")
	case len(p.Wifi) > 32:
		return Pairing{}, errors.New("wifi is over 32 bytes")
	}
	return p, nil
}

// Client talks to one receiver as one app instance: its own key pair, its
// own counters.
type Client struct {
	HTTP *http.Client
	Base string
	pub  []byte
	up   cipher.AEAD
	down cipher.AEAD
	next uint64
	// lastDown: the highest downstream counter seen, -1 before any.
	lastDown int64
	Hello    HelloReply
}

// HelloReply is the receiver's answer to hello.
type HelloReply struct {
	Session  string   `json:"session"`
	Accept   []string `json:"accept"`
	MaxBatch int      `json:"max_batch"`
	Chunk    int      `json:"chunk"`
	MaxBody  int      `json:"max_body"`
}

// New pairs with the receiver p names; priv is the app's X25519 private key
// (nil: a new one, as the app makes at every start).
func New(p Pairing, priv []byte) (*Client, error) {
	if priv == nil {
		priv = make([]byte, 32)
		if _, err := rand.Read(priv); err != nil {
			return nil, err
		}
	}
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	peer, err := ecdh.X25519().NewPublicKey(p.Key)
	if err != nil {
		return nil, err
	}
	shared, err := k.ECDH(peer)
	if err != nil {
		return nil, err
	}
	okm, err := hkdf.Key(sha256.New, shared, p.Token, "kario-switch/1", 64)
	if err != nil {
		return nil, err
	}
	c := &Client{HTTP: &http.Client{Timeout: 2 * time.Minute}, Base: "http://" + net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), pub: k.PublicKey().Bytes(), lastDown: -1}
	if c.up, err = gcm(okm[:32]); err != nil {
		return nil, err
	}
	if c.down, err = gcm(okm[32:]); err != nil {
		return nil, err
	}
	return c, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// PublicKey is the app key hello carries in X-Kario-Key.
func (c *Client) PublicKey() string { return base64.RawURLEncoding.EncodeToString(c.pub) }

// Seal makes the next upstream envelope for method and target (the path
// and query as sent).
func (c *Client) Seal(method, target string, plain []byte) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint32(nonce, 1)
	binary.BigEndian.PutUint64(nonce[4:], c.next)
	c.next++
	return c.up.Seal(nonce, nonce, plain, []byte(method+" "+target))
}

// SetCounter makes the next envelope's counter n.
func (c *Client) SetCounter(n uint64) { c.next = n }

// Reply is an answer: its status and plaintext; Sealed is false for one in
// plain JSON (401, or a 413 before any request opened).
type Reply struct {
	Status  int
	Plain   []byte
	Sealed  bool
	Counter uint64
}

// Send posts a sealed body and opens the answer: under the downstream key
// with the request's AAD, its counter above the last one's.
func (c *Client) Send(method, target string, body []byte, hello bool) (Reply, error) {
	req, err := http.NewRequest(method, c.Base+target, bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if hello {
		req.Header.Set("X-Kario-Key", c.PublicKey())
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Reply{}, err
	}
	r := Reply{Status: resp.StatusCode}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		r.Plain = raw
		return r, nil
	}
	if len(raw) < 28 || binary.BigEndian.Uint32(raw) != 2 {
		return r, fmt.Errorf("%s %s: %d: not a downstream envelope", method, target, r.Status)
	}
	plain, err := c.down.Open(nil, raw[:12], raw[12:], []byte(method+" "+target))
	if err != nil {
		return r, fmt.Errorf("%s %s: %d: the reply does not open", method, target, r.Status)
	}
	n := binary.BigEndian.Uint64(raw[4:12])
	if int64(n) <= c.lastDown {
		return r, fmt.Errorf("%s %s: downstream counter %d not above %d", method, target, n, c.lastDown)
	}
	c.lastDown = int64(n)
	r.Plain, r.Sealed, r.Counter = plain, true, n
	return r, nil
}

// Error is an answer other than 200: its status and code (and where a file
// stands, for bad_offset).
type Error struct {
	Status int
	Code   string `json:"code"`
	Text   string `json:"error"`
	Offset int64  `json:"offset"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Text) }

// Call sends req as JSON and reads the answer into out; an answer other
// than 200 is an *Error.
func (c *Client) Call(method, target string, req, out any) error {
	plain, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return c.call(method, target, plain, out, target == "/v1/hello")
}

func (c *Client) call(method, target string, plain []byte, out any, hello bool) error {
	r, err := c.Send(method, target, c.Seal(method, target, plain), hello)
	if err != nil {
		return err
	}
	if r.Status != http.StatusOK {
		e := &Error{Status: r.Status}
		_ = json.Unmarshal(r.Plain, e)
		return e
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Plain, out)
}

// Device is the old phone as hello describes it.
type Device struct {
	Brand     string `json:"brand"`
	Model     string `json:"model"`
	OS        string `json:"os"`
	OSVersion string `json:"os_version"`
}

// SayHello is the first request; counts may be partial.
func (c *Client) SayHello(d Device, counts map[string]int) (HelloReply, error) {
	err := c.Call(http.MethodPost, "/v1/hello", map[string]any{"app": "kario-switch", "version": "1.0.0", "device": d, "counts": counts}, &c.Hello)
	return c.Hello, err
}

// Items sends a kind's items in batches of the receiver's max_batch, the
// last marked; it gives what the receiver stored and skipped. A batch the
// receiver finds too large (413) goes again as two halves.
func (c *Client) Items(kind string, items []any) (stored, skipped int, err error) {
	size := max(1, c.Hello.MaxBatch)
	for i := 0; i < len(items) || i == 0; {
		n := min(size, len(items)-i)
		var res struct {
			Stored  int `json:"stored"`
			Skipped int `json:"skipped"`
		}
		err := c.Call(http.MethodPost, "/v1/items/"+kind, map[string]any{"items": items[i : i+n], "last": i+n == len(items)}, &res)
		var e *Error
		if errors.As(err, &e) && e.Status == http.StatusRequestEntityTooLarge && n > 1 {
			size = max(1, n/2)
			continue
		}
		if err != nil {
			return stored, skipped, err
		}
		stored, skipped, i = stored+res.Stored, skipped+res.Skipped, i+n
		if len(items) == 0 {
			break
		}
	}
	return stored, skipped, nil
}

// FileInfo is a file offer.
type FileInfo struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mime    string `json:"mime"`
	TakenAt int64  `json:"taken_at"`
	SHA256  string `json:"sha256"`
}

// Offer asks where the receiver stands with a file.
func (c *Client) Offer(f FileInfo) (int64, error) {
	var res struct {
		Offset int64 `json:"offset"`
	}
	err := c.Call(http.MethodPost, "/v1/files", f, &res)
	return res.Offset, err
}

// Chunk sends data at offset.
func (c *Client) Chunk(id string, offset int64, data []byte) (int64, error) {
	target := "/v1/files/" + id + "?offset=" + strconv.FormatInt(offset, 10)
	var res struct {
		Offset int64 `json:"offset"`
	}
	err := c.call(http.MethodPut, target, data, &res, false)
	return res.Offset, err
}

// SendFile offers a file and sends what the receiver does not have yet.
func (c *Client) SendFile(id, kind, name, mime string, takenAt int64, data []byte) error {
	sum := sha256.Sum256(data)
	off, err := c.Offer(FileInfo{ID: id, Kind: kind, Name: name, Size: int64(len(data)), Mime: mime, TakenAt: takenAt, SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return err
	}
	chunk := int64(max(1, c.Hello.Chunk))
	for off < int64(len(data)) {
		end := min(off+chunk, int64(len(data)))
		if off, err = c.Chunk(id, off, data[off:end]); err != nil {
			return err
		}
	}
	return nil
}

// DoneReply is what the receiver stored, by kind, and what failed.
type DoneReply struct {
	Contacts int `json:"contacts"`
	CallLog  int `json:"call_log"`
	SMS      int `json:"sms"`
	Calendar int `json:"calendar"`
	Photos   int `json:"photos"`
	Videos   int `json:"videos"`
	Failed   []struct {
		Kind   string `json:"kind"`
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"failed"`
}

// Done ends the session.
func (c *Client) Done() (DoneReply, error) {
	var res DoneReply
	err := c.Call(http.MethodPost, "/v1/done", struct{}{}, &res)
	return res, err
}
