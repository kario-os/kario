package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// My location: the receiver's fix, live, through location.watch while the
// page is open. The system pauses the watch while the app is in the
// background and goes on when it comes back; Back stops it.

type here struct {
	app  *sdk.App
	open bool
	fix  *ui.Location
	at   time.Time
	note string
}

func (h *here) screen() ui.Screen {
	s := ui.Screen{ID: "here", Title: tr.T("My location")}
	if h.fix == nil {
		note := h.note
		if note == "" {
			note = tr.T("Waiting for a fix…")
		}
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, note)}
		return s
	}
	l := h.fix
	row := func(id, title, value string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Trailing: value}
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleBig, fmt.Sprintf("%.5f, %.5f", l.Lat, l.Lon)),
		row("accuracy", tr.T("Accuracy"), tr.T("±%.0f m", l.AccuracyM)))
	if l.Approximate {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Approximate location: this app may only know the area")))
	}
	if l.Source == ui.LocationSourceNetwork {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Placed by Wi-Fi and the mobile network: no satellite fix")))
	}
	if l.AltitudeM != nil {
		s.Body = append(s.Body, row("altitude", tr.T("Altitude"), tr.T("%.0f m", *l.AltitudeM)))
	}
	if l.SpeedMS != nil {
		s.Body = append(s.Body, row("speed", tr.T("Speed"), tr.T("%.1f km/h", *l.SpeedMS*3.6)))
	}
	if l.Bearing != nil {
		s.Body = append(s.Body, row("bearing", tr.T("Heading"), fmt.Sprintf("%.0f°", *l.Bearing)))
	}
	if l.Satellites > 0 {
		s.Body = append(s.Body, row("satellites", tr.T("Satellites"), fmt.Sprint(l.Satellites)))
	}
	sub := tr.T("Updated %s", h.at.Format("15:04:05"))
	if h.note != "" {
		sub = h.note
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, sub))
	return s
}

func (h *here) show() {
	h.open, h.fix, h.note = true, nil, ""
	h.app.Push(h.screen())
	h.app.Go(ui.CapLocationWatch, ui.LocationWatchArgs{IntervalS: 1}, nil, func(err error) {
		if err != nil {
			h.note = tr.T("No location: %s", err.Error())
			h.app.Update(h.screen())
		}
	})
}

func (h *here) closed() {
	h.open = false
	h.app.Go(ui.CapLocationStop, nil, nil, func(error) {})
}

// event takes the watch's events; false for the others.
func (h *here) event(name, value string) bool {
	switch name {
	case ui.EventLocation:
		var l ui.Location
		if json.Unmarshal([]byte(value), &l) != nil {
			return true
		}
		h.fix, h.at, h.note = &l, time.Now(), ""
	case ui.EventLocationOff:
		h.note = tr.T("Location is off in Settings")
	default:
		return false
	}
	if h.open {
		h.app.Update(h.screen())
	}
	return true
}
