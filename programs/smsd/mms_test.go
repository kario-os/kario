package smsd

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/mms"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

func testPNG(t *testing.T) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 40, 30))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type mmsEnv struct {
	s      *service
	mmsc   *mms.FakeMMSC
	srv    *httptest.Server
	mu     sync.Mutex
	events []svc.SMSEvent
}

func (e *mmsEnv) last() svc.SMSEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.events[len(e.events)-1]
}

func newMMSEnv(t *testing.T) *mmsEnv {
	st, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	e := &mmsEnv{mmsc: &mms.FakeMMSC{}}
	e.srv = httptest.NewServer(e.mmsc)
	t.Cleanup(e.srv.Close)
	e.s = newService(st, func(ev svc.SMSEvent) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.events = append(e.events, ev)
	})
	e.s.simMMSC = e.srv.URL
	return e
}

// offer puts a message at the MMSC and gives the WAP push SMS that
// announces it, from the operator's gateway.
func (e *mmsEnv) offer(t *testing.T, id, from string, to []string, text string, pic []byte) telSMS {
	parts := []mms.Part{{ContentType: mms.TypeText, Charset: "utf-8", ContentID: "t", Location: "text.txt", Data: []byte(text)}}
	if pic != nil {
		parts = append([]mms.Part{{ContentType: mms.TypePNG, Name: "pic.png", ContentID: "p", Location: "pic.png", Data: pic}}, parts...)
	}
	parts = append([]mms.Part{{ContentType: mms.TypeSMIL, ContentID: "smil", Location: "smil.xml", Data: mms.SMIL(parts)}}, parts...)
	loc, err := e.mmsc.Offer(e.srv.URL, mms.RetrieveConfOf(from, to, "Trip", 1700000000, parts, mms.TypeSMIL))
	if err != nil {
		t.Fatal(err)
	}
	return telSMS{SMS: ui.SMS{ID: id, Peer: "1065800001", Dir: "in", At: time.Now().Unix()},
		Data: hex.EncodeToString(mms.NotificationPush("tx-"+id, from, "Trip", loc, 4000, 3600))}
}

func TestMMSReceive(t *testing.T) {
	e := newMMSEnv(t)
	s := e.s
	pic := testPNG(t)
	hold, err := s.receivedTel(e.offer(t, "w1", "+86 138 0013 8000", []string{"+8613900139000"}, "Look at this", pic), "k1")
	if hold || err != nil {
		t.Fatalf("hold %v err %v", hold, err)
	}
	m := s.st.byID["w1"]
	if m == nil || m.Kind != ui.SMSKindMMS || m.MMS != ui.MMSPending || m.Peer != "+86 138 0013 8000" || m.Thread != "+8613800138000" || m.Subject != "Trip" {
		t.Fatalf("notification stored as %+v", m)
	}
	if msgs, _ := s.st.unnotified(); len(msgs) != 0 {
		t.Fatal("notified before the download")
	}
	// The MMSC sends the same notification again: one message.
	if _, err := s.receivedTel(e.offer(t, "w1", "+8613800138000", nil, "x", nil), "k1"); err != nil || len(s.st.msgs) != 1 {
		t.Fatalf("the same notification twice: %d messages", len(s.st.msgs))
	}
	s.mmsRun()
	m = s.st.byID["w1"]
	if m.MMS != "" || m.Text != "Look at this" || len(m.Parts) != 1 || m.Parts[0].Type != mms.TypePNG || !m.Parts[0].Local || m.At != 1700000000 || m.Members != nil {
		t.Fatalf("downloaded: %+v", m)
	}
	if msgs, _ := s.st.unnotified(); len(msgs) != 1 || msgs[0].ID != "w1" {
		t.Fatalf("unnotified %+v", msgs)
	}
	if last := e.last(); !last.New || last.SMS.ID != "w1" {
		t.Fatalf("event %+v", last)
	}
	waitFor(t, "the notify response", func() bool { return len(e.mmsc.Acks()) == 1 })
	if a := e.mmsc.Acks()[0]; a.Type != mms.NotifyRespInd || a.TransactionID != "tx-w1" || a.Status != mms.StatusRetrieved {
		t.Fatalf("ack %+v", a)
	}
	res, err := s.part(svc.SMSPartArgs{ID: "w1", Index: 0})
	if err != nil || !bytes.Equal(res.Data, pic) || res.Type != mms.TypePNG {
		t.Fatalf("part: %v", err)
	}
	if st, err := os.Stat(s.st.partPath("w1", 0)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("part file %v", err)
	}
	raw, _ := json.Marshal(svc.SMSPartArgs{ID: "w1", Index: 0, Media: "media:p7"})
	if _, err := s.handle(svc.SMSPartMedia, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.st.partPath("w1", 0)); !os.IsNotExist(err) {
		t.Fatal("the part stays after the library took it")
	}
	s.st = reopen(t, s.st)
	if p := s.st.byID["w1"].Parts[0]; p.Media != "media:p7" || p.Local {
		t.Fatalf("after a restart: %+v", p)
	}
}

// A message to several people is a group conversation of everyone but
// this phone; a blocked sender's notification is not downloaded.
func TestMMSGroupAndBlocked(t *testing.T) {
	e := newMMSEnv(t)
	s := e.s
	s.tel.setStatus(telStatus{Own: "+8613900139000"})
	_, _ = s.receivedTel(e.offer(t, "g1", "13800138000", []string{"+8613900139000", "+8613700137000"}, "Dinner?", nil), "k")
	s.mmsRun()
	m := s.st.byID["g1"]
	if !slices.Equal(m.Members, []string{"13800138000", "+8613700137000"}) || m.Thread != "+8613700137000,+8613800138000" {
		t.Fatalf("group: %+v", m)
	}
	th := s.st.threads("")
	if len(th) != 1 || th[0].Peer != "13800138000, +8613700137000" {
		t.Fatalf("threads %+v", th)
	}
	if got := s.st.list(ui.SMSListArgs{Peer: th[0].Peer}); len(got) != 1 {
		t.Fatal("the group's messages by its peer")
	}
	_ = s.st.block("10690000", true)
	_, _ = s.receivedTel(e.offer(t, "b1", "10690000", nil, "Win", nil), "k2")
	s.mmsRun()
	if b := s.st.byID["b1"]; b.Folder != ui.SMSFolderBlocked || b.MMS != ui.MMSPending {
		t.Fatalf("blocked: %+v", b)
	}
	if err := s.redownload("b1"); err != nil {
		t.Fatal(err)
	}
	s.mmsRun()
	if b := s.st.byID["b1"]; b.MMS != "" || b.Text != "Win" {
		t.Fatalf("downloaded when asked: %+v", b)
	}
}

func TestMMSDownloadFailsAndExpires(t *testing.T) {
	e := newMMSEnv(t)
	s := e.s
	now := time.Unix(1700000200, 0)
	s.st.now = func() time.Time { return now }
	sms := telSMS{SMS: ui.SMS{ID: "f1", Peer: "10658", Dir: "in", At: 1700000100},
		Data: hex.EncodeToString(mms.NotificationPush("tx", "13800138000", "", e.srv.URL+"/mms/none", 10, 86400))}
	_, _ = s.receivedTel(sms, "k")
	for i := 0; i <= len(mmsRetry); i++ {
		s.mmsRun()
		now = now.Add(time.Hour)
	}
	if m := s.st.byID["f1"]; m.MMS != ui.MMSFailed {
		t.Fatalf("after %d tries: %+v %+v", len(mmsRetry)+1, m, s.st.jobs["f1"])
	}
	if msgs, _ := s.st.unnotified(); len(msgs) != 1 {
		t.Fatal("a failed download is notified")
	}
	if err := s.redownload("f1"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	s.mmsRun()
	if m := s.st.byID["f1"]; m.MMS != ui.MMSExpired || s.st.jobs["f1"] != nil {
		t.Fatalf("expired: %+v", m)
	}
	// A location on another host than the MMSC, without a proxy, is not fetched.
	if err := fetchable("http://evil.example/x", svc.MMSAPN{MMSC: e.srv.URL}); err == nil {
		t.Error("a foreign host fetched")
	}
	if err := fetchable("http://evil.example/x", svc.MMSAPN{MMSC: "http://mmsc.monternet.com", Proxy: "10.0.0.172"}); err != nil {
		t.Error("over the operator's proxy any location goes")
	}
	if err := fetchable("file:///etc/passwd", svc.MMSAPN{MMSC: "http://x", Proxy: "10.0.0.172"}); err == nil {
		t.Error("a file location")
	}
}

func TestMMSSend(t *testing.T) {
	e := newMMSEnv(t)
	s := e.s
	got := make(chan string, 4)
	e.mmsc.OnSend = func(_ *mms.PDU, id string) { got <- id }
	pic := testPNG(t)
	out, err := s.send(svc.SMSSendReq{To: "13900000001, 13900000002", Text: "Photos", Parts: []svc.SMSPartIn{{Type: "image/png", Data: pic, Media: "media:p3"}}})
	if err != nil || out.Kind != ui.SMSKindMMS || out.State != ui.SMSSending || len(out.Members) != 2 || out.Parts[0].Media != "media:p3" {
		t.Fatalf("send: %+v %v", out, err)
	}
	msgID := <-got
	waitFor(t, "sent", func() bool { m, _ := s.message(out.ID); return m.State == ui.SMSSent })
	// postMMS removes the PDU after the message is marked sent.
	waitFor(t, "PDU removed after the MMSC took it", func() bool {
		_, err := os.Stat(s.st.outboxPath(out.ID))
		return os.IsNotExist(err)
	})
	req := e.mmsc.Sent()[0]
	if !slices.Equal(req.To, []string{"13900000001", "13900000002"}) || !req.DeliveryReport {
		t.Fatalf("m-send.req %+v", req)
	}
	ordered := mms.Order(req)
	if len(ordered) != 2 || ordered[0].ContentType != mms.TypePNG || !bytes.Equal(ordered[0].Data, pic) || ordered[1].Text() != "Photos" {
		t.Fatalf("parts %+v", ordered)
	}
	if _, err := s.receivedTel(telSMS{SMS: ui.SMS{ID: "d1", Peer: "10658", Dir: "in"}, Data: hex.EncodeToString(mms.DeliveryPush(msgID, "13900000001", mms.StatusRetrieved, 1))}, "k"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.message(out.ID); m.State != ui.SMSDelivered {
		t.Fatalf("delivery report: %+v", m)
	}
	if len(s.st.msgs) != 1 {
		t.Fatal("a delivery report stored as a message")
	}

	e.mmsc.SetFail(true)
	failed, err := s.send(svc.SMSSendReq{To: "13900000001", Text: "x", Subject: "Hi"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed", func() bool { m, _ := s.message(failed.ID); return m.State == ui.SMSFailed })
	e.mmsc.SetFail(false)
	if _, err := s.send(svc.SMSSendReq{ID: failed.ID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "sent again", func() bool { m, _ := s.message(failed.ID); return m.State == ui.SMSSent })
	if _, err := s.send(svc.SMSSendReq{To: "1, 2", Text: ""}); err == nil {
		t.Error("an empty message sent")
	}
}

// A class 0 message is shown, not stored; telephonyd keeps it until the
// shell showed it; the user can keep it.
func TestFlash(t *testing.T) {
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "telephonyd.sock")
	tel := newFakeTel(t, sock)
	defer tel.close()
	zero := 0
	tel.set([]map[string]any{{"event": "sms", "key": "f1", "sms": telSMS{SMS: ui.SMS{ID: "f1", Peer: "10086", Dir: "in", Text: "Balance 3.20", At: 5}, Class: &zero}}}, nil)
	d := startSMSD(t, filepath.Join(tmp, "smsd"), sock)
	defer d.halt()
	ev := d.event(t, svc.SMSEventFlash)
	if ev.SMS.ID != "f1" || ev.SMS.Text != "Balance 3.20" {
		t.Fatalf("flash event %+v", ev)
	}
	raw, _ := d.s.handle(svc.SMSUnnotified, nil)
	if res := raw.(svc.SMSUnnotifiedRes); len(res.Flash) != 1 || len(res.Messages) != 0 {
		t.Fatalf("unnotified %+v", res)
	}
	time.Sleep(100 * time.Millisecond)
	if n, _ := tel.state(); n != 1 {
		t.Fatal("telephonyd let a flash message go before it was shown")
	}
	ids, _ := json.Marshal(map[string][]string{"ids": {"f1"}})
	if _, err := d.s.handle(svc.SMSNotified, ids); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the acknowledgement", func() bool { n, _ := tel.state(); return n == 0 })
	d.s.mu.Lock()
	stored := len(d.s.st.msgs)
	d.s.mu.Unlock()
	if stored != 0 {
		t.Fatal("a flash message stored")
	}
	id, _ := json.Marshal(ui.SMSIDArgs{ID: "f1"})
	if _, err := d.s.handle(svc.SMSFlashSave, id); err != nil {
		t.Fatal(err)
	}
	d.s.mu.Lock()
	m := d.s.st.byID["f1"]
	d.s.mu.Unlock()
	if m == nil || !m.Flash || !m.Read || m.Folder != "" {
		t.Fatalf("kept: %+v", m)
	}
	if _, err := d.s.handle(svc.SMSFlashSave, id); err == nil {
		t.Error("kept twice")
	}
}

func TestBlockListAndUnknown(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := s.setBlock(ui.BlockSetArgs{Number: "170*", On: true, Unknown: &yes}); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.receive(ui.SMS{ID: "a", Peer: "+86 170 1234 5678", Text: "spam"}); m.Folder != ui.SMSFolderBlocked {
		t.Fatal("prefix not blocked")
	}
	if m, _ := s.receive(ui.SMS{ID: "b", Peer: "13800138000", Text: "unknown, contacts not known yet"}); m.Folder != "" {
		t.Fatal("blocked as unknown before the shell gave the contacts")
	}
	s.known = map[string]bool{s.key("13800138000"): true}
	if m, _ := s.receive(ui.SMS{ID: "c", Peer: "+8613800138000", Text: "a contact"}); m.Folder != "" {
		t.Fatal("a contact blocked")
	}
	if m, _ := s.receive(ui.SMS{ID: "d", Peer: "13700137000", Text: "a stranger"}); m.Folder != ui.SMSFolderBlocked {
		t.Fatal("a stranger got through")
	}
	s = reopen(t, s)
	if bl := s.blockList(); !slices.Equal(bl.Numbers, []string{"170*"}) || !bl.Unknown || bl.Hidden {
		t.Fatalf("after a restart: %+v", bl)
	}
}

// A store from before E.164 keys (Chinese numbers without +86) reads them
// again; a region change re-reads every conversation.
func TestRegionRekeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "smsd")
	_ = os.MkdirAll(dir, 0o700)
	old := `{"seq":2,"messages":[{"id":"m1","peer":"13800000001","dir":"in","text":"hi","at":1,"thread":"13800000001"}],"blocked":["13900000001"],"seen":["m1"],"notify":[],"alerts":[]}`
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.byID["m1"].Thread != "+8613800000001" || !slices.Equal(s.blockedList(), []string{"+8613900000001"}) {
		t.Fatalf("%+v %v", s.byID["m1"], s.blockedList())
	}
	if m, _ := s.receive(ui.SMS{ID: "m2", Peer: "612345678", Text: "hola"}); m.Thread != "612345678" {
		t.Fatalf("a Spanish number read in China: %q", m.Thread)
	}
	if err := s.setRegion("es"); err != nil {
		t.Fatal(err)
	}
	if s.byID["m2"].Thread != "+34612345678" {
		t.Fatalf("after the region changed: %q", s.byID["m2"].Thread)
	}
	s = reopen(t, s)
	if s.region != "ES" || s.byID["m2"].Thread != "+34612345678" || s.byID["m1"].Thread != "13800000001" {
		t.Fatalf("after a restart: %q %q %q", s.region, s.byID["m2"].Thread, s.byID["m1"].Thread)
	}
}

func TestAlertArea(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	sv := newService(s, func(svc.SMSEvent) {})
	area := ui.AlertArea{Points: []ui.GeoPoint{{Lat: 39.9, Lon: 116.4}}, RadiusM: 5000}
	if err := sv.broadcast(telCB{ID: "b1", Channel: 4371, Serial: 1, Text: "Flood", At: 1, WAC: hex.EncodeToString(ui.EncodeWAC([]ui.AlertArea{area}, 0))}); err != nil {
		t.Fatal(err)
	}
	if err := sv.broadcast(telCB{ID: "b2", Channel: 4371, Serial: 2, Text: "Bad area", At: 1, WAC: "ff"}); err != nil {
		t.Fatal(err)
	}
	got := s.alertList()
	if len(got) != 2 || len(got[1].Area) != 1 || got[1].Area[0].RadiusM != 5000 || got[0].Area != nil {
		t.Fatalf("%+v", got)
	}
}

func TestServiceIndication(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "smsd"))
	if err != nil {
		t.Fatal(err)
	}
	sv := newService(s, func(svc.SMSEvent) {})
	si := append([]byte{2, 5, 0x6a, 0, 0x45, 0xc6, 0x0c, 0x03}, []byte("example.com/up\x00\x01\x03Update ready\x00\x01\x01")...)
	if _, err := sv.receivedTel(telSMS{SMS: ui.SMS{ID: "s1", Peer: "10086", Dir: "in"}, Data: hex.EncodeToString(mms.EncodePush(1, mms.TypeSIC, si))}, "k"); err != nil {
		t.Fatal(err)
	}
	if m := s.byID["s1"]; m == nil || m.Text != "Update ready\nhttp://example.com/up" {
		t.Fatalf("%+v", m)
	}
	if _, err := sv.receivedTel(telSMS{SMS: ui.SMS{ID: "s2", Peer: "10086", Dir: "in"}, Data: "0001ff"}, "k"); err != nil || s.byID["s2"] != nil {
		t.Fatal("a data message that is not a push is dropped")
	}
}

// The MMS access point: telephonyd's APN database first, answered in the
// form it writes APNs; without the method, the SIM operator's defaults.
func TestMMSAPNFromTelephonyd(t *testing.T) {
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "telephonyd.sock")
	tel := newFakeTel(t, sock)
	defer tel.close()
	st := telStatus{Backend: "modemmanager", Operator: "CHN-UNICOM", SimMCC: "460", SimMNC: "01"}
	tel.setAPN(st, nil)
	d := startSMSD(t, filepath.Join(tmp, "smsd"), sock)
	defer d.halt()
	waitFor(t, "telephonyd's status", func() bool { return d.s.tel.statusNow().SimMNC == "01" })
	if a, err := d.s.mmsAPN(); err != nil || a != (svc.MMSAPN{APN: "3gwap", MMSC: "http://mmsc.myuni.com.cn", Proxy: "10.0.0.172", Port: 80}) {
		t.Fatalf("telephonyd without APNs: %+v %v", a, err)
	}
	tel.setAPN(st, map[string]any{"id": "mms", "name": "Unicom MMS", "apn": "uniwap", "types": []string{"mms"}, "ip_type": "ipv4v6", "auth": "none",
		"user": "", "password_set": false, "mmsc": "http://mmsc.example/mms", "mms_proxy": "10.1.2.3", "mms_port": 8080, "edited": true})
	if a, err := d.s.mmsAPN(); err != nil || a != (svc.MMSAPN{APN: "uniwap", MMSC: "http://mmsc.example/mms", Proxy: "10.1.2.3", Port: 8080}) {
		t.Fatalf("telephonyd's APN: %+v %v", a, err)
	}
	tel.setAPN(st, map[string]any{})
	d.s.tel.setStatus(telStatus{Backend: "modemmanager", SimMCC: "460", SimMNC: "99"})
	if a, err := d.s.mmsAPN(); err == nil {
		t.Fatalf("an unknown operator without APNs: %+v", a)
	}
}

// MMS on an access point of its own: smsd asks telephonyd for the link
// before an exchange and lets it go after, and its connection leaves from
// the link's address (127.0.0.2 here). Shared, or a telephonyd without
// the method: over the phone's routes. The link refused, or never given
// its address by the Shell: the message fails.
func TestMMSOwnAccessPoint(t *testing.T) {
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "telephonyd.sock")
	tel := newFakeTel(t, sock)
	defer tel.close()
	mmsc := &mms.FakeMMSC{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		tel.note("mmsc " + r.Method + " from " + host)
		mmsc.ServeHTTP(w, r)
	}))
	defer srv.Close()
	addrs, wait := ifaceAddrs, mmsLinkWait
	defer func() { ifaceAddrs, mmsLinkWait = addrs, wait }()
	ifaceAddrs = func(name string) ([]net.Addr, error) {
		if name != "rmnet_data1" {
			return nil, errors.New("no such interface")
		}
		return []net.Addr{&net.IPNet{IP: net.ParseIP("127.0.0.2"), Mask: net.CIDRMask(8, 32)}}, nil
	}
	mmsLinkWait = 300 * time.Millisecond
	tel.setAPN(telStatus{Backend: "modemmanager", SimMCC: "460", SimMNC: "00"}, map[string]any{"apn": "cmwap", "mmsc": srv.URL})
	d := startSMSD(t, filepath.Join(tmp, "smsd"), sock)
	defer d.halt()
	waitFor(t, "telephonyd's status", func() bool { return d.s.tel.statusNow().Backend == "modemmanager" })
	link := func(iface string) map[string]any {
		return map[string]any{"shared": false, "iface": iface, "mtu": 1400,
			"ipv4": map[string]any{"method": "static", "address": "127.0.0.2", "prefix": 30, "dns": []string{"127.0.0.53"}}}
	}
	send := func(answer any, state string) []string {
		t.Helper()
		tel.mu.Lock()
		tel.link, tel.calls = answer, nil
		tel.mu.Unlock()
		out, err := d.s.send(svc.SMSSendReq{To: "13900000001", Text: "Look", Parts: []svc.SMSPartIn{{Type: mms.TypePNG, Data: testPNG(t)}}})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, state, func() bool { m, _ := d.s.message(out.ID); return m.State == state })
		tel.mu.Lock()
		defer tel.mu.Unlock()
		return append([]string(nil), tel.calls...)
	}
	for _, c := range []struct {
		answer any
		state  string
		calls  []string
	}{
		{link("rmnet_data1"), ui.SMSSent, []string{svc.TelMMSConnect, "mmsc POST from 127.0.0.2", svc.TelMMSDisconnect}},
		{map[string]any{"shared": true}, ui.SMSSent, []string{svc.TelMMSConnect, "mmsc POST from 127.0.0.1"}},
		{nil, ui.SMSSent, []string{svc.TelMMSConnect, "mmsc POST from 127.0.0.1"}},
		{"mobile data is off", ui.SMSFailed, []string{svc.TelMMSConnect}},
		{link("rmnet_data9"), ui.SMSFailed, []string{svc.TelMMSConnect, svc.TelMMSDisconnect}},
	} {
		if calls := send(c.answer, c.state); !slices.Equal(calls, c.calls) {
			t.Errorf("mms.connect answering %v: %q", c.answer, calls)
		}
	}
}

// With the simulated baseband and no MMSC from the selftest, smsd's own
// takes what is sent (whatever the simulated SIM's APNs say), and the
// simulated network reports it delivered by a WAP push.
func TestSimulatedMMSC(t *testing.T) {
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "telephonyd.sock")
	tel := newFakeTel(t, sock)
	defer tel.close()
	tel.setAPN(telStatus{Backend: "sim", SimMCC: "460", SimMNC: "00"}, map[string]any{"apn": "cmwap", "mmsc": "http://mmsc.monternet.com", "mms_proxy": "10.0.0.172", "mms_port": 80})
	d := startSMSD(t, filepath.Join(tmp, "smsd"), sock)
	defer d.halt()
	waitFor(t, "telephonyd's status", func() bool { return d.s.tel.statusNow().Backend == "sim" })
	out, err := d.s.send(svc.SMSSendReq{To: "13900000001", Text: "Look", Parts: []svc.SMSPartIn{{Type: mms.TypePNG, Data: testPNG(t)}}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the delivery report", func() bool { m, _ := d.s.message(out.ID); return m.State == ui.SMSDelivered })
	tel.mu.Lock()
	n := tel.simSMS
	tel.mu.Unlock()
	d.s.mu.Lock()
	stored, own := len(d.s.st.msgs), d.s.ownMMSC
	d.s.mu.Unlock()
	if n != 1 || !strings.HasPrefix(own, "http://127.0.0.1:") {
		t.Fatalf("%d pushes from the simulated network, MMSC %q", n, own)
	}
	if stored != 1 {
		t.Fatal("the delivery report stored as a message")
	}
}

func TestOperatorAPN(t *testing.T) {
	for _, c := range []struct {
		st   telStatus
		mmsc string
	}{
		{telStatus{SimMCC: "460", SimMNC: "00"}, "http://mmsc.monternet.com"},
		{telStatus{SimMCC: "460", SimMNC: "01"}, "http://mmsc.myuni.com.cn"},
		{telStatus{SimMCC: "460", SimMNC: "11"}, "http://mmsc.vnet.mobi"},
		{telStatus{SimMCC: "460", Operator: "CHN-UNICOM"}, "http://mmsc.myuni.com.cn"},
		{telStatus{Operator: "\u4e2d\u56fd\u79fb\u52a8"}, "http://mmsc.monternet.com"},
		{telStatus{SimMCC: "234", Operator: "China Mobile UK"}, ""},
		{telStatus{SimMCC: "460", SimMNC: "99", Operator: "China Mobile"}, ""},
	} {
		a, ok := operatorAPN(c.st)
		if a.MMSC != c.mmsc || ok != (c.mmsc != "") {
			t.Errorf("%+v: %+v", c.st, a)
		}
	}
	if a, _ := operatorAPN(telStatus{SimMCC: "460", SimMNC: "00"}); a.Proxy != "10.0.0.172" || a.Port != 80 || a.APN != "cmwap" {
		t.Errorf("China Mobile %+v", a)
	}
}
