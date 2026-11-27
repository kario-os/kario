package chargercmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
)

// The test binary is the renderer too: CHARGER_TEST_RENDERER names the
// file it writes every line it was sent to.
func TestMain(m *testing.M) {
	if log := os.Getenv("CHARGER_TEST_RENDERER"); log != "" {
		fakeRenderer(log, os.Getenv("CHARGER_TEST_SCRIPT"), os.Getenv("CHARGER_TEST_SYS"))
		return
	}
	os.Exit(m.Run())
}

// fakeRenderer speaks the protocol as phone-paint on a panel: ready with
// the power key and the panel's power, each "power" answered. What it
// does with the key follows the script:
//   - "hold": holds the power key once the first frame came, exits after
//     the frame that follows;
//   - "look": exits after the first frame;
//   - "low": holds the key at the first frame; once a frame says the
//     start waits for the level, the battery (in the fake sysfs) charges
//     to 3%; exits once a frame says "Starting…";
//   - "sleep": presses the key at the first frame (the screen goes off),
//     exits once the charger suspended (sysfs's power/state says mem).
//
// It exits after 20 s in any case.
func fakeRenderer(logPath, script, sys string) {
	logf, err := os.Create(logPath)
	if err != nil {
		os.Exit(3)
	}
	defer logf.Close()
	go func() {
		time.Sleep(20 * time.Second)
		os.Exit(0)
	}()
	if script == "sleep" {
		go func() {
			for {
				if raw, _ := os.ReadFile(filepath.Join(sys, "power", "state")); string(raw) == "mem" {
					os.Exit(0)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	out := json.NewEncoder(os.Stdout)
	frames := 0
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		fmt.Fprintln(logf, sc.Text())
		var m struct {
			T    string `json:"t"`
			On   bool   `json:"on"`
			Idle struct {
				Hint string `json:"hint"`
			} `json:"idle"`
		}
		_ = json.Unmarshal(sc.Bytes(), &m)
		switch m.T {
		case "hello":
			_ = out.Encode(map[string]any{"t": "ready", "proto": "phone.frame/1.3", "features": []string{"kms", "keys", "power"}, "renderer": map[string]string{"name": "fake"}})
		case "power":
			_ = out.Encode(map[string]any{"t": "power", "on": m.On})
		case "frame":
			frames++
			if frames == 1 {
				_ = out.Encode(map[string]any{"t": "key", "key": "power", "text": "", "long": script != "sleep", "ms": 2000})
			}
			switch {
			case script == "look", script == "hold" && frames == 2:
				return
			case script == "low" && m.Idle.Hint == "Starting at 3%":
				_ = os.WriteFile(filepath.Join(sys, "class", "power_supply", "bq27411-0", "capacity"), []byte("3\n"), 0o644)
			case script == "low" && m.Idle.Hint == "Starting…":
				return
			}
		}
	}
}

// chargerRun is what a run of phone-charger left: its log, the frames
// it sent and its last status.
type chargerRun struct {
	log    string
	hello  string
	keys   string
	frames []map[string]any
	status map[string]any
	sys    string
}

// lines is a frame's idle lines and hint, "|" between them.
func lines(f map[string]any) string {
	idle := f["idle"].(map[string]any)
	var out []string
	for _, l := range idle["lines"].([]any) {
		out = append(out, l.(string))
	}
	return strings.Join(append(out, idle["hint"].(string)), "|")
}

// runCharger runs phone-charger on a fake sysfs (the 6T's bq27411 with
// battery's attributes, its PMI8998 USB charger, an RTC, /sys/power with
// mem, a power key that wakes) and the fake renderer playing script; sku
// is sku.json's extra fields.
func runCharger(t *testing.T, script, sku string, battery map[string]string) chargerRun {
	t.Helper()
	dir := t.TempDir()
	saved := hal.SysRoot
	hal.SysRoot = filepath.Join(dir, "sys")
	t.Cleanup(func() { hal.SysRoot = saved })
	files := map[string]string{
		"class/power_supply/pmi8998/type":                "USB",
		"class/power_supply/pmi8998/online":              "1",
		"class/power_supply/pmi8998/usb_type":            "[SDP] DCP CDP",
		"class/power_supply/pmi8998/device/power/wakeup": "disabled",
		"class/rtc/rtc0/wakealarm":                       "",
		"class/input/input0/device/power/wakeup":         "enabled",
		"power/state":                                    "freeze mem",
		"power/wakeup_count":                             "42",
	}
	for f, v := range battery {
		files["class/power_supply/bq27411-0/"+f] = v
	}
	for f, v := range files {
		p := filepath.Join(hal.SysRoot, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	skuFile := filepath.Join(dir, "sku.json")
	if err := os.WriteFile(skuFile, []byte(`{"id":"test","device":"bar-t9","width":1080,"height":2180,"scale":4.5`+sku+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "settings.json"), []byte(`{"language":"en","theme":"amber"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sent := filepath.Join(dir, "sent.jsonl")
	t.Setenv("CHARGER_TEST_RENDERER", sent)
	t.Setenv("CHARGER_TEST_SCRIPT", script)
	t.Setenv("CHARGER_TEST_SYS", hal.SysRoot)
	status := filepath.Join(dir, "run", "charger")
	var log bytes.Buffer
	code := run([]string{"--sku", skuFile, "--renderer", exe, "--state-dir", state, "--status", status, "--dry-run"}, &log, dir)
	if code != 1 || !strings.Contains(log.String(), "the renderer exited") {
		t.Fatalf("run: %d, log:\n%s", code, log.String())
	}
	raw, err := os.ReadFile(sent)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Split(strings.TrimSpace(string(raw)), "\n")
	res := chargerRun{log: log.String(), hello: all[0], keys: all[1], sys: hal.SysRoot}
	for _, l := range all[2:] {
		var f map[string]any
		if json.Unmarshal([]byte(l), &f) == nil && f["t"] == "frame" {
			res.frames = append(res.frames, f)
		}
	}
	raw, _ = os.ReadFile(status)
	if err := json.Unmarshal(raw, &res.status); err != nil {
		t.Fatalf("status %q: %v", raw, err)
	}
	return res
}

var charging64 = map[string]string{"type": "Battery", "capacity": "64", "status": "Charging"}

func TestRunStartsTheSystemOnAHold(t *testing.T) {
	r := runCharger(t, "hold", "", charging64)
	if !strings.Contains(r.log, "dry run: would reboot") {
		t.Errorf("no reboot on the hold; log:\n%s", r.log)
	}
	// Without "suspend" in sku.json (the emulator's), the Shell's no sleep.
	if !strings.Contains(r.log, "sleep: off (the board cannot be woken from suspend)") || r.status["sleep"] != "off: the board cannot be woken from suspend" {
		t.Errorf("sleep %v; log:\n%s", r.status["sleep"], r.log)
	}
	var hello struct {
		T, Proto string
		Device   struct{ ID string }
	}
	if err := json.Unmarshal([]byte(r.hello), &hello); err != nil || hello.T != "hello" || hello.Proto != "phone.frame/1" || hello.Device.ID != "bar-t9" {
		t.Errorf("hello %q", r.hello)
	}
	if r.keys != `{"power_long":2000,"t":"keys"}` {
		t.Errorf("keys %q, want the power key's hold at 2000 ms", r.keys)
	}
	if len(r.frames) != 2 {
		t.Fatalf("%d frames, want 2: %v", len(r.frames), r.frames)
	}
	st := r.frames[0]["status"].(map[string]any)
	if st["battery"] != 64.0 || st["charging"] != true || r.frames[0]["theme"] != "amber" || lines(r.frames[0]) != "Charging, 64%|Hold the power key to start" {
		t.Errorf("first frame %v", r.frames[0])
	}
	if l := lines(r.frames[1]); l != "Charging, 64%|Starting…" {
		t.Errorf("after the hold: %q", l)
	}
	if r.status["leaving"] != "reboot" || r.status["level"] != 64.0 || r.status["screen"] != "on" {
		t.Errorf("status %v", r.status)
	}
}

// The 6T's gauge full, with its current: the screen says Full and the
// current.
func TestRunShowsFullAndTheCurrent(t *testing.T) {
	r := runCharger(t, "look", "", map[string]string{"type": "Battery", "capacity": "100", "status": "Full", "capacity_level": "Full", "current_now": "120000"})
	if len(r.frames) != 1 || lines(r.frames[0]) != "Full|120 mA|Hold the power key to start" {
		t.Fatalf("frames %v", r.frames)
	}
	if r.status["status"] != "Full" || r.status["current_ua"] != 120000.0 {
		t.Errorf("status %v", r.status)
	}
}

// At 2% a hold waits; the battery at 3%, the system starts.
func TestRunLowBatteryStartsOnceCharged(t *testing.T) {
	r := runCharger(t, "low", "", map[string]string{"type": "Battery", "capacity": "2", "status": "Charging"})
	var seen []string
	for _, f := range r.frames {
		if l := lines(f); len(seen) == 0 || seen[len(seen)-1] != l {
			seen = append(seen, l)
		}
	}
	want := []string{
		"Charging, 2% - too low to start|Hold the power key to start",
		"Charging, 2% - too low to start|Starting at 3%",
		"Charging, 3%|Starting…",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Errorf("screens:\n%s\nwant:\n%s\nlog:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"), r.log)
	}
	low := strings.Index(r.log, "battery at 2%: too low to start; starting at 3%")
	reboot := strings.Index(r.log, "dry run: would reboot")
	if low < 0 || reboot < low || !strings.Contains(r.log, "battery at 3%: starting") {
		t.Errorf("log:\n%s", r.log)
	}
}

// On a board that sleeps, the screen off: the phone suspends through
// /sys/power with the RTC set for the refresh, the charger made a wake
// source.
func TestRunSuspendsWithTheScreenOff(t *testing.T) {
	r := runCharger(t, "sleep", `,"suspend":true`, charging64)
	if !strings.Contains(r.log, "sleep: mem 5s after the screen goes off; woken by the power key, the RTC every 10m0s and the chargers pmi8998") {
		t.Errorf("no sleep at the start; log:\n%s", r.log)
	}
	if !strings.Contains(r.log, "sleep: mem until ") {
		t.Fatalf("never suspended; log:\n%s", r.log)
	}
	for f, want := range map[string]string{"power/state": "mem", "power/wakeup_count": "42", "class/rtc/rtc0/wakealarm": "0",
		"class/power_supply/pmi8998/device/power/wakeup": "enabled"} {
		if raw, _ := os.ReadFile(filepath.Join(r.sys, f)); string(raw) != want {
			t.Errorf("%s: %q, want %q", f, raw, want)
		}
	}
	if r.status["sleep"] != "mem" || r.status["screen"] != "off" || r.status["panel"] != "off" {
		t.Errorf("status %v", r.status)
	}
}
