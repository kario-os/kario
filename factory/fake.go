package factory

import (
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

// Fake is a unit in a directory: kernel trees with every device the SKU
// declares, present and working; keystored over a real keystore.Store in
// the directory (unless the caller has a keystored), telephonyd's status,
// audiod's tone, and the ALSA helpers answering from the SKU's verbs and
// the microphone's level (louder by toneRise while the tone plays). The tests use it,
// and the runner on a developer's machine (phone-factory-test --fake), the
// selftest's factory screen talking to that runner.
type Fake struct {
	Env   *Env
	SKU   SKU
	Dir   string
	Store *keystore.Store

	mu      sync.Mutex
	tel     telStatus
	verbs   []string
	micDBFS float64
	tones   int
	// silent: the speaker's tone does not reach the microphone.
	silent bool
	// paths are each check's files: Remove takes them away.
	paths map[string][]string
	lns   []net.Listener
}

// FakeOptions: Keystore serves a keystored from a store in the directory;
// RunDir is the services' directory otherwise (the Shell's own keystored
// and audiod in the selftest).
type FakeOptions struct {
	Keystore bool
	RunDir   string
}

// skus are the SKUs the tests and the selftest run factory mode for, as
// tools/sku.py writes them.
//
//go:embed testdata/*.json
var skus embed.FS

// FakeSKU is one of them by its id.
func FakeSKU(id string) (SKU, error) {
	b, err := skus.ReadFile("testdata/" + id + ".json")
	if err != nil {
		return SKU{}, fmt.Errorf("no test SKU %q", id)
	}
	var s SKU
	if err := json.Unmarshal(b, &s); err != nil {
		return SKU{}, fmt.Errorf("test SKU %s: %w", id, err)
	}
	return s, nil
}

// NewFake builds the unit for the SKU in dir.
func NewFake(dir string, s SKU, o FakeOptions) (*Fake, error) {
	f := &Fake{SKU: s, Dir: dir, paths: map[string][]string{}, micDBFS: -40,
		tel: telStatus{Backend: "mm", Sim: "ready", IMEI: "fake"}}
	if s.Audio.Verb != "" {
		f.verbs = append(f.verbs, s.Audio.Verb)
	}
	if s.Audio.CallVerb != "" && s.Audio.CallVerb != s.Audio.Verb {
		f.verbs = append(f.verbs, s.Audio.CallVerb)
	}
	run := o.RunDir
	if run == "" {
		run = filepath.Join(dir, "run")
	}
	f.Env = &Env{Sys: filepath.Join(dir, "sys"), Dev: filepath.Join(dir, "dev"), Proc: filepath.Join(dir, "proc"), RunDir: run,
		Run: f.run, Sleep: f.sleep, ModemWait: 0}
	if err := f.build(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(run, 0o755); err != nil {
		return nil, err
	}
	if o.Keystore {
		st, err := keystore.Open(filepath.Join(dir, "keystore"))
		if err != nil {
			return nil, err
		}
		f.Store = st
		if err := f.serve(svc.KeystoredSock, f.keystored); err != nil {
			return nil, err
		}
	}
	for name, h := range map[string]wire.Handler{svc.TelephonydSock: f.telephonyd, svc.AudiodSock: f.audiod} {
		if err := f.serve(name, h); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// Close stops the fake services.
func (f *Fake) Close() {
	for _, ln := range f.lns {
		ln.Close()
	}
	f.lns = nil
}

func (f *Fake) serve(name string, h wire.Handler) error {
	path := filepath.Join(f.Env.RunDir, name)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	f.lns = append(f.lns, ln)
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go wire.ServeConn(wire.New(nc), h)
		}
	}()
	return nil
}

func (f *Fake) keystored(method string, params json.RawMessage) (any, error) {
	var p struct {
		App, Nonce, Alg, Cert, Root string
		CertSM2                     string `json:"cert_sm2"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	switch method {
	case svc.KeyCSR:
		id, pub, pubSM2 := f.Store.CSR()
		return map[string]any{"device_id": id, "device_pub": pub, "device_pub_sm2": pubSM2, "provisioned": f.Store.Provisioned()}, nil
	case svc.KeyAttest:
		return f.Store.Attest(p.App, p.Nonce, p.Alg)
	case svc.KeyInstallCert:
		return map[string]bool{"ok": true}, f.Store.InstallCert(p.Cert, p.CertSM2, p.Root)
	}
	return nil, fmt.Errorf("fake keystored: %s", method)
}

func (f *Fake) telephonyd(method string, _ json.RawMessage) (any, error) {
	if method != svc.TelStatus {
		return nil, fmt.Errorf("fake telephonyd: %s", method)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tel, nil
}

// toneRise is how much louder the microphone records with the tone
// playing.
const toneRise = 30.0

func (f *Fake) audiod(method string, _ json.RawMessage) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case svc.AudioPlay:
		f.tones++
		return audio.Stream{ID: "tone"}, nil
	case svc.AudioControl:
		f.tones = max(0, f.tones-1)
		return nil, nil
	}
	return nil, fmt.Errorf("fake audiod: %s", method)
}

// run answers for alsaucm and arecord.
func (f *Fake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch name {
	case "alsaucm":
		if _, err := os.Stat(filepath.Join(f.Env.Sys, "class/sound")); err != nil {
			return nil, errors.New("alsaucm: no card")
		}
		if len(args) >= 3 && args[2] == "set" {
			return []byte("  " + args[len(args)-1] + "=hw:0,1\n"), nil
		}
		var b strings.Builder
		for i, v := range f.verbs {
			fmt.Fprintf(&b, "  %d: %s\n    %s\n", i, v, v)
		}
		return []byte(b.String()), nil
	case "arecord":
		if _, err := os.Stat(filepath.Join(f.Env.Sys, "class/sound")); err != nil {
			return nil, errors.New("arecord: no card")
		}
		db := f.micDBFS
		if f.tones > 0 && !f.silent {
			db += toneRise
		}
		return sine(db, micRate*2), nil
	}
	return nil, fmt.Errorf("fake: no program %s", name)
}

// sine is n 16-bit samples of a 1 kHz sine at the given RMS level.
func sine(dbfs float64, n int) []byte {
	amp := 32767 * math.Sqrt2 * math.Pow(10, dbfs/20)
	out := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := amp * math.Sin(2*math.Pi*1000*float64(i)/micRate)
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v)))
	}
	return out
}

// sleep moves the fake RTC on.
func (f *Fake) sleep(d time.Duration) {
	p := filepath.Join(f.Env.Sys, "class/rtc/rtc0/since_epoch")
	if s, err := read(p); err == nil {
		n, _ := strconv.ParseInt(s, 10, 64)
		_ = os.WriteFile(p, []byte(strconv.FormatInt(n+int64((d+time.Second-1)/time.Second), 10)+"\n"), 0o644)
	}
}

// SetTelephony sets what telephonyd answers: imei "" is no modem; sim one
// of telephonyd's SIM states.
func (f *Fake) SetTelephony(imei, sim string) {
	f.mu.Lock()
	f.tel.IMEI, f.tel.Sim = imei, sim
	f.mu.Unlock()
}

// SetMic sets the level the microphone records.
func (f *Fake) SetMic(dbfs float64) {
	f.mu.Lock()
	f.micDBFS = dbfs
	f.mu.Unlock()
}

// Remove takes away the hardware a check looks for, so that it fails.
func (f *Fake) Remove(check string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch check {
	case "modem":
		f.tel.IMEI = ""
		return nil
	case "sim":
		f.tel.Sim = "absent"
		return nil
	case "audio.ucm":
		f.verbs = nil
		return nil
	case "keystore":
		return os.Remove(filepath.Join(f.Env.RunDir, svc.KeystoredSock))
	}
	ps, ok := f.paths[check]
	if !ok {
		return fmt.Errorf("fake: nothing to remove for %s", check)
	}
	for _, p := range ps {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

func (f *Fake) write(path, content string) error {
	full := filepath.Join(f.Dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content+"\n"), 0o644)
}

func (f *Fake) owns(check, path string) {
	f.paths[check] = append(f.paths[check], filepath.Join(f.Dir, path))
}

// formatBitmap prints capability bits as sysfs does.
func formatBitmap(set ...int) string {
	hi := 0
	for _, n := range set {
		hi = max(hi, n/bits.UintSize)
	}
	words := make([]uint64, hi+1)
	for _, n := range set {
		words[n/bits.UintSize] |= 1 << (n % bits.UintSize)
	}
	parts := make([]string, len(words))
	for i, w := range words {
		parts[len(words)-1-i] = strconv.FormatUint(w, 16)
	}
	return strings.Join(parts, " ")
}

type fakeInput struct {
	check               string
	name                string
	key, sw, abs, ff, p []int
}

func (f *Fake) build() error {
	s := f.SKU
	var ins []fakeInput
	if kp := s.Factory.Keypad; kp != nil {
		codes := kp.Codes
		if kp.Chip == "usb" {
			codes = []int{key1, keyA, keyEnter}
		}
		ins = append(ins, fakeInput{check: "input.keypad", name: "fake " + kp.Chip + " keypad", key: codes})
	}
	for _, b := range s.Factory.Buttons {
		ins = append(ins, fakeInput{check: "input.buttons", name: b, key: []int{116}})
	}
	if s.SoundSwitch != "" {
		ins = append(ins, fakeInput{check: "input.sound-switch", name: s.SoundSwitch, abs: []int{absSndProfile}})
	}
	if s.Lid != "" {
		ins = append(ins, fakeInput{check: "input.lid", name: s.Lid, sw: []int{swLid}})
	}
	if s.Touch {
		ins = append(ins, fakeInput{check: "input.touch", name: "fake touchscreen", abs: []int{absMTPositionX, absMTPositionX + 1}, p: []int{propDirect}})
	}
	if s.Has("haptics") {
		ins = append(ins, fakeInput{check: "vibrator", name: "fake haptics", ff: []int{ffRumble}})
	}
	for i, in := range ins {
		d := fmt.Sprintf("sys/class/input/event%d", i)
		files := map[string]string{"device/name": in.name, "device/capabilities/key": formatBitmap(in.key...),
			"device/capabilities/sw": formatBitmap(in.sw...), "device/capabilities/abs": formatBitmap(in.abs...),
			"device/capabilities/ff": formatBitmap(in.ff...), "device/properties": formatBitmap(in.p...)}
		for k, v := range files {
			if err := f.write(filepath.Join(d, k), v); err != nil {
				return err
			}
		}
		f.owns(in.check, d)
	}
	w, h := s.Factory.Panel[0], s.Factory.Panel[1]
	if w == 0 {
		w, h = 480, 640
	}
	conn := "sys/class/drm/card0-DSI-1"
	if err := f.write(conn+"/status", "connected"); err != nil {
		return err
	}
	if err := f.write(conn+"/modes", fmt.Sprintf("%dx%d", w, h)); err != nil {
		return err
	}
	f.owns("display.mode", "sys/class/drm")
	card := strings.TrimPrefix(s.Audio.Card, "hw:")
	if _, err := strconv.Atoi(card); err == nil || card == "" {
		card = "fakecard"
	}
	if err := f.write("sys/class/sound/card0/id", card); err != nil {
		return err
	}
	f.owns("audio.card", "sys/class/sound")
	sensors := []struct{ part, ch, v string }{
		{"light", "in_illuminance_raw", "120"}, {"proximity", "in_proximity_raw", "4"},
		{"accelerometer", "in_accel_x_raw", "-12"}, {"gyroscope", "in_anglvel_x_raw", "0"}, {"magnetometer", "in_magn_x_raw", "310"},
	}
	n := 0
	for _, se := range sensors {
		if !s.Has(se.part) {
			continue
		}
		d := fmt.Sprintf("sys/bus/iio/devices/iio:device%d", n)
		n++
		if err := f.write(d+"/name", "fake-"+se.part); err != nil {
			return err
		}
		if err := f.write(d+"/"+se.ch, se.v); err != nil {
			return err
		}
		f.owns("sensor."+se.part, d)
	}
	for k, v := range map[string]string{"battery/type": "Battery", "battery/present": "1", "battery/voltage_now": "3920000",
		"battery/capacity": "64", "battery/status": "Charging", "usb/type": "USB", "usb/online": "1"} {
		if err := f.write("sys/class/power_supply/"+k, v); err != nil {
			return err
		}
	}
	f.owns("battery", "sys/class/power_supply/battery")
	if err := f.write("sys/class/rtc/rtc0/name", "rtc-fake"); err != nil {
		return err
	}
	if err := f.write("sys/class/rtc/rtc0/since_epoch", "1795651200"); err != nil {
		return err
	}
	f.owns("rtc", "sys/class/rtc")
	gb := max(s.Factory.StorageGB, 8)
	if err := f.write("sys/block/mmcblk0/size", strconv.FormatInt(int64(gb)*1_000_000_000/512+2048, 10)); err != nil {
		return err
	}
	if err := f.write("sys/block/mmcblk0/removable", "0"); err != nil {
		return err
	}
	if err := f.write("sys/block/loop0/size", "2048"); err != nil {
		return err
	}
	f.owns("storage", "sys/block/mmcblk0")
	mb := max(s.MemoryMB, 512)
	dt := "sys/firmware/devicetree/base"
	cells := func(n uint32) string { return string(binary.BigEndian.AppendUint32(nil, n)) }
	reg := make([]byte, 16)
	binary.BigEndian.PutUint64(reg[0:], 0x80000000)
	binary.BigEndian.PutUint64(reg[8:], uint64(mb)<<20)
	for k, v := range map[string]string{"#address-cells": cells(2), "#size-cells": cells(2), "memory@80000000/reg": string(reg)} {
		full := filepath.Join(f.Dir, dt, k)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(v), 0o644); err != nil {
			return err
		}
	}
	if err := f.write("proc/meminfo", fmt.Sprintf("MemTotal: %d kB", (mb-64)<<10)); err != nil {
		return err
	}
	f.owns("memory", dt)
	f.owns("memory", "proc/meminfo")
	if s.Has("wifi") {
		if err := os.MkdirAll(filepath.Join(f.Dir, "sys/class/ieee80211/phy0"), 0o755); err != nil {
			return err
		}
		f.owns("wifi", "sys/class/ieee80211")
	}
	if s.Has("bluetooth") {
		if err := os.MkdirAll(filepath.Join(f.Dir, "sys/class/bluetooth/hci0"), 0o755); err != nil {
			return err
		}
		f.owns("bluetooth", "sys/class/bluetooth")
	}
	return os.MkdirAll(filepath.Join(f.Dir, "dev"), 0o755)
}
