package shellcmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kario-os/kario/shell"
)

// partResultEnv names the file where a part's phone-shell of a parallel run
// leaves its cases and snapshot counts for the parent.
const partResultEnv = "PHONE_PART_RESULT"

// partResult is what a part's phone-shell leaves its parent.
type partResult struct {
	Cases          []shell.CaseResult `json:"cases"`
	Snaps          map[string]int     `json:"snaps,omitempty"`
	PixelBaselines string             `json:"pixel_baselines,omitempty"`
}

func writePartResult(path string, r *shell.Runner) error {
	res := partResult{Cases: r.Cases}
	if b := r.Baselines; b != nil {
		res.Snaps, res.PixelBaselines = b.Counts(), b.PixelBaselines()
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// parallelRun is --parallel: the scenario's parts at once, each in a
// phone-shell of its own (its services and state, from the boot), as go
// test ./shell and the emulators run them. Each part writes its snapshots
// and PNGs into a directory of its own ("parts/<name>" in the run's),
// moved into the run's when it is done; the JUnit report and the snapshot
// counts are put together here, the compatibility reports are one a part
// (phone-cts-verify checks them together).
type parallelRun struct {
	self                                    string
	args                                    []string // this program's, but --parallel
	script                                  string
	only                                    *regexp.Regexp
	snapDir, pixelDir, junit, cts, printPNG string
	out                                     io.Writer
}

// withoutFlag is args without the boolean flag name.
func withoutFlag(args []string, name string) []string {
	var out []string
	for _, a := range args {
		if f, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "="); strings.HasPrefix(a, "-") && f == name {
			continue
		}
		out = append(out, a)
	}
	return out
}

// partReport is where a part's compatibility report goes: beside the run's,
// named for the part (cts-report.json: cts-report-<part>.json).
func partReport(path, part string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "-" + part + ext
}

// selected are the parts with a case --only selects (all without it).
func (p parallelRun) selected() ([]string, error) {
	all, err := shell.ScriptParts(p.script)
	if err != nil || p.only == nil {
		return all, err
	}
	var parts []string
	for _, name := range all {
		text, err := shell.SelectParts(p.script, []string{name})
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(text, "\n") {
			if c, ok := strings.CutPrefix(l, "== "); ok && p.only.MatchString(c) {
				parts = append(parts, name)
				break
			}
		}
	}
	return parts, nil
}

func (p parallelRun) child(name, tmp string) (*exec.Cmd, error) {
	args := append(slices.Clone(p.args), "--parts", name, "--junit=", "--print-png=")
	if p.snapDir != "" {
		args = append(args, "--snap-dir", filepath.Join(p.snapDir, "parts", name))
	}
	if p.pixelDir != "" {
		args = append(args, "--pixel-dir", filepath.Join(p.pixelDir, "parts", name))
	}
	if p.cts != "" {
		args = append(args, "--cts", partReport(p.cts, name))
	}
	cmd := exec.Command(p.self, args...)
	cmd.Env = append(os.Environ(), partResultEnv+"="+filepath.Join(tmp, name+".json"))
	if os.Getenv(shell.MotionStepsEnv) != "" {
		cmd.Env = append(cmd.Env, shell.MotionStepsEnv+"="+filepath.Join(tmp, name+".steps"))
	}
	log, err := os.Create(filepath.Join(tmp, name+".log"))
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return cmd, nil
}

// adopt moves what a part wrote in its own directory into the run's; the
// run manifest is merged instead.
func adopt(from, to string) error {
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		if rel == shell.RunManifest {
			return nil
		}
		dst := filepath.Join(to, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.Rename(path, dst)
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ownPaths is a part's output with the paths into its own directories
// made the run's, where merge moves what it wrote.
func (p parallelRun) ownPaths(name string, raw []byte) []byte {
	sep := string(filepath.Separator)
	for _, d := range []string{p.snapDir, p.pixelDir} {
		if d != "" {
			raw = bytes.ReplaceAll(raw, []byte(filepath.Join(d, "parts", name)+sep), []byte(filepath.Clean(d)+sep))
		}
	}
	return raw
}

// run runs the parts and reports whether every one passed.
func (p parallelRun) run() bool {
	parts, err := p.selected()
	if err != nil {
		fail("--parallel: " + err.Error())
	}
	tmp, err := os.MkdirTemp("", "phone-parallel-")
	if err != nil {
		fail(err.Error())
	}
	defer os.RemoveAll(tmp)
	for _, d := range []string{p.snapDir, p.pixelDir} {
		if d != "" {
			os.RemoveAll(filepath.Join(d, "parts"))
			os.RemoveAll(filepath.Join(d, "diff"))
		}
	}
	if p.pixelDir != "" {
		os.Remove(filepath.Join(p.pixelDir, shell.RunManifest))
	}
	fmt.Fprintf(p.out, "phone-shell selftest: the parts %s at once, each in a phone-shell of its own\n", strings.Join(parts, ","))
	ok := true
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range parts {
		cmd, err := p.child(name, tmp)
		if err == nil {
			err = cmd.Start()
		}
		if err != nil {
			fmt.Fprintf(p.out, "=== part %s: %v\n", name, err)
			ok = false
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			t0 := time.Now()
			err := cmd.Wait()
			mu.Lock()
			defer mu.Unlock()
			status := "PASS"
			if err != nil {
				status, ok = "FAIL ("+err.Error()+")", false
			}
			fmt.Fprintf(p.out, "=== part %s: %s in %.0f s\n", name, status, time.Since(t0).Seconds())
			if raw, err := os.ReadFile(filepath.Join(tmp, name+".log")); err == nil {
				_, _ = p.out.Write(p.ownPaths(name, raw))
			}
		}()
	}
	wg.Wait()
	return p.merge(parts, tmp, ok)
}

// merge puts the parts' results together, in the script's order; ok: every
// part's phone-shell ended well.
func (p parallelRun) merge(parts []string, tmp string, ok bool) bool {
	at := map[string]int{}
	for i, l := range strings.Split(p.script, "\n") {
		if c, found := strings.CutPrefix(l, "== "); found {
			if _, seen := at[c]; !seen {
				at[c] = i
			}
		}
	}
	var cases []shell.CaseResult
	snaps := map[string]int{}
	pixelBase := ""
	var manifests []string
	for _, name := range parts {
		var res partResult
		raw, err := os.ReadFile(filepath.Join(tmp, name+".json"))
		if err == nil {
			err = json.Unmarshal(raw, &res)
		}
		if err != nil {
			fmt.Fprintf(p.out, "phone-shell: part %s left no result: %v\n", name, err)
			ok = false
		}
		cases = append(cases, res.Cases...)
		for k, n := range res.Snaps {
			snaps[k] += n
		}
		pixelBase = firstOf(pixelBase, res.PixelBaselines)
		for _, d := range []string{p.snapDir, p.pixelDir} {
			if d == "" {
				continue
			}
			if err := adopt(filepath.Join(d, "parts", name), d); err != nil {
				fmt.Fprintln(p.out, "phone-shell:", err)
				ok = false
			}
		}
		if p.pixelDir != "" {
			manifests = append(manifests, filepath.Join(p.pixelDir, "parts", name, shell.RunManifest))
		}
	}
	slices.SortStableFunc(cases, func(a, b shell.CaseResult) int { return at[a.Name] - at[b.Name] })
	if p.pixelDir != "" {
		if err := shell.MergeRunManifests(filepath.Join(p.pixelDir, shell.RunManifest), manifests); err != nil {
			fmt.Fprintln(p.out, "phone-shell:", err)
			ok = false
		}
	}
	for _, d := range []string{p.snapDir, p.pixelDir} {
		if d != "" {
			os.RemoveAll(filepath.Join(d, "parts"))
		}
	}
	if p.junit != "" {
		if err := shell.WriteJUnit(p.junit, "phone-selftest", cases); err != nil {
			fmt.Fprintln(p.out, "junit:", err)
		}
	}
	if len(snaps) > 0 {
		fmt.Fprintf(p.out, "PHONE-SNAPSHOTS: %s\n", shell.SnapCounts(snaps))
	}
	if pixelBase != "" {
		fmt.Fprintf(p.out, "PHONE-SNAPSHOTS-PIXEL: baselines %s; this run's hashes %s\n", pixelBase, filepath.Join(p.pixelDir, shell.RunManifest))
	}
	if steps := os.Getenv(shell.MotionStepsEnv); steps != "" {
		var all []byte
		for _, name := range parts {
			raw, _ := os.ReadFile(filepath.Join(tmp, name+".steps"))
			all = append(all, raw...)
		}
		if err := os.WriteFile(steps, all, 0o644); err == nil {
			if rep, err := shell.MotionReport(steps); err == nil {
				fmt.Fprintln(p.out, "PHONE-GUI-MOTION:", rep)
			}
		}
	}
	if p.pixelDir != "" {
		printSnapshots(p.out, p.pixelDir, p.printPNG)
	}
	if p.cts != "" {
		fmt.Fprintf(p.out, "PHONE-CTS: a report a part, %s: phone-cts-verify --root-beside checks them together\n", partReport(p.cts, "*"))
	}
	pass, ran := 0, 0
	for _, c := range cases {
		switch c.Status {
		case "FAIL":
			ok = false
			fmt.Fprintf(p.out, "PHONE-SELFTEST-FAILED: %s\n", strings.TrimSuffix(c.Name+": "+c.Message, ": "))
		case "PASS":
			pass++
		}
		if c.Status != "SKIP" {
			ran++
		}
	}
	verdict := "PASS"
	if !ok {
		verdict = "FAIL"
	}
	fmt.Fprintf(p.out, "PHONE-SELFTEST-RESULT: %s (%d/%d cases), %d parts at once\n", verdict, pass, ran, len(parts))
	return ok
}
