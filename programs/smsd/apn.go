package smsd

import (
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/mms"
	"github.com/kario-os/kario/svc"
)

// Where multimedia messages go: the MMSC and its proxy on the SIM's MMS
// access point. telephonyd's APN database answers svc.TelAPNMMS; when it
// has no entry, smsd uses the settings the three
// Chinese operators publish, picked by the SIM's MCC and MNC, or by the
// operator's name when telephonyd does not give the MNC. The simulated
// baseband's MMSC is the selftest's (svc.SMSMMSSim), else one of smsd's
// own (simMMSC) whatever the simulated SIM's APNs say.
//
// Bringing up the MMS access point itself (a second packet data context
// where the operator keeps MMS apart from the internet) is telephonyd's:
// smsd reaches the proxy over whatever route the phone has.

// operatorAPNs: China Mobile, China Unicom, China Telecom.
var operatorAPNs = []struct {
	mncs  []string
	names []string
	apn   svc.MMSAPN
}{
	{[]string{"00", "02", "04", "07", "08", "13"}, []string{"china mobile", "cmcc", "chn-cmcc", "\u4e2d\u56fd\u79fb\u52a8"},
		svc.MMSAPN{APN: "cmwap", MMSC: "http://mmsc.monternet.com", Proxy: "10.0.0.172", Port: 80}},
	{[]string{"01", "06", "09", "10"}, []string{"china unicom", "unicom", "cucc", "chn-unicom", "\u4e2d\u56fd\u8054\u901a"},
		svc.MMSAPN{APN: "3gwap", MMSC: "http://mmsc.myuni.com.cn", Proxy: "10.0.0.172", Port: 80}},
	{[]string{"03", "05", "11", "12"}, []string{"china telecom", "telecom", "chn-ct", "ctcc", "\u4e2d\u56fd\u7535\u4fe1"},
		svc.MMSAPN{APN: "ctwap", MMSC: "http://mmsc.vnet.mobi", Proxy: "10.0.0.200", Port: 80}},
}

// operatorAPN is the MMS access point of the SIM's operator, when it is
// one of operatorAPNs.
func operatorAPN(st telStatus) (svc.MMSAPN, bool) {
	if st.SimMCC != "" && st.SimMCC != "460" {
		return svc.MMSAPN{}, false
	}
	mnc := strings.TrimLeft(st.SimMNC, "0")
	name := strings.ToLower(st.Operator)
	for _, o := range operatorAPNs {
		for _, m := range o.mncs {
			if st.SimMCC == "460" && st.SimMNC != "" && strings.TrimLeft(m, "0") == mnc {
				return o.apn, true
			}
		}
		if st.SimMNC == "" {
			for _, n := range o.names {
				if strings.Contains(name, n) {
					return o.apn, true
				}
			}
		}
	}
	return svc.MMSAPN{}, false
}

// mmsAPN is the access point to use now.
func (s *service) mmsAPN() (svc.MMSAPN, error) {
	s.mu.Lock()
	sim := s.simMMSC
	s.mu.Unlock()
	if sim != "" {
		return svc.MMSAPN{MMSC: sim}, nil
	}
	if s.tel.statusNow().Backend == "sim" {
		u, err := s.simulatedMMSC()
		return svc.MMSAPN{MMSC: u}, err
	}
	var a svc.MMSAPN
	if err := s.tel.call(svc.TelAPNMMS, nil, &a); err == nil && a.MMSC != "" {
		return a, nil
	}
	if a, ok := operatorAPN(s.tel.statusNow()); ok {
		return a, nil
	}
	return svc.MMSAPN{}, errors.New("no MMS settings for this SIM")
}

// The simulated network's MMS gateway (the number its WAP pushes come
// from) and how long after sending it reports a message delivered, as it
// does for SMS.
const (
	simGateway   = "1065800000"
	simDelivered = 500 * time.Millisecond
)

// simulatedMMSC is the simulated baseband's MMSC when the selftest gave
// none: a mms.FakeMMSC on a loopback port of smsd's own, which takes what
// is sent and reports each recipient's copy delivered by a WAP push
// through telephonyd's simulated network (svc.TelSimSMS).
func (s *service) simulatedMMSC() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ownMMSC != "" {
		return s.ownMMSC, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	f := &mms.FakeMMSC{OnSend: func(req *mms.PDU, id string) {
		time.Sleep(simDelivered)
		for _, to := range append(append([]string(nil), req.To...), req.Cc...) {
			push := mms.DeliveryPush(id, to, mms.StatusRetrieved, time.Now().Unix())
			if err := s.tel.call(svc.TelSimSMS, map[string]string{"from": simGateway, "data": hex.EncodeToString(push)}, nil); err != nil {
				log.Printf("simulated MMSC: delivery report of %s: %v", id, err)
				return
			}
		}
	}}
	go func() { _ = http.Serve(ln, f) }()
	s.ownMMSC = "http://" + ln.Addr().String()
	log.Printf("simulated baseband: MMSC on %s", s.ownMMSC)
	return s.ownMMSC, nil
}

// Timeouts of an MMS exchange: connecting, the first answer, the whole.
const (
	mmsDial   = 20 * time.Second
	mmsAnswer = 60 * time.Second
	mmsWhole  = 2 * time.Minute
)

// mmsClient talks to the MMSC through the access point's proxy; it follows
// no redirects (a message is fetched where the notification said).
func mmsClient(a svc.MMSAPN) *http.Client {
	tr := &http.Transport{DialContext: (&net.Dialer{Timeout: mmsDial}).DialContext, ResponseHeaderTimeout: mmsAnswer, DisableKeepAlives: true}
	if a.Proxy != "" {
		port := a.Port
		if port == 0 {
			port = 80
		}
		tr.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: net.JoinHostPort(a.Proxy, strconv.Itoa(port))})
	}
	return &http.Client{Transport: tr, Timeout: mmsWhole, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// fetchable: a content location smsd fetches with this access point. Over
// a proxy, the operator's network decides what it reaches; without one, a
// notification (anyone can send an SMS) must not make the phone fetch
// from wherever it likes: only the MMSC's own host.
func fetchable(loc string, a svc.MMSAPN) error {
	u, err := url.Parse(loc)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("not an http location")
	}
	if a.Proxy != "" {
		return nil
	}
	m, err := url.Parse(a.MMSC)
	if err != nil || !strings.EqualFold(m.Hostname(), u.Hostname()) {
		return errors.New("the location is not the MMSC's")
	}
	return nil
}
