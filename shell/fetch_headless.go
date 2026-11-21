package shell

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// fetchSink is the selftest's server for an app's own fetch: on the
// device's loopback, which an app with the "net" permission reaches, it
// answers every request "ok" and keeps "METHOD PATH BODY" of each.
type fetchSink struct {
	mu  sync.Mutex
	got []string
}

func (s *fetchSink) seen(prefix, text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.got {
		if strings.HasPrefix(g, prefix) && strings.Contains(g, text) {
			return true
		}
	}
	return false
}

// fetchServe is the step fetch-server <host:port>; a second one on the same
// run keeps the first server.
func (r *Runner) fetchServe(arg string) error {
	if r.fetch != nil {
		return nil
	}
	ln, err := net.Listen("tcp", strings.TrimSpace(arg))
	if err != nil {
		return err
	}
	sink := &fetchSink{}
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		sink.mu.Lock()
		sink.got = append(sink.got, req.Method+" "+req.URL.Path+" "+string(body))
		sink.mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	})}
	go func() { _ = srv.Serve(ln) }()
	r.fetch = sink
	return nil
}

// fetchGot is the step fetch-got <METHOD> <path> <text> [ms]: the server got
// such a request with text in its body.
func (r *Runner) fetchGot(arg string) error {
	if r.fetch == nil {
		return errors.New("fetch-got before fetch-server")
	}
	rest, ms := lastNum(arg, 8000)
	f := strings.Fields(rest)
	if len(f) < 3 {
		return errors.New("usage: fetch-got <METHOD> <path> <text> [ms]")
	}
	prefix, text := f[0]+" "+f[1]+" ", strings.Join(f[2:], " ")
	if _, ok := r.waitFor(func(string) bool { return r.fetch.seen(prefix, text) }, ms); !ok {
		r.fetch.mu.Lock()
		defer r.fetch.mu.Unlock()
		return fmt.Errorf("no %s%s… on the server; it got %q", prefix, text, r.fetch.got)
	}
	return nil
}
