package pkg

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Catalog is a store's list of published apps, signed by its root with the
// time it was made. A mirror or CDN can serve it but not change what the
// user sees (who publishes an app, its filing number, its version), and a
// device takes none older than the last it accepted, so an update cannot be
// hidden by serving yesterday's list. Apps are the entries as the store
// encodes them: the signature covers those exact bytes.
type Catalog struct {
	Format string          `json:"format"`
	Store  string          `json:"store"`
	Issued int64           `json:"issued"`
	Apps   json.RawMessage `json:"apps"`
	Sig    string          `json:"sig,omitempty"`
}

const CatalogFormat = "phoneapp-catalog/1"

// CatalogMaxAge: a catalog signed longer ago is refused; a store serving it
// from static hosting signs a fresh one at least this often.
const CatalogMaxAge = 7 * 24 * time.Hour

// Release is one version of an app a catalogue offers; Privacy is its
// manifest's privacy label (ui.Privacy).
type Release struct {
	Version     string          `json:"version"`
	Size        int             `json:"size"`
	SHA256      string          `json:"sha256"`
	Permissions []string        `json:"permissions,omitempty"`
	Privacy     json.RawMessage `json:"privacy,omitempty"`
}

// A staged entry (one with "previous") offers its version to "rollout"
// percent of devices and the previous release to the rest; rollout 0 (or
// none) holds the new version back from everyone. Each device decides by a
// bucket it keeps to itself, so the store never learns who got what.

// OfferedApps rewrites a catalogue's entries for one device: in reports
// whether it is within an app's rollout percentage.
func OfferedApps(apps json.RawMessage, in func(id string, percent int) bool) (json.RawMessage, error) {
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(apps, &list); err != nil {
		return nil, err
	}
	for _, e := range list {
		raw, staged := e["previous"]
		if !staged {
			continue
		}
		var id string
		var percent int
		var prev Release
		_ = json.Unmarshal(e["id"], &id)
		_ = json.Unmarshal(e["rollout"], &percent)
		if err := json.Unmarshal(raw, &prev); err != nil || prev.Version == "" {
			return nil, fmt.Errorf("%s: bad previous release", id)
		}
		if percent > 0 && in(id, percent) {
			continue
		}
		e["version"], _ = json.Marshal(prev.Version)
		e["size"], _ = json.Marshal(prev.Size)
		e["sha256"], _ = json.Marshal(prev.SHA256)
		e["permissions"], _ = json.Marshal(prev.Permissions)
		delete(e, "privacy")
		if len(prev.Privacy) > 0 {
			e["privacy"] = prev.Privacy
		}
	}
	return json.Marshal(list)
}

// RolloutMsg is what a developer signs (with their store key, at time t) to
// move a staged version to percent of devices.
func RolloutMsg(name string, t int64, app, version string, percent int) string {
	return fmt.Sprintf("phone-rollout/1|%s|%d|%s|%s|%d", name, t, app, version, percent)
}

// InRollout: a device's bucket (0-9999, its own) is within percent of an
// app's rollout; mixing in the app ID gives each app other early devices.
func InRollout(bucket int, id string, percent int) bool {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", bucket, id)))
	return int(binary.BigEndian.Uint32(h[:4])%10000) < percent*100
}

func (c Catalog) signedBytes() []byte {
	return append([]byte(fmt.Sprintf("%s\n%s\n%d\n", CatalogFormat, c.Store, c.Issued)), c.Apps...)
}

// SignCatalog signs the entries (any JSON array) with the store's root.
func (s *Store) SignCatalog(apps any, issued time.Time) (Catalog, error) {
	raw, err := json.Marshal(apps)
	if err != nil {
		return Catalog{}, err
	}
	c := Catalog{Format: CatalogFormat, Store: s.Name, Issued: issued.Unix(), Apps: raw}
	c.Sig = b64.EncodeToString(ed25519.Sign(s.Key, c.signedBytes()))
	return c, nil
}

// VerifyCatalog checks a catalog against the store roots a device trusts
// and its age at now.
func VerifyCatalog(raw []byte, roots map[string]ed25519.PublicKey, now time.Time) (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(raw, &c); err != nil || c.Format != CatalogFormat {
		return nil, errors.New("not a store catalog")
	}
	root, ok := roots[c.Store]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUntrustedStore, c.Store)
	}
	sig, err := b64.DecodeString(c.Sig)
	if err != nil || !ed25519.Verify(root, c.signedBytes(), sig) {
		return nil, errors.New("the catalog is not signed by the store")
	}
	at := time.Unix(c.Issued, 0)
	switch {
	case now.Sub(at) > CatalogMaxAge:
		return nil, fmt.Errorf("the catalog was signed %s, too long ago", at.Format(time.DateOnly))
	case at.Sub(now) > 24*time.Hour:
		return nil, errors.New("the catalog is signed in the future")
	}
	return &c, nil
}
