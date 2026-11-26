package smsd

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
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
// Where the operator keeps MMS on an access point of its own (a second
// packet data context: cmwap beside cmnet), telephonyd brings it up for
// each exchange (svc.TelMMSConnect) and the Shell routes what leaves from
// its address over it: smsd's connections, and its DNS queries to the
// link's servers, go out from that address. Otherwise smsd reaches the
// proxy over whatever route the phone has.

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

// mmsVia is the MMS access point's own link for an exchange: its IPv4
// address and DNS servers.
type mmsVia struct {
	addr net.IP
	dns  []string
}

// mmsLinkWait: how long smsd waits for the Shell to give the link its
// address once telephonyd has it up.
var mmsLinkWait = 10 * time.Second

// ifaceAddrs is net's, a test's stand-in otherwise.
var ifaceAddrs = func(name string) ([]net.Addr, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	return ifi.Addrs()
}

// mmsLink brings up the MMS access point's link for an exchange; release
// lets it go once no exchange uses it. A nil link: MMS goes over the
// phone's routes (the data link carries it, the simulated baseband, a
// telephonyd without the method). The count goes up before the request
// and the last release asks telephonyd under the lock, so a release never
// takes the link from an exchange that has just asked for it.
func (s *service) mmsLink() (*mmsVia, func(), error) {
	none := func() {}
	s.mu.Lock()
	sim := s.simMMSC
	s.mu.Unlock()
	if sim != "" || s.tel.statusNow().Backend == "sim" {
		return nil, none, nil
	}
	s.linkMu.Lock()
	s.linkUsers++
	s.linkMu.Unlock()
	release := sync.OnceFunc(func() {
		s.linkMu.Lock()
		defer s.linkMu.Unlock()
		if s.linkUsers--; s.linkUsers > 0 || !s.linkHeld {
			return
		}
		s.linkHeld = false
		if err := s.tel.call(svc.TelMMSDisconnect, nil, nil); err != nil {
			log.Printf("letting the MMS access point go: %v", err)
		}
	})
	var l svc.MMSLink
	err := s.tel.callTimeout(svc.TelMMSConnect, nil, &l, mmsWhole)
	switch {
	case errors.Is(err, errNoTel) || (err != nil && strings.Contains(err.Error(), "unknown method")):
		release()
		return nil, none, nil
	case err != nil:
		release()
		return nil, none, fmt.Errorf("the MMS access point: %w", err)
	case l.Shared:
		release()
		return nil, none, nil
	}
	s.linkMu.Lock()
	s.linkHeld = true
	s.linkMu.Unlock()
	via, err := linkUp(l)
	if err != nil {
		release()
		return nil, none, err
	}
	return via, release, nil
}

// linkUp waits for the link's IPv4 address on its interface.
func linkUp(l svc.MMSLink) (*mmsVia, error) {
	var addr net.IP
	if l.IPv4 != nil {
		addr = net.ParseIP(l.IPv4.Address).To4()
	}
	if addr == nil || l.Iface == "" {
		return nil, errors.New("the MMS access point has no IPv4 address")
	}
	via := &mmsVia{addr: addr}
	for _, d := range l.IPv4.DNS {
		if ip := net.ParseIP(d); ip != nil && ip.To4() != nil {
			via.dns = append(via.dns, ip.String())
		}
	}
	for end := time.Now().Add(mmsLinkWait); ; time.Sleep(100 * time.Millisecond) {
		addrs, _ := ifaceAddrs(l.Iface)
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(addr) {
				return via, nil
			}
		}
		if time.Now().After(end) {
			return nil, fmt.Errorf("%s has no address %s", l.Iface, addr)
		}
	}
}

// mmsClient talks to the MMSC through the access point's proxy; it follows
// no redirects (a message is fetched where the notification said). Over
// the access point's own link, connections and DNS queries leave from its
// address.
func mmsClient(a svc.MMSAPN, via *mmsVia) *http.Client {
	d := &net.Dialer{Timeout: mmsDial}
	if via != nil {
		d.LocalAddr = &net.TCPAddr{IP: via.addr}
		if len(via.dns) > 0 {
			d.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				rd := net.Dialer{Timeout: mmsDial, LocalAddr: &net.UDPAddr{IP: via.addr}}
				if strings.HasPrefix(network, "tcp") {
					rd.LocalAddr = &net.TCPAddr{IP: via.addr}
				}
				return rd.DialContext(ctx, network, net.JoinHostPort(via.dns[0], "53"))
			}}
		}
	}
	tr := &http.Transport{DialContext: d.DialContext, ResponseHeaderTimeout: mmsAnswer, DisableKeepAlives: true}
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
