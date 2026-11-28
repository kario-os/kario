package main

import "context"

// autoFetch is what the background updates download under: the system's
// job.stop for them (the phone left unmetered Wi-Fi) cancels it, and the
// job's next run, which the system starts once the network is back,
// starts afresh. What the user downloads does not go through it.
type autoFetch struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (f *autoFetch) start() {
	f.stop()
	f.ctx, f.cancel = context.WithCancel(context.Background())
}

func (f *autoFetch) stop() {
	if f.cancel != nil {
		f.cancel()
	}
}

// context is the one a download runs under: the background updates', or
// none to cancel it for the user's own.
func (f *autoFetch) context(auto bool) context.Context {
	if !auto || f.ctx == nil {
		return context.Background()
	}
	return f.ctx
}
