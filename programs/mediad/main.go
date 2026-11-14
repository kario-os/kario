// phone-mediad is the media service: camera capture, call media sessions,
// the QR scanner and video playback (player.go). Apps never talk to it directly; the shell brokers control
// requests and subscribes to frames only for surfaces that are on screen.
package mediad

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/media"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// demo-merchant.txt is the payment code the simulated scanner reads, as a
// shop's code carries it: PAY|merchant|its name|amount in fen.
//
//go:embed demo-merchant.txt
var demoMerchantFile string

var demoMerchantCode = strings.TrimSpace(demoMerchantFile)

type call struct {
	ID, App, Peer string
	Mic, Cam      bool
	Started       time.Time
}

type scan struct {
	done chan struct{}
}

type server struct {
	started time.Time
	mu      sync.Mutex
	streams map[string]media.Source
	calls   map[string]*call
	scans   map[string]*scan
	players map[string]*player
	seq     int
}

// serve takes files only for player.* (the shell's video and audio ring).
func (s *server) serve(method string, params json.RawMessage, files []*os.File) (any, error) {
	if strings.HasPrefix(method, "player.") {
		return s.handlePlayer(method, params, files)
	}
	for _, f := range files {
		f.Close()
	}
	return s.handle(method, params)
}

func (s *server) handle(method string, params json.RawMessage) (any, error) {
	var p struct {
		App    string `json:"app"`
		Peer   string `json:"peer"`
		Video  bool   `json:"video"`
		CallID string `json:"call_id"`
		Mic    *bool  `json:"mic"`
		Cam    *bool  `json:"cam"`
		ScanID string `json:"scan_id"`
		CamID  string `json:"cam_id"`
	}
	_ = json.Unmarshal(params, &p)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case svc.MediaCallStart:
		s.seq++
		c := &call{ID: fmt.Sprintf("call%d", s.seq), App: p.App, Peer: p.Peer, Mic: true, Cam: p.Video, Started: time.Now()}
		s.calls[c.ID] = c
		s.streams[c.ID+".local"] = func(t float64, f *media.Frame) {
			s.mu.Lock()
			on := c.Cam
			s.mu.Unlock()
			if on {
				camera(t, f)
			} else {
				media.Off(t, f)
			}
		}
		s.streams[c.ID+".remote"] = media.Peer
		log.Printf("call %s started app=%s peer=%s", c.ID, c.App, c.Peer)
		return map[string]string{"call_id": c.ID, "local": c.ID + ".local", "remote": c.ID + ".remote"}, nil
	case svc.MediaCallControl:
		c := s.calls[p.CallID]
		if c == nil {
			return nil, errors.New("no such call")
		}
		if p.Mic != nil {
			c.Mic = *p.Mic
		}
		if p.Cam != nil {
			c.Cam = *p.Cam
		}
		return map[string]bool{"mic": c.Mic, "cam": c.Cam}, nil
	case svc.MediaCallEnd:
		c := s.calls[p.CallID]
		if c == nil {
			return nil, errors.New("no such call")
		}
		delete(s.calls, c.ID)
		delete(s.streams, c.ID+".local")
		delete(s.streams, c.ID+".remote")
		log.Printf("call %s ended after %s", c.ID, time.Since(c.Started).Round(time.Second))
		return map[string]int{"seconds": int(time.Since(c.Started).Seconds())}, nil
	case svc.MediaCameraOpen:
		// The viewfinder of a still capture: the camera stream until
		// camera.capture or camera.close.
		s.seq++
		id := fmt.Sprintf("cam%d", s.seq)
		s.streams[id] = camera
		return map[string]string{"cam_id": id, "stream": id}, nil
	case svc.MediaCameraCapture:
		if s.streams[p.CamID] == nil {
			return nil, errors.New("no such camera")
		}
		f, err := capturePNG(time.Since(s.started).Seconds())
		if err != nil {
			return nil, err
		}
		delete(s.streams, p.CamID)
		return wire.Files{Result: map[string]int{"w": photoW, "h": photoH}, Files: []*os.File{f}}, nil
	case svc.MediaInfo:
		info := map[string]any{"camera": cameraName}
		if cam != nil {
			info["frames"] = cam.Frames()
		}
		return info, nil
	case svc.MediaCameraClose:
		delete(s.streams, p.CamID)
		return map[string]bool{"closed": true}, nil
	case svc.MediaScanOpen:
		s.seq++
		id := fmt.Sprintf("scan%d", s.seq)
		s.scans[id] = &scan{done: make(chan struct{})}
		s.streams[id] = media.Scanner(demoMerchantCode)
		return map[string]string{"scan_id": id, "stream": id}, nil
	case svc.MediaScanWait:
		sc := s.scans[p.ScanID]
		if sc == nil {
			return nil, errors.New("no such scan")
		}
		s.mu.Unlock()
		select {
		case <-time.After(1800 * time.Millisecond):
		case <-sc.done:
		}
		s.mu.Lock()
		if _, alive := s.scans[p.ScanID]; !alive {
			return nil, errors.New(ui.ErrCancelled)
		}
		s.closeScan(p.ScanID)
		return map[string]string{"text": demoMerchantCode}, nil
	case svc.MediaScanClose:
		s.closeScan(p.ScanID)
		return map[string]bool{"closed": true}, nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

// camera is what the viewfinder, photos and the local side of video calls
// show: a V4L2 camera when the device has one (vivid on the emulator), else
// the synthetic scene.
var (
	camera     media.Source = media.Camera
	cameraName              = "synthetic"
	cam        *media.V4L2
)

func openCamera() {
	devs, _ := filepath.Glob("/dev/video*")
	for _, d := range devs {
		c, err := media.OpenCamera(d, 640, 480)
		if err != nil {
			log.Printf("camera %s: %v", d, err)
			continue
		}
		camera, cameraName, cam = c.Source(), "v4l2:"+c.Driver, c
		log.Printf("camera: %s (%s, %s)", d, c.Driver, c.Card)
		return
	}
	log.Printf("camera: none, using the synthetic scene")
}

// Still photos: what the camera sees now, at a photo's resolution rather than
// the preview's, as JPEG (what a camera pipeline produces) in a memfd the
// caller reads.
const photoW, photoH = 480, 360

func capturePNG(t float64) (*os.File, error) {
	fr := media.NewFrame(photoW, photoH)
	camera(t, fr)
	img := image.NewRGBA(image.Rect(0, 0, photoW, photoH))
	for i := 0; i < photoW*photoH; i++ {
		copy(img.Pix[4*i:4*i+3], fr.Pix[3*i:3*i+3])
		img.Pix[4*i+3] = 255
	}
	fd, err := unix.MemfdCreate("photo", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "photo")
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *server) closeScan(id string) {
	if sc := s.scans[id]; sc != nil {
		close(sc.done)
		delete(s.scans, id)
		delete(s.streams, id)
	}
}

func (s *server) source(name string) media.Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[name]
}

func (s *server) serveFrames(path string) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.stream(nc)
	}
}

func (s *server) stream(nc net.Conn) {
	defer nc.Close()
	line, err := bufio.NewReader(nc).ReadBytes('\n')
	if err != nil {
		return
	}
	var req media.SubscribeReq
	if json.Unmarshal(line, &req) != nil || req.W <= 0 || req.H <= 0 || req.W > 1920 || req.H > 1080 {
		return
	}
	start := time.Now()
	tick := time.NewTicker(time.Second / 15)
	defer tick.Stop()
	f := media.NewFrame(req.W, req.H)
	// "surface": frames go into shared memory handed over once; the
	// connection then carries only frame numbers.
	var surf *media.Surface
	if req.T == "surface" {
		uc, ok := nc.(*net.UnixConn)
		var err error
		if surf, err = media.NewSurface(req.W, req.H); !ok || err != nil || media.ServeSurface(uc, surf) != nil {
			log.Printf("surface %s: %v", req.Stream, err)
			return
		}
		defer surf.Close()
	}
	w := bufio.NewWriterSize(nc, len(f.Pix)+16)
	for range tick.C {
		src := s.source(req.Stream)
		if src == nil {
			return
		}
		src(time.Since(start).Seconds(), f)
		f.Seq++
		if surf != nil {
			surf.Publish(f)
			if media.Notify(w, f.Seq) != nil || w.Flush() != nil {
				return
			}
			continue
		}
		if err := media.WriteFrame(w, f); err != nil || w.Flush() != nil {
			return
		}
	}
}

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	runDir := flag.String("run-dir", "/run/phone", "directory for service sockets")
	flag.Parse()
	log.SetPrefix("phone-mediad: ")
	if err := os.MkdirAll(*runDir, 0o755); err != nil {
		log.Fatal(err)
	}
	openCamera()
	s := &server{started: time.Now(), streams: map[string]media.Source{}, calls: map[string]*call{}, scans: map[string]*scan{}, players: map[string]*player{}}
	go func() { log.Fatal(s.serveFrames(filepath.Join(*runDir, svc.MediadFramesSock))) }()
	log.Printf("listening in %s", *runDir)
	log.Fatal(wire.ServeFiles(filepath.Join(*runDir, svc.MediadSock), s.serve))
}
