package shell

import (
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// The shell's clock moved (timeshift, clock-at, a part's own) is the apps'
// too: an app starting has the offset in its environment, a running one
// hears each change, one that started before a change hears it at its
// hello, and an offset it knows is not sent again.
func TestClockTellsApps(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		event := func(a *App, want string) {
			t.Helper()
			select {
			case o := <-a.out:
				if o.m.T != ui.MsgEvent || o.m.Event != ui.EventClock || o.m.Value != want {
					t.Errorf("%s was sent %+v, want the clock %s", a.M.ID, o.m, want)
				}
			default:
				if want != "" {
					t.Errorf("%s heard nothing, want the clock %s", a.M.ID, want)
				}
			}
		}
		app := func(id string, ready bool) *App {
			a := &App{M: &Manifest{ID: id, Name: id}, running: true, ready: ready, out: make(chan outbound, 4)}
			c.apps = append(c.apps, a)
			return a
		}
		running := app("com.test.running", true)
		starting := app("com.test.starting", false)
		if env := c.clockEnv(starting); env != nil {
			t.Errorf("with the machine's clock the environment has %v", env)
		}
		c.SetClockSkew(90 * time.Minute)
		event(running, "5400000")
		event(starting, "")
		c.setClockSkew(90 * time.Minute)
		event(running, "")
		c.tellClock(starting)
		event(starting, "5400000")
		late := app("com.test.late", false)
		if env := c.clockEnv(late); len(env) != 1 || env[0] != ui.EnvClockOffset+"=5400000" {
			t.Errorf("an app starting 90 minutes ahead has %v", env)
		}
		c.tellClock(late)
		event(late, "")
		c.setClockSkew(-time.Second)
		event(running, "-1000")
		return nil
	})
}
