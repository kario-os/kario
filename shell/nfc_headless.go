package shell

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/ota"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// nfcStep is the script's nfc command (headless.go): the simulated world
// around the phone's NFC controller.
func (r *Runner) nfcStep(arg string) error {
	c := r.C
	f := strings.Fields(arg)
	if len(f) == 0 {
		return errors.New("nfc card|reader|tap …")
	}
	switch {
	case f[0] == "card" && len(f) == 5 && f[2] == "transit":
		// A smart card of the simplified transit flow, its balance in cents.
		n, err := strconv.Atoi(f[4])
		if err != nil {
			return fmt.Errorf("nfc card %s transit %s: %q is not a balance in cents", f[1], f[3], f[4])
		}
		return c.nfcRPC(svc.NFCSimCard, map[string]any{"uid": f[1], "aid": f[3], "balance": n}, nil)
	case f[0] == "card" && len(f) >= 2:
		p := map[string]any{}
		if f[1] != "none" {
			p["uid"] = f[1]
		}
		for _, x := range f[2:] {
			switch x {
			case "encrypted":
				p["encrypted"] = true
			case "cpu":
				p["cpu"] = true
			default:
				return fmt.Errorf("nfc card %s: %q is not encrypted or cpu", f[1], x)
			}
		}
		return c.nfcRPC(svc.NFCSimCard, p, nil)
	case f[0] == "reader" && len(f) >= 4:
		a := svc.NFCTapArgs{Name: f[1], Kind: f[2]}
		switch {
		case a.Kind == "door" && len(f) == 4:
			a.Accept = strings.Split(f[3], ",")
		case (a.Kind == "gate" || a.Kind == "pay") && len(f) == 5:
			n, err := strconv.Atoi(f[4])
			if err != nil {
				return fmt.Errorf("nfc reader %s: %q is not an amount in cents", a.Name, f[4])
			}
			a.AID, a.Fare, a.Amount = f[3], n, n
		default:
			return errors.New("nfc reader <name> door <uid>[,<uid>...] | gate <aid> <fare> | pay <aid> <amount>")
		}
		if c.nfc.readers == nil {
			c.nfc.readers = map[string]svc.NFCTapArgs{}
		}
		c.nfc.readers[a.Name] = a
		return nil
	case f[0] == "tap" && len(f) >= 3 && (f[2] == "ok" || f[2] == "fail"):
		a, ok := c.nfc.readers[f[1]]
		if !ok {
			return fmt.Errorf("no reader %q (nfc reader first)", f[1])
		}
		return r.nfcTap(a, f[2] == "ok", strings.Join(f[3:], " "))
	case f[0] == "clock" && len(f) >= 2:
		return r.nfcClock(f[1], strings.Join(f[2:], " "))
	case f[0] == "asleep" && len(f) == 1:
		// The simulated sleep has begun (system-sleep on, the screen off).
		if _, ok := r.waitFor(func(string) bool { return c.Asleep() }, 15000); !ok {
			_, why := c.Sleeps()
			return fmt.Errorf("the phone did not go to sleep (awake: %s)", why)
		}
		return nil
	case f[0] == "picker" && len(f) == 2 && (f[1] == "on" || f[1] == "off"):
		c.SetEndKeyPicker(f[1] == "on")
		return nil
	case f[0] == "woken" && len(f) == 1:
		// A reader's field ended the last sleep.
		if got := c.WokenBy(); got != "simulated NFC field" {
			return fmt.Errorf("the last sleep ended by %q, not a reader's field", got)
		}
		return nil
	case f[0] == "deactivated" && len(f) == 4:
		// The last nfc.deactivated the shell delivered: app, card, reason.
		var got svc.NFCEvent
		if _, ok := r.waitFor(func(string) bool {
			got, _ = c.NFCDeactivated()
			return got.App == f[1] && got.Card == f[2] && got.Reason == f[3]
		}, 3000); !ok {
			return fmt.Errorf("the last nfc.deactivated went to %s %s (%s)", got.App, got.Card, got.Reason)
		}
		return nil
	case f[0] == "holidays" && len(f) == 2 && (f[1] == "fixture" || f[1] == "none"):
		return r.nfcHolidays(f[1] == "fixture")
	case f[0] == "usual" && len(f) == 4 && (f[2] == "workday" || f[2] == "dayoff"):
		return nfcUsual(c, f[1], f[2] == "dayoff", f[3])
	}
	return errors.New("nfc card <uid> [encrypted|cpu] | nfc card <uid> transit <aid> <cents> | nfc reader … | nfc tap <reader> ok|fail [<card name>] | nfc clock <HH:MM> [workday|weekend|holiday|makeup] | nfc asleep | nfc woken | nfc picker on|off | nfc deactivated <app> <card> <reason> | nfc holidays fixture|none | nfc usual <card name> workday|dayoff <HH:MM>-<HH:MM>")
}

// nfcUsual asserts that a door card's usual hours, as nfcd gives them to
// the card pack, have the span on workdays or days off (the card pack's
// line is longer than a selftest screen).
func nfcUsual(c *Core, name string, dayOff bool, span string) error {
	var st ui.NFCState
	if err := c.nfcRPC(svc.NFCState, nil, &st); err != nil {
		return err
	}
	clock := func(m int) string { return fmt.Sprintf("%d:%02d", m/60, m%60) }
	for _, cd := range st.Cards {
		if cd.Name != name {
			continue
		}
		var got []string
		for _, h := range cd.Usual {
			s := clock(h.From) + "-" + clock(h.To)
			if h.Weekend == dayOff && s == span {
				return nil
			}
			got = append(got, fmt.Sprintf("%v %s", h.Weekend, s))
		}
		return fmt.Errorf("%s's usual hours (day off, span): %v", name, got)
	}
	return fmt.Errorf("no card %q", name)
}

// nfcHolidays gives the shell a fixture calendar the way a phone gets
// the real one: signed by an update server (a test one, with a key of its
// own) and fetched, for the phone's region. The fixture is no official
// calendar: the first Saturday after tomorrow is a workday, the
// Wednesday after it a day off. none forgets the calendar.
func (r *Runner) nfcHolidays(fixture bool) error {
	c := r.C
	if !fixture {
		_ = os.Remove(c.holidaysPath())
		c.nfc.holidays = nil
		c.syncNFC()
		return nil
	}
	day := c.now().AddDate(0, 0, 2)
	for day.Weekday() != time.Saturday {
		day = day.AddDate(0, 0, 1)
	}
	cal := ota.Holidays{Format: ota.HolidaysFormat, Regions: map[string]ota.RegionHolidays{c.region(): {
		Source: "selftest fixture (not an official calendar)", Workdays: []string{day.Format(time.DateOnly)},
		DaysOff: []string{day.AddDate(0, 0, 4).Format(time.DateOnly)}}}}
	body, _ := json.Marshal(cal)
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		issued := time.Now().Unix()
		w.Header().Set("X-Offer-Issued", strconv.FormatInt(issued, 10))
		w.Header().Set("X-Offer-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(key, ota.DataMessage(ota.HolidaysFile, issued, body))))
		_, _ = w.Write(body)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	done := make(chan error, 1)
	_ = os.Remove(c.holidaysPath())
	c.fetchHolidays("http://"+ln.Addr().String()+"/"+ota.HolidaysFile, pub, func(err error) { done <- err })
	for end := time.Now().Add(r.scaled(10 * time.Second)); ; {
		select {
		case err := <-done:
			if err != nil {
				return err
			}
			c.Trace("nfc holidays: %s workday %s, day off %s", c.region(), cal.Regions[c.region()].Workdays[0], cal.Regions[c.region()].DaysOff[0])
			c.syncNFC()
			if _, ok := r.waitFor(func(string) bool { return c.nfc.applied == c.nfc.sent }, 3000); !ok {
				return errors.New("nfcd did not take the calendar")
			}
			return nil
		default:
			if time.Now().After(end) {
				return errors.New("the calendar was not fetched")
			}
			r.pump(20 * time.Millisecond)
		}
	}
}

// nfcClock moves the shell's clock forward to the next HH:MM, when asked
// on a workday (Monday to Friday), at a weekend, or on a day the holiday
// calendar makes a day off (holiday) or a workday (makeup); nfcd takes it
// with the next policy.
func (r *Runner) nfcClock(hhmm, day string) error {
	t, err := time.Parse("15:04", hhmm)
	if err != nil || (day != "" && day != "workday" && day != "weekend" && day != "holiday" && day != "makeup") {
		return errors.New("nfc clock <HH:MM> [workday|weekend|holiday|makeup]")
	}
	c := r.C
	off, work := c.nfc.holidays.regionDays(c.region())
	listed := map[string][]string{"holiday": off, "makeup": work}[day]
	if (day == "holiday" || day == "makeup") && len(listed) == 0 {
		return fmt.Errorf("nfc clock %s %s: the holiday calendar has no such day (nfc holidays fixture)", hhmm, day)
	}
	now := c.now()
	next := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	weekend := func(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }
	wrong := func(t time.Time) bool {
		switch day {
		case "workday":
			return weekend(t)
		case "weekend":
			return !weekend(t)
		case "holiday", "makeup":
			return !slices.Contains(listed, t.Format(time.DateOnly))
		}
		return false
	}
	for tries := 0; !next.After(now) || wrong(next); tries++ {
		if tries > 800 {
			return fmt.Errorf("nfc clock %s %s: no such day ahead", hhmm, day)
		}
		next = next.AddDate(0, 0, 1)
	}
	c.SetClockSkew(next.Sub(now))
	c.Trace("nfc clock: %s", next.Format("Mon 2006-01-02 15:04"))
	r.pump(200 * time.Millisecond)
	return nil
}

// nfcTap holds the phone to the reader while the shell's loop runs (the
// reader's commands go through an app), after nfcd has the phone's state.
func (r *Runner) nfcTap(a svc.NFCTapArgs, want bool, card string) error {
	c := r.C
	c.syncNFC()
	if _, ok := r.waitFor(func(string) bool { return c.nfc.applied == c.nfc.sent }, 3000); !ok {
		return errors.New("nfcd did not take the phone's state")
	}
	a.PatienceMS = 3000 * max(1, r.Scale)
	type tapped struct {
		res svc.NFCTapRes
		err error
	}
	done := make(chan tapped, 1)
	go func() {
		var res svc.NFCTapRes
		err := c.nfcRPC(svc.NFCSimTap, a, &res)
		done <- tapped{res, err}
	}()
	end := time.Now().Add(time.Duration(30*max(1, r.Scale)) * time.Second)
	for {
		select {
		case t := <-done:
			r.pump(200 * time.Millisecond)
			if t.err != nil {
				return t.err
			}
			if t.res.OK != want {
				return fmt.Errorf("%s: ok %v (%s) at the card %q", a.Name, t.res.OK, t.res.Note, t.res.Name)
			}
			if card != "" && t.res.Name != card {
				return fmt.Errorf("%s saw the card %q, not %q (%s)", a.Name, t.res.Name, card, t.res.Note)
			}
			c.Trace("nfc tap %s: ok=%v card=%q (%s)", a.Name, t.res.OK, t.res.Name, t.res.Note)
			return nil
		default:
			if time.Now().After(end) {
				return fmt.Errorf("%s: the tap did not end", a.Name)
			}
			r.pump(20 * time.Millisecond)
		}
	}
}
