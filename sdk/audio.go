package sdk

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/ui"
)

// AudioStream is the app's side of a stream opened with ui.CapAudioStream
// (the app writes, the system plays) or ui.CapAudioRecord (the microphone
// writes, the app reads): a shared ring of 48 kHz mono samples. Use it on
// the dispatch goroutine or one goroutine of the app's own.
type AudioStream struct {
	ID   string
	Ring *audio.RingBuf
	app  *App
	rec  bool
}

// GoAudio opens a stream without blocking the dispatch goroutine (recording
// may first ask the user for the microphone); done runs on it.
func (a *App) GoAudio(capName, name string, done func(*AudioStream, error)) {
	go func() {
		data, files, err := a.roundTrip("", capName, ui.AudioOpenArgs{Name: name})
		var s *AudioStream
		if err == nil {
			s, err = openStream(a, capName, data, files)
		} else {
			for _, f := range files {
				f.Close()
			}
		}
		a.Post(func() { done(s, err) })
	}()
}

func openStream(a *App, capName string, data json.RawMessage, files []*os.File) (*AudioStream, error) {
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	var st struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	if len(files) != 1 {
		return nil, errors.New("audio stream without its ring")
	}
	r, err := audio.MapRing(files[0])
	if err != nil {
		return nil, err
	}
	return &AudioStream{ID: st.ID, Ring: r, app: a, rec: capName == ui.CapAudioRecord}, nil
}

// Close ends the stream: a recording stops, a playback finishes once what
// was written has played.
func (s *AudioStream) Close() {
	if s.Ring == nil {
		return
	}
	if s.rec {
		s.app.Go(ui.CapAudioControl, ui.AudioControlArgs{ID: s.ID, Op: "stop"}, nil, func(error) {})
	} else {
		s.Ring.Finish()
	}
	s.Ring.Close()
	s.Ring = nil
}
