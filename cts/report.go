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
	Result       string             `json:"result"` // PASS needs every case and every capability; PART: see Parts
	Cases        []CaseResult       `json:"cases"`
	Capabilities map[string]CapStat `json:"capabilities"`
	Uncovered    []string           `json:"uncovered"`
	// Skipped are the capabilities not called because the cases that
	// call them (covers) were skipped, each with the first such case and
	// why it was skipped; they are not in Uncovered.
	Skipped  map[string]string `json:"skipped,omitempty"`
	Coverage string            `json:"coverage"`
	// Renderer is the pixel renderer the run drew with (its handshake).
	Renderer *RendererInfo `json:"renderer,omitempty"`
	// Sandbox is what the device's kernel lacks of the sandbox and the app
	// lifecycle (24-sku-framework.md §7.5); absent from reports made
	// before it was added, which verify as they did.
	Sandbox *Sandbox `json:"sandbox,omitempty"`
	// Parts, in the report of a run of some parts of the scenario
	// (phone-shell --parts), are those parts, and AllParts the scenario's.
	// Such a report never passes alone: its Result is PART, PartResult says
	// whether its cases passed, and only the reports of all the parts
	// together (MergeParts) can pass.
	Parts      []string `json:"parts,omitempty"`
	AllParts   []string `json:"all_parts,omitempty"`
	PartResult string   `json:"part_result,omitempty"`
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
	// Covers are the capabilities a skipped case says it calls (the
	// script's covers lines).
	Covers []string `json:"covers,omitempty"`
}

// Sandbox: Missing names the protections not in force on the device (a
// certifier, like a payment service seeing "unlocked", may refuse such a
// device), Fallback what it does another way with nothing lost; each
// says why.
type Sandbox struct {
	Kernel   string            `json:"kernel"`
	Missing  map[string]string `json:"missing"`
	Fallback map[string]string `json:"fallback,omitempty"`
	// Foreign says why the kernel the run had is not one the device's
	// system ships (another system's, which the run's root was mounted
	// in): Missing is then that system's, not the device's.
	Foreign string `json:"foreign,omitempty"`
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

// OfParts makes r the report of a run of the parts named, of all the
// scenario's, whose cases passed or not.
func (r *CompatReport) OfParts(parts, all []string, passed bool) {
	r.Parts, r.AllParts, r.Result, r.PartResult = parts, all, "PART", "FAIL"
	if passed {
		r.PartResult = "PASS"
	}
}

// MergeParts is what the reports of one build's runs of the scenario's
// parts say together, every part in exactly one of them; a whole run's
// report stands alone. The merged report's cases are theirs, a capability
// counts as exercised when any of them did, and it says PASS when every
// one did and no capability was left uncalled. It carries no attestation:
// each report's is checked on its own.
func MergeParts(reports []CompatReport) (CompatReport, error) {
	if len(reports) == 0 {
		return CompatReport{}, errors.New("no report")
	}
	first := reports[0]
	if len(first.Parts) == 0 {
		if len(reports) > 1 {
			return CompatReport{}, errors.New("the report of a whole run goes alone")
		}
		return first, nil
	}
	m := first
	m.Cases, m.Capabilities, m.Uncovered, m.Skipped, m.Attestation = nil, map[string]CapStat{}, []string{}, nil, nil
	m.Parts, m.PartResult = nil, ""
	ran := map[string]int{}
	known := map[string]bool{}
	skipped := map[string]string{}
	result := "PASS"
	for i, r := range reports {
		switch {
		case len(r.Parts) == 0 || r.Result != "PART":
			return CompatReport{}, fmt.Errorf("report %d is a whole run's, the others parts'", i+1)
		case r.System != first.System || r.Version != first.Version || r.Proto != first.Proto:
			return CompatReport{}, fmt.Errorf("report %d is of %s %s (%s), report 1 of %s %s (%s)", i+1, r.System, r.Version, r.Proto, first.System, first.Version, first.Proto)
		case !slices.Equal(r.AllParts, first.AllParts):
			return CompatReport{}, fmt.Errorf("report %d has the parts %s, report 1 %s", i+1, strings.Join(r.AllParts, ","), strings.Join(first.AllParts, ","))
		}
		for _, p := range r.Parts {
			if !slices.Contains(first.AllParts, p) {
				return CompatReport{}, fmt.Errorf("report %d ran %q, which is not a part of the scenario", i+1, p)
			}
			if j, dup := ran[p]; dup {
				return CompatReport{}, fmt.Errorf("part %q is in reports %d and %d", p, j, i+1)
			}
			ran[p] = i + 1
		}
		if r.PartResult != "PASS" {
			result = "FAIL"
		}
		if r.Date > m.Date {
			m.Date = r.Date
		}
		m.Cases = append(m.Cases, r.Cases...)
		for name, st := range r.Capabilities {
			known[name] = true
			sum := m.Capabilities[name]
			sum.Calls, sum.OK, sum.Errors = sum.Calls+st.Calls, sum.OK+st.OK, sum.Errors+st.Errors
			m.Capabilities[name] = sum
		}
		for _, name := range r.Uncovered {
			known[name] = true
		}
		for name, why := range r.Skipped {
			known[name] = true
			if _, ok := skipped[name]; !ok {
				skipped[name] = why
			}
		}
	}
	for _, p := range first.AllParts {
		if _, ok := ran[p]; !ok {
			return CompatReport{}, fmt.Errorf("no report has the part %q", p)
		}
	}
	for name := range known {
		if _, ok := m.Capabilities[name]; ok {
			continue
		}
		if why, ok := skipped[name]; ok {
			if m.Skipped == nil {
				m.Skipped = map[string]string{}
			}
			m.Skipped[name] = why
		} else {
			m.Uncovered = append(m.Uncovered, name)
		}
	}
	slices.Sort(m.Uncovered)
	m.Coverage = fmt.Sprintf("%d/%d", len(m.Capabilities), len(known))
	if len(m.Uncovered) > 0 {
		result = "FAIL"
	}
	m.Result = result
	m.Parts = first.AllParts
	return m, nil
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
