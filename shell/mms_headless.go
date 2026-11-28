package shell

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/mms"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The selftest's operator for multimedia messages: an MMSC (mms.FakeMMSC)
// on a loopback port of the shell's own process, which phone-smsd sends to
// (svc.SMSMMSSim, taken only with the simulated baseband) and fetches from;
// a new message is announced as operators do, by a WAP push in an 8-bit SMS
// from the operator's gateway (telephonyd's sim.sms with data).
//
//	mms offer <from> <text>   a message with the text and a picture (photo.png) from that number
//	mms sent <n> [ms]         wait until the MMSC has taken n messages
//	mms album <text> [ms]     wait until the media library has a picture whose name holds text
//	mms album-drop <text>     take those pictures out again: the cases after count the library
//	mms vcard <from> <name> <vcard>   a message with a text and a contact card named name (\n in vcard is a line break)
//	flashes <n> [ms]          wait until n class 0 messages have been shown
//	sms due                   phone-smsd sends its scheduled messages now (svc.SMSScheduleSim)
//	sms later <seconds> <number> <text>   phone-smsd keeps a message to the number scheduled that far ahead
//	                          (on its clock), as the default SMS app's sms.send with at; only with the
//	                          simulated baseband
//	sms drop <number>         phone-smsd deletes the conversation with the number, scheduled messages too
//	sms scheduled <n> [ms]    wait until phone-smsd keeps n messages scheduled (svc.SMSScheduled)
//	files-drop <text>         take the files whose name holds text out of Files

type mmsSim struct {
	f   *mms.FakeMMSC
	url string
}

// mmsGateway is the number the simulated operator's WAP pushes come from.
const mmsGateway = "1065800000"

func (r *Runner) mmsStart() (*mmsSim, error) {
	if r.mmsc != nil {
		return r.mmsc, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	m := &mmsSim{f: &mms.FakeMMSC{}, url: "http://" + ln.Addr().String()}
	go func() { _ = http.Serve(ln, m.f) }()
	if err := r.C.smsRPC(svc.SMSMMSSim, map[string]string{"mmsc": m.url}, nil); err != nil {
		ln.Close()
		return nil, fmt.Errorf("phone-smsd took no MMSC: %w", err)
	}
	r.mmsc = m
	return m, nil
}

// mmsPicture is a small PNG, a gradient.
func mmsPicture() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 160, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 160; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / 160), uint8(y * 255 / 120), 160, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func (r *Runner) mmsStep(cmd, arg string) error {
	switch {
	case cmd == "sms" && strings.TrimSpace(arg) == "due":
		var res map[string]int
		if err := r.C.smsRPC(svc.SMSScheduleSim, nil, &res); err != nil {
			return err
		}
		r.pump(100 * time.Millisecond)
		return nil
	case cmd == "sms":
		return r.smsStep(strings.Fields(arg))
	case cmd == "files-drop":
		text := strings.TrimSpace(arg)
		var drop []string
		for _, f := range r.C.files.Items {
			if text != "" && strings.Contains(f.Name, text) {
				drop = append(drop, f.ID)
			}
		}
		if len(drop) == 0 {
			return fmt.Errorf("no file named with %q in Files", text)
		}
		for _, id := range drop {
			r.C.files.remove(id)
		}
		return nil
	}
	if cmd == "flashes" {
		rest, ms := lastNum(arg, 8000)
		want, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return fmt.Errorf("usage: flashes <n> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.FlashesShown() >= want }, ms); !ok {
			return fmt.Errorf("%d flash messages shown, want %d", r.C.FlashesShown(), want)
		}
		return nil
	}
	f := strings.SplitN(arg, " ", 3)
	switch {
	case len(f) == 3 && f[0] == "offer":
		return r.mmsOffer(f[1], []mms.Part{
			{ContentType: mms.TypePNG, Name: "photo.png", ContentID: "photo", Location: "photo.png", Data: mmsPicture()},
			{ContentType: mms.TypeText, Charset: "utf-8", ContentID: "text", Location: "text.txt", Data: []byte(f[2])},
		})
	case len(f) == 3 && f[0] == "vcard" && strings.Contains(f[2], " "):
		name, card, _ := strings.Cut(f[2], " ")
		return r.mmsOffer(f[1], []mms.Part{
			{ContentType: mms.TypeText, Charset: "utf-8", ContentID: "text", Location: "text.txt", Data: []byte(name)},
			{ContentType: ui.SMSTypeXVCard, Name: name, ContentID: "card", Location: name, Data: []byte(strings.ReplaceAll(card, `\n`, "\r\n"))},
		})
	case len(f) >= 2 && f[0] == "sent":
		rest, ms := lastNum(strings.TrimPrefix(arg, "sent "), 8000)
		want, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil || r.mmsc == nil {
			return fmt.Errorf("usage: mms sent <n> [ms], after an mms offer")
		}
		if _, ok := r.waitFor(func(string) bool { return len(r.mmsc.f.Sent()) >= want }, ms); !ok {
			return fmt.Errorf("the MMSC took %d messages, want %d", len(r.mmsc.f.Sent()), want)
		}
	case len(f) >= 2 && f[0] == "album":
		rest, ms := lastNum(strings.TrimPrefix(arg, "album "), 8000)
		text := strings.TrimSpace(rest)
		found := func() bool {
			for _, it := range r.C.media.items {
				if strings.Contains(it.Name, text) {
					return true
				}
			}
			return false
		}
		if _, ok := r.waitFor(func(string) bool { return found() }, ms); !ok {
			return fmt.Errorf("no picture named with %q in the media library", text)
		}
	case len(f) >= 2 && f[0] == "album-drop":
		text := strings.TrimSpace(strings.TrimPrefix(arg, "album-drop "))
		var drop []*mediaItem
		for _, it := range r.C.media.items {
			if strings.Contains(it.Name, text) {
				drop = append(drop, it)
			}
		}
		if len(drop) == 0 {
			return fmt.Errorf("no picture named with %q in the media library", text)
		}
		for _, it := range drop {
			r.C.media.remove(it)
		}
	default:
		return fmt.Errorf("usage: mms offer <from> <text> | mms vcard <from> <name> <vcard> | mms sent <n> [ms] | mms album <text> [ms] | mms album-drop <text> | flashes <n> [ms]")
	}
	return nil
}

func (r *Runner) smsStep(f []string) error {
	usage := fmt.Errorf("usage: sms due | sms later <seconds> <number> <text> | sms drop <number> | sms scheduled <n> [ms]")
	switch {
	case len(f) >= 4 && f[0] == "later":
		secs, err := strconv.Atoi(f[1])
		if err != nil || secs <= 0 {
			return usage
		}
		if r.C.tel == nil || r.C.tel.Backend != "sim" {
			return fmt.Errorf("sms later: only with the simulated baseband (a real one sends it when due)")
		}
		req := svc.SMSSendReq{To: f[2], Text: strings.Join(f[3:], " "), At: time.Now().Add(time.Duration(secs) * time.Second).Unix()}
		var m ui.SMS
		if err := r.C.smsRPC(svc.SMSSend, req, &m); err != nil {
			return err
		}
		if m.State != ui.SMSScheduled {
			return fmt.Errorf("phone-smsd took the message as %q", m.State)
		}
	case len(f) == 2 && f[0] == "drop":
		if err := r.C.smsRPC(svc.SMSDelete, ui.SMSDeleteArgs{Peer: f[1]}, nil); err != nil {
			return err
		}
	case (len(f) == 2 || len(f) == 3) && f[0] == "scheduled":
		want, err := strconv.Atoi(f[1])
		ms := 5000
		if len(f) == 3 && err == nil {
			ms, err = strconv.Atoi(f[2])
		}
		if err != nil {
			return usage
		}
		n := -1
		if _, ok := r.waitFor(func(string) bool { n = r.C.smsScheduled(); return n == want }, ms); !ok {
			if n < 0 {
				return fmt.Errorf("phone-smsd does not say how many messages it keeps scheduled")
			}
			return fmt.Errorf("phone-smsd keeps %d messages scheduled, want %d", n, want)
		}
	default:
		return usage
	}
	r.pump(100 * time.Millisecond)
	return nil
}

// mmsOffer puts a message with these parts (and a presentation) at the
// MMSC and announces it from the operator's gateway.
func (r *Runner) mmsOffer(from string, parts []mms.Part) error {
	m, err := r.mmsStart()
	if err != nil {
		return err
	}
	parts = append([]mms.Part{{ContentType: mms.TypeSMIL, ContentID: "smil", Location: "smil.xml", Data: mms.SMIL(parts)}}, parts...)
	now := time.Now().Unix()
	loc, err := m.f.Offer(m.url, mms.RetrieveConfOf(from, nil, "", now, parts, mms.TypeSMIL))
	if err != nil {
		return err
	}
	push := mms.NotificationPush(fmt.Sprintf("T%d", now), from, "", loc, 20000, 3*24*3600)
	if err := r.C.TelHook(svc.TelSimSMS, map[string]string{"from": mmsGateway, "data": hex.EncodeToString(push)}); err != nil {
		return err
	}
	r.pump(100 * time.Millisecond)
	return nil
}
