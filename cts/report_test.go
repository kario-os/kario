package cts

import (
	"strings"
	"testing"
)

func partReport(parts []string, result string, caps map[string]CapStat, uncovered []string, skipped map[string]string, cases ...string) CompatReport {
	r := CompatReport{System: "Kario", Version: "0.6", Proto: "1", Date: "2026-11-25T10:00:00Z", Capabilities: caps, Uncovered: uncovered, Skipped: skipped}
	for _, c := range cases {
		r.Cases = append(r.Cases, CaseResult{Name: c, Status: "PASS"})
	}
	r.OfParts(parts, []string{"a", "b", "c"}, result == "PASS")
	return r
}

// The reports of the parts of one run, together: their cases, a capability
// exercised in any of them, skipped where none called it and a case that
// would have was skipped, and PASS only with every case and capability.
func TestMergeParts(t *testing.T) {
	a := partReport([]string{"a"}, "PASS", map[string]CapStat{"x": {Calls: 2, OK: 2}}, []string{"y", "z"}, nil, "a1", "a2")
	bc := partReport([]string{"c", "b"}, "PASS", map[string]CapStat{"x": {Calls: 1, Errors: 1}, "y": {Calls: 3, OK: 3}}, []string{}, map[string]string{"z": "b1: needs tel"}, "b1", "c1")
	bc.Date = "2026-11-25T10:05:00Z"
	m, err := MergeParts([]CompatReport{a, bc})
	if err != nil {
		t.Fatal(err)
	}
	if m.Result != "PASS" || m.Coverage != "2/3" || len(m.Uncovered) != 0 || m.Skipped["z"] != "b1: needs tel" || len(m.Cases) != 4 ||
		m.Capabilities["x"] != (CapStat{Calls: 3, OK: 2, Errors: 1}) || m.Date != bc.Date || m.Attestation != nil {
		t.Errorf("merged: %+v", m)
	}
	if a.Result != "PART" || a.PartResult != "PASS" {
		t.Errorf("a part's report passes alone: %s, its cases %s", a.Result, a.PartResult)
	}
	failed := partReport([]string{"c", "b"}, "FAIL", bc.Capabilities, nil, bc.Skipped, "b1", "c1")
	if m, _ := MergeParts([]CompatReport{a, failed}); m.Result != "FAIL" || failed.Result != "PART" {
		t.Error("a failed part's report merged into PASS")
	}
	never := bc
	never.Skipped = nil
	if m, _ := MergeParts([]CompatReport{a, never}); m.Result != "FAIL" || strings.Join(m.Uncovered, " ") != "z" || m.Coverage != "2/3" {
		t.Errorf("a capability no part called: %+v", m)
	}
	other := bc
	other.Version = "0.7"
	edited := bc
	edited.Result = "PASS"
	whole := CompatReport{System: "Kario", Version: "0.6", Result: "PASS"}
	for name, reports := range map[string][]CompatReport{
		"a part missing":             {a},
		"a part twice":               {a, bc, partReport([]string{"a"}, "PASS", nil, nil, nil)},
		"another build":              {a, other},
		"a whole run's report":       {a, whole},
		"a part's report as a whole": {a, edited},
		"two whole runs":             {whole, whole},
		"no report":                  nil,
	} {
		if _, err := MergeParts(reports); err == nil {
			t.Errorf("%s: merged", name)
		}
	}
	if m, err := MergeParts([]CompatReport{whole}); err != nil || m.Result != "PASS" || m.Parts != nil {
		t.Errorf("a whole run's report alone: %+v, %v", m, err)
	}
}
