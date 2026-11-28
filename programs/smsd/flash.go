package smsd

import (
	"errors"
	"slices"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Class 0 ("flash") messages (3GPP TS 23.038 §4) are shown at once and not
// stored unless the user keeps one. smsd holds them in memory only and
// hands them to the shell (a flash event, or unnotified once it
// subscribes); telephonyd is told to let one go only once the shell showed
// it, so a flash message that arrives while the shell is away, or that
// smsd loses in a crash, is offered again rather than lost. A blocked
// sender's goes to the blocked folder like any message.

// maxFlash: class 0 messages held at once; the oldest shown one goes first.
const maxFlash = 20

type flashMsg struct {
	m     ui.SMS
	key   string
	shown bool
}

// flashIn holds a class 0 message for the shell; hold: telephonyd keeps
// it until the shell showed it.
func (s *service) flashIn(m ui.SMS, key string) (hold bool, err error) {
	s.mu.Lock()
	if s.st.blockedFor(m) != "" {
		s.mu.Unlock()
		return false, s.received(m)
	}
	if s.st.seenSet[m.ID] || s.st.byID[m.ID] != nil {
		s.mu.Unlock()
		return false, nil
	}
	for _, f := range s.flash {
		if f.m.ID == m.ID {
			f.key = key
			shown := f.shown
			s.mu.Unlock()
			return !shown, nil
		}
	}
	m.Dir, m.Read, m.State, m.Name, m.Folder = "in", false, "", "", ""
	m.Code = findCode(m.Text)
	s.flash = append(s.flash, &flashMsg{m: m, key: key})
	if len(s.flash) > maxFlash {
		i := slices.IndexFunc(s.flash, func(f *flashMsg) bool { return f.shown })
		if i < 0 {
			i = 0
		}
		s.flash = slices.Delete(s.flash, i, i+1)
	}
	s.mu.Unlock()
	s.emit(svc.SMSEvent{Event: svc.SMSEventFlash, SMS: &m})
	return true, nil
}

// flashShown marks the ids the shell showed and gives the telephonyd keys
// to acknowledge; the other ids are left for the store.
func (s *service) flashShown(ids []string) (keys, rest []string) {
	for _, id := range ids {
		i := slices.IndexFunc(s.flash, func(f *flashMsg) bool { return f.m.ID == id })
		if i < 0 {
			rest = append(rest, id)
			continue
		}
		if f := s.flash[i]; !f.shown {
			f.shown = true
			keys = append(keys, f.key)
		}
	}
	return keys, rest
}

// flashSave keeps a flash message: it goes into the inbox, read, marked.
func (s *service) flashSave(id string) (ui.SMS, error) {
	s.mu.Lock()
	i := slices.IndexFunc(s.flash, func(f *flashMsg) bool { return f.m.ID == id })
	if i < 0 {
		s.mu.Unlock()
		return ui.SMS{}, errors.New("no such flash message")
	}
	f := s.flash[i]
	m := f.m
	m.Read, m.Flash = true, true
	if err := s.st.commit(change{Op: "add", Msgs: []*ui.SMS{&m}, Seen: []string{m.ID}}); err != nil {
		s.mu.Unlock()
		return ui.SMS{}, err
	}
	s.flash = slices.Delete(s.flash, i, i+1)
	out := *s.st.byID[m.ID]
	key, shown := f.key, f.shown
	s.mu.Unlock()
	if !shown {
		s.tel.ack(key)
	}
	s.emit(svc.SMSEvent{Event: svc.SMSEventMessage, SMS: &out})
	return out, nil
}

// flashWaiting are the flash messages the shell has not shown.
func (s *service) flashWaiting() []ui.SMS {
	out := []ui.SMS{}
	for _, f := range s.flash {
		if !f.shown {
			out = append(out, f.m)
		}
	}
	return out
}
