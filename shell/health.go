package shell

import (
	"fmt"
	"os"
	"time"
)

// Health (R40): what the update health check (phone-ota's ota-healthy)
// waits for before it marks a new system slot good: the shell runs, its
// renderer answered the handshake, and an app starts and speaks the
// protocol (the settings app, started in the background and stopped once
// it is ready). The shell writes the file once; a slot whose shell never
// gets there is not marked good, and after its tries the phone goes back
// to the last good slot.

const (
	healthApp     = "com.phone.settings"
	healthTimeout = 60 * time.Second
)

// CheckHealth writes path once the shell, its renderer (named, "" without
// one) and an app are up.
func (c *Core) CheckHealth(path, renderer string) {
	if path == "" {
		return
	}
	c.Post(func() {
		a := c.appByID(healthApp)
		if a == nil {
			c.log.Printf("health: no %s in the image", healthApp)
			return
		}
		ours := !a.running
		if ours {
			a.arg = ""
			if err := c.spawn(a); err != nil {
				c.log.Printf("health: %s does not start: %v", healthApp, err)
				return
			}
		}
		deadline := time.Now().Add(healthTimeout)
		var poll func()
		poll = func() {
			switch {
			case a.ready:
				line := fmt.Sprintf("shell=%s renderer=%q app=%s ready_ms=%d\n", shownVersion(), renderer, a.M.ID, time.Since(a.started).Milliseconds())
				if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
					c.log.Printf("health: %v", err)
					return
				}
				c.log.Printf("health: %s", line[:len(line)-1])
				if ours && c.fg != a && a.proc != nil {
					a.killed = true
					_ = a.proc.Kill()
				}
			case !a.running || time.Now().After(deadline):
				c.log.Printf("health: %s did not come up", healthApp)
			default:
				time.AfterFunc(200*time.Millisecond, func() { c.Post(poll) })
			}
		}
		poll()
	})
}
