package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

// Client is a JSON-RPC style client over a Unix socket; calls may be issued
// concurrently and are matched to responses by id.
type Client struct {
	conn    *Conn
	mu      sync.Mutex
	next    int
	pending map[int]chan reply
	closed  error
}

type reply struct {
	rpc   RPC
	files []*os.File
}

func Dial(path string) (*Client, error) {
	nc, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: New(nc), pending: map[int]chan reply{}}
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	for {
		var r RPC
		if err := c.conn.Recv(&r); err != nil {
			c.mu.Lock()
			c.closed = fmt.Errorf("service connection lost: %w", err)
			for id, ch := range c.pending {
				ch <- reply{rpc: RPC{ID: id, Err: c.closed.Error()}}
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var files []*os.File
		if r.FDs > 0 {
			files = c.conn.TakeFiles(r.FDs)
		}
		c.mu.Lock()
		ch := c.pending[r.ID]
		delete(c.pending, r.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- reply{rpc: r, files: files}
		} else {
			closeAll(files)
		}
	}
}

func (c *Client) Call(method string, params any, out any) error {
	files, err := c.CallFiles(method, params, out)
	closeAll(files)
	return err
}

// ErrTimeout is returned by CallTimeout when the service does not answer.
var ErrTimeout = errors.New("service did not answer in time")

// CallTimeout is Call for callers that must not hang on a stuck service
// (the Shell's key path); a late answer is dropped.
func (c *Client) CallTimeout(method string, params any, out any, d time.Duration) error {
	files, err := c.callFiles(method, params, nil, out, d)
	closeAll(files)
	return err
}

// CallFiles is Call for methods that answer with open files; the caller
// owns them.
func (c *Client) CallFiles(method string, params any, out any) ([]*os.File, error) {
	return c.callFiles(method, params, nil, out, 0)
}

// CallWith is Call handing the service open files (only services started
// with ServeFiles take them); the caller still owns and closes them.
func (c *Client) CallWith(method string, params any, send []*os.File, out any) error {
	files, err := c.callFiles(method, params, send, out, 0)
	closeAll(files)
	return err
}

// CallFilesWith hands the service files and takes the ones it answers with.
func (c *Client) CallFilesWith(method string, params any, send []*os.File, out any) ([]*os.File, error) {
	return c.callFiles(method, params, send, out, 0)
}

func (c *Client) callFiles(method string, params any, send []*os.File, out any, timeout time.Duration) ([]*os.File, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	ch := make(chan reply, 1)
	c.mu.Lock()
	if c.closed != nil {
		c.mu.Unlock()
		return nil, c.closed
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.conn.SendFiles(RPC{ID: id, Method: method, Params: raw, FDs: len(send)}, send...); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	var r reply
	if timeout > 0 {
		select {
		case r = <-ch:
		case <-time.After(timeout):
			c.mu.Lock()
			delete(c.pending, id)
			c.mu.Unlock()
			return nil, ErrTimeout
		}
	} else {
		r = <-ch
	}
	if !r.rpc.OK {
		closeAll(r.files)
		return nil, errors.New(r.rpc.Err)
	}
	if out != nil && len(r.rpc.Result) > 0 {
		if err := json.Unmarshal(r.rpc.Result, out); err != nil {
			closeAll(r.files)
			return nil, err
		}
	}
	return r.files, nil
}

func closeAll(files []*os.File) {
	for _, f := range files {
		f.Close()
	}
}

// Files is a handler result that carries open files to the client; they are
// closed once sent.
type Files struct {
	Result any
	Files  []*os.File
}

func (c *Client) Close() error { return c.conn.Close() }

type Handler func(method string, params json.RawMessage) (any, error)

// FileHandler is a Handler that also gets the files the request carried;
// it owns them.
type FileHandler func(method string, params json.RawMessage, files []*os.File) (any, error)

// listen takes the socket systemd passed (socket activation: the service
// starts when a client first connects), or makes it (mode 0600).
func listen(path string) (net.Listener, error) {
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) && os.Getenv("LISTEN_FDS") != "" && os.Getenv("LISTEN_FDS") != "0" {
		// Not for the processes this one starts.
		os.Unsetenv("LISTEN_PID")
		os.Unsetenv("LISTEN_FDS")
		os.Unsetenv("LISTEN_FDNAMES")
		f := os.NewFile(3, path)
		defer f.Close()
		return net.FileListener(f)
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return ln, os.Chmod(path, 0o600)
}

// Serve listens on a Unix socket (or takes systemd's) and handles every
// request in its own goroutine so slow methods do not block the connection.
func Serve(path string, h Handler) error {
	ln, err := listen(path)
	if err != nil {
		return err
	}
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		go serveConn(New(nc), h)
	}
}

// ServeFiles is Serve for a service whose clients may hand it open files
// (the shell giving mediad a video to decode). Other services refuse them,
// so that a client cannot fill their descriptor table.
func ServeFiles(path string, h FileHandler) error {
	ln, err := listen(path)
	if err != nil {
		return err
	}
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		go serveConnFiles(New(nc), h)
	}
}

// Subscribe is the method a client of a Hub calls to receive its events.
const Subscribe = "subscribe"

// Hub is a Serve that also pushes events: a client that calls "subscribe"
// receives every Broadcast as its own line (no id).
type Hub struct {
	mu   sync.Mutex
	subs map[*Conn]bool
}

func ServeHub(path string, h Handler) (*Hub, error) {
	ln, err := listen(path)
	if err != nil {
		return nil, err
	}
	hub := &Hub{subs: map[*Conn]bool{}}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			c := New(nc)
			go serveConn(c, func(method string, params json.RawMessage) (any, error) {
				if method == Subscribe {
					hub.mu.Lock()
					hub.subs[c] = true
					hub.mu.Unlock()
					return map[string]bool{"ok": true}, nil
				}
				return h(method, params)
			})
		}
	}()
	return hub, nil
}

func (h *Hub) Broadcast(v any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		if c.Send(v) != nil {
			delete(h.subs, c)
		}
	}
}

func serveConn(c *Conn, h Handler) {
	c.RefuseFiles()
	serveConnFiles(c, func(method string, params json.RawMessage, _ []*os.File) (any, error) { return h(method, params) })
}

func serveConnFiles(c *Conn, h FileHandler) {
	defer c.Close()
	for {
		var req RPC
		if err := c.Recv(&req); err != nil {
			return
		}
		var in []*os.File
		if req.FDs > 0 {
			in = c.TakeFiles(req.FDs)
		}
		go func(req RPC) {
			res, err := h(req.Method, req.Params, in)
			resp := RPC{ID: req.ID}
			var files []*os.File
			if fr, ok := res.(Files); ok {
				res, files = fr.Result, fr.Files
				defer closeAll(files)
			}
			if err != nil {
				resp.Err = err.Error()
				files = nil
			} else {
				resp.OK = true
				resp.Result, _ = json.Marshal(res)
				resp.FDs = len(files)
			}
			_ = c.SendFiles(resp, files...)
		}(req)
	}
}
