package main

import (
	"errors"
	"log"
	"strings"
	"time"

	"github.com/kario-os/kario/ui"
)

// hinter is what the refresh rate check asks of the app (*sdk.App).
type hinter interface {
	Call(capName string, args any, out any) error
	Go(capName string, args any, out any, done func(error))
}

// displayBurst is how many hints the check sends at once: twice the
// Shell's bucket, so that a Shell without a limit grants more than the
// bucket can have refilled while the burst is answered unless that takes
// over 2 seconds.
const displayBurst = 2 * ui.DisplayHintBurst

// displayCheck: the app in front hints the refresh rate, rate-limited. The
// burst goes out without waiting for answers and is judged by the hints
// granted against what the bucket holds plus what it refills while the
// burst is answered: a device slow enough to refill it between answers
// may grant them all.
func displayCheck(app hinter) error {
	var res ui.DisplayHintRes
	if err := app.Call(ui.CapDisplayHint, ui.DisplayHintArgs{AnimatingMS: 200}, &res); err != nil {
		return err
	}
	if res.Hz <= 0 || len(res.Rates) == 0 {
		return errors.New(tr.T("display.hint answered %+v", res))
	}
	if app.Call(ui.CapDisplayHint, ui.DisplayHintArgs{AnimatingMS: 200, FPS: 30}, nil) == nil {
		return errors.New(tr.T("a display hint of two kinds was not refused"))
	}
	answers := make(chan error, displayBurst)
	start := time.Now()
	for range displayBurst {
		app.Go(ui.CapDisplayHint, ui.DisplayHintArgs{Clear: true}, nil, func(err error) { answers <- err })
	}
	granted := 0
	for range displayBurst {
		switch err := <-answers; {
		case err == nil:
			granted++
		case !strings.HasSuffix(err.Error(), ui.ErrDisplayHintRate):
			return err
		}
	}
	took := time.Since(start)
	most := ui.DisplayHintBurst + took.Seconds()*ui.DisplayHintPerSecond
	if float64(granted) > most {
		return errors.New(tr.T("display hints were not rate-limited: %d granted, the limit allows %d in %v", granted, int(most), took.Round(time.Millisecond)))
	}
	if granted == displayBurst {
		log.Printf("display hints: all %d sent at once granted in %v, too slow to see the limit", displayBurst, took.Round(time.Millisecond))
	}
	return nil
}
