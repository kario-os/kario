// Package push is what the push gateway and phone-pushd share: the message
// format and the gateway's tokens.
//
// A token names one app on one device. The gateway signs it (HMAC), so an
// app server can only address the device and app it was handed a token for,
// and a token reveals nothing a server could reuse for another app.
package push

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"strconv"
	"strings"
)

// Message is one push as the device receives it.
type Message struct {
	ID     string `json:"id"`
	App    string `json:"app"`
	Kind   string `json:"kind,omitempty"` // "" or "voip"
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	Data   string `json:"data,omitempty"`
	Thread string `json:"thread,omitempty"`
	// Collapse: a newer message with the same key replaces this one, at the
	// gateway while queued and on the device (one notification, one wake).
	Collapse string `json:"collapse_id,omitempty"`
	Priority string `json:"priority,omitempty"` // "" (high) | "normal"
	Sent     int64  `json:"sent"`               // unix ms at the gateway
	Expires  int64  `json:"expires"`
}

// Send is what an app server posts to the gateway. Title/Body become a
// system notification; Data is handed to the app.
type Send struct {
	Token string `json:"token"`
	Kind  string `json:"kind,omitempty"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Data  string `json:"data,omitempty"`
	// Thread groups notifications (one per conversation) and tells the app
	// what the user opened.
	Thread string `json:"thread,omitempty"`
	// Collapse replaces an earlier message of the same app with this key
	// (an unread count, the latest score): only the newest matters.
	Collapse string `json:"collapse_id,omitempty"`
	// Priority "normal": the data may wait for a moment the device is awake
	// anyway (APNs priority 5); "high" (the default) wakes the app now.
	Priority string `json:"priority,omitempty"`
	TTL      int    `json:"ttl,omitempty"` // seconds; default DefaultTTL
}

// MaxCollapse bounds a collapse key, like APNs (64 bytes).
const MaxCollapse = 64

// RegisterApp is the identity pushd attests as (keystored), and
// RegisterNonce what it attests: the device's push identity, so an
// attestation registers that identity and no other.
const RegisterApp = "phone.pushd"

func RegisterNonce(device, secret string) string {
	sum := sha256.Sum256([]byte("phone-push-register|" + device + "|" + secret))
	return b64.EncodeToString(sum[:])
}

const (
	MaxData    = 4096 // bytes, like APNs
	DefaultTTL = 24 * 3600
)

var b64 = base64.RawURLEncoding

func mac(key []byte, payload string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(payload))
	return h.Sum(nil)[:16]
}

func Token(key []byte, device, app string) string { return TokenAt(key, device, app, 0) }

// TokenAt names one install of an app: gen counts how often the app was
// uninstalled on the device, so a token issued before an uninstall stops
// working (the gateway answers 410 Gone) and a reinstalled app gets a new one.
func TokenAt(key []byte, device, app string, gen int) string {
	p := device + "\x00" + app
	if gen > 0 {
		p += "\x00" + strconv.Itoa(gen)
	}
	return b64.EncodeToString([]byte(p)) + "." + b64.EncodeToString(mac(key, p))
}

func ParseToken(key []byte, tok string) (device, app string, err error) {
	device, app, _, err = ParseTokenGen(key, tok)
	return device, app, err
}

// ParseTokenGen also returns the install generation (0 for the first).
func ParseTokenGen(key []byte, tok string) (device, app string, gen int, err error) {
	bad := errors.New("invalid push token")
	ps, ms, ok := strings.Cut(tok, ".")
	if !ok {
		return "", "", 0, bad
	}
	p, err1 := b64.DecodeString(ps)
	m, err2 := b64.DecodeString(ms)
	if err1 != nil || err2 != nil || !hmac.Equal(m, mac(key, string(p))) {
		return "", "", 0, bad
	}
	parts := strings.Split(string(p), "\x00")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return "", "", 0, bad
	}
	if len(parts) == 3 {
		if gen, err = strconv.Atoi(parts[2]); err != nil || gen <= 0 {
			return "", "", 0, bad
		}
	}
	return parts[0], parts[1], gen, nil
}

// Gateways lists the gateway URLs a client may use, in order: the URLs in
// flag (comma-separated), or those in the files (comma-separated paths, a
// URL per line; a file not written yet is skipped). Instances of one
// cluster share state, so a client moves on to the next when one fails.
func Gateways(flag, files string) ([]string, error) {
	var out []string
	if flag != "" {
		for _, u := range strings.Split(flag, ",") {
			if u = strings.TrimSpace(u); u != "" {
				out = append(out, u)
			}
		}
		return out, nil
	}
	for _, f := range strings.Split(files, ",") {
		raw, err := os.ReadFile(strings.TrimSpace(f))
		if err != nil {
			continue
		}
		out = append(out, strings.Fields(string(raw))...)
	}
	if len(out) == 0 {
		return nil, errors.New("no push gateway yet")
	}
	return out, nil
}
