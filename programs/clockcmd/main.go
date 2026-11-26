// phone-clock keeps the time across a power-off where the board's RTC is
// read-only (hal/rtc, sku.json's rtc): phone-clock.service runs restore
// early at boot, before time-set.target and the services, and save
// --if-synced at shutdown. The Shell keeps the offset whenever it sets the
// time, and while NTP keeps the clock.
//
//	phone-clock restore              the system clock from the RTC plus the offset kept
//	                                 (a board that keeps one; nothing on the others)
//	phone-clock save [--if-synced]   keep the time now: the offset, or a settable RTC set
//	                                 from the system clock (hwclock --systohc); --if-synced:
//	                                 only while NTP keeps the clock, and the offset only
//	                                 when it moved
//	phone-clock show                 the board's RTC, its time, the system clock, the offset
//
// Only where the clock is this system's (rtc.Ours): not in the chroot kit.
package clockcmd

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kario-os/kario/hal/rtc"
	"github.com/kario-os/kario/shell"
)

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	exe, _ := os.Executable()
	os.Exit(run(os.Args[1:], os.Stdout, filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "phone", "sku.json")))
}

// slack: how far the offset may move before save --if-synced keeps it;
// rtcWait: how long restore waits for udev to load the RTC's driver.
const (
	slack   = 500 * time.Millisecond
	rtcWait = 15 * time.Second
)

func run(args []string, out io.Writer, skuPath string) int {
	say := func(f string, a ...any) { fmt.Fprintf(out, "phone-clock: "+f+"\n", a...) }
	if len(args) == 0 || (args[0] != "restore" && args[0] != "save" && args[0] != "show") {
		say("restore | save [--if-synced] | show")
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("phone-clock "+cmd, flag.ContinueOnError)
	fs.SetOutput(out)
	skuFile := fs.String("sku", skuPath, "the board's sku.json")
	persist := fs.String("persist-dir", "/var/lib/slots", "the persistent area, where the offset is kept")
	ifSynced := fs.Bool("if-synced", false, "with save: only while NTP keeps the clock")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || (*ifSynced && cmd != "save") {
		return 2
	}
	sku, err := shell.LoadSKUFile(*skuFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		say("%v", err)
		return 1
	}
	r, path := sku.RTC, filepath.Join(*persist, rtc.OffsetFile)
	if cmd == "show" {
		show(out, r, path)
		return 0
	}
	if !rtc.Ours() {
		say("the clock is not this system's (not root, a chroot, or %s=host): left alone", rtc.OwnerEnv)
		return 1
	}
	switch {
	case cmd == "restore" && !r.Offset:
		say("the board's RTC keeps no offset (sku.json rtc): the kernel sets the clock from it (%s hctosys: %v)", rtc.Name, rtc.Hctosys())
	case cmd == "restore":
		if err := rtc.Wait(rtcWait); err != nil {
			say("restore: %v", err)
			return 1
		}
		t, why, err := rtc.Restore(path)
		if err != nil {
			say("restore: %v", err)
			return 1
		}
		if why != "" {
			say("the clock left at %s: %s", time.Now().UTC().Format(time.RFC3339), why)
			return 0
		}
		say("the clock set from %s plus the offset kept: %s", rtc.Name, t.Format(time.RFC3339))
	case *ifSynced && !rtc.Synced():
		say("NTP does not keep the clock: nothing kept")
	case r.Offset:
		return save(say, path, *ifSynced)
	case r.ReadOnly:
		say("the board's RTC is read-only and keeps no offset: nothing kept")
	default:
		if err := rtc.Write(time.Now()); err != nil {
			say("setting %s: %v", rtc.Name, err)
			return 1
		}
		say("%s set from the system clock", rtc.Name)
	}
	return 0
}

func save(say func(string, ...any), path string, ifSynced bool) int {
	by := map[bool]string{true: "ntp", false: "save"}[ifSynced]
	o, err := rtc.Measure(by)
	if err != nil {
		say("measuring the offset: %v", err)
		return 1
	}
	if kept, err := rtc.Load(path); ifSynced && err == nil && !o.Moved(kept, slack) {
		say("the offset kept is still right (%+.3f s)", float64(kept.MS)/1000)
		return 0
	}
	if err := rtc.Save(path, o); err != nil {
		say("keeping the offset: %v", err)
		return 1
	}
	say("offset %+.3f s kept in %s", float64(o.MS)/1000, path)
	return 0
}

func show(out io.Writer, r shell.SKURTC, path string) {
	board := "settable: setting the time writes it"
	switch {
	case r.Offset:
		board = "read-only: the time is kept as an offset from it"
	case r.ReadOnly:
		board = "read-only, no offset kept: the clock waits for the network"
	}
	alarm := r.Alarm
	if alarm == "" {
		alarm = "relative"
	}
	fmt.Fprintf(out, "board: %s's RTC is %s; wake alarms %s\n", rtc.Name, board, alarm)
	now := time.Now()
	fmt.Fprintf(out, "system clock: %s, NTP keeps it: %v, this system's: %v\n", now.UTC().Format(time.RFC3339Nano), rtc.Synced(), rtc.Ours())
	s, err := rtc.Read()
	if err != nil {
		fmt.Fprintf(out, "%s: %v\n", rtc.Name, err)
		return
	}
	fmt.Fprintf(out, "%s: %s (since_epoch %d), the kernel set the clock from it at boot: %v\n", rtc.Name, time.Unix(s, 0).UTC().Format(time.RFC3339), s, rtc.Hctosys())
	o, err := rtc.Load(path)
	if err != nil {
		fmt.Fprintf(out, "offset: none kept (%v)\n", err)
		return
	}
	at := time.Unix(s, 0).Add(time.Second/2 + o.Duration())
	fmt.Fprintf(out, "offset: %+.3f s, measured %s after %s set the time (%s)\n", float64(o.MS)/1000, o.At.Format(time.RFC3339), o.By, path)
	fmt.Fprintf(out, "%s plus the offset: %s, %+.1f s from the system clock\n", rtc.Name, at.UTC().Format(time.RFC3339), at.Sub(now).Seconds())
}
