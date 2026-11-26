package location

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Stream is a receiver that writes NMEA lines to a file: a GNSS device
// (/dev/gnss0: the kernel powers the receiver while it is open), a serial
// port, or a named pipe.
type Stream struct {
	Path string
	// FIFO: the path is a named pipe, made if missing. It is opened for
	// reading and writing, so whoever writes into it may come and go
	// without an end of file.
	FIFO bool
	// Baud sets a serial port's speed; a serial port is put in raw mode
	// either way.
	Baud int
	Now  func() time.Time
}

func (s *Stream) Run(ctx context.Context, fix func(Fix)) error {
	f, err := s.open()
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { f.Close() })
	defer func() {
		if stop() {
			f.Close()
		}
	}()
	d := &Decoder{Now: s.Now}
	err = readLines(f, func(line string) {
		if fx, ok := d.Feed(line); ok {
			fix(fx)
		}
	})
	if ctx.Err() != nil {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return fmt.Errorf("%s: %w", s.Path, err)
}

// readLines hands over each line of r, cutting lines longer than a
// sentence can be (they are refused whole).
func readLines(r io.Reader, line func(string)) error {
	br := bufio.NewReaderSize(r, 4096)
	long := false
	for {
		b, err := br.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			long = true
			continue
		case err != nil:
			return err
		case long:
			long = false
			continue
		}
		if len(b) <= MaxSentence+2 {
			line(string(b))
		}
	}
}

func (s *Stream) open() (*os.File, error) {
	if s.FIFO {
		if err := MakeFIFO(s.Path); err != nil {
			return nil, err
		}
		return os.OpenFile(s.Path, os.O_RDWR, 0)
	}
	f, err := os.OpenFile(s.Path, os.O_RDONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(s.Path, "/dev/tty") {
		if err := rawSerial(f, s.Baud); err != nil {
			f.Close()
			return nil, fmt.Errorf("%s: %w", s.Path, err)
		}
	}
	return f, nil
}

// MakeFIFO makes a named pipe at path unless one is there; anything else
// there is refused, not replaced.
func MakeFIFO(path string) error {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := syscall.Mkfifo(path, 0o600); err != nil && !os.IsExist(err) {
			return fmt.Errorf("%s: %w", path, err)
		}
		st, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeNamedPipe == 0 {
		return fmt.Errorf("%s is not a named pipe", path)
	}
	return nil
}

func termiosSpeed(baud int) uint32 {
	return map[int]uint32{4800: unix.B4800, 9600: unix.B9600, 19200: unix.B19200, 38400: unix.B38400,
		57600: unix.B57600, 115200: unix.B115200, 230400: unix.B230400, 460800: unix.B460800, 921600: unix.B921600}[baud]
}

// rawSerial turns a serial port's line discipline off (no echo back to
// the receiver, no line editing) and sets its speed.
func rawSerial(f *os.File, baud int) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ioErr error
	err = rc.Control(func(fd uintptr) {
		t, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
		if err != nil {
			ioErr = err
			return
		}
		t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
		t.Oflag &^= unix.OPOST
		t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
		t.Cflag &^= unix.CSIZE | unix.PARENB
		t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL
		if sp := termiosSpeed(baud); sp != 0 {
			t.Cflag = t.Cflag&^unix.CBAUD | sp
			t.Ispeed, t.Ospeed = sp, sp
		}
		t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
		ioErr = unix.IoctlSetTermios(int(fd), unix.TCSETS, t)
	})
	if err != nil {
		return err
	}
	return ioErr
}
