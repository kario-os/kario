package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// kario run -profile FILE (29-app-development.md §3, as Instruments): the
// Shell writes the app's timeline (phone-shell --profile, shell/trace.go)
// beside FILE as it happens; when the run ends kario makes FILE of it, a
// Chrome Trace Event JSON object that Perfetto (ui.perfetto.dev) and
// chrome://tracing open. Under Node.js the app also writes V8's CPU
// profiles (--cpu-prof) into cpuProfileDir(FILE), which VS Code and Chrome
// DevTools open.

// rawTrace is where the Shell writes the timeline for FILE.
func rawTrace(file string) string { return file + ".part" }

// cpuProfileDir is where Node.js writes the app's CPU profiles, one for
// each process the run started.
func cpuProfileDir(file string) string {
	return strings.TrimSuffix(file, filepath.Ext(file)) + ".cpuprofiles"
}

// traceFile is the trace kario writes: Chrome's JSON object format.
type traceFile struct {
	TraceEvents     []json.RawMessage `json:"traceEvents"`
	DisplayTimeUnit string            `json:"displayTimeUnit"`
	OtherData       map[string]string `json:"otherData"`
}

// finishTrace turns the Shell's event array (unterminated when the Shell
// was killed) into file, and removes it.
func finishTrace(raw, file string, other map[string]string) (int, error) {
	body, err := os.ReadFile(raw)
	if err != nil {
		return 0, fmt.Errorf("-profile: the Shell wrote no timeline: %w", err)
	}
	body = bytes.TrimSpace(body)
	if !bytes.HasSuffix(body, []byte("]")) {
		body = append(bytes.TrimSuffix(body, []byte(",")), '\n', ']')
	}
	var events []json.RawMessage
	if err := json.Unmarshal(body, &events); err != nil {
		return 0, fmt.Errorf("-profile: %s: %w", raw, err)
	}
	out, err := json.Marshal(traceFile{TraceEvents: events, DisplayTimeUnit: "ms", OtherData: other})
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(file, append(out, '\n'), 0o644); err != nil {
		return 0, err
	}
	return len(events), os.Remove(raw)
}

// cpuProfiles are the CPU profiles Node.js wrote in dir since start.
func cpuProfiles(dir string, start time.Time) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if st, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".cpuprofile") && !st.ModTime().Before(start) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// reportProfile finishes the trace when the run ends and says where it is.
func reportProfile(o runOptions, start time.Time, appID string) error {
	n, err := finishTrace(rawTrace(o.profile), o.profile, map[string]string{
		"app": appID, "engine": o.engine, "device": firstNonEmpty(o.sku, o.device), "started": start.Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "kario run: the timeline, %d events: %s (open it in https://ui.perfetto.dev or chrome://tracing)\n", n, o.profile)
	if o.engine == "node" {
		for _, p := range cpuProfiles(cpuProfileDir(o.profile), start) {
			fmt.Fprintf(os.Stderr, "kario run: the app's JavaScript CPU profile: %s (open it in VS Code or Chrome DevTools)\n", p)
		}
	}
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
