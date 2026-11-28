package shell

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/switchrecv/oldphone"
	"github.com/kario-os/kario/ui"
)

// The selftest's old phone (switch.go): switch-send plays the "Kario
// Switch" app with a client of the protocol written apart from the
// receiver (package switchrecv/oldphone), over HTTP to where the code on
// the screen says, with the small data set of switch_sample.json; files
// checks what Files has.

//go:embed switch_sample.json
var switchSample []byte

type switchData struct {
	Device   oldphone.Device `json:"device"`
	Contacts []string        `json:"contacts"`
	CallLog  []struct {
		Number   string `json:"number"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Ago      int64  `json:"ago"`
		Duration int64  `json:"duration"`
	} `json:"call_log"`
	SMS []struct {
		Address string `json:"address"`
		Body    string `json:"body"`
		Type    string `json:"type"`
		Ago     int64  `json:"ago"`
		Read    bool   `json:"read"`
	} `json:"sms"`
	Calendar []string `json:"calendar"`
	Photos   []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		TakenAgo int64  `json:"taken_ago"`
	} `json:"photos"`
}

func (r *Runner) switchStep(cmd, arg string) error {
	switch cmd {
	case "switch-send":
		kinds := strings.Fields(arg)
		code := r.C.sw.code
		if code == "" {
			return errors.New("no pairing code: start receiving in Settings first")
		}
		p, err := oldphone.ParsePairing(code)
		if err != nil {
			return err
		}
		switch w := r.C.wifi.(type) {
		case *simWifi:
			if p.SSID != "" {
				if err := w.joinHotspot("old phone", p.PSK); err != nil {
					return err
				}
			} else if l, _ := w.link(); p.Wifi != "" && l.ssid != p.Wifi {
				return fmt.Errorf("the code names Wi-Fi %q, the phone is on %q", p.Wifi, l.ssid)
			}
		case *iwdRadio:
			h := r.C.hotspot.client
			switch {
			case p.SSID != "" && h == nil:
				if err := r.hotspotJoin(w, "old phone", p.SSID, p.PSK, true); err != nil {
					return err
				}
			case p.SSID == "" && h == nil:
				return errors.New("the old phone is on no network: wifi-client <name> <ssid> <passphrase> first")
			case p.SSID == "" && p.Wifi != "" && h.ssid != p.Wifi:
				return fmt.Errorf("the old phone is on %q, the code names Wi-Fi %q", h.ssid, p.Wifi)
			}
		}
		via := "the hotspot " + strconv.Quote(p.SSID)
		if p.SSID == "" {
			via = "Wi-Fi " + strconv.Quote(p.Wifi)
		}
		fmt.Fprintf(r.Out, "PHONE-SWITCH: pairing with %s over %s\n", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), via)
		cl, err := oldphone.New(p, nil)
		if err != nil {
			return err
		}
		if h := r.C.hotspot.client; h != nil {
			// The emulator's old phone: from its own namespace, over the
			// radio to the phone's access point (or the shared Wi-Fi's).
			cl.HTTP.Transport = &http.Transport{DialContext: h.dial}
		}
		cl.HTTP.Timeout = time.Duration(30*max(1, r.Scale)) * time.Second
		res := make(chan error, 1)
		var done oldphone.DoneReply
		go func() {
			var err error
			done, err = switchSend(cl, kinds)
			res <- err
		}()
		end := time.Now().Add(time.Duration(60*max(1, r.Scale)) * time.Second)
		for {
			select {
			case err := <-res:
				if err != nil {
					return fmt.Errorf("the old phone: %w", err)
				}
				fmt.Fprintf(r.Out, "PHONE-SWITCH: sent %s; the receiver stored contacts %d, call log %d, sms %d, calendar %d, photos %d, videos %d, %d failed\n",
					strings.Join(kinds, " "), done.Contacts, done.CallLog, done.SMS, done.Calendar, done.Photos, done.Videos, len(done.Failed))
				return nil
			default:
			}
			if time.Now().After(end) {
				return errors.New("the old phone did not finish")
			}
			r.pump(20 * time.Millisecond)
		}
	case "files":
		// files <text> present|absent [ms]: a file whose name holds text is
		// in Files, or none is.
		rest, ms := lastNum(arg, 5000)
		i := strings.LastIndex(strings.TrimSpace(rest), " ")
		if i < 0 {
			return errors.New("usage: files <text> present|absent [ms]")
		}
		text, want := strings.TrimSpace(rest)[:i], strings.TrimSpace(rest)[i+1:]
		has := func(string) bool {
			return slices.ContainsFunc(r.C.files.Items, func(f *fileItem) bool { return strings.Contains(f.Name, text) })
		}
		if _, ok := r.waitFor(func(s string) bool { return has(s) == (want == "present") }, ms); !ok {
			return fmt.Errorf("Files has a file named like %q: %v, want %s", text, has(""), want)
		}
	}
	return nil
}

// switchSend sends the sample's data of each kind, then done.
func switchSend(cl *oldphone.Client, kinds []string) (oldphone.DoneReply, error) {
	var d switchData
	if err := json.Unmarshal(switchSample, &d); err != nil {
		return oldphone.DoneReply{}, err
	}
	now := time.Now()
	counts := map[string]int{ui.SwitchContacts: len(d.Contacts), ui.SwitchCallLog: len(d.CallLog), ui.SwitchSMS: len(d.SMS), ui.SwitchCalendar: len(d.Calendar), ui.SwitchPhotos: len(d.Photos)}
	if _, err := cl.SayHello(d.Device, counts); err != nil {
		return oldphone.DoneReply{}, err
	}
	items := map[string][]any{}
	for _, v := range d.Contacts {
		items[ui.SwitchContacts] = append(items[ui.SwitchContacts], map[string]string{"vcard": v})
	}
	for _, e := range d.CallLog {
		items[ui.SwitchCallLog] = append(items[ui.SwitchCallLog], map[string]any{"number": e.Number, "name": e.Name, "type": e.Type, "at": now.Unix() - e.Ago, "duration": e.Duration})
	}
	for _, m := range d.SMS {
		items[ui.SwitchSMS] = append(items[ui.SwitchSMS], map[string]any{"address": m.Address, "body": m.Body, "type": m.Type, "at": now.Add(-time.Duration(m.Ago) * time.Second).UnixMilli(), "read": m.Read})
	}
	for _, v := range d.Calendar {
		items[ui.SwitchCalendar] = append(items[ui.SwitchCalendar], map[string]string{"ics": v})
	}
	for _, k := range kinds {
		if k == ui.SwitchPhotos {
			for _, p := range d.Photos {
				if err := cl.SendFile(p.ID, "photo", p.Name, "image/jpeg", now.Unix()-p.TakenAgo, switchPhoto(p.Name)); err != nil {
					return oldphone.DoneReply{}, fmt.Errorf("photo: %w", err)
				}
			}
			continue
		}
		if _, ok := items[k]; !ok {
			return oldphone.DoneReply{}, fmt.Errorf("no sample data of %q", k)
		}
		if _, _, err := cl.Items(k, items[k]); err != nil {
			return oldphone.DoneReply{}, fmt.Errorf("%s: %w", k, err)
		}
	}
	return cl.Done()
}

// switchPhoto is a small JPEG, as a camera's would be.
func switchPhoto(name string) []byte {
	var b bytes.Buffer
	_ = jpeg.Encode(&b, samplePhoto("gen:"+name, 64, 48), &jpeg.Options{Quality: 80})
	return b.Bytes()
}
