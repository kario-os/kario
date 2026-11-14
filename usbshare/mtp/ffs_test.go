package mtp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// FunctionFS without root: SOCK_SEQPACKET socket pairs stand in for the
// function's files. Each read or write of one is a record, as each of an
// endpoint file is one USB request; a write of nothing is a zero-length
// record (packet); a read of nothing returns at once without taking a
// record, as the stalling read on ep0 returns without data.
func seqPair(t *testing.T) (*os.File, int) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	return os.NewFile(uintptr(fds[0]), "ep"), fds[1]
}

// seqOut is the computer's bulk-out: a transfer goes as packets, one
// record each.
type seqOut struct{ fd, packet int }

func (w seqOut) Write(p []byte) (int, error) {
	if len(p) == 0 {
		_, err := unix.Write(w.fd, nil)
		return 0, err
	}
	for off := 0; off < len(p); off += w.packet {
		if _, err := unix.Write(w.fd, p[off:min(off+w.packet, len(p))]); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

// seqIn is the computer's bulk-in or interrupt endpoint: the device's
// writes arrive as records, and a short or empty one ends a transfer.
type seqIn struct {
	fd, packet int
	rest       []byte
}

func (r *seqIn) Read(p []byte) (int, error) {
	if len(r.rest) == 0 {
		rec := make([]byte, 2*chunk)
		n, err := unix.Read(r.fd, rec)
		if err != nil {
			return 0, err
		}
		if n <= 0 {
			return 0, nil
		}
		r.rest = rec[:n]
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

func ctlEvents(t *testing.T, fd int, evs ...CtlEvent) {
	t.Helper()
	var b []byte
	for _, e := range evs {
		b = append(b, e.encode()...)
	}
	if _, err := unix.Write(fd, b); err != nil {
		t.Fatal(err)
	}
}

// ctlAnswer is the device's next record on ep0, nil when none comes.
func ctlAnswer(t *testing.T, fd int, wait time.Duration) []byte {
	t.Helper()
	pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(pfd, int(wait/time.Millisecond))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return nil
		}
		break
	}
	b := make([]byte, 4096)
	n, err := unix.Read(fd, b)
	if err != nil {
		t.Fatal(err)
	}
	return b[:n]
}

func TestOverFunctionFS(t *testing.T) {
	lib := newLib(t)
	ep0, hostCtl := seqPair(t)
	in, hostIn := seqPair(t)
	out, hostOut := seqPair(t)
	intr, hostIntr := seqPair(t)
	var mu sync.Mutex
	var logs []string
	logged := func(s string) (bool, string) {
		var all string
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			mu.Lock()
			all = strings.Join(logs, "\n")
			mu.Unlock()
			if strings.Contains(all, s) {
				return true, all
			}
		}
		return false, all
	}
	r := NewResponder(Config{Lib: lib, Info: DeviceInfo{Manufacturer: "Kario", Model: "Kario phone"}, Log: func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(f, a...))
		mu.Unlock()
	}})
	done := make(chan error, 1)
	go func() { done <- r.Serve(FFS(ep0, in, out, intr, Packet)) }()
	status := []byte{4, 0, 0x01, 0x20}

	// FunctionFS gives several events in one read of ep0.
	ctlEvents(t, hostCtl, CtlEvent{Kind: EvBind}, CtlEvent{Kind: EvEnable},
		CtlEvent{Kind: EvSetup, Setup: Setup{RequestType: 0xA1, Request: reqGetDeviceStatus, Length: 20}})
	if b := ctlAnswer(t, hostCtl, 5*time.Second); !bytes.Equal(b, status) {
		t.Errorf("device status % x, want % x", b, status)
	}

	i := NewInitiator(&HostTransport{In: &seqIn{fd: hostIn, packet: Packet}, Out: seqOut{fd: hostOut, packet: Packet},
		Intr: &seqIn{fd: hostIntr, packet: Packet}, Packet: Packet})
	if err := i.OpenSession(); err != nil {
		t.Fatal(err)
	}
	if di, err := i.DeviceInfo(); err != nil || !strings.Contains(di.VendorExtension, "microsoft.com: 1.0") || di.Model != "Kario phone" {
		t.Errorf("device info %+v, %v", di, err)
	}
	// Downloads of several chunks and of a container ending on a packet
	// (the device's zero-length packet), and a piece of a file.
	for _, p := range []string{"/Files/big.bin", "/Photos/exact.jpg"} {
		f := mustFind(t, i, p)
		var got bytes.Buffer
		if err := i.Get(f.Handle, &got); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		folder, name, _ := strings.Cut(strings.ToLower(p[1:]), "/")
		if want, _ := lib.get(folder + "/" + name); !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: %d bytes, want %d", p, got.Len(), len(want))
		}
	}
	notes := mustFind(t, i, "/Files/notes.txt")
	if b, err := i.Partial(notes.Handle, 10, 100, true); err != nil || string(b) != "abcdef" {
		t.Errorf("GetPartialObject64: %q, %v", b, err)
	}
	// Uploads ending on a packet (the computer's zero-length packet) and
	// of several chunks; the event comes on the interrupt endpoint.
	files := mustFind(t, i, "/Files")
	for _, n := range []int{Packet - headerLen, 3*chunk + 100} {
		data := bytes.Repeat([]byte{byte(n)}, n)
		name := fmt.Sprintf("ffs-%d.bin", n)
		if _, err := i.Put(files.Storage, rootParent, name, data); err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		if b, _ := lib.get("files/" + name); !bytes.Equal(b, data) {
			t.Errorf("%d bytes stored as %d", n, len(b))
		}
	}
	if ev, err := i.WaitEvent(EventStorageInfoChanged, 5*time.Second); err != nil || len(ev.Params) == 0 || ev.Params[0] != files.Storage {
		t.Errorf("storage event: %+v, %v", ev, err)
	}
	wantCode(t, "delete", i.Delete(notes.Handle), RespObjectWriteProtected)

	// Cancel brings its data on ep0 after the event.
	ctlEvents(t, hostCtl, CtlEvent{Kind: EvSetup, Setup: Setup{RequestType: 0x21, Request: reqCancel, Length: 6}})
	if _, err := unix.Write(hostCtl, binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint16(nil, cancelCode), 77)); err != nil {
		t.Fatal(err)
	}
	if ok, all := logged("cancelled transaction 77"); !ok {
		t.Errorf("Cancel not taken: %s", all)
	}
	// Requests the function does not know are stalled: an OUT one with a
	// write of nothing, an IN one with a read of nothing; ep0 stays in step.
	ctlEvents(t, hostCtl, CtlEvent{Kind: EvSetup, Setup: Setup{RequestType: 0x21, Request: 0x70}})
	if b := ctlAnswer(t, hostCtl, 5*time.Second); b == nil || len(b) != 0 {
		t.Errorf("an unknown OUT request: % x, want a stall", b)
	}
	ctlEvents(t, hostCtl, CtlEvent{Kind: EvSetup, Setup: Setup{RequestType: 0xA1, Request: reqExtendedEvent, Length: 16}},
		CtlEvent{Kind: EvSetup, Setup: Setup{RequestType: 0xA1, Request: reqGetDeviceStatus, Length: 20}})
	if b := ctlAnswer(t, hostCtl, 5*time.Second); !bytes.Equal(b, status) {
		t.Errorf("after the stalls: % x, want % x", b, status)
	}

	// The computer lets go of the function: the session ends with it, and
	// the computer opens another.
	ctlEvents(t, hostCtl, CtlEvent{Kind: EvDisable}, CtlEvent{Kind: EvEnable},
		CtlEvent{Kind: EvSetup, Setup: Setup{RequestType: 0xA1, Request: reqGetDeviceStatus, Length: 20}})
	ctlAnswer(t, hostCtl, 5*time.Second)
	_, err := i.StorageIDs()
	wantCode(t, "after the function was disabled", err, RespSessionNotOpen)
	if err := i.OpenSession(); err != nil {
		t.Fatal(err)
	}
	if ids, err := i.StorageIDs(); err != nil || len(ids) != 4 {
		t.Errorf("storages in the new session: %v, %v", ids, err)
	}

	// A FunctionFS file never ends: every descriptor gets /dev/null in its
	// place, which wakes the reads blocked on the sockets (their peers are
	// gone) and fails the next ones, so the loops on them return.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	host := []int{hostCtl, hostIn, hostOut, hostIntr}
	for _, fd := range append([]int{int(ep0.Fd()), int(in.Fd()), int(out.Fd()), int(intr.Fd())}, host...) {
		if err := unix.Dup2(int(null.Fd()), fd); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		if !errors.Is(err, unix.EBADF) {
			t.Errorf("Serve ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the responder outlived its endpoints")
	}
	for {
		if _, err := i.Event(5 * time.Second); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal("the computer's event reader outlived its endpoint")
		}
	}
	// The device's files stay open: the control goroutine, which nothing
	// waits for, may still be on its last read of ep0.
	for _, fd := range host {
		unix.Close(fd)
	}
}
