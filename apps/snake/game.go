package main

import (
	"math/rand"
	"time"
)

const (
	cols, rows = 17, 10
	levels     = 9
	startLen   = 5
	// A bonus bug comes after every bugEvery foods and stays bugSteps
	// moves; it takes two cells side by side.
	bugEvery = 5
	bugSteps = 20
	// Turns pressed faster than the snake moves wait their turn, up to this
	// many, so a quick down-left still makes the corner.
	maxQueued = 2
	// After a crash the snake blinks this many half-periods.
	blinks     = 8
	blinkEvery = 150 * time.Millisecond
	defaultLvl = 5
)

// stepMS is how long one move takes at each level.
var stepMS = [levels]int{400, 330, 270, 220, 180, 145, 120, 100, 80}

type pt struct{ x, y int }

func (p pt) add(q pt) pt { return pt{p.x + q.x, p.y + q.y} }

var (
	up    = pt{0, -1}
	down  = pt{0, 1}
	left  = pt{-1, 0}
	right = pt{1, 0}
)

type phase int

const (
	inMenu phase = iota
	playing
	paused
	dying
	over
)

// prefs is what the app keeps in its data directory across runs.
type prefs struct {
	Level int  `json:"level"`
	Wrap  bool `json:"wrap,omitempty"`
	Mute  bool `json:"mute,omitempty"`
	Top   int  `json:"top"`
}

type event int

const (
	nothing event = iota
	moved
	ateFood
	ateBug
	died
	blinked
)

type game struct {
	prefs
	phase   phase
	snake   []pt // head first
	dir     pt
	queued  []pt
	food    pt
	bug     pt // its left cell
	bugLeft int
	eaten   int
	score   int
	blink   int
	newTop  bool
	rnd     *rand.Rand
}

func newGame(p prefs, rnd *rand.Rand) *game {
	if p.Level < 1 || p.Level > levels {
		p.Level = defaultLvl
	}
	return &game{prefs: p, rnd: rnd}
}

func (g *game) setLevel(l int) { g.Level = min(max(l, 1), levels) }

func (g *game) interval() time.Duration {
	if g.phase == dying {
		return blinkEvery
	}
	return time.Duration(stepMS[g.Level-1]) * time.Millisecond
}

func (g *game) start() {
	g.snake = g.snake[:0]
	for i := range startLen {
		g.snake = append(g.snake, pt{cols/2 - i, rows / 2})
	}
	g.dir, g.queued = right, nil
	g.bugLeft, g.eaten, g.score, g.newTop = 0, 0, 0, false
	g.placeFood()
	g.phase = playing
}

func (g *game) turn(d pt) {
	if g.phase != playing {
		return
	}
	last := g.dir
	if n := len(g.queued); n > 0 {
		last = g.queued[n-1]
	}
	if d == last || d == (pt{-last.x, -last.y}) || len(g.queued) == maxQueued {
		return
	}
	g.queued = append(g.queued, d)
}

func (g *game) step() event {
	switch g.phase {
	case dying:
		if g.blink--; g.blink <= 0 {
			g.phase = over
		}
		return blinked
	case playing:
	default:
		return nothing
	}
	if len(g.queued) > 0 {
		g.dir, g.queued = g.queued[0], g.queued[1:]
	}
	h := g.snake[0].add(g.dir)
	if g.Wrap {
		h = pt{(h.x + cols) % cols, (h.y + rows) % rows}
	}
	grow := h == g.food
	body := g.snake
	if !grow {
		// The tail moves on in the same step, so its cell is free.
		body = body[:len(body)-1]
	}
	if h.x < 0 || h.y < 0 || h.x >= cols || h.y >= rows || contains(body, h) {
		g.die()
		return died
	}
	g.snake = append([]pt{h}, body...)
	ev := moved
	if g.bugLeft > 0 {
		if h == g.bug || h == g.bug.add(right) {
			g.score += g.Level * (5 + g.bugLeft/2)
			g.bugLeft = 0
			ev = ateBug
		} else {
			g.bugLeft--
		}
	}
	if grow {
		g.score += g.Level
		g.eaten++
		ev = ateFood
		if !g.placeFood() {
			g.die()
			return died
		}
		if g.eaten%bugEvery == 0 && g.bugLeft == 0 {
			g.placeBug()
		}
	}
	return ev
}

// end finishes the game the way a crash does, without the blinking.
func (g *game) end() {
	g.die()
	g.phase, g.blink = over, 0
}

func (g *game) die() {
	g.phase, g.blink = dying, blinks
	if g.score > g.Top {
		g.Top, g.newTop = g.score, true
	}
}

func contains(ps []pt, p pt) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}

func (g *game) taken() []bool {
	t := make([]bool, cols*rows)
	for _, s := range g.snake {
		t[s.y*cols+s.x] = true
	}
	if g.bugLeft > 0 {
		t[g.bug.y*cols+g.bug.x], t[g.bug.y*cols+g.bug.x+1] = true, true
	}
	return t
}

// placeFood is false when the snake fills the field.
func (g *game) placeFood() bool {
	t := g.taken()
	var free []pt
	for i, used := range t {
		if !used {
			free = append(free, pt{i % cols, i / cols})
		}
	}
	if len(free) == 0 {
		return false
	}
	g.food = free[g.rnd.Intn(len(free))]
	return true
}

func (g *game) placeBug() {
	t := g.taken()
	t[g.food.y*cols+g.food.x] = true
	var spots []pt
	for y := range rows {
		for x := range cols - 1 {
			if !t[y*cols+x] && !t[y*cols+x+1] {
				spots = append(spots, pt{x, y})
			}
		}
	}
	if len(spots) > 0 {
		g.bug, g.bugLeft = spots[g.rnd.Intn(len(spots))], bugSteps
	}
}
