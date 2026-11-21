package tts

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// voice is an engine that takes d for any line and notes what it is told.
type voice struct {
	mu    sync.Mutex
	d     time.Duration
	calls []string
}

func (v *voice) Name() string { return "test" }
func (v *voice) Say(text, lang string) (time.Duration, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, lang+":"+text)
	return v.d, nil
}
func (v *voice) Stop() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, "stop")
}
func (v *voice) got() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return strings.Join(v.calls, " | ")
}

func texts(st State) string {
	var out []string
	for _, u := range st.Said {
		out = append(out, u.Text)
	}
	return strings.Join(out, " | ")
}

// A line that waits is said once the one before is done; one that
// interrupts cuts it and drops what waits.
func TestQueueAndInterrupt(t *testing.T) {
	v := &voice{d: 300 * time.Millisecond}
	s := New(v)
	_ = s.Say("Settings", "en", true)
	_ = s.Say("toast", "en", false)
	if got := v.got(); got != "en:Settings" {
		t.Fatalf("the second line did not wait: %s", got)
	}
	if !s.State().Speaking {
		t.Fatal("not speaking")
	}
	deadline := time.Now().Add(2 * time.Second)
	for v.got() != "en:Settings | en:toast" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := v.got(); got != "en:Settings | en:toast" {
		t.Fatalf("the waiting line was not said: %s", got)
	}
	_ = s.Say("Bluetooth", "en", true)
	_ = s.Say("later", "en", false)
	_ = s.Say("Wi-Fi", "en", true)
	if got := v.got(); got != "en:Settings | en:toast | stop | en:Bluetooth | stop | en:Wi-Fi" {
		t.Fatalf("interrupts: %s", got)
	}
	for s.State().Speaking && time.Now().Before(deadline.Add(time.Second)) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := texts(s.State()); got != "Settings | toast | Bluetooth | Wi-Fi" {
		t.Fatalf("said (the line an interrupt dropped is not): %s", got)
	}
	if s.State().Speaking {
		t.Fatal("still speaking")
	}
}

func TestStopEmptyAndLong(t *testing.T) {
	v := &voice{d: time.Hour}
	s := New(v)
	_ = s.Say("  ", "en", true)
	if v.got() != "" {
		t.Fatalf("an empty line was said: %s", v.got())
	}
	_ = s.Say(strings.Repeat("a", MaxRunes+10), "en", true)
	_ = s.Say("next", "en", false)
	s.Stop()
	st := s.State()
	if st.Speaking || len(st.Said) != 1 || len([]rune(st.Said[0].Text)) != MaxRunes || !strings.HasSuffix(st.Said[0].Text, "…") {
		t.Fatalf("%+v", st)
	}
	if !strings.HasSuffix(v.got(), "| stop") {
		t.Fatalf("stop: %s", v.got())
	}
	for i := range kept + 5 {
		_ = s.Say(fmt.Sprint("line ", i), "en", true)
	}
	if st := s.State(); len(st.Said) != kept || st.Said[kept-1].Text != fmt.Sprint("line ", kept+4) {
		t.Fatalf("kept %d, last %q", len(st.Said), st.Said[len(st.Said)-1].Text)
	}
}

func TestSimulated(t *testing.T) {
	var logged []string
	s := New(Simulated{Log: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }})
	_ = s.Say("\u84dd\u7259\uff0c\u5f00\u5173", "zh-Hans", true)
	st := s.State()
	if !st.Simulated || st.Engine != "simulated" || !st.Speaking || len(logged) != 1 || !strings.Contains(logged[0], "zh-Hans: \u84dd\u7259") {
		t.Fatalf("%+v %q", st, logged)
	}
	for _, c := range []struct {
		text string
		want time.Duration
	}{
		{"\u84dd\u7259\uff0c\u5f00\u5173", 1200 * time.Millisecond},
		{"Bluetooth, switch, on", 1600 * time.Millisecond},
		{"", 200 * time.Millisecond},
		{"don't", 400 * time.Millisecond},
	} {
		if got := Duration(c.text); got != c.want {
			t.Errorf("Duration(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
