// phone-imed runs the system input method out of the Shell: the Shell
// forwards keys while a text field has focus and draws the state it gets
// back. A crash here costs the current composition, not the Shell, and the
// dictionary is mapped from share/phone/ime.dict instead of living in the
// Shell's heap.
package imed

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

type server struct {
	mu sync.Mutex
	e  *ime.Engine
}

type openArgs struct {
	Keypad ime.Keypad `json:"keypad"`
	Mode   ime.Mode   `json:"mode"`
	Fuzzy  bool       `json:"fuzzy"`
}

type keyReply struct {
	Handled bool      `json:"handled"`
	Text    string    `json:"text,omitempty"`
	State   ime.State `json:"state"`
}

// state reports the engine; asking for the symbol table is an event, so it
// is cleared once reported.
func (s *server) state() ime.State {
	st := s.e.State()
	s.e.WantSymbols = false
	return st
}

func (s *server) handle(method string, params json.RawMessage) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case svc.IMEOpen:
		var a openArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if a.Keypad != "" {
			s.e.SetKeypad(a.Keypad)
		}
		if a.Mode != "" {
			s.e.SetMode(a.Mode)
		}
		s.e.Fuzzy = a.Fuzzy
		return s.state(), nil
	case svc.IMEKey:
		var k ime.Key
		if err := json.Unmarshal(params, &k); err != nil {
			return nil, err
		}
		ok, text := s.e.Handle(k)
		return keyReply{Handled: ok, Text: text, State: s.state()}, nil
	case svc.IMETick:
		return keyReply{Handled: true, Text: s.e.Tick(), State: s.state()}, nil
	case svc.IMECancel:
		s.e.Cancel()
		return s.state(), nil
	case svc.IMEState:
		return s.state(), nil
	case svc.IMEStats:
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return map[string]any{"pid": os.Getpid(), "dict": ime.DictSource, "heap_inuse": m.HeapInuse, "heap_idle": m.HeapIdle,
			"heap_released": m.HeapReleased, "stack": m.StackSys, "sys": m.Sys, "gcs": m.NumGC, "total_alloc": m.TotalAlloc}, nil
	}
	return nil, errors.New("unknown method " + method)
}

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	exe, _ := os.Executable()
	prefix := filepath.Dir(filepath.Dir(exe))
	runDir := flag.String("run-dir", "/run/phone", "directory for imed.sock")
	stateDir := flag.String("state-dir", "/var/lib/phone", "keeps ime/user.json (the user dictionary)")
	dict := flag.String("dict", filepath.Join(prefix, "share", "phone", "ime.dict"), "compiled dictionary (phone-imedict)")
	keypad := flag.String("keypad", "qwerty", "qwerty | t9 until the Shell opens a session")
	flag.Parse()
	// Little is live (the dictionary is mapped, not on the heap) and keys
	// arrive slowly: collecting early keeps the heap near 1 MB instead of
	// the runtime's default 4 MB floor.
	debug.SetGCPercent(25)
	ime.SetDictPath(*dict)
	ime.Preload()
	e := ime.New(ime.Keypad(*keypad))
	if err := os.MkdirAll(filepath.Join(*stateDir, "ime"), 0o700); err != nil {
		log.Fatal(err)
	}
	e.SetUserDict(filepath.Join(*stateDir, "ime", "user.json"))
	s := &server{e: e}
	log.Fatal(wire.Serve(filepath.Join(*runDir, svc.ImedSock), s.handle))
}
