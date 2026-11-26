package main

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/ui"
)

// A mini-program closed (its exit action, back on its last page) stays
// warm, as WeChat keeps one for 5 minutes: its runtime runs on with its
// pages off the screen, and opening it again shows them as they are then
// (the runtime sends them again, as to a Shell that took over its
// connection), without starting it afresh. At most warmMax are kept,
// within warmMemory of their runtimes' resident memory; past either, or
// warmFor after it closed, the one closed longest ago ends. A hidden
// program shows nothing: its toasts and errors go to the log only, its
// bridge calls are refused, a page it opens or closes goes on or off its
// shelf. Its runtime hears lifecycle background and foreground (a page's
// onHide and onShow).

const (
	warmFor    = 5 * time.Minute
	warmMax    = 3
	warmMemory = 32 << 20
)

// errHidden: a bridge call of a program that is closed but kept warm.
const errHidden = "the mini-program is not open"

// close closes p's pages on top of the stack, keeping it warm when it can
// be; otherwise its runtime ends once the last of them is gone (popped).
func (h *host) close(p *program) {
	if !h.keepable(p) {
		h.exit(p)
		return
	}
	n := len(h.stack)
	for n > 0 {
		if pg := h.pages[h.stack[n-1]]; pg == nil || pg.prog != p {
			break
		}
		n--
	}
	shelf := slices.Clone(h.stack[n:])
	if len(shelf) == 0 {
		return
	}
	for _, id := range shelf {
		h.pages[id].shelved = true
	}
	for range shelf {
		h.pop()
	}
	h.hide(p, shelf)
}

// keepable: p's runtime may be kept warm once its pages close.
func (h *host) keepable(p *program) bool {
	c := p.run
	return c != nil && !c.gone && c.hello && h.warmMax > 0
}

// hide keeps p warm with its pages, bottom first, on its shelf.
func (h *host) hide(p *program, shelf []string) {
	c := p.run
	p.warm, p.shelf = true, shelf
	closed := time.Now()
	p.closedAt = closed
	c.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateBackground})
	c.after(h.warmFor, func() {
		if p.run == c && p.warm && p.closedAt.Equal(closed) {
			h.end(p)
		}
	})
	h.trimWarm()
}

// show opens p again: its runtime sends its pages, which go on the stack
// as they come (child.handle).
func (h *host) show(p *program) {
	c := p.run
	c.reopen = map[string]bool{}
	for _, id := range p.shelf {
		c.reopen[id] = true
		if pg := h.pages[id]; pg != nil {
			pg.shelved = false
		}
	}
	p.warm, p.shelf = false, nil
	c.send(ui.Msg{T: ui.MsgResync})
	c.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateForeground})
}

// end ends a warm program's runtime, its pages with it.
func (h *host) end(p *program) {
	for _, id := range p.shelf {
		delete(h.pages, id)
	}
	if p.run != nil {
		p.run.stop()
	}
	p.warm, p.shelf = false, nil
}

// trimWarm ends the programs closed longest ago while more than warmMax
// are warm or their runtimes hold more than warmMemory.
func (h *host) trimWarm() {
	for {
		var warm []*program
		var mem int64
		for _, p := range h.progs {
			if p.warm && p.run != nil {
				warm = append(warm, p)
				mem += resident(p.run.cmd.Process.Pid)
			}
		}
		if len(warm) == 0 || len(warm) <= h.warmMax && mem <= h.warmMemory {
			return
		}
		slices.SortFunc(warm, func(a, b *program) int { return a.closedAt.Compare(b.closedAt) })
		h.end(warm[0])
	}
}

// shelve takes a page a hidden program opened: on its shelf, not shown.
func (c *child) shelve(s ui.Screen) {
	h, p := c.h, c.prog
	if h.pageCount(p) >= maxPages {
		c.send(ui.Msg{T: ui.MsgPopped, ScreenID: s.ID})
		return
	}
	h.pages[s.ID] = &page{prog: p, screen: s.ID, shelved: true}
	p.shelf = append(p.shelf, s.ID)
}

// unshelve closes the top page of a hidden program; it ends with its last.
func (c *child) unshelve() {
	p := c.prog
	n := len(p.shelf)
	if n == 0 {
		return
	}
	id := p.shelf[n-1]
	p.shelf = p.shelf[:n-1]
	delete(c.h.pages, id)
	c.send(ui.Msg{T: ui.MsgPopped, ScreenID: id})
	if n == 1 {
		c.h.end(p)
	}
}

// resident is the memory a process holds (its resident set); 0 when it
// cannot be read.
func resident(pid int) int64 {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(raw))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize())
}
