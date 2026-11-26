package smsd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/mms"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Multimedia messages (OMA MMS 1.3 over WAP push and HTTP, 32-messaging.md
// §11). A notification arrives in an 8-bit SMS: smsd stores it as a
// message waiting to be downloaded, acknowledges the SMS, then fetches the
// m-retrieve.conf from the content location over the MMS access point
// (apn.go), keeps its parts (the shell moves pictures into the media
// library), tells the MMSC it has it (m-notifyresp.ind) and notifies. A
// download that fails is tried again three times, then left for the user
// (sms.download). Sending writes an m-send.req (pictures made to fit),
// keeps it until the MMSC answers m-send.conf, and matches the delivery
// report (m-delivery.ind, a WAP push again) by the MMSC's message ID.

// Limits: the largest 8-bit SMS user data read (a WAP push of several
// parts), a message fetched, a message sent (what operators take), its
// text.
const (
	maxDataSMS   = 4 << 10
	maxMMSBytes  = 2 << 20
	maxSendBytes = 300 << 10
	maxMMSText   = 5000
	maxPartName  = 64
)

// mmsRetry: the waits before the second, third and fourth try.
var mmsRetry = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}

// receivedData takes an 8-bit message: a WAP push for the MMS user agent,
// or a service indication; anything else is not for the user (no app
// listens on data ports) and goes.
func (s *service) receivedData(m ui.SMS, raw []byte) error {
	push, err := mms.ParsePush(raw)
	if err != nil {
		log.Printf("%s: data message from %s is not a WAP push (%v): dropped", m.ID, m.Peer, err)
		return nil
	}
	switch {
	case push.IsMMS():
		p, err := mms.Decode(push.Body)
		if err != nil {
			log.Printf("%s: unreadable MMS PDU from %s (%v): dropped", m.ID, m.Peer, err)
			return nil
		}
		switch p.Type {
		case mms.NotificationInd:
			return s.mmsNotified(m, p)
		case mms.DeliveryInd:
			return s.mmsDelivered(p)
		}
		log.Printf("%s: MMS PDU %d from %s not handled", m.ID, p.Type, m.Peer)
		return nil
	case strings.EqualFold(push.ContentType, mms.TypeSIC):
		si, err := mms.ParseSI(push.Body)
		if err != nil {
			log.Printf("%s: unreadable service indication from %s (%v): dropped", m.ID, m.Peer, err)
			return nil
		}
		m.Text = strings.TrimSpace(si.Text + "\n" + si.Href)
		return s.received(m)
	}
	log.Printf("%s: WAP push of %s from %s: dropped", m.ID, push.ContentType, m.Peer)
	return nil
}

// mmsNotified stores a notification as a message to download. The SMS
// comes from the operator's MMS gateway; the message is the sender's.
func (s *service) mmsNotified(sms ui.SMS, p *mms.PDU) error {
	if p.ContentLocation == "" {
		log.Printf("%s: MMS notification without a location: dropped", sms.ID)
		return nil
	}
	at := sms.At
	if at == 0 {
		at = s.st.now().Unix()
	}
	m := ui.SMS{ID: sms.ID, Peer: firstOf(p.From, sms.Peer), Subject: limitText(p.Subject, 200), At: at}
	job := mmsJob{Location: p.ContentLocation, TxID: p.TransactionID, Expiry: p.Expiry, Size: p.Size}
	if p.ExpiryAfter > 0 {
		job.Expiry = at + p.ExpiryAfter
	}
	s.mu.Lock()
	stored, err := s.st.receiveMMS(m, job)
	var out ui.SMS
	if stored != nil {
		out = *stored
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if stored != nil {
		s.emit(svc.SMSEvent{Event: svc.SMSEventMessage, SMS: &out})
		s.kickMMS()
	}
	return nil
}

// mmsDelivered records a delivery report of a message smsd sent.
func (s *service) mmsDelivered(p *mms.PDU) error {
	s.mu.Lock()
	id := s.st.bySentID(p.MessageID)
	s.mu.Unlock()
	if id == "" {
		return nil
	}
	switch p.Status {
	case mms.StatusRetrieved, 134:
		return s.settle(id, ui.SMSDelivered)
	case mms.StatusExpired, mms.StatusRejected, mms.StatusUnrecognised, mms.StatusUnreachable:
		return s.settle(id, ui.SMSFailed)
	}
	return nil
}

func firstOf(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func limitText(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (s *service) kickMMS() {
	select {
	case s.mmsKick <- struct{}{}:
	default:
	}
}

// mmsLoop downloads what is due, when kicked and every 30 seconds.
func (s *service) mmsLoop() {
	for {
		select {
		case <-s.mmsKick:
		case <-time.After(30 * time.Second):
		}
		s.mmsRun()
	}
}

func (s *service) mmsRun() {
	s.mu.Lock()
	jobs := s.st.dueJobs(s.st.now().Unix())
	s.mu.Unlock()
	for _, j := range jobs {
		s.download(j)
	}
}

func (s *service) message(id string) (ui.SMS, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.st.byID[id]
	if m == nil {
		return ui.SMS{}, false
	}
	return *m, true
}

// download fetches one message and stores what it holds.
func (s *service) download(j mmsJob) {
	m, ok := s.message(j.ID)
	if !ok {
		return
	}
	if j.Expiry != 0 && s.st.now().Unix() > j.Expiry {
		m.MMS = ui.MMSExpired
		s.mmsStore(m, nil, false)
		return
	}
	apn, err := s.mmsAPN()
	var conf *mms.PDU
	if err == nil {
		conf, err = s.fetch(apn, j.Location)
	}
	if err != nil {
		j.Tries++
		log.Printf("%s: download %d failed: %v", j.ID, j.Tries, err)
		if j.Tries > len(mmsRetry) {
			m.MMS = ui.MMSFailed
			j.Next, j.Force = 0, false
			s.mmsStore(m, &j, true)
			return
		}
		j.Next = s.st.now().Add(mmsRetry[j.Tries-1]).Unix()
		s.mu.Lock()
		err := s.st.commit(change{Op: "job", Job: &j})
		s.mu.Unlock()
		if err != nil {
			log.Print(err)
		}
		return
	}
	u, files := s.content(m, conf)
	s.mu.Lock()
	if u.Folder == "" && s.st.blockedFor(u) != "" {
		u.Folder = ui.SMSFolderBlocked
	}
	s.mu.Unlock()
	for i, data := range files {
		if data == nil {
			continue
		}
		if err := writeSynced(s.st.partPath(m.ID, i), data); err != nil {
			log.Printf("%s: keeping part %d: %v", m.ID, i, err)
			u.Parts[i].Local = false
		}
	}
	s.mmsStore(u, nil, true)
	go s.notifyResp(apn, j.TxID)
}

// mmsStore records a received message's new state and tells the shell.
func (s *service) mmsStore(u ui.SMS, j *mmsJob, notify bool) {
	s.mu.Lock()
	m, err := s.st.mmsUpdate(u, j, notify)
	var out ui.SMS
	if m != nil {
		out = *m
	}
	s.mu.Unlock()
	if err != nil {
		log.Printf("%s: %v", u.ID, err)
		return
	}
	s.emit(svc.SMSEvent{Event: svc.SMSEventMessage, SMS: &out, New: notify && out.Folder == ""})
}

func (s *service) fetch(a svc.MMSAPN, loc string) (*mms.PDU, error) {
	if err := fetchable(loc, a); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, loc, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", mms.TypeMMS+", */*")
	req.Header.Set("User-Agent", userAgent)
	via, release, err := s.mmsLink()
	if err != nil {
		return nil, err
	}
	defer release()
	resp, err := mmsClient(a, via).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the MMSC answered %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMMSBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxMMSBytes {
		return nil, errors.New("the message is too large")
	}
	p, err := mms.Decode(raw)
	if err != nil {
		return nil, err
	}
	if p.Type != mms.RetrieveConf || (p.RetrieveStatus != 0 && p.RetrieveStatus != mms.RetrieveOK) {
		return nil, fmt.Errorf("the MMSC gave message type %d, status %d %s", p.Type, p.RetrieveStatus, p.RetrieveText)
	}
	return p, nil
}

const userAgent = "Kario-MMS/1.0"

// content is what a retrieved message holds: its text parts together, the
// other parts (files: their bytes, by index), the sender and, in a group,
// everyone else but this phone.
func (s *service) content(m ui.SMS, p *mms.PDU) (ui.SMS, [][]byte) {
	m.MMS = ""
	m.Peer = firstOf(p.From, m.Peer)
	m.Subject = limitText(firstOf(p.Subject, m.Subject), 200)
	if p.Date > 0 {
		m.At = p.Date
	}
	var texts []string
	var files [][]byte
	m.Parts = nil
	for _, part := range mms.Order(p) {
		ct := strings.ToLower(part.ContentType)
		if ct == mms.TypeText {
			texts = append(texts, part.Text())
			continue
		}
		name := limitText(strings.Map(func(r rune) rune {
			if r < 0x20 || r == '/' || r == '\\' {
				return '_'
			}
			return r
		}, firstOf(part.Name, part.Location, part.ContentID)), maxPartName)
		m.Parts = append(m.Parts, ui.SMSPart{Type: ct, Name: name, Size: int64(len(part.Data)), Local: true})
		files = append(files, part.Data)
	}
	m.Text = limitText(strings.TrimSpace(strings.Join(texts, "\n")), maxMMSText)
	m.Code = findCode(m.Text)
	m.Members = s.members(m.Peer, append(append([]string(nil), p.To...), p.Cc...))
	return m, files
}

// members are a group's numbers besides this phone's: the sender and the
// other recipients; nil for a message to this phone alone. Without this
// phone's number, a single recipient is this phone.
func (s *service) members(from string, to []string) []string {
	own := s.tel.statusNow().Own
	s.mu.Lock()
	defer s.mu.Unlock()
	ownKey := s.st.key(own)
	if own == "" && len(to) == 1 {
		return nil
	}
	seen := map[string]bool{s.st.key(from): true}
	out := []string{from}
	for _, t := range to {
		k := s.st.key(t)
		if k == "" || seen[k] || (own != "" && k == ownKey) {
			continue
		}
		seen[k] = true
		out = append(out, t)
		if len(out) == ui.MaxGroup {
			break
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// notifyResp tells the MMSC the message arrived (m-notifyresp.ind).
func (s *service) notifyResp(a svc.MMSAPN, txid string) {
	raw, err := mms.Encode(&mms.PDU{Type: mms.NotifyRespInd, TransactionID: txid, Status: mms.StatusRetrieved})
	if err == nil {
		_, err = s.post(a, raw)
	}
	if err != nil {
		log.Printf("telling the MMSC about %s: %v", txid, err)
	}
}

func (s *service) post(a svc.MMSAPN, raw []byte) (*mms.PDU, error) {
	req, err := http.NewRequest(http.MethodPost, a.MMSC, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mms.TypeMMS)
	req.Header.Set("Accept", mms.TypeMMS+", */*")
	req.Header.Set("User-Agent", userAgent)
	via, release, err := s.mmsLink()
	if err != nil {
		return nil, err
	}
	defer release()
	resp, err := mmsClient(a, via).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("the MMSC answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || len(body) == 0 {
		return nil, err
	}
	return mms.Decode(body)
}

// redownload is sms.download: a failed (or blocked) message tried again.
func (s *service) redownload(id string) error {
	s.mu.Lock()
	m := s.st.byID[id]
	j := s.st.jobs[id]
	if m == nil || j == nil || m.Dir != "in" || (m.MMS != ui.MMSFailed && m.MMS != ui.MMSPending) {
		s.mu.Unlock()
		return errors.New("nothing to download")
	}
	job := *j
	job.Tries, job.Next, job.Force = 0, 0, true
	u := *m
	u.MMS = ui.MMSPending
	_, err := s.st.mmsUpdate(u, &job, false)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.kickMMS()
	return nil
}

// sendMMS stores a multimedia message going out and hands it to the MMSC.
func (s *service) sendMMS(to []string, req svc.SMSSendReq) (ui.SMS, error) {
	if len(req.Parts) > ui.MaxMMSMedia {
		return ui.SMS{}, fmt.Errorf("at most %d pictures a message", ui.MaxMMSMedia)
	}
	if utf8.RuneCountInString(req.Text) > maxMMSText {
		return ui.SMS{}, errors.New("the message is too long")
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Parts) == 0 {
		return ui.SMS{}, errors.New("empty message")
	}
	room := maxSendBytes - len(req.Text) - len(req.Subject) - 2<<10
	var atts []mms.Attachment
	var parts []ui.SMSPart
	for _, p := range req.Parts {
		data, mt, err := mms.FitImage(p.Data, room/max(1, len(req.Parts)))
		if err != nil {
			return ui.SMS{}, err
		}
		name := fmt.Sprintf("image%d%s", len(atts)+1, map[string]string{mms.TypeJPEG: ".jpg", mms.TypePNG: ".png", mms.TypeGIF: ".gif"}[mt])
		atts = append(atts, mms.Attachment{ContentType: mt, Name: name, Data: data})
		parts = append(parts, ui.SMSPart{Type: mt, Name: name, Size: int64(len(data)), Media: p.Media})
	}
	s.mu.Lock()
	m, err := s.st.addOutgoingMMS(to, req.Text, limitText(req.Subject, 200), parts, req.At, req.Zone, func(id string) ([]byte, error) {
		raw, err := mms.Encode(mms.NewSendReq(id, to, req.Subject, req.Text, atts))
		if err == nil && len(raw) > maxSendBytes {
			err = errors.New("the message is too large")
		}
		return raw, err
	})
	var out ui.SMS
	if m != nil {
		out = *m
	}
	s.mu.Unlock()
	if err != nil {
		return ui.SMS{}, err
	}
	s.emit(svc.SMSEvent{Event: svc.SMSEventMessage, SMS: &out})
	if out.State == ui.SMSScheduled {
		s.kickSchedule()
		return out, nil
	}
	go s.postMMS(out.ID)
	return out, nil
}

// postMMS hands a stored outgoing message to the MMSC.
func (s *service) postMMS(id string) {
	raw, err := os.ReadFile(s.st.outboxPath(id))
	var a svc.MMSAPN
	if err == nil {
		a, err = s.mmsAPN()
	}
	var conf *mms.PDU
	if err == nil {
		conf, err = s.post(a, raw)
	}
	if err == nil && (conf == nil || conf.Type != mms.SendConf || conf.ResponseStatus != mms.ResponseOK) {
		err = errors.New("the MMSC refused it")
		if conf != nil {
			err = fmt.Errorf("the MMSC refused it (%d %s)", conf.ResponseStatus, conf.ResponseText)
		}
	}
	if err != nil {
		log.Printf("%s: not sent: %v", id, err)
		if serr := s.settle(id, ui.SMSFailed); serr != nil {
			log.Print(serr)
		}
		return
	}
	s.mu.Lock()
	err = s.st.recordSent(conf.MessageID, id)
	s.mu.Unlock()
	if err != nil {
		log.Print(err)
	}
	if err := s.settle(id, ui.SMSSent); err != nil {
		log.Print(err)
	}
	_ = os.Remove(s.st.outboxPath(id))
}

// part is SMSPart: the bytes of a part smsd keeps.
func (s *service) part(a svc.SMSPartArgs) (svc.SMSPartRes, error) {
	s.mu.Lock()
	m := s.st.byID[a.ID]
	var p ui.SMSPart
	ok := m != nil && a.Index >= 0 && a.Index < len(m.Parts) && m.Parts[a.Index].Local
	if ok {
		p = m.Parts[a.Index]
	}
	s.mu.Unlock()
	if !ok {
		return svc.SMSPartRes{}, errors.New("no such part")
	}
	raw, err := os.ReadFile(s.st.partPath(a.ID, a.Index))
	if err != nil {
		return svc.SMSPartRes{}, err
	}
	return svc.SMSPartRes{Type: p.Type, Name: p.Name, Data: raw}, nil
}
