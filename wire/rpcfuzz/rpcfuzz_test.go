package rpcfuzz

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeT struct {
	mu     sync.Mutex
	failed []string
}

func (*fakeT) Helper() {}

func (f *fakeT) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, fmt.Sprintf(format, args...))
}

func (f *fakeT) Fatalf(format string, args ...any) {
	f.Errorf(format, args...)
	runtime.Goexit()
}

var sink []byte

// Run passes a service that answers everything at once, or once Cancel
// ends what it waits on, and fails one that does not, answers an error
// without a message or a result it cannot send, allocates far more than
// the requests ask, or leaves a goroutine running.
func TestRun(t *testing.T) {
	answerWait = 300 * time.Millisecond
	defer func() { answerWait = Answer }()
	methods := []string{"ok", "wait", "stuck", "empty", "nan", "big", "leak"}
	for _, c := range []struct{ input, fail string }{
		{"\x00\n\x01\n{\"id\":1,\"method\":\"ok\"}\n{not a request\n\n\x00[1,2\n", ""},
		{"\x02\n", "unanswered"},
		{"\x03\n", "without a message"},
		{"\x04\n", "cannot be sent"},
		{"\x05\n", "allocated"},
		{"\x06\n", "goroutines left behind"},
	} {
		var cancel sync.Once
		waiting, stuck, leak := make(chan struct{}), make(chan struct{}), make(chan struct{})
		h := func(method string, _ json.RawMessage) (any, error) {
			switch method {
			case "ok":
				return map[string]bool{"ok": true}, nil
			case "wait":
				<-waiting
				return nil, errors.New("cancelled")
			case "stuck":
				<-stuck
				return 1, nil
			case "empty":
				return nil, errors.New("")
			case "nan":
				return math.NaN(), nil
			case "big":
				sink = make([]byte, 64<<20)
				return len(sink), nil
			case "leak":
				go func() { <-leak }()
				return 1, nil
			}
			return nil, fmt.Errorf("unknown method %q", method)
		}
		f := &fakeT{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			Run(f, Service{Methods: methods, Handler: h, Cancel: func() { cancel.Do(func() { close(waiting) }) }}, []byte(c.input))
		}()
		<-done
		close(stuck)
		close(leak)
		sink = nil
		got := strings.Join(f.failed, "; ")
		if (c.fail == "") != (got == "") || !strings.Contains(got, c.fail) {
			t.Errorf("%q: failures %q, want %q", c.input, got, c.fail)
		}
	}
}
