package switchrecv

import (
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Scheme starts the pairing URL; a phone with the app opens it (a deep
// link).
const Scheme = "kario-switch://pair"

// Pairing is what the QR code carries: where the receiver listens, the
// hotspot to join (SSID and PSK together, or neither) or else the name of
// the Wi-Fi network both phones are to be on (Wifi, no password), the
// token and the receiver's X25519 public key, and the name the app shows.
type Pairing struct {
	Host      string
	Port      int
	SSID, PSK string
	Wifi      string
	Token     []byte
	Key       []byte
	Name      string
}

var b64 = base64.RawURLEncoding

// URL is the code's text. Each value is form-encoded as url.Values.Encode
// does (a space is "+"); the fields keep the order docs/36 lists them in,
// which Values.Encode would sort. wifi takes the place of ssid and psk,
// never beside them, and is left out unless it is 1 to 32 bytes of UTF-8.
func (p Pairing) URL() string {
	q := []string{"v=1", "host=" + url.QueryEscape(p.Host), "port=" + strconv.Itoa(p.Port)}
	switch {
	case p.SSID != "":
		q = append(q, "ssid="+url.QueryEscape(p.SSID), "psk="+url.QueryEscape(p.PSK))
	case WifiName(p.Wifi):
		q = append(q, "wifi="+url.QueryEscape(p.Wifi))
	}
	q = append(q, "token="+b64.EncodeToString(p.Token), "key="+b64.EncodeToString(p.Key), "name="+url.QueryEscape(p.Name))
	return Scheme + "?" + strings.Join(q, "&")
}

// WifiName says whether a network's name can go into the code: an SSID
// is 1 to 32 bytes, and the app reads the value as UTF-8 text.
func WifiName(ssid string) bool {
	return ssid != "" && len(ssid) <= 32 && utf8.ValidString(ssid)
}
