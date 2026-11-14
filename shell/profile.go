package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Composition layers (R11, after DSH's bundle / profile / patch): one Shell
// image, several products. The profile is built from JSON layers, each a
// merge patch (RFC 7386) over the ones before it, in file-name order:
//
//	<prefix>/share/phone/profile.d/*.json   the image: 00-base, then the
//	                                        product's and the OEM's layers
//	<state>/profile.d/*.json                developer mode only
//
// The image's layers live on the read-only, verified system partition, so
// they need no signature of their own; a device owner's own layers count
// only in developer mode. phone-shell --dump-profile prints the result and
// which layer set each key.

type Profile struct {
	// Product is the name About phone shows under the system's.
	Product string `json:"product,omitempty"`
	// HiddenApps are system apps this product leaves out (not listed, not
	// started).
	HiddenApps []string `json:"hidden_apps,omitempty"`
	// AppOrder places apps in the menu (lower first), over their manifests.
	AppOrder map[string]int `json:"app_order,omitempty"`
	// Settings are the system settings' defaults (the user's choices win).
	Settings json.RawMessage `json:"settings,omitempty"`
	// Providers route a capability to a system service on the wire protocol
	// ("service:<name>" → <run>/<name>.sock, method "capability"): an OEM
	// supplies location, say, without touching the Shell. The broker still
	// checks permissions first and passes the caller's identity.
	Providers map[string]string `json:"providers,omitempty"`

	// Origin is the layer that last set each top-level key.
	Origin map[string]string `json:"-"`
}

var providerRef = regexp.MustCompile(`^service:[a-z][a-z0-9-]{0,31}$`)

// ProfileDirs are the layer directories, in order.
func ProfileDirs(cfg Config, developer bool) []string {
	dirs := []string{filepath.Join(filepath.Dir(cfg.AppsDir), "profile.d")}
	if developer {
		dirs = append(dirs, filepath.Join(cfg.StateDir, "profile.d"))
	}
	return dirs
}

// LoadProfile merges the layers in dirs (each dir's files in name order).
func LoadProfile(dirs ...string) (*Profile, error) {
	doc := map[string]any{}
	origin := map[string]string{}
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.json"))
		sort.Strings(files)
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			var layer map[string]any
			if err := json.Unmarshal(raw, &layer); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			for k := range layer {
				origin[k] = f
			}
			applyMergePatch(doc, layer)
		}
	}
	raw, _ := json.Marshal(doc)
	p := &Profile{}
	if err := json.Unmarshal(raw, p); err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	for cap, ref := range p.Providers {
		if !providerRef.MatchString(ref) {
			return nil, fmt.Errorf("profile: provider of %s must be service:<name>, not %q", cap, ref)
		}
	}
	p.Origin = origin
	return p, nil
}

// applyMergePatch applies an RFC 7386 merge patch to doc in place.
func applyMergePatch(doc map[string]any, patch map[string]any) {
	for k, v := range patch {
		switch pv := v.(type) {
		case nil:
			delete(doc, k)
		case map[string]any:
			cur, ok := doc[k].(map[string]any)
			if !ok {
				cur = map[string]any{}
			}
			applyMergePatch(cur, pv)
			doc[k] = cur
		default:
			doc[k] = v
		}
	}
}

func (c *Core) loadProfile() {
	var peek struct {
		DeveloperMode bool `json:"developer_mode"`
	}
	if raw, err := os.ReadFile(c.settingsPath()); err == nil {
		_ = json.Unmarshal(raw, &peek)
	}
	p, err := LoadProfile(ProfileDirs(c.cfg, peek.DeveloperMode)...)
	if err != nil {
		c.log.Printf("profile: %v; using none", err)
		p = &Profile{}
	}
	c.profile = p
	if len(p.Origin) > 0 {
		c.log.Printf("profile %q from %d keys of layers (%d hidden apps, %d providers)", p.Product, len(p.Origin), len(p.HiddenApps), len(p.Providers))
	}
}

// provider is the service a capability is routed to ("" for the Shell's
// own implementation).
func (p *Profile) provider(cap string) string {
	if p == nil {
		return ""
	}
	return strings.TrimPrefix(p.Providers[cap], "service:")
}

func (p *Profile) hidden(id string) bool {
	return p != nil && contains(p.HiddenApps, id)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Dump is the merged profile with each key's layer.
func (p *Profile) Dump() string {
	raw, _ := json.MarshalIndent(p, "", "  ")
	var b strings.Builder
	b.Write(raw)
	b.WriteString("\n")
	keys := make([]string, 0, len(p.Origin))
	for k := range p.Origin {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, p.Origin[k])
	}
	return b.String()
}
