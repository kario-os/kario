package main

import (
	"log"
	"math"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// buzz is a square wave, the sound of a phone buzzer, with a short fade at
// the end so it does not click.
func buzz(hz float64, ms int) []int16 {
	n := audio.Rate * ms / 1000
	fade := audio.Rate / 500
	out := make([]int16, n)
	period := float64(audio.Rate) / hz
	for i := range out {
		v := 6000
		if math.Mod(float64(i), period) >= period/2 {
			v = -v
		}
		if rest := n - i; rest < fade {
			v = v * rest / fade
		}
		out[i] = int16(v)
	}
	return out
}

var (
	eatSound = buzz(1568, 45)
	bugSound = append(buzz(1568, 40), buzz(2093, 60)...)
)

// beeper plays effects through a stream of the app's own (audio.stream,
// no permission). The system plays it as media, which has one stream at a
// time, so a beep also ends music playing in the background.
type beeper struct {
	app      *sdk.App
	busy     bool
	disabled bool
}

func (b *beeper) play(samples []int16) {
	if b.busy || b.disabled {
		return
	}
	b.busy = true
	b.app.GoAudio(ui.CapAudioStream, tr.T("Snake sounds"), func(s *sdk.AudioStream, err error) {
		b.busy = false
		if err != nil {
			log.Printf("no sound: %v", err)
			b.disabled = true
			return
		}
		s.Ring.Write(samples)
		s.Close()
	})
}
