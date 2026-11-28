package location

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/statefile"
)

// Network location: where the phone is by the Wi-Fi access points it hears
// and the cell it is on, from a network location service, while the
// satellites give no fix. What leaves the phone is limited (Usable), the
// answers are kept on the phone (Locator), and the service is asked only
// so often.

// AccessPoint is a Wi-Fi access point in range.
type AccessPoint struct {
	BSSID string // aa:bb:cc:dd:ee:ff
	SSID  string
	// SignalDBm: 0 when not known.
	SignalDBm int
	// Hidden: the network does not broadcast its name.
	Hidden bool
}

// Cell is a mobile network cell by its global identity.
type Cell struct {
	Radio string // gsm | wcdma | lte | nr
	MCC   int
	MNC   int
	// Area is the LAC (GSM, UMTS) or the TAC (LTE, NR); ID the cell's
	// identity in it.
	Area      int
	ID        int64
	SignalDBm int
}

func (c Cell) key() string { return fmt.Sprintf("%s:%d:%d:%d:%d", c.Radio, c.MCC, c.MNC, c.Area, c.ID) }

// Observation is what the phone hears now.
type Observation struct {
	WiFi  []AccessPoint
	Cells []Cell
}

// At most maxAPs access points are sent, the strongest; fewer than minAPs
// none (the services want two, so one access point, a phone's hotspot
// say, is never looked up alone).
const (
	maxAPs = 20
	minAPs = 2
)

// optedOut: the owner asked for the network to stay out of location
// services by its name (Google's _nomap; Microsoft's _optout).
func optedOut(ssid string) bool {
	s := strings.ToLower(ssid)
	return strings.Contains(s, "_nomap") || strings.Contains(s, "_optout")
}

// Usable is what may be sent: access points with a fixed, globally
// assigned address (not a randomised or multicast one) whose network
// broadcasts its name without opting out, the strongest maxAPs and only
// if there are minAPs of them; cells with their whole identity.
func (o Observation) Usable() Observation {
	var out Observation
	seen := map[string]bool{}
	for _, ap := range o.WiFi {
		mac, err := net.ParseMAC(ap.BSSID)
		if err != nil || len(mac) != 6 || mac[0]&3 != 0 || bytes.Equal(mac, make([]byte, 6)) ||
			ap.Hidden || ap.SSID == "" || optedOut(ap.SSID) || seen[mac.String()] {
			continue
		}
		seen[mac.String()] = true
		ap.BSSID = mac.String()
		out.WiFi = append(out.WiFi, ap)
	}
	sort.SliceStable(out.WiFi, func(i, j int) bool { return strength(out.WiFi[i]) > strength(out.WiFi[j]) })
	if len(out.WiFi) > maxAPs {
		out.WiFi = out.WiFi[:maxAPs]
	}
	if len(out.WiFi) < minAPs {
		out.WiFi = nil
	}
	for _, c := range o.Cells {
		switch c.Radio {
		case "gsm", "wcdma", "lte", "nr":
		default:
			continue
		}
		if c.MCC > 0 && c.MCC < 1000 && c.MNC >= 0 && c.MNC < 1000 && c.Area > 0 && c.ID > 0 {
			out.Cells = append(out.Cells, c)
		}
	}
	return out
}

func strength(ap AccessPoint) int {
	if ap.SignalDBm == 0 {
		return -200
	}
	return ap.SignalDBm
}

func (o Observation) empty() bool { return len(o.WiFi) == 0 && len(o.Cells) == 0 }

// Provider is a network location service.
type Provider interface {
	Locate(ctx context.Context, o Observation) (Fix, error)
}

var (
	// ErrNotFound: the service does not know where those networks are.
	ErrNotFound = errors.New("the network location service does not know these networks")
	// ErrNoNetworks: no access point or cell it may be told about.
	ErrNoNetworks = errors.New("no Wi-Fi or mobile cell to place the phone by")
	// ErrRateLimited: the service was asked too often; the phone waits.
	ErrRateLimited = errors.New("the network location service was asked too often")
)

// HTTPProvider asks a service that speaks the JSON of the Geolocation API
// (Google's geolocate; Mozilla's Ichnaea and those that run it, such as
// BeaconDB): POST {considerIp, wifiAccessPoints, cellTowers}, answered by
// {location: {lat, lng}, accuracy}. Key, if the service needs one, goes in
// the query as key=; it never shows in an error.
type HTTPProvider struct {
	URL    string
	Key    string
	Client *http.Client
}

// maxAnswer bounds what is read of the service's answer.
const maxAnswer = 64 << 10

type geoWiFi struct {
	MAC    string `json:"macAddress"`
	Signal int    `json:"signalStrength,omitempty"`
}

type geoCell struct {
	Radio  string `json:"radioType"`
	MCC    int    `json:"mobileCountryCode"`
	MNC    int    `json:"mobileNetworkCode"`
	Area   int    `json:"locationAreaCode"`
	ID     int64  `json:"cellId"`
	Signal int    `json:"signalStrength,omitempty"`
}

type geoRequest struct {
	// ConsiderIP and Fallbacks: never placed by the address the request
	// comes from, or by the area of a cell the service does not know.
	ConsiderIP bool      `json:"considerIp"`
	WiFi       []geoWiFi `json:"wifiAccessPoints,omitempty"`
	Cells      []geoCell `json:"cellTowers,omitempty"`
	Fallbacks  struct {
		LAC bool `json:"lacf"`
		IP  bool `json:"ipf"`
	} `json:"fallbacks"`
}

func (p *HTTPProvider) Locate(ctx context.Context, o Observation) (Fix, error) {
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Fix{}, fmt.Errorf("network location: %q is not an http(s) URL", p.URL)
	}
	if p.Key != "" {
		q := u.Query()
		q.Set("key", p.Key)
		u.RawQuery = q.Encode()
	}
	var req geoRequest
	for _, ap := range o.WiFi {
		req.WiFi = append(req.WiFi, geoWiFi{MAC: ap.BSSID, Signal: ap.SignalDBm})
	}
	for _, c := range o.Cells {
		req.Cells = append(req.Cells, geoCell{Radio: c.Radio, MCC: c.MCC, MNC: c.MNC, Area: c.Area, ID: c.ID, Signal: c.SignalDBm})
	}
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return Fix{}, fmt.Errorf("network location: %s: bad request", u.Host)
	}
	hr.Header.Set("Content-Type", "application/json")
	cl := p.Client
	if cl == nil {
		cl = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := cl.Do(hr)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Fix{}, fmt.Errorf("network location: %s: %v", u.Host, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxAnswer))
	var ans struct {
		Location *struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		} `json:"location"`
		Accuracy float64 `json:"accuracy"`
		Error    struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &ans)
	switch {
	case res.StatusCode == http.StatusNotFound:
		return Fix{}, ErrNotFound
	case res.StatusCode != http.StatusOK:
		msg := ans.Error.Message
		if p.Key != "" {
			msg = strings.ReplaceAll(msg, p.Key, "…")
		}
		if len(msg) > 120 {
			msg = msg[:120]
		}
		return Fix{}, fmt.Errorf("network location: %s: %s %s", u.Host, res.Status, msg)
	case ans.Location == nil || ans.Location.Lat < -90 || ans.Location.Lat > 90 || ans.Location.Lng < -180 || ans.Location.Lng > 180 ||
		!(ans.Accuracy > 0 && ans.Accuracy < 1e6):
		return Fix{}, fmt.Errorf("network location: %s: no position in the answer", u.Host)
	}
	return Fix{Lat: ans.Location.Lat, Lon: ans.Location.Lng, AccuracyM: ans.Accuracy, Network: true}, nil
}

// FakeProvider stands in for the service on the developer machine and the
// emulator: an observation with access points is placed within
// FakeWiFiAccuracyM of where Where says the phone is, one with a cell only
// within FakeCellAccuracyM. Calls counts the requests that reached it.
type FakeProvider struct {
	Where func() (lat, lon float64)
	Calls atomic.Int32
}

const (
	FakeWiFiAccuracyM = 25.0
	FakeCellAccuracyM = 1200.0
)

func (p *FakeProvider) Locate(ctx context.Context, o Observation) (Fix, error) {
	p.Calls.Add(1)
	if o.empty() {
		return Fix{}, ErrNotFound
	}
	lat, lon := SimLat, SimLon
	if p.Where != nil {
		lat, lon = p.Where()
	}
	acc := FakeWiFiAccuracyM
	if len(o.WiFi) == 0 {
		acc = FakeCellAccuracyM
	}
	return Fix{Lat: lat, Lon: lon, AccuracyM: acc, Network: true}, nil
}

// The Locator's defaults: requests per hour and between two requests;
// how long answers are kept (an access point seldom moves), and a "not
// known"; how many answers; how long a failing service is left alone, at
// first and at most.
const (
	netMaxPerHour = 60
	netMinGap     = 10 * time.Second
	netKeep       = 7 * 24 * time.Hour
	netKeepMiss   = time.Hour
	netMaxCached  = 512
	netBackoff    = time.Minute
	netBackoffMax = 30 * time.Minute
)

// Locator asks a Provider sparingly. An answer is kept on the phone and
// answers a later observation of the same networks: two of its access
// points, half of the fewer, in common, or for cells alone the same cell.
// Requests are spaced and counted per hour, and a service that fails is
// left alone for a while, longer each time.
type Locator struct {
	Provider Provider
	// Path keeps the answers across restarts (0600); "" in memory only.
	Path string
	// MaxPerHour and MinGap override netMaxPerHour and netMinGap.
	MaxPerHour int
	MinGap     time.Duration
	Now        func() time.Time

	mu       sync.Mutex
	loaded   bool
	cache    []netAnswer
	sent     []time.Time
	backoff  time.Duration
	quietTil time.Time
	stats    NetStats
}

// NetStats: requests sent, observations answered from the phone's own
// answers, and the last failure.
type NetStats struct {
	Requests int    `json:"requests"`
	Cached   int    `json:"cached"`
	Error    string `json:"error,omitempty"`
}

type netAnswer struct {
	APs      []string `json:"aps,omitempty"`
	Cell     string   `json:"cell,omitempty"`
	Lat      float64  `json:"lat"`
	Lon      float64  `json:"lon"`
	Accuracy float64  `json:"accuracy_m"`
	At       int64    `json:"at"`
	Miss     bool     `json:"miss,omitempty"`
}

func (l *Locator) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Locate places what the phone hears: from the answers kept, else by the
// service, if it may be asked now.
func (l *Locator) Locate(ctx context.Context, o Observation) (Fix, error) {
	o = o.Usable()
	if o.empty() {
		return Fix{}, ErrNoNetworks
	}
	aps := make([]string, len(o.WiFi))
	for i, ap := range o.WiFi {
		aps[i] = ap.BSSID
	}
	cell := ""
	if len(o.Cells) > 0 {
		cell = o.Cells[0].key()
	}
	l.mu.Lock()
	l.load()
	now := l.now()
	if a, ok := l.lookup(aps, cell, now); ok {
		l.stats.Cached++
		l.mu.Unlock()
		if a.Miss {
			return Fix{}, ErrNotFound
		}
		return Fix{Time: now, Lat: a.Lat, Lon: a.Lon, AccuracyM: a.Accuracy, Network: true}, nil
	}
	if err := l.allow(now); err != nil {
		l.mu.Unlock()
		return Fix{}, err
	}
	l.sent = append(l.sent, now)
	l.stats.Requests++
	l.mu.Unlock()

	f, err := l.Provider.Locate(ctx, o)

	l.mu.Lock()
	defer l.mu.Unlock()
	now = l.now()
	switch {
	case err == nil:
		l.backoff, l.quietTil, l.stats.Error = 0, time.Time{}, ""
		l.remember(netAnswer{APs: aps, Cell: cell, Lat: f.Lat, Lon: f.Lon, Accuracy: f.AccuracyM, At: now.Unix()})
		f.Time, f.Network = now, true
		return f, nil
	case errors.Is(err, ErrNotFound):
		l.backoff, l.quietTil, l.stats.Error = 0, time.Time{}, ""
		l.remember(netAnswer{APs: aps, Cell: cell, At: now.Unix(), Miss: true})
		return Fix{}, err
	case ctx.Err() != nil:
		return Fix{}, err
	}
	l.backoff = min(netBackoffMax, max(netBackoff, 2*l.backoff))
	l.quietTil = now.Add(l.backoff)
	l.stats.Error = err.Error()
	return Fix{}, err
}

// allow: the service may be asked now (the caller holds mu).
func (l *Locator) allow(now time.Time) error {
	per, gap := l.MaxPerHour, l.MinGap
	if per <= 0 {
		per = netMaxPerHour
	}
	if gap <= 0 {
		gap = netMinGap
	}
	kept := l.sent[:0]
	for _, t := range l.sent {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	l.sent = kept
	switch {
	case now.Before(l.quietTil):
		return fmt.Errorf("%w (it failed; again in %s)", ErrRateLimited, l.quietTil.Sub(now).Round(time.Second))
	case len(l.sent) >= per:
		return fmt.Errorf("%w (%d requests in the last hour)", ErrRateLimited, len(l.sent))
	case len(l.sent) > 0 && now.Sub(l.sent[len(l.sent)-1]) < gap:
		return ErrRateLimited
	}
	return nil
}

// lookup is the newest answer kept that fits the observation.
func (l *Locator) lookup(aps []string, cell string, now time.Time) (netAnswer, bool) {
	for i := len(l.cache) - 1; i >= 0; i-- {
		a := l.cache[i]
		keep := netKeep
		if a.Miss {
			keep = netKeepMiss
		}
		if now.Sub(time.Unix(a.At, 0)) > keep {
			continue
		}
		if len(aps) == 0 {
			if len(a.APs) == 0 && a.Cell == cell {
				return a, true
			}
			continue
		}
		common := 0
		for _, x := range a.APs {
			for _, y := range aps {
				if x == y {
					common++
				}
			}
		}
		if common >= minAPs && 2*common >= min(len(aps), len(a.APs)) {
			return a, true
		}
	}
	return netAnswer{}, false
}

func (l *Locator) remember(a netAnswer) {
	now := l.now()
	kept := l.cache[:0]
	for _, x := range l.cache {
		if now.Sub(time.Unix(x.At, 0)) <= netKeep {
			kept = append(kept, x)
		}
	}
	l.cache = append(kept, a)
	if len(l.cache) > netMaxCached {
		l.cache = l.cache[len(l.cache)-netMaxCached:]
	}
	l.save()
}

func (l *Locator) load() {
	if l.loaded {
		return
	}
	l.loaded = true
	if l.Path == "" {
		return
	}
	if raw, err := os.ReadFile(l.Path); err == nil {
		_ = json.Unmarshal(raw, &l.cache)
	}
}

func (l *Locator) save() {
	if l.Path == "" {
		return
	}
	raw, _ := json.Marshal(l.cache)
	_ = statefile.Write(l.Path, raw, 0o600)
}

// Forget drops every answer kept, on the phone too.
func (l *Locator) Forget() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cache, l.loaded = nil, true
	if l.Path != "" {
		_ = os.Remove(l.Path)
	}
}

// Stats are the Locator's counts so far.
func (l *Locator) Stats() NetStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stats
}
