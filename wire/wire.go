// Package wire implements newline-delimited JSON framing used by phone.Ui and
// the system service control sockets.
//
// On Unix sockets a message may also carry open files (SCM_RIGHTS), e.g. the
// shared-memory ring of an audio stream. The message says how many it
// carries ("fds"); the receiver queues files in arrival order and takes that
// many when it handles the message, so read-ahead cannot mismatch them.
package wire

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
)

const (
	maxLine     = 4 << 20
	maxFDsInMsg = 8
)

type Conn struct {
	rw  io.ReadWriteCloser
	uc  *net.UnixConn
	sc  *bufio.Scanner
	wmu sync.Mutex

	fmu    sync.Mutex
	files  []*os.File
	refuse bool
}

// RefuseFiles makes the connection close every file the peer sends: for
// peers that have no business passing files (apps to the shell), which
// could otherwise fill this process's descriptor table.
func (c *Conn) RefuseFiles() {
	c.fmu.Lock()
	c.refuse = true
	for _, f := range c.files {
		f.Close()
	}
	c.files = nil
	c.fmu.Unlock()
}

// New frames rw. A Unix socket (or an *os.File holding one, like the fd an
// app inherits) can also carry files.
// File duplicates the connection's socket (to hand it to another process).
func (c *Conn) File() (*os.File, error) {
	if c.uc == nil {
		return nil, errors.New("not a Unix socket connection")
	}
	return c.uc.File()
}

func New(rw io.ReadWriteCloser) *Conn {
	c := &Conn{rw: rw}
	if f, ok := rw.(*os.File); ok {
		if nc, err := net.FileConn(f); err == nil {
			if uc, ok := nc.(*net.UnixConn); ok {
				f.Close()
				c.rw, c.uc = uc, uc
			} else {
				nc.Close()
			}
		}
	} else if uc, ok := rw.(*net.UnixConn); ok {
		c.uc = uc
	}
	var r io.Reader = c.rw
	if c.uc != nil {
		r = &fdReader{c: c, oob: make([]byte, syscall.CmsgSpace(4*maxFDsInMsg))}
	}
	c.sc = bufio.NewScanner(r)
	c.sc.Buffer(make([]byte, 64<<10), maxLine)
	return c
}

type fdReader struct {
	c   *Conn
	oob []byte
}

func (r *fdReader) Read(p []byte) (int, error) {
	n, oobn, flags, _, err := r.c.uc.ReadMsgUnix(p, r.oob)
	if oobn > 0 {
		msgs, perr := syscall.ParseSocketControlMessage(r.oob[:oobn])
		for _, m := range msgs {
			fds, ferr := syscall.ParseUnixRights(&m)
			if perr != nil || ferr != nil {
				continue
			}
			r.c.fmu.Lock()
			for _, fd := range fds {
				if r.c.refuse {
					syscall.Close(fd)
					continue
				}
				syscall.CloseOnExec(fd)
				r.c.files = append(r.c.files, os.NewFile(uintptr(fd), "received"))
			}
			r.c.fmu.Unlock()
		}
	}
	if flags&syscall.MSG_CTRUNC != 0 {
		log.Printf("wire: more than %d files in one read, some were dropped", maxFDsInMsg)
	}
	return n, err
}

// Send is safe for concurrent use.
func (c *Conn) Send(v any) error { return c.SendFiles(v) }

// SendFiles sends v with files attached; the caller still owns (and should
// close) its copies.
func (c *Conn) SendFiles(v any, files ...*os.File) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if len(files) == 0 {
		_, err = c.rw.Write(b)
		return err
	}
	if c.uc == nil {
		return errors.New("wire: this connection cannot carry files")
	}
	if len(files) > maxFDsInMsg {
		return errors.New("wire: too many files in one message")
	}
	fds := make([]int, len(files))
	for i, f := range files {
		fds[i] = int(f.Fd())
	}
	n, _, err := c.uc.WriteMsgUnix(b, syscall.UnixRights(fds...), nil)
	if err == nil && n < len(b) {
		_, err = c.uc.Write(b[n:])
	}
	return err
}

// TakeFiles returns the next n received files, in order.
func (c *Conn) TakeFiles(n int) []*os.File {
	c.fmu.Lock()
	defer c.fmu.Unlock()
	n = min(n, len(c.files))
	out := c.files[:n:n]
	c.files = c.files[n:]
	return out
}

// Recv must be called from a single goroutine.
func (c *Conn) Recv(v any) error {
	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			return err
		}
		return io.EOF
	}
	return json.Unmarshal(c.sc.Bytes(), v)
}

func (c *Conn) Close() error {
	c.fmu.Lock()
	for _, f := range c.files {
		f.Close()
	}
	c.files = nil
	c.fmu.Unlock()
	return c.rw.Close()
}

// RPC is a request/response envelope for service control sockets.
type RPC struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	OK     bool            `json:"ok,omitempty"`
	Err    string          `json:"err,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	FDs    int             `json:"fds,omitempty"`
}
