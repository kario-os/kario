package main

import (
	"math/rand"
	"path/filepath"
	"testing"
	"time"
)

func started(p prefs) *game {
	g := newGame(p, rand.New(rand.NewSource(7)))
	g.start()
	g.food = pt{0, 0}
	return g
}

func TestStartAndWalls(t *testing.T) {
	g := started(prefs{})
	if g.Level != defaultLvl || len(g.snake) != startLen || g.snake[0] != (pt{cols / 2, rows / 2}) || g.dir != right {
		t.Fatalf("start: level %d, snake %v, dir %v", g.Level, g.snake, g.dir)
	}
	for i := 0; i < cols/2; i++ {
		if ev := g.step(); ev != moved {
			t.Fatalf("move %d: %v", i, ev)
		}
	}
	if g.snake[0].x != cols-1 || g.step() != died || g.phase != dying {
		t.Fatalf("the wall: head %v, phase %v", g.snake[0], g.phase)
	}
	if g.interval() != blinkEvery {
		t.Fatal("the crash does not blink")
	}
	for range blinks {
		g.step()
	}
	if g.phase != over || g.step() != nothing {
		t.Fatalf("after blinking: %v", g.phase)
	}
}

func TestWrapAround(t *testing.T) {
	g := started(prefs{Wrap: true})
	for range cols/2 + 1 {
		if g.step() != moved {
			t.Fatal("crashed into a wrap-around edge")
		}
	}
	if g.snake[0] != (pt{0, rows / 2}) {
		t.Fatalf("head %v", g.snake[0])
	}
	g.turn(up)
	for range rows/2 + 1 {
		g.step()
	}
	if g.snake[0] != (pt{0, rows - 1}) || g.phase != playing {
		t.Fatalf("head %v, %v", g.snake[0], g.phase)
	}
}

func TestNoReverseAndQueuedTurns(t *testing.T) {
	g := started(prefs{})
	g.turn(left)
	g.turn(right)
	g.step()
	if g.dir != right || g.snake[0] != (pt{cols/2 + 1, rows / 2}) {
		t.Fatalf("reversed: dir %v head %v", g.dir, g.snake[0])
	}
	g.turn(up)
	g.turn(left)
	g.turn(down)
	if len(g.queued) != 2 {
		t.Fatalf("queued %v", g.queued)
	}
	g.step()
	g.step()
	if g.dir != left || g.snake[0] != (pt{cols / 2, rows/2 - 1}) {
		t.Fatalf("corner: dir %v head %v", g.dir, g.snake[0])
	}
	g.turn(right)
	g.step()
	if g.dir != left {
		t.Fatal("reversed after a corner")
	}
}

func TestEatingAndBug(t *testing.T) {
	g := started(prefs{Level: 4})
	for i := 1; i <= bugEvery; i++ {
		g.food = g.snake[0].add(g.dir)
		n := len(g.snake)
		if ev := g.step(); ev != ateFood {
			t.Fatalf("food %d: %v", i, ev)
		}
		if len(g.snake) != n+1 || g.score != 4*i || contains(g.snake, g.food) {
			t.Fatalf("food %d: length %d, score %d, food %v on %v", i, len(g.snake), g.score, g.food, g.snake)
		}
	}
	if g.bugLeft != bugSteps || contains(g.snake, g.bug) || contains(g.snake, g.bug.add(right)) || g.bug.x >= cols-1 || g.bug == g.food {
		t.Fatalf("bug %v for %d, snake %v", g.bug, g.bugLeft, g.snake)
	}
	// The bug's right half just ahead of the head.
	g.bug, g.food = g.snake[0], pt{0, 0}
	if g.step() != ateBug {
		t.Fatalf("bug not eaten at %v (head %v)", g.bug, g.snake[0])
	}
	if want := 4*bugEvery + 4*(5+bugSteps/2); g.score != want || g.bugLeft != 0 {
		t.Fatalf("score %d, want %d", g.score, want)
	}
	g = started(prefs{Wrap: true})
	g.bug, g.bugLeft = pt{0, rows - 1}, bugSteps
	for range bugSteps {
		g.step()
	}
	if g.bugLeft != 0 {
		t.Fatalf("the bug stays %d more", g.bugLeft)
	}
}

func TestSelfAndTail(t *testing.T) {
	g := started(prefs{})
	g.snake = []pt{{5, 5}, {6, 5}, {6, 6}, {5, 6}}
	g.dir = left
	g.turn(down)
	if g.step() != moved {
		t.Fatal("moving into the cell the tail leaves is a crash")
	}
	g = started(prefs{})
	g.snake = []pt{{5, 5}, {6, 5}, {6, 6}, {5, 6}, {4, 6}}
	g.dir = left
	g.turn(down)
	if g.step() != died {
		t.Fatal("ran into itself")
	}
}

func TestTopScoreAndPrefs(t *testing.T) {
	g := started(prefs{Top: 3, Level: 2})
	g.score = 10
	g.end()
	if g.Top != 10 || !g.newTop || g.phase != over {
		t.Fatalf("top %d new %v phase %v", g.Top, g.newTop, g.phase)
	}
	path := filepath.Join(t.TempDir(), "snake.json")
	if err := savePrefs(path, g.prefs); err != nil {
		t.Fatal(err)
	}
	if p := loadPrefs(path); p != g.prefs {
		t.Fatalf("loaded %+v, saved %+v", p, g.prefs)
	}
	if p := loadPrefs(filepath.Join(t.TempDir(), "none.json")); newGame(p, nil).Level != defaultLvl {
		t.Fatal("no saved level")
	}
	g = started(prefs{Top: 50})
	g.score = 7
	g.end()
	if g.Top != 50 || g.newTop {
		t.Fatal("a lower score is not the top")
	}
}

func TestLevels(t *testing.T) {
	g := newGame(prefs{Level: 1}, nil)
	slow := g.interval()
	g.setLevel(99)
	if g.Level != levels || g.interval() >= slow || g.interval() < 50*time.Millisecond {
		t.Fatalf("level %d: %v vs %v", g.Level, g.interval(), slow)
	}
	g.setLevel(0)
	if g.Level != 1 {
		t.Fatal("level 0")
	}
	for i := 1; i < levels; i++ {
		if stepMS[i] >= stepMS[i-1] {
			t.Fatalf("level %d is not faster", i+1)
		}
	}
}

func TestKeys(t *testing.T) {
	s := &snakeApp{g: newGame(prefs{}, rand.New(rand.NewSource(1)))}
	for k, want := range map[string]string{"enter": "start", "5": "start", "up": "faster", "6": "faster", "8": "slower", "left": "slower", "#": "walls", "*": "sound", "0": ""} {
		if got := s.keyAction(k); got != want {
			t.Fatalf("menu: %s is %q, want %q", k, got, want)
		}
	}
	s.g.start()
	if s.keyAction("5") != "pause" || s.keyAction("#") != "" {
		t.Fatal("playing keys")
	}
	s.g.phase = paused
	if s.keyAction("enter") != "resume" || s.keyAction("0") != "end" || s.keyAction("up") != "" {
		t.Fatal("paused keys")
	}
	s.g.die()
	if s.keyAction("5") != "" || len(s.actions()) != 0 {
		t.Fatal("keys while the snake blinks")
	}
	s.g.phase = over
	if s.keyAction("5") != "new" {
		t.Fatal("game over keys")
	}
}
