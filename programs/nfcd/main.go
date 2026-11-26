// phone-nfcd owns the NFC controller: it decides which card the phone
// answers a reader as, hands the reader's commands for an app's card to
// the shell (which hands them to the app and its answer back), keeps the
// door cards the phone copied and when each was tapped. Only the shell
// talks to it (30-nfc.md).
//
//	phone-nfcd --run-dir DIR --state-dir DIR [--controller sim|nci:/dev/nxpnfc]
package nfcd

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/hal/nfc"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	runDir := flag.String("run-dir", "/run/phone", "directory for service sockets")
	stateDir := flag.String("state-dir", "/var/lib/phone", "persistent state")
	exe, _ := os.Executable()
	controller := flag.String("controller", "", "sim (simulated readers and cards) or nci:<device> (a raw NCI device; not run on hardware yet); by default the board's (share/phone/sku.json), simulated without one (a developer machine)")
	flag.Parse()
	log.SetPrefix("phone-nfcd: ")
	if *controller == "" {
		*controller = boardController(filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "phone", "sku.json"))
	}
	if *controller == "" {
		// A board without NFC: nothing to serve, and no failure to restart on.
		log.Print("this board has no NFC controller")
		return
	}
	if err := os.MkdirAll(*runDir, 0o755); err != nil {
		log.Fatal(err)
	}
	ctl, err := openController(*controller)
	if err != nil {
		log.Fatal(err)
	}
	var hub atomic.Pointer[wire.Hub]
	e := newEngine(ctl, *stateDir, func(ev svc.NFCEvent) {
		if h := hub.Load(); h != nil {
			h.Broadcast(ev)
		}
	})
	sock := filepath.Join(*runDir, svc.NfcdSock)
	h, err := wire.ServeHub(sock, func(method string, params json.RawMessage) (any, error) { return e.handle(method, params) })
	if err != nil {
		log.Fatal(err)
	}
	hub.Store(h)
	log.Printf("%s on %s", ctl.Info().Name, sock)
	e.run()
	log.Fatal("the controller went away")
}

func openController(spec string) (nfc.Controller, error) {
	switch {
	case spec == "sim":
		return nfc.NewSim(), nil
	case strings.HasPrefix(spec, "nci:"):
		dev := strings.TrimPrefix(spec, "nci:")
		n, err := nfc.OpenNCI(dev)
		if err != nil {
			return nil, err
		}
		if err := nfc.WakeSource(dev); err != nil {
			log.Printf("a reader cannot wake the suspended phone: %v", err)
		}
		return n, nil
	}
	return nil, fmt.Errorf("--controller %q: sim or nci:<device>", spec)
}

func (e *engine) handle(method string, params json.RawMessage) (any, error) {
	var p struct {
		UID, Card, Session, App, APDU, AID string
		Seconds, Balance                   int
		Encrypted, CPU                     bool
	}
	_ = json.Unmarshal(params, &p)
	var ra svc.NFCReaderArgs
	_ = json.Unmarshal(params, &ra)
	switch method {
	case svc.NFCReaderWait:
		return e.readerWait(ra)
	case svc.NFCReaderExchange:
		return e.readerExchange(ra)
	case svc.NFCSimTap:
		var a svc.NFCTapArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		return e.tap(a)
	case svc.NFCSimCard:
		sim, ok := e.ctl.(*nfc.Sim)
		if !ok {
			return nil, errors.New("not the simulated controller")
		}
		if p.UID == "" {
			sim.Hold(nil)
			return map[string]bool{"ok": true}, nil
		}
		uid, err := hex.DecodeString(p.UID)
		if err != nil || len(uid) < 4 {
			return nil, errors.New("a UID in hex")
		}
		t := &nfc.Tag{UID: uid, ATQA: [2]byte{0x04, 0x00}, SAK: 0x08, Encrypted: p.Encrypted}
		if p.CPU || p.AID != "" {
			t.SAK, t.ISODep, t.ATQA = 0x20, true, [2]byte{0x08, 0x00}
		}
		var applet nfc.Applet
		if p.AID != "" {
			aid, err := ui.ParseAID(p.AID)
			if err != nil {
				return nil, err
			}
			applet = nfc.TransitApplet{AID: aid, Balance: p.Balance}
		}
		sim.HoldApplet(t, applet)
		return map[string]bool{"ok": true}, nil
	case svc.NFCSet:
		var a ui.NFCSetArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if a.Key == ui.NFCSetRead {
			r := e.readDoor(time.Duration(min(max(a.Seconds, 1), 60)) * time.Second)
			e.mu.Lock()
			defer e.mu.Unlock()
			st := e.state()
			st.Read = &r
			return st, nil
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if err := e.set(a); err != nil {
			return nil, err
		}
		return e.state(), nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch method {
	case svc.NFCState:
		return e.state(), nil
	case svc.NFCPolicy:
		var a svc.NFCPolicyArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		e.setPolicy(a)
		return map[string]bool{"ok": true}, nil
	case svc.NFCPresent:
		return map[string]bool{"ok": true}, e.present(p.Card, time.Duration(p.Seconds)*time.Second)
	case svc.NFCReply:
		return map[string]bool{"ok": true}, e.reply(p.Session, p.App, p.APDU)
	case svc.NFCReaderOpen:
		return e.readerOpen(ra.App)
	case svc.NFCReaderClose:
		e.readerClose(ra)
		return map[string]bool{"ok": true}, nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

// tap holds the phone to a simulated reader and answers once nfcd has
// judged the field.
func (e *engine) tap(a svc.NFCTapArgs) (svc.NFCTapRes, error) {
	sim, ok := e.ctl.(*nfc.Sim)
	if !ok {
		return svc.NFCTapRes{}, errors.New("not the simulated controller")
	}
	var r nfc.Reader
	switch a.Kind {
	case "door":
		d := nfc.DoorReader{Name: a.Name}
		for _, u := range a.Accept {
			b, err := hex.DecodeString(u)
			if err != nil {
				return svc.NFCTapRes{}, fmt.Errorf("UID %q", u)
			}
			d.Accept = append(d.Accept, b)
		}
		r = d
	case "gate", "pay":
		aid, err := hex.DecodeString(a.AID)
		if err != nil || len(aid) < 5 {
			return svc.NFCTapRes{}, fmt.Errorf("AID %q", a.AID)
		}
		if a.Kind == "gate" {
			r = nfc.TransitGate{Name: a.Name, AID: aid, Fare: a.Fare}
		} else {
			r = nfc.PaymentTerminal{Name: a.Name, AID: aid, Amount: a.Amount}
		}
	default:
		return svc.NFCTapRes{}, fmt.Errorf("reader kind %q: door, gate or pay", a.Kind)
	}
	e.mu.Lock()
	before := e.done
	e.mu.Unlock()
	res := sim.Tap(r, time.Duration(max(a.PatienceMS, 100))*time.Millisecond)
	out := svc.NFCTapRes{OK: res.OK, Note: res.Note}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		e.mu.Lock()
		if e.done > before {
			if e.last != nil {
				out.Card, out.Name = e.last.ID, e.last.Name
			}
			e.mu.Unlock()
			return out, nil
		}
		e.mu.Unlock()
	}
	return out, errors.New("nfcd did not see the field end")
}

// boardController is the NFC controller the board names in sku.json ("" for
// none); no sku.json is a developer machine, simulated.
func boardController(path string) string {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "sim"
	}
	var sku struct {
		NFC string `json:"nfc"`
	}
	if err != nil || json.Unmarshal(raw, &sku) != nil {
		return ""
	}
	return sku.NFC
}
