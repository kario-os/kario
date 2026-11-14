// Package cts is the compatibility report (phone-shell --cts,
// 22-developer-api.md §11): what a device maker runs to apply for
// certification, and how a certifier checks it. The report is the built-in
// scenario plus which capabilities it exercised, so a device that passes
// has served every capability of the developer documentation at least once.
package cts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ui"
)

type CompatReport struct {
	System       string             `json:"system"`
	Version      string             `json:"version"`
	Proto        string             `json:"proto"`
	About        map[string]any     `json:"about"`
	Date         string             `json:"date"`
	Result       string             `json:"result"` // PASS needs every case and every capability
	Cases        []CaseResult       `json:"cases"`
	Capabilities map[string]CapStat `json:"capabilities"`
	Uncovered    []string           `json:"uncovered"`
	Coverage     string             `json:"coverage"`
	// Renderer is the pixel renderer the run drew with (its handshake).
	Renderer *RendererInfo `json:"renderer,omitempty"`
	// Attestation is keystored's device attestation over the report's
	// digest (ReportNonce), so a device maker cannot edit the report: the
	// certificate chain must reach the root the certifier holds.
	Attestation *ui.Attestation `json:"attestation,omitempty"`
}

// CaseResult is one case of a selftest run.
type CaseResult struct {
	Name    string  `json:"name"`
	Status  string  `json:"status"` // PASS | FAIL | SKIP
	Seconds float64 `json:"seconds"`
	Message string  `json:"message,omitempty"` // why it failed, or was skipped
}

// CapStat counts a capability's calls.
type CapStat struct {
	Calls  int `json:"calls"`
	OK     int `json:"ok"`
	Errors int `json:"errors"`
}

// RendererInfo is what a renderer says about itself in "ready".
type RendererInfo struct {
	Proto    string   `json:"proto"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	License  string   `json:"license,omitempty"`
	Toolkit  string   `json:"toolkit,omitempty"`
	Features []string `json:"features,omitempty"`
}

func (i RendererInfo) Has(feature string) bool { return slices.Contains(i.Features, feature) }

func (i RendererInfo) String() string {
	s := strings.TrimSpace(i.Name + " " + i.Version)
	if i.License != "" {
		s += " · " + i.License
	}
	return s
}

// Signer is the app identity the system signs compatibility reports as.
const Signer = "system.cts"

// ReportNonce is the digest the attestation covers: the report without its
// attestation, in Go's (deterministic) compact JSON.
func ReportNonce(r CompatReport) string {
	r.Attestation = nil
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	return "cts:" + hex.EncodeToString(sum[:])
}

// VerifyReport checks a signed report against the root the certifier holds.
func VerifyReport(raw []byte, root *keystore.Roots) (CompatReport, error) {
	var r CompatReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	if r.Attestation == nil {
		return r, errors.New("report is not signed")
	}
	if err := keystore.VerifyAttestation(root, r.Attestation, Signer, ReportNonce(r)); err != nil {
		return r, fmt.Errorf("report signature: %w", err)
	}
	return r, nil
}
