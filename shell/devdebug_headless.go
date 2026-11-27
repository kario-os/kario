package shell

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/kario-os/kario/devctl"
)

// The selftest's computer as a debugger (29-app-development.md §5.5): it
// says to an app over the developer link what kario dap says, in
// kario-jsrt's own lines, without the editor and the source map.

// devDebugger is the session dev-debug opened.
type devDebugger struct {
	app   string
	toApp *os.File
	lines chan string
	// events: lines read while waiting for an answer, for dev-debug-wait.
	events []string
	notes  lockedBuffer
	done   chan error
}

// devDebugOpen is the step dev-debug [-ssh] [-attach] <app-id> -> <text>:
// the computer runs phone-dev debug [-attach] <app-id>, on the phone
// itself as dev does or, with -ssh, through dropbear's client in the
// emulator's computer, over the cable. The Shell's answer contains text,
// then the app says hello. Attached, the app keeps its process; otherwise
// it runs anew under the debugger, waiting for run.
func (r *Runner) devDebugOpen(arg string) error {
	spec, want, _ := strings.Cut(arg, " -> ")
	f := strings.Fields(spec)
	overSSH := len(f) > 0 && f[0] == "-ssh"
	if overSSH {
		f = f[1:]
	}
	attach := len(f) > 0 && f[0] == "-attach"
	args := []string{devctl.OpDebug}
	if attach {
		f = f[1:]
		args = append(args, "-attach")
	}
	if len(f) != 1 || strings.TrimSpace(want) == "" {
		return errors.New("usage: dev-debug [-ssh] [-attach] <app-id> -> <text>")
	}
	if r.dbg != nil {
		return errors.New("a debugger is open already (dev-debug-end)")
	}
	app := f[0]
	args = append(args, app)
	var dbclient string
	if overSSH {
		var err error
		if dbclient, err = r.dbclient(); err != nil {
			return err
		}
	}
	toApp, w, err := os.Pipe()
	if err != nil {
		return err
	}
	fromApp, out, err := os.Pipe()
	if err != nil {
		toApp.Close()
		w.Close()
		return err
	}
	d := &devDebugger{app: app, toApp: w, lines: make(chan string, 256), done: make(chan error, 1)}
	run := func(cmd *exec.Cmd) error {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = toApp, out, &d.notes
		err := cmd.Start()
		toApp.Close()
		out.Close()
		if err != nil {
			return err
		}
		return cmd.Wait()
	}
	if overSSH {
		addr, _ := r.C.USBSharing()
		go func() {
			d.done <- r.C.usbComputer(addr, func(httpDo, dialPort, string) error {
				cmd := exec.Command(dbclient, "-T", "-y", "-y", "root@"+usbAddr, r.devRemote(args))
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/tmp", "DROPBEAR_PASSWORD="}
				return run(cmd)
			})
		}()
	} else {
		cmd := r.devLocalCmd(args)
		go func() { d.done <- run(cmd) }()
	}
	go func() {
		sc := bufio.NewScanner(fromApp)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			d.lines <- sc.Text()
		}
		fromApp.Close()
		close(d.lines)
	}()
	pid := r.C.AppPid(app)
	r.dbg = d
	first, err := r.devDebugLine(r.devDebugWaitFor(30))
	if err != nil {
		return fmt.Errorf("phone-dev debug: %w", err)
	}
	if !strings.Contains(first, strings.TrimSpace(want)) {
		return fmt.Errorf("phone-dev debug said %q, not %q", first, want)
	}
	if _, err := r.devDebugEvent(`"event":"hello"`, r.devDebugWaitFor(30)); err != nil {
		return err
	}
	now := r.C.AppPid(app)
	switch {
	case attach && now != pid:
		return fmt.Errorf("attaching restarted %s: pid %d, was %d", app, now, pid)
	case !attach && (now == 0 || now == pid):
		return fmt.Errorf("%s did not start anew under the debugger: pid %d, was %d", app, now, pid)
	}
	return nil
}

// devDebugSend is the step dev-debug-send <request> [-> text]: one JSON
// line with an id to the app's debugger; its answer (the line with that
// id) contains text, without it says ok.
func (r *Runner) devDebugSend(arg string) error {
	d := r.dbg
	if d == nil {
		return errors.New("no debugger open (dev-debug)")
	}
	req, want, _ := strings.Cut(arg, " -> ")
	var sent struct {
		ID *int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(req), &sent); err != nil || sent.ID == nil {
		return errors.New("usage: dev-debug-send {\"id\": <n>, \"cmd\": ...} [-> text]")
	}
	if want = strings.TrimSpace(want); want == "" {
		want = `"ok":true`
	}
	if _, err := d.toApp.WriteString(strings.TrimSpace(req) + "\n"); err != nil {
		return fmt.Errorf("the session ended: %v: %s", err, strings.TrimSpace(d.notes.String()))
	}
	end := time.Now().Add(r.devDebugWaitFor(10))
	for {
		l, err := r.devDebugLine(time.Until(end))
		if err != nil {
			return fmt.Errorf("no answer to %s: %w", req, err)
		}
		var got struct {
			ID    *int64 `json:"id"`
			Event string `json:"event"`
		}
		if json.Unmarshal([]byte(l), &got) != nil || got.Event != "" || got.ID == nil || *got.ID != *sent.ID {
			d.events = append(d.events, l)
			continue
		}
		if !strings.Contains(l, want) {
			return fmt.Errorf("the debugger answered %s, not %s", l, want)
		}
		return nil
	}
}

// devDebugWaitStep is the step dev-debug-wait <text> [ms]: an event from
// the app's debugger containing text (those before it are passed over).
func (r *Runner) devDebugWaitStep(arg string) error {
	if r.dbg == nil {
		return errors.New("no debugger open (dev-debug)")
	}
	text, ms := lastNum(strings.TrimSpace(arg), 10000)
	_, err := r.devDebugEvent(text, time.Duration(ms*max(1, r.Scale))*time.Millisecond)
	return err
}

// devDebugEnd is the step dev-debug-end: the computer goes. The session
// ends, and the app, no longer debugged, runs on in the same process.
func (r *Runner) devDebugEnd() error {
	d := r.dbg
	if d == nil {
		return errors.New("no debugger open (dev-debug)")
	}
	pid := r.C.AppPid(d.app)
	r.devDebugDrop()
	end := time.Now().Add(r.devDebugWaitFor(10))
	for ended := false; ; r.pump(10 * time.Millisecond) {
		if !ended {
			select {
			case <-d.done:
				ended = true
			default:
			}
		}
		a := r.C.appByID(d.app)
		if ended && (a == nil || !a.debugging) {
			break
		}
		if time.Now().After(end) {
			return fmt.Errorf("the session went on after the computer went (ended %v): %s", ended, strings.TrimSpace(d.notes.String()))
		}
	}
	if now := r.C.AppPid(d.app); pid == 0 || now != pid {
		return fmt.Errorf("%s did not run on after the debugger went: pid %d, was %d", d.app, now, pid)
	}
	return nil
}

// devDebugDrop hangs up the session, if one is open, without waiting.
func (r *Runner) devDebugDrop() {
	d := r.dbg
	if d == nil {
		return
	}
	r.dbg = nil
	d.toApp.Close()
	go func() {
		for range d.lines {
		}
	}()
}

func (r *Runner) devDebugWaitFor(sec int) time.Duration {
	return time.Duration(sec*max(1, r.Scale)) * time.Second
}

// devDebugEvent is the first event since the last one taken that contains
// text, read within limit.
func (r *Runner) devDebugEvent(text string, limit time.Duration) (string, error) {
	d := r.dbg
	for i, l := range d.events {
		if strings.Contains(l, text) {
			d.events = d.events[i+1:]
			return l, nil
		}
	}
	d.events = nil
	end := time.Now().Add(limit)
	for {
		l, err := r.devDebugLine(time.Until(end))
		if err != nil {
			return "", fmt.Errorf("no %s from the app's debugger: %w", text, err)
		}
		if strings.Contains(l, text) {
			return l, nil
		}
	}
}

// devDebugLine is the session's next line, read within limit while the
// Shell's loop runs.
func (r *Runner) devDebugLine(limit time.Duration) (string, error) {
	d := r.dbg
	end := time.Now().Add(limit)
	for {
		select {
		case l, ok := <-d.lines:
			if !ok {
				return "", fmt.Errorf("the session ended: %s", strings.TrimSpace(d.notes.String()))
			}
			return l, nil
		default:
		}
		if time.Now().After(end) {
			return "", errors.New("nothing in time")
		}
		r.pump(10 * time.Millisecond)
	}
}
