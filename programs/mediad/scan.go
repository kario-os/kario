package mediad

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/kario-os/kario/media"
	"github.com/kario-os/kario/media/qrscan"
	"github.com/kario-os/kario/ui"
)

// The scanner (scan.qr): scan.open shows what the camera sees and decodes
// it a few times a second until a code is read; scan.wait answers with that
// code, scan.close ends the scan. Decoding happens here, in the service that
// owns the camera, so a frame that trips the decoder takes down at most this
// sandboxed process, never the Shell, and no frame leaves it but to the
// screen.

const (
	// scanW×scanH is the decoder's frame: a camera's preview size, and the
	// bound on its time (2–6 ms on the developer machine, an estimated
	// 12–40 ms on a Cortex-A53).
	scanW, scanH = 640, 480
	scanEvery    = 250 * time.Millisecond
	// maxScans bounds the scans open at once (each decodes in a goroutine).
	maxScans = 4
)

type scan struct {
	id    string
	start time.Time
	done  chan struct{} // closed by closeScan
	found chan struct{} // closed once text is read
	text  string
	tries int
}

// scanStats are the decoder's numbers since the service started (info, for
// the selftest's scan-report); Open is the newest open scan's tries, -1
// without one.
type scanStats struct {
	Tries   int     `json:"tries"`
	Found   int     `json:"found"`
	TotalMS float64 `json:"total_ms"`
	MaxMS   float64 `json:"max_ms"`
	W       int     `json:"w"`
	H       int     `json:"h"`
	Open    int     `json:"open_tries"`
}

// openScan starts a scan; s.mu is held.
func (s *server) openScan() (*scan, error) {
	if len(s.scans) >= maxScans {
		return nil, errors.New("too many scans open")
	}
	s.seq++
	sc := &scan{id: fmt.Sprintf("scan%d", s.seq), start: time.Now(), done: make(chan struct{}), found: make(chan struct{})}
	s.scans[sc.id] = sc
	bg, look := media.Source(media.Table), media.GraySource(media.TableLook)
	if cam != nil {
		bg, look = camera, cam.Look
	}
	show, lookCard := media.ScanScene(bg, look, s.heldCard)
	s.streams[sc.id] = show
	go s.watch(sc, lookCard)
	return sc, nil
}

// waitScan blocks until the scan reads a code or is closed; s.mu is held,
// and released while it waits.
func (s *server) waitScan(id string) (any, error) {
	sc := s.scans[id]
	if sc == nil {
		return nil, errors.New("no such scan")
	}
	s.mu.Unlock()
	select {
	case <-sc.found:
	case <-sc.done:
	}
	s.mu.Lock()
	if _, alive := s.scans[id]; !alive {
		return nil, errors.New(ui.ErrCancelled)
	}
	s.closeScan(id)
	return map[string]string{"text": sc.text}, nil
}

func (s *server) closeScan(id string) {
	if sc := s.scans[id]; sc != nil {
		close(sc.done)
		delete(s.scans, id)
		delete(s.streams, id)
	}
}

// watch decodes what the scan's camera sees until a code is read or the
// scan closes. One frame is decoded at a time across all scans.
func (s *server) watch(sc *scan, look media.GraySource) {
	g := media.NewGray(scanW, scanH)
	tick := time.NewTicker(scanEvery)
	defer tick.Stop()
	logged := false
	for {
		select {
		case <-sc.done:
			return
		case <-tick.C:
		}
		s.decoding.Lock()
		look(time.Since(sc.start).Seconds(), g)
		t0 := time.Now()
		text, err := qrscan.Decode(g)
		took := time.Since(t0)
		s.decoding.Unlock()
		s.mu.Lock()
		sc.tries++
		ms := float64(took.Microseconds()) / 1000
		s.stats.Tries++
		s.stats.TotalMS += ms
		s.stats.MaxMS = max(s.stats.MaxMS, ms)
		if err == nil {
			sc.text = text
			s.stats.Found++
			close(sc.found)
			s.mu.Unlock()
			// The length only: the code may be someone's payment code.
			log.Printf("scan %s: read a code of %d bytes on try %d, %s after it opened (decoding took %s)",
				sc.id, len(text), sc.tries, time.Since(sc.start).Round(time.Millisecond), took.Round(10*time.Microsecond))
			return
		}
		s.mu.Unlock()
		if !errors.Is(err, qrscan.ErrNotFound) && !logged {
			log.Printf("scan %s: %v", sc.id, err)
			logged = true
		}
	}
}

// heldCard is the code held up to the camera now, if any.
func (s *server) heldCard() *media.Card {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.card
}

// anyCameraCodes lets sim.code hold codes up to a sensor too (--test-codes:
// the developer machine's selftest, which may find a webcam; the image's
// service never has it).
var anyCameraCodes bool

// holdCode is the test hook sim.code; s.mu is held.
func (s *server) holdCode(text string) (any, error) {
	if cam != nil && !cam.Test() && !anyCameraCodes {
		return nil, errors.New("codes are held up only to the synthetic camera or a test driver's")
	}
	var c *media.Card
	if text != "" {
		var err error
		if c, err = media.NewCard(text); err != nil {
			return nil, err
		}
	}
	s.card = c
	return map[string]bool{"ok": true}, nil
}

// scanInfo is info's scan part; s.mu is held.
func (s *server) scanInfo() scanStats {
	st := s.stats
	st.W, st.H, st.Open = scanW, scanH, -1
	var newest *scan
	for _, sc := range s.scans {
		if newest == nil || sc.start.After(newest.start) {
			newest = sc
		}
	}
	if newest != nil {
		st.Open = newest.tries
	}
	return st
}
