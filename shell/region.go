package shell

import (
	_ "embed"
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

// The region (Settings › Region) is the country the phone is set up for: it
// picks the automatic language (i18n.ForRegion) and the time zone when the
// network says nothing. Left to the system it is the SIM's country, from
// the mobile country code telephonyd reads off the SIM (the last SIM's while
// none is in), else the SKU's region, else defaultRegion.

// regions.json is written by regions.py: each region's mobile country
// codes and time zones (the main one first), the names of regions and
// zones in the languages offered.
//
//go:embed regions.json
var regionsJSON []byte

type regionInfo struct {
	Code  string            `json:"code"`
	MCC   []string          `json:"mcc"`
	Zones []string          `json:"zones"`
	Names map[string]string `json:"names"`
}

type regionTables struct {
	Regions []regionInfo                 `json:"regions"`
	Zones   map[string]map[string]string `json:"zones"`
	byCode  map[string]*regionInfo
	byMCC   map[string]string
	zoneOf  map[string]string // the region of each zone
}

var regionData = sync.OnceValue(func() *regionTables {
	t := &regionTables{byCode: map[string]*regionInfo{}, byMCC: map[string]string{}, zoneOf: map[string]string{}}
	if err := json.Unmarshal(regionsJSON, t); err != nil {
		panic("shell: regions.json: " + err.Error())
	}
	for i := range t.Regions {
		r := &t.Regions[i]
		t.byCode[r.Code] = r
		for _, m := range r.MCC {
			t.byMCC[m] = r.Code
		}
		for _, z := range r.Zones {
			t.zoneOf[z] = r.Code
		}
	}
	return t
})

// defaultRegion is a phone's that has no SIM, never had one and whose SKU
// names no region (a developer machine).
const defaultRegion = "CN"

func regionByCode(code string) *regionInfo { return regionData().byCode[strings.ToUpper(code)] }

// regionByMCC is the region of a mobile country code ("334": MX), or "".
func regionByMCC(mcc string) string {
	if len(mcc) < 3 {
		return ""
	}
	return regionData().byMCC[mcc[:3]]
}

// localName is a name in the languages offered, in lang (by its chain).
func localName(names map[string]string, lang, fallback string) string {
	for _, l := range i18n.Chain(lang) {
		if n := names[l]; n != "" {
			return n
		}
	}
	return fallback
}

func regionName(code, lang string) string {
	if r := regionByCode(code); r != nil {
		return localName(r.Names, lang, code)
	}
	return code
}

// zoneCity is a time zone's city in lang ("America/Mexico_City":
// "Ciudad de México").
func zoneCity(id, lang string) string {
	if n, ok := regionData().Zones[id]; ok {
		return localName(n, lang, id)
	}
	return strings.ReplaceAll(id[strings.LastIndex(id, "/")+1:], "_", " ")
}

// sortKey orders names for a list: Chinese names by the region's English
// name (no collation tables in the shell), others with accents folded.
func sortKey(name, english, lang string) string {
	if strings.HasPrefix(lang, "zh") {
		name = english
	}
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if f, ok := latinFold[r]; ok {
			r = f
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var latinFold = map[rune]rune{'á': 'a', 'à': 'a', 'â': 'a', 'ä': 'a', 'ã': 'a', 'å': 'a', 'é': 'e', 'è': 'e', 'ê': 'e',
	'ë': 'e', 'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i', 'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o', 'ú': 'u', 'ù': 'u',
	'û': 'u', 'ü': 'u', 'ñ': 'n', 'ç': 'c', 'ý': 'y', 'ÿ': 'y'}

// regionList is Settings › Region's list, by name in lang.
func regionList(lang string) []regionInfo {
	out := append([]regionInfo(nil), regionData().Regions...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return sortKey(localName(a.Names, lang, a.Code), a.Names[i18n.English], lang) < sortKey(localName(b.Names, lang, b.Code), b.Names[i18n.English], lang)
	})
	return out
}

// netInfo is what telephonyd says about where the phone is: the SIM's
// home country and the network's (mobile country codes, "" when it does
// not say) and the time the network sent (NITZ).
type netInfo struct {
	Sim    string `json:"sim"`
	SimMCC string `json:"sim_mcc"`
	NetMCC string `json:"net_mcc"`
	NITZ   *nitz  `json:"nitz"`
}

type nitz struct {
	Offset int `json:"offset"` // minutes east of UTC, daylight saving included
	DST    int `json:"dst"`    // the part of it that is daylight saving
	// Time is the network's time when it came (Unix seconds), Received
	// the phone's clock then; 0 when the network gave only its zone.
	Time     int64 `json:"time"`
	Received int64 `json:"received"`
}

// watchNetwork follows telephonyd's status for the SIM's and the network's
// country and time, reconnecting if it restarts; the call and SMS events
// on the same subscription are telephony.go's.
func (c *Core) watchNetwork() {
	go func() {
		for !c.stopping.Load() {
			nc, err := net.Dial("unix", c.telSock())
			if err != nil {
				time.Sleep(time.Second)
				continue
			}
			conn := wire.New(nc)
			_ = conn.Send(wire.RPC{ID: 1, Method: wire.Subscribe})
			_ = conn.Send(wire.RPC{ID: 2, Method: svc.TelStatus})
			for {
				var ev struct {
					ID     int             `json:"id"`
					OK     bool            `json:"ok"`
					Result json.RawMessage `json:"result"`
					Event  string          `json:"event"`
					Status json.RawMessage `json:"status"`
				}
				if err := conn.Recv(&ev); err != nil {
					break
				}
				raw := ev.Status
				if ev.ID == 2 && ev.OK {
					raw = ev.Result
				} else if ev.Event != svc.TelEventStatus {
					continue
				}
				var info netInfo
				if json.Unmarshal(raw, &info) == nil {
					c.Post(func() { c.networkSays(info) })
				}
			}
			nc.Close()
			time.Sleep(time.Second)
		}
	}()
}

// networkSays takes what telephonyd reports: a SIM's country is kept for
// when no SIM is in, and a change of region or zone is applied.
func (c *Core) networkSays(info netInfo) {
	old := c.locale.net
	c.locale.net, c.locale.netKnown = info, true
	if r := regionByMCC(info.SimMCC); r != "" && r != c.settings.SimRegion {
		c.settings.SimRegion = r
		c.saveSettings()
	}
	c.syncFromNITZ(info.NITZ)
	if old.SimMCC != info.SimMCC || old.NetMCC != info.NetMCC || !sameNITZZone(old.NITZ, info.NITZ) {
		c.applyLocale()
	}
}

func sameNITZZone(a, b *nitz) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Offset == b.Offset && a.DST == b.DST
}

// autoRegion is the region left to the system and where it comes from:
// "sim", "sku" or "default".
func (c *Core) autoRegion() (string, string) {
	if r := regionByMCC(c.locale.net.SimMCC); r != "" {
		return r, "sim"
	}
	if c.settings.SimRegion != "" && regionByCode(c.settings.SimRegion) != nil {
		return c.settings.SimRegion, "sim"
	}
	if r := strings.ToUpper(c.cfg.SKU.Region); regionByCode(r) != nil {
		return r, "sku"
	}
	return defaultRegion, "default"
}

// region is the region in force.
func (c *Core) region() string {
	if r := regionByCode(c.settings.Region); r != nil {
		return r.Code
	}
	r, _ := c.autoRegion()
	return r
}

// Region is the region in force (selftest, dump).
func (c *Core) Region() string { return c.region() }

// simNetworkArgs: <sim-mcc> <network-mcc> [<nitz-minutes>].
func simNetworkArgs(f []string) map[string]any {
	p := map[string]any{}
	if len(f) > 0 {
		p["sim_mcc"] = f[0]
	}
	if len(f) > 1 {
		p["net_mcc"] = f[1]
	}
	if len(f) > 2 {
		if n, err := strconv.Atoi(f[2]); err == nil {
			p["nitz_offset"] = n
		}
	}
	return p
}
