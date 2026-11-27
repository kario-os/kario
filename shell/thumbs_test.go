package shell

import (
	"testing"
	"time"
)

// A photo's thumbnail is made off the Shell's loop: the frame that first
// shows its tile goes without it at once, and a frame after it is made
// names its surface (report 008 §1.5: decoding the rows a key brought into
// view held that key's frame 50 ms on a PC).
func TestThumbnailsAreMadeOffTheLoop(t *testing.T) {
	c := loopCore(t, t.TempDir())
	var it *mediaItem
	_ = c.onLoop(func() error {
		var err error
		if it, err = c.media.add(testJPEG(t, 1200, 800), "wide.jpg", time.Now()); err != nil {
			t.Fatal(err)
		}
		c.renderer = &RendererInfo{Features: []string{"shm"}}
		c.screenDev.Width, c.frameSurfs = 480, map[string]bool{}
		if f := c.thumbFrame(it, 3); f != nil {
			t.Errorf("the first frame waited for the thumbnail: %+v", f)
		}
		return nil
	})
	until(t, c, "the thumbnail made", func() bool {
		f := c.thumbFrame(it, 3)
		return f != nil && f.Surface != "" && c.thumbSurfs[f.Surface] != nil && f.W == 160
	})
}

// The loader makes each surface once, wakes the loop after it, hands it
// over once, and closes the ones made for a screen gone by.
func TestThumbLoaderMakesEachOnceAndDropsWhatNoFrameWants(t *testing.T) {
	c := loopCore(t, t.TempDir())
	var job, gone thumbJob
	_ = c.onLoop(func() error {
		a, err := c.media.add(testJPEG(t, 640, 480), "a.jpg", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		b, err := c.media.add(testJPEG(t, 640, 480), "b.jpg", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		job = thumbJob{key: "thumb:a", thumb: c.media.thumb(a.ID), photo: c.media.path(a), minW: thumbSide, side: 64}
		gone = thumbJob{key: "thumb:b", thumb: c.media.thumb(b.ID), photo: c.media.path(b), minW: thumbSide, side: 64}
		return nil
	})
	var l thumbLoader
	woke := make(chan struct{}, 4)
	wake := func() { woke <- struct{}{} }
	l.load(job, wake)
	l.load(job, wake)
	l.load(gone, wake)
	if !l.settle(10 * time.Second) {
		t.Fatal("settle found nothing being made")
	}
	if len(woke) != 2 {
		t.Fatalf("woken %d times for two thumbnails", len(woke))
	}
	l.keep(map[string]bool{job.key: true})
	s := l.take(job.key)
	if s == nil || s.W != 64 || s.H != 64 {
		t.Fatalf("the surface made: %+v", s)
	}
	defer s.Close()
	if l.take(job.key) != nil || l.take(gone.key) != nil {
		t.Fatal("a surface handed over twice, or one no frame wanted kept")
	}
	if l.settle(time.Second) {
		t.Fatal("settle waited with nothing queued")
	}
}

// A thumbnail made between the frame that asked for it and the snapshot's
// settle is one the snapshot's frame must take: under load the picker's
// last tile was made in that gap and 37b-picker-last showed its icon.
func TestSettleCountsAThumbnailMadeBeforeIt(t *testing.T) {
	c := loopCore(t, t.TempDir())
	var job thumbJob
	_ = c.onLoop(func() error {
		a, err := c.media.add(testJPEG(t, 640, 480), "a.jpg", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		job = thumbJob{key: "thumb:a", thumb: c.media.thumb(a.ID), photo: c.media.path(a), minW: thumbSide, side: 64}
		return nil
	})
	var l thumbLoader
	woke := make(chan struct{}, 1)
	l.load(job, func() { woke <- struct{}{} })
	select {
	case <-woke:
	case <-time.After(10 * time.Second):
		t.Fatal("the thumbnail was not made")
	}
	if !l.settle(time.Second) {
		t.Fatal("settle said no frame would have more, with a thumbnail made that no frame took")
	}
	s := l.take(job.key)
	if s == nil {
		t.Fatal("the surface made is gone")
	}
	defer s.Close()
	if l.settle(time.Second) {
		t.Fatal("settle asked for another frame after the surface was taken")
	}
}
