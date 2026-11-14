// Weather: the location in the foreground (the location permission), and
// an hourly background refresh through system jobs, so the app is current
// without staying resident. The forecast is simulated (no weather service).
package main

import (
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Reverse geocoding is the app's own business; a tiny table stands in.
var cities = []struct {
	name     string
	lat, lon float64
}{{"Beijing", 39.90, 116.40}, {"Shanghai", 31.23, 121.47}, {"Guangzhou", 23.13, 113.26}, {"Shenzhen", 22.54, 114.06}}

// nearest is the name of the city nearest to l, as shown.
func nearest(l ui.Location) string {
	best, bd := "", math.MaxFloat64
	for _, c := range cities {
		if d := math.Hypot(c.lat-l.Lat, c.lon-l.Lon); d < bd {
			best, bd = c.name, d
		}
	}
	return tr.T(best)
}

// unitFahrenheit is the manifest's choice of units (settings: units) for
// Fahrenheit.
const unitFahrenheit = "\u534e\u6c0f"

var conditions = []string{"Sunny", "Cloudy", "Overcast", "Light rain", "Showers"}

// forecast is deterministic for a city (its name as shown) and day, so it
// reads like data.
func forecast(city string, day time.Time) (low, high int, cond string) {
	h := fnv.New32a()
	fmt.Fprintf(h, "%s%s", city, day.Format("2006-01-02"))
	v := h.Sum32()
	low = 8 + int(v%10)
	return low, low + 5 + int(v/10%6), tr.T(conditions[v/100%uint32(len(conditions))])
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	city, updated, refreshes, located := tr.T("Beijing"), time.Time{}, 0, false
	// The unit is the user's choice in Settings › App settings (declared in
	// the manifest, kept by the system).
	fahrenheit, days := false, 3
	deg := func(c int) string {
		if fahrenheit {
			return fmt.Sprintf("%d°F", c*9/5+32)
		}
		return fmt.Sprintf("%d°C", c)
	}
	screen := func() ui.Screen {
		now := time.Now()
		low, high, cond := forecast(city, now)
		where := city
		if !located {
			where = tr.T("%s (not located)", city)
		}
		s := ui.Screen{ID: "main", Title: tr.T("Weather"), Actions: []ui.Action{{ID: "refresh", Label: tr.T("Refresh"), Key: "r"}, {ID: "share", Label: tr.T("Share"), Key: "s"}}}
		s.Body = append(s.Body,
			sdk.Styled(ui.StyleTitle, where),
			sdk.Styled(ui.StyleBig, deg((low+high)/2)+" "+cond),
			sdk.Styled(ui.StyleDim, tr.T("Today %s–%s", deg(low), deg(high))),
			sdk.Section(tr.T("Next %d days", days)))
		for i := 1; i <= days; i++ {
			d := now.AddDate(0, 0, i)
			l, h, c := forecast(city, d)
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("d", i), Title: d.Format(tr.T("Jan 2")), Subtitle: c,
				Trailing: deg(l) + "–" + deg(h)})
		}
		s.Body = append(s.Body,
			sdk.Styled(ui.StyleDim, tr.T("Updated %s · %d background refreshes · simulated data", updated.Format("15:04"), refreshes)))
		return s
	}
	locate := func() {
		var l ui.Location
		app.Go(ui.CapLocationGet, nil, &l, func(err error) {
			if err == nil {
				city, located = nearest(l), true
			}
			updated = time.Now()
			app.Update(screen())
		})
	}
	// widget keeps the standby line in step with the forecast and unit.
	widget := func() {
		low, high, cond := forecast(city, time.Now())
		app.Go(ui.CapWidgetSet, ui.WidgetArgs{Text: city + " " + cond + " " + deg((low+high)/2), Arg: "today"}, nil, func(error) {})
	}
	loadUnit := func(then func()) {
		var v map[string]any
		app.Go(ui.CapAppSettings, nil, &v, func(error) {
			fahrenheit = v["units"] == unitFahrenheit
			if n, ok := v["days"].(float64); ok {
				days = int(n)
			}
			then()
		})
	}
	app.OnStart = func(string) {
		updated = time.Now()
		loadUnit(func() { app.Update(screen()); widget() })
		app.Reset(screen())
		locate()
		app.Go(ui.CapJobSchedule, ui.JobArgs{ID: "refresh", EveryMin: 60}, nil, func(err error) {
			if err != nil {
				app.Toast(tr.T("Cannot schedule the background refresh: %s", err.Error()))
			}
		})
		// Dropping old forecasts can wait for the charger.
		app.Go(ui.CapJobSchedule, ui.JobArgs{ID: "cleanup", EveryMin: 24 * 60, Charging: true}, nil, func(error) {})
	}
	app.OnEvent = func(name, value string) {
		if name == "settings" {
			loadUnit(func() { app.Update(screen()); widget() })
			return
		}
		if name == "job" && value == "refresh" {
			// In the background there is no location; refresh the forecast
			// for the last place.
			refreshes++
			updated = time.Now()
			app.Update(screen())
		}
	}
	app.OnAction = func(e sdk.ActionEvent) {
		switch e.Action {
		case "refresh":
			locate()
		case "share":
			low, high, cond := forecast(city, time.Now())
			text := tr.T("%s today: %s, %s–%s", city, cond, deg(low), deg(high))
			app.Go(ui.CapShareOpen, ui.ShareArgs{Kind: "text", Text: text}, nil, func(err error) {
				if err != nil && !ui.Cancelled(err) {
					app.Toast(tr.T("Cannot share: %s", err.Error()))
				}
			})
		}
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
