package shell

import (
	"io"
	"log"
	"time"

	"github.com/kario-os/kario/i18n"
)

// Standby is the standby screen's words as the Shell would show them, for
// a program that draws one without the Shell (phone-charger, charger
// mode): the language, time zone, clock, theme and brightness the user
// chose (settings.json in the state directory), else the system's for the
// board. What only the running Shell knows (the network's zone and
// country) is left out. Loading it sets the process's language.
type Standby struct{ c *Core }

func LoadStandby(stateDir string, sku SKU) Standby {
	c := &Core{cfg: Config{StateDir: stateDir, SKU: sku}, log: log.New(io.Discard, "", 0)}
	c.loadSettings()
	loc, _ := c.zoneInForce()
	c.locale.zone.Store(loc)
	c.locale.lang = i18n.SetLang(c.languageInForce())
	return Standby{c}
}

func (s Standby) Lang() string { return s.c.locale.lang }

// Theme is the frame's theme ("" dark).
func (s Standby) Theme() string { return s.c.settings.Theme }

// Brightness is the backlight's level in percent.
func (s Standby) Brightness() int { return s.c.brightness() }

// Clock is the time of day on the clock the user chose.
func (s Standby) Clock(t time.Time) string { return s.c.clockText(t) }

// Date is the standby screen's date line.
func (s Standby) Date(t time.Time) string { return dateText(t.In(s.c.zone())) }
