package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Schema names the report's format (docs/40-testing.md §10.3). A field
// added keeps the number; one renamed, dropped or changed in meaning makes
// it kario.factory-report/2.
const Schema = "kario.factory-report/1"

// Result is a check's outcome.
type Result string

const (
	Pass Result = "pass"
	Fail Result = "fail"
	// Skip: the check did not run (the SKU has no such hardware, Kario
	// cannot test it yet, or the operator has not done it): Reason says why.
	Skip Result = "skip"
)

// CheckResult is one check in a report: Value is what it measured, Reason
// why it failed or was skipped.
type CheckResult struct {
	ID         string `json:"id"`
	Kind       Kind   `json:"kind"`
	Required   bool   `json:"required"`
	Result     Result `json:"result"`
	Value      string `json:"value,omitempty"`
	Reason     string `json:"reason,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// Report is one run of the test on one unit.
type Report struct {
	Schema   string `json:"schema"`
	DeviceID string `json:"device_id"`
	SKU      string `json:"sku"`
	Board    string `json:"board"`
	// Mode: "auto" (the automatic checks only) or "full" (and the
	// interactive ones the operator did on the factory screen).
	Mode       string    `json:"mode"`
	Started    time.Time `json:"started"`
	DurationMS int64     `json:"duration_ms"`
	// Passed: every required check passed (the exit code is 0).
	Passed bool `json:"passed"`
	// Refused: factory mode is not available, so nothing ran (the exit
	// code is 3).
	Refused string        `json:"refused,omitempty"`
	Checks  []CheckResult `json:"checks"`
}

// The runner's exit codes (docs/40-testing.md §10.3).
const (
	ExitPass    = 0 // every required check passed
	ExitFail    = 1 // a required check failed or did not run
	ExitError   = 2 // the test could not run: usage, no SKU, the unit unreachable or not the one asked for
	ExitRefused = 3 // factory mode is not available: the keystore is provisioned or did not answer
)

// Mode names.
const (
	ModeAuto = "auto"
	ModeFull = "full"
)

// Finish sets Passed from the checks.
func (r *Report) Finish() {
	r.Passed = r.Refused == ""
	for _, c := range r.Checks {
		if c.Required && c.Result != Pass {
			r.Passed = false
		}
	}
}

// ExitCode is the runner's exit code for the report.
func (r *Report) ExitCode() int {
	switch {
	case r.Refused != "":
		return ExitRefused
	case r.Passed:
		return ExitPass
	}
	return ExitFail
}

// Result is the report's check of this id.
func (r *Report) Result(id string) (CheckResult, bool) {
	for _, c := range r.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return CheckResult{}, false
}

// Write saves the report as indented JSON, replacing the file whole.
func (r *Report) Write(path string) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// skipped is the result of a check that does not apply to the SKU.
func skipped(c Check) CheckResult {
	return CheckResult{ID: c.ID, Kind: c.Kind, Required: c.Required, Result: Skip, Reason: c.Skip}
}
