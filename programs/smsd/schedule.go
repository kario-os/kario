package smsd

import (
	"errors"
	"log"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Scheduled messages (32-messaging.md §5.1) are outgoing messages kept as
// scheduled with the instant they go (Due, Unix seconds) and the zone the
// user picked it in (Zone, shown only): a change of time zone moves the
// clock on the screen, not the instant. The store keeps them like any
// message, so they survive restarts; one that came due while smsd was not
// running (the phone off) goes as soon as it runs again. Sending one is
// sending it again (send with its id): it becomes the conversation's latest.

// maxAhead: how far ahead a message may be scheduled; dueSlack: how far in
// the past a time picked to the minute may already be.
const (
	maxAhead = 366 * 24 * time.Hour
	dueSlack = time.Minute
)

func (s *service) checkDue(at int64, zone string) error {
	now := s.st.now()
	switch {
	case at < now.Add(-dueSlack).Unix():
		return errors.New("that time has passed")
	case at > now.Add(maxAhead).Unix():
		return errors.New("at most a year ahead")
	case len(zone) > 64:
		return errors.New("not a time zone")
	}
	return nil
}

func (s *service) kickSchedule() {
	select {
	case s.schedKick <- struct{}{}:
	default:
	}
}

// schedLoop sends scheduled messages when due: at the next one's time,
// when kicked, and at least every 30 seconds, so a clock set forward or
// back sends each by the wall clock rather than by a timer started before.
func (s *service) schedLoop() {
	for {
		next := s.runDue()
		wait := 30 * time.Second
		if next > 0 {
			wait = min(wait, max(0, time.Until(time.Unix(next, 0))))
		}
		select {
		case <-s.schedKick:
		case <-time.After(wait):
		}
	}
}

// runDue sends the scheduled messages due now and says when the next one
// is (0: none).
func (s *service) runDue() int64 {
	s.mu.Lock()
	ids, next := s.st.dueScheduled(s.st.now().Unix())
	s.mu.Unlock()
	for _, id := range ids {
		s.sendNow(id)
	}
	return next
}

func (s *service) sendNow(id string) {
	if _, err := s.send(svc.SMSSendReq{ID: id}); err != nil {
		log.Printf("%s: scheduled message not sent: %v", id, err)
	}
}

// sendScheduled sends every scheduled message now (the selftest's hook)
// and says how many.
func (s *service) sendScheduled() int {
	s.mu.Lock()
	var ids []string
	for _, m := range s.st.msgs {
		if m.State == ui.SMSScheduled {
			ids = append(ids, m.ID)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.sendNow(id)
	}
	return len(ids)
}
