package factory

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// unit is one run's state the checks share: telephonyd's status is asked
// once for the modem and the SIM.
type unit struct {
	*Env
	sku    SKU
	tel    *telStatus
	telErr error
	csr    *keystoreCSR
	csrErr error
}

type measure func(ctx context.Context, u *unit) (string, error)

var autos = map[string]measure{
	"input.keypad":         checkKeypad,
	"input.buttons":        checkButtons,
	"input.sound-switch":   checkSoundSwitch,
	"input.lid":            checkLid,
	"input.touch":          checkTouch,
	"display.mode":         checkDisplay,
	"audio.card":           checkAudioCard,
	"audio.ucm":            checkUCM,
	"vibrator":             checkVibrator,
	"sensor.light":         iioCheck("in_illuminance"),
	"sensor.proximity":     iioCheck("in_proximity"),
	"sensor.accelerometer": iioCheck("in_accel_"),
	"sensor.gyroscope":     iioCheck("in_anglvel_"),
	"sensor.magnetometer":  iioCheck("in_magn_"),
	"battery":              checkBattery,
	"rtc":                  checkRTC,
	"storage":              checkStorage,
	"memory":               checkMemory,
	"wifi":                 checkWifi,
	"bluetooth":            checkBluetooth,
	"modem":                checkModem,
	"sim":                  checkSIM,
	"keystore":             checkKeystore,
}

// RunAuto runs the plan's automatic checks in order and returns their
// results with the unit's device id ("" when the keystore does not say).
func (e *Env) RunAuto(ctx context.Context, s SKU, plan []Check) ([]CheckResult, string) {
	u := &unit{Env: e, sku: s}
	var out []CheckResult
	for _, c := range Only(plan, Auto) {
		if c.Skip != "" {
			out = append(out, skipped(c))
			continue
		}
		t0 := time.Now()
		r := CheckResult{ID: c.ID, Kind: c.Kind, Required: c.Required, Result: Pass}
		m := autos[c.ID]
		if m == nil {
			r.Result, r.Reason = Skip, "no automatic check of this id"
		} else if v, err := m(ctx, u); err != nil {
			r.Result, r.Value, r.Reason = Fail, v, err.Error()
		} else {
			r.Value = v
		}
		r.DurationMS = time.Since(t0).Milliseconds()
		out = append(out, r)
	}
	csr, _ := u.keystoreOnce()
	return out, csr.DeviceID
}

func (u *unit) keystoreOnce() (keystoreCSR, error) {
	if u.csr == nil {
		c, err := u.keystore()
		u.csr, u.csrErr = &c, err
	}
	return *u.csr, u.csrErr
}

func (u *unit) telephonyOnce(ctx context.Context) (telStatus, error) {
	if u.tel == nil {
		st, err := u.telephony(ctx)
		u.tel, u.telErr = &st, err
	}
	return *u.tel, u.telErr
}

func names(devs []inputDev) string {
	var n []string
	for _, d := range devs {
		n = append(n, strconv.Quote(d.name))
	}
	if len(n) == 0 {
		return "none"
	}
	return strings.Join(n, ", ")
}

func checkKeypad(_ context.Context, u *unit) (string, error) {
	kp := u.sku.Factory.Keypad
	devs := u.inputs()
	codes := kp.Codes
	if kp.Chip == "usb" {
		codes = []int{key1, keyA, keyEnter}
	}
	var best inputDev
	var bestMissing []int
	for _, d := range devs {
		var missing []int
		for _, c := range codes {
			if !d.key.has(c) {
				missing = append(missing, c)
			}
		}
		if len(missing) == 0 {
			return fmt.Sprintf("%s (%s, %d keys)", d.name, kp.Chip, len(codes)), nil
		}
		if bestMissing == nil || len(missing) < len(bestMissing) {
			best, bestMissing = d, missing
		}
	}
	if bestMissing == nil {
		return "", fmt.Errorf("no input devices (the %s keypad's is missing)", kp.Chip)
	}
	return "", fmt.Errorf("no input device has the %s keypad's %d keys: the closest, %q, lacks codes %v", kp.Chip, len(codes), best.name, bestMissing)
}

func byName(devs []inputDev, name string) (inputDev, bool) {
	for _, d := range devs {
		if d.name == name {
			return d, true
		}
	}
	return inputDev{}, false
}

func checkButtons(_ context.Context, u *unit) (string, error) {
	devs := u.inputs()
	var missing []string
	for _, n := range u.sku.Factory.Buttons {
		if _, ok := byName(devs, n); !ok {
			missing = append(missing, strconv.Quote(n))
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("missing input devices %s (present: %s)", strings.Join(missing, ", "), names(devs))
	}
	return strings.Join(u.sku.Factory.Buttons, ", "), nil
}

func checkSoundSwitch(_ context.Context, u *unit) (string, error) {
	d, ok := byName(u.inputs(), u.sku.SoundSwitch)
	switch {
	case !ok:
		return "", fmt.Errorf("no input device %q", u.sku.SoundSwitch)
	case d.abs.has(absSndProfile):
		return d.name + " (ABS_SND_PROFILE)", nil
	case d.sw.has(swMuteDevice):
		return d.name + " (SW_MUTE_DEVICE)", nil
	}
	return "", fmt.Errorf("%q reports neither ABS_SND_PROFILE nor SW_MUTE_DEVICE", d.name)
}

func checkLid(_ context.Context, u *unit) (string, error) {
	d, ok := byName(u.inputs(), u.sku.Lid)
	if !ok {
		return "", fmt.Errorf("no input device %q", u.sku.Lid)
	}
	if !d.sw.has(swLid) {
		return "", fmt.Errorf("%q does not report SW_LID", d.name)
	}
	return d.name + " (SW_LID)", nil
}

func checkTouch(_ context.Context, u *unit) (string, error) {
	devs := u.inputs()
	for _, d := range devs {
		if d.props.has(propDirect) && (d.abs.has(absMTPositionX) || d.abs.has(absX)) {
			return d.name, nil
		}
	}
	return "", fmt.Errorf("no direct (touchscreen) input device among %s", names(devs))
}

func checkDisplay(_ context.Context, u *unit) (string, error) {
	want := fmt.Sprintf("%dx%d", u.sku.Factory.Panel[0], u.sku.Factory.Panel[1])
	var seen []string
	for _, conn := range glob(filepath.Join(u.Sys, "class/drm/card*-*")) {
		if st, _ := read(filepath.Join(conn, "status")); st != "connected" {
			continue
		}
		modes, _ := read(filepath.Join(conn, "modes"))
		first, _, _ := strings.Cut(modes, "\n")
		name := strings.SplitN(filepath.Base(conn), "-", 2)[1]
		if first == want {
			return name + " " + first, nil
		}
		seen = append(seen, name+" "+first)
	}
	if len(seen) == 0 {
		return "", fmt.Errorf("no connected display (the panel is %s)", want)
	}
	return strings.Join(seen, ", "), fmt.Errorf("the panel is %s but the connected display's preferred mode is %s", want, strings.Join(seen, ", "))
}

// cardDir is the sound card's sysfs directory: the SKU's ([audio] card,
// hw:N or hw:ID), or the first card.
func (u *unit) cardDir() (string, error) {
	cards := glob(filepath.Join(u.Sys, "class/sound/card*"))
	want := strings.TrimPrefix(u.sku.Audio.Card, "hw:")
	for _, c := range cards {
		id, _ := read(filepath.Join(c, "id"))
		if want == "" || want == strings.TrimPrefix(filepath.Base(c), "card") || want == id {
			return c, nil
		}
	}
	if want == "" {
		return "", errors.New("no sound card")
	}
	return "", fmt.Errorf("no sound card %s (%d others)", u.sku.Audio.Card, len(cards))
}

func checkAudioCard(_ context.Context, u *unit) (string, error) {
	c, err := u.cardDir()
	if err != nil {
		return "", err
	}
	id, _ := read(filepath.Join(c, "id"))
	return filepath.Base(c) + " " + id, nil
}

// checkUCM asks alsaucm for the card's verbs: the board's (sku.json's
// audio verb and call verb) must be among them.
func checkUCM(ctx context.Context, u *unit) (string, error) {
	out, err := u.run(ctx, "alsaucm", "-c", u.sku.Audio.Card, "list", "_verbs")
	if err != nil {
		return "", fmt.Errorf("alsaucm: %w", err)
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		n, name, ok := strings.Cut(strings.TrimSpace(l), ": ")
		if _, err := strconv.Atoi(n); ok && err == nil {
			have[strings.TrimSpace(name)] = true
		}
	}
	var want, missing []string
	for _, v := range []string{u.sku.Audio.Verb, u.sku.Audio.CallVerb} {
		if v != "" && !slices.Contains(want, v) {
			want = append(want, v)
			if !have[v] {
				missing = append(missing, v)
			}
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("the card's UCM lacks verbs %s", strings.Join(missing, ", "))
	}
	return strings.Join(want, ", "), nil
}

func checkVibrator(_ context.Context, u *unit) (string, error) {
	if _, err := os.Stat(filepath.Join(u.Sys, "class/leds/vibrator")); err == nil {
		return "leds/vibrator", nil
	}
	for _, d := range u.inputs() {
		if d.ff.has(ffRumble) || d.ff.has(ffPeriodic) {
			return d.name + " (force feedback)", nil
		}
	}
	return "", errors.New("no vibrator: no leds/vibrator and no force-feedback input device")
}

// iioCheck finds an IIO device with a channel of this prefix and reads it.
func iioCheck(prefix string) measure {
	return func(_ context.Context, u *unit) (string, error) {
		for _, dev := range glob(filepath.Join(u.Sys, "bus/iio/devices/iio:device*")) {
			for _, ch := range glob(filepath.Join(dev, prefix+"*")) {
				base := filepath.Base(ch)
				if !strings.HasSuffix(base, "_raw") && !strings.HasSuffix(base, "_input") {
					continue
				}
				v, err := read(ch)
				name, _ := read(filepath.Join(dev, "name"))
				if err != nil {
					return "", fmt.Errorf("%s: %s unreadable: %w", name, base, err)
				}
				return fmt.Sprintf("%s %s=%s", name, base, v), nil
			}
		}
		return "", fmt.Errorf("no IIO device with %s* channels", prefix)
	}
}

// checkBattery wants the battery present with its voltage and capacity
// readable, and charging (or full) while a charger (USB) is online.
func checkBattery(_ context.Context, u *unit) (string, error) {
	var bat string
	var chargers []string
	for _, ps := range glob(filepath.Join(u.Sys, "class/power_supply/*")) {
		t, _ := read(filepath.Join(ps, "type"))
		if t == "Battery" {
			if p, err := read(filepath.Join(ps, "present")); bat == "" && (err != nil || p != "0") {
				bat = ps
			}
			continue
		}
		if on, _ := read(filepath.Join(ps, "online")); on == "1" {
			chargers = append(chargers, filepath.Base(ps))
		}
	}
	if bat == "" {
		return "", errors.New("no battery present in power_supply")
	}
	uv, err := read(filepath.Join(bat, "voltage_now"))
	microvolts, err2 := strconv.Atoi(uv)
	if err != nil || err2 != nil {
		return "", fmt.Errorf("%s: voltage unreadable", filepath.Base(bat))
	}
	capacity, err := read(filepath.Join(bat, "capacity"))
	if _, err2 := strconv.Atoi(capacity); err != nil || err2 != nil {
		return "", fmt.Errorf("%s: capacity unreadable", filepath.Base(bat))
	}
	status, _ := read(filepath.Join(bat, "status"))
	v := fmt.Sprintf("%s %.2f V %s%% %s", filepath.Base(bat), float64(microvolts)/1e6, capacity, status)
	if len(chargers) == 0 {
		return v, errors.New("no charger online: plug USB in to see the battery charge")
	}
	v += " (" + strings.Join(chargers, ", ") + " online)"
	if status != "Charging" && status != "Full" {
		return v, fmt.Errorf("not charging with %s online (status %s)", strings.Join(chargers, ", "), status)
	}
	return v, nil
}

// checkRTC reads the clock twice a second apart: it must be running.
func checkRTC(_ context.Context, u *unit) (string, error) {
	rtcs := glob(filepath.Join(u.Sys, "class/rtc/rtc*"))
	if len(rtcs) == 0 {
		return "", errors.New("no RTC")
	}
	rtc := rtcs[0]
	at := func() (int64, error) {
		s, err := read(filepath.Join(rtc, "since_epoch"))
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(s, 10, 64)
	}
	t0, err := at()
	if err != nil {
		return "", fmt.Errorf("%s unreadable: %w", filepath.Base(rtc), err)
	}
	u.sleep(1100 * time.Millisecond)
	t1, err := at()
	if err != nil {
		return "", fmt.Errorf("%s unreadable: %w", filepath.Base(rtc), err)
	}
	name, _ := read(filepath.Join(rtc, "name"))
	v := fmt.Sprintf("%s (%s) %d", filepath.Base(rtc), name, t1)
	if t1 <= t0 {
		return v, fmt.Errorf("%s is not running (%d, then %d a second later)", filepath.Base(rtc), t0, t1)
	}
	return v, nil
}

// internalDisk: a block device that is the phone's own storage, not a
// partition's view, a RAM disk, a mapper or a card.
func internalDisk(dir string) bool {
	n := filepath.Base(dir)
	for _, p := range []string{"loop", "ram", "zram", "dm-", "md", "nbd", "sr", "mtdblock", "fd"} {
		if strings.HasPrefix(n, p) {
			return false
		}
	}
	if strings.HasPrefix(n, "mmcblk") && (strings.Contains(n, "boot") || strings.Contains(n, "rpmb")) {
		return false
	}
	r, _ := read(filepath.Join(dir, "removable"))
	return r != "1"
}

func checkStorage(_ context.Context, u *unit) (string, error) {
	var total int64
	var disks []string
	for _, b := range glob(filepath.Join(u.Sys, "block/*")) {
		if !internalDisk(b) {
			continue
		}
		s, _ := read(filepath.Join(b, "size"))
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n == 0 {
			continue
		}
		total += n * 512
		disks = append(disks, filepath.Base(b))
	}
	v := fmt.Sprintf("%.1f GB (%s)", float64(total)/1e9, strings.Join(disks, ", "))
	if want := int64(u.sku.Factory.StorageGB) * 1e9; total < want {
		return v, fmt.Errorf("internal storage %.1f GB is less than the SKU's %d GB", float64(total)/1e9, u.sku.Factory.StorageGB)
	}
	return v, nil
}

// checkMemory sums the device tree's memory nodes (what the board has;
// MemTotal is less by the kernel's and the firmware's reservations), or
// takes MemTotal on a board without them.
func checkMemory(_ context.Context, u *unit) (string, error) {
	want := int64(u.sku.MemoryMB) << 20
	got, src := u.dtMemory(), "device tree"
	if got == 0 {
		src = "MemTotal"
		mi, err := os.ReadFile(filepath.Join(u.Proc, "meminfo"))
		if err != nil {
			return "", fmt.Errorf("meminfo: %w", err)
		}
		for _, l := range strings.Split(string(mi), "\n") {
			if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				got = kb << 10
			}
		}
		// MemTotal lacks what the kernel keeps; up to an eighth short of
		// the SKU's still counts as all of it there.
		want -= want / 8
	}
	v := fmt.Sprintf("%d MiB (%s)", got>>20, src)
	if got < want {
		return v, fmt.Errorf("%d MiB of RAM is less than the SKU's %d MiB", got>>20, u.sku.MemoryMB)
	}
	return v, nil
}

func (u *unit) dtMemory() int64 {
	base := filepath.Join(u.Sys, "firmware/devicetree/base")
	cells := func(f string, def int) int {
		b, err := os.ReadFile(filepath.Join(base, f))
		if err != nil || len(b) != 4 {
			return def
		}
		return int(binary.BigEndian.Uint32(b))
	}
	ac, sc := cells("#address-cells", 2), cells("#size-cells", 1)
	var total int64
	for _, m := range glob(filepath.Join(base, "memory*")) {
		if st, err := os.ReadFile(filepath.Join(m, "status")); err == nil && !strings.HasPrefix(string(st), "ok") {
			continue
		}
		reg, err := os.ReadFile(filepath.Join(m, "reg"))
		if err != nil {
			continue
		}
		for stride := 4 * (ac + sc); len(reg) >= stride && stride > 0; reg = reg[stride:] {
			var size int64
			for i := 0; i < sc; i++ {
				size = size<<32 | int64(binary.BigEndian.Uint32(reg[4*(ac+i):]))
			}
			total += size
		}
	}
	return total
}

func checkWifi(_ context.Context, u *unit) (string, error) {
	phys := glob(filepath.Join(u.Sys, "class/ieee80211/phy*"))
	if len(phys) == 0 {
		return "", errors.New("no Wi-Fi adapter (no ieee80211 phy)")
	}
	var v []string
	for _, p := range phys {
		s := filepath.Base(p)
		if drv, err := os.Readlink(filepath.Join(p, "device/driver")); err == nil {
			s += " (" + filepath.Base(drv) + ")"
		}
		v = append(v, s)
	}
	return strings.Join(v, ", "), nil
}

func checkBluetooth(_ context.Context, u *unit) (string, error) {
	hcis := glob(filepath.Join(u.Sys, "class/bluetooth/hci*"))
	var v []string
	for _, h := range hcis {
		if b := filepath.Base(h); !strings.Contains(b, ":") {
			v = append(v, b)
		}
	}
	if len(v) == 0 {
		return "", errors.New("no Bluetooth adapter (no hci device)")
	}
	return strings.Join(v, ", "), nil
}

func checkModem(ctx context.Context, u *unit) (string, error) {
	st, err := u.telephonyOnce(ctx)
	if err != nil {
		return "", err
	}
	if st.IMEI == "" {
		return "", fmt.Errorf("telephonyd sees no modem after %s", u.ModemWait.Round(time.Second))
	}
	if st.Backend == "sim" {
		return "telephonyd's simulated modem", nil
	}
	return "modem with an IMEI (" + st.Backend + ")", nil
}

func checkSIM(ctx context.Context, u *unit) (string, error) {
	st, err := u.telephonyOnce(ctx)
	switch {
	case err != nil:
		return "", err
	case st.IMEI == "":
		return "", errors.New("no modem, so no SIM")
	}
	switch st.Sim {
	case "", "ready", "pin", "puk", "locked":
		if st.Sim == "" {
			st.Sim = "ready"
		}
		return "SIM " + st.Sim, nil
	case "absent":
		return "", errors.New("no SIM in the tray")
	}
	return "", fmt.Errorf("SIM %s", st.Sim)
}

func checkKeystore(_ context.Context, u *unit) (string, error) {
	csr, err := u.keystoreOnce()
	if err != nil {
		return "", err
	}
	if csr.Provisioned {
		return "reachable, provisioned", nil
	}
	return "reachable, not provisioned", nil
}
