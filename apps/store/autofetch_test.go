package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A background download in flight ends when the system stops the updates
// job; the user's own goes on, and the job's next run downloads again.
func TestAutoFetchStops(t *testing.T) {
	started := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
			_, _ = w.Write([]byte("package"))
		}
	}))
	defer srv.Close()
	get := func(ctx context.Context) <-chan error {
		done := make(chan error, 1)
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				res.Body.Close()
			}
			done <- err
		}()
		<-started
		return done
	}
	var f autoFetch
	if f.context(true) != context.Background() {
		t.Fatal("a background download before the job ran")
	}
	f.start()
	auto, user := get(f.context(true)), get(f.context(false))
	f.stop()
	if err := <-auto; !errors.Is(err, context.Canceled) {
		t.Fatalf("the background download: %v", err)
	}
	if err := <-user; err != nil {
		t.Fatalf("the user's download: %v", err)
	}
	f.start()
	if err := f.context(true).Err(); err != nil {
		t.Fatalf("the next run: %v", err)
	}
}
