package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kario-os/kario/hal/usbgadget"
)

// The computer at the other end of the cable, for the selftest. On the
// emulator dummy_hcd puts both ends in one machine: the host side of the
// link is a network interface of its own (cdc_ncm), which goes into a
// network namespace of one locked thread and takes its address from the
// phone's DHCP server, so every request really crosses the USB link. On the
// developer machine sharing listens on the loopback and is reached directly.

// httpDo sends one request and reads its answer; dialPort connects to a
// port of the phone over the link.
type (
	httpDo   func(*http.Request) (*http.Response, error)
	dialPort func(port int) (net.Conn, error)
)

// usbComputer runs f as the computer; it must not run on the shell's loop
// (sharing asks the loop for the library), so the runner calls it from a
// goroutine of its own and keeps the loop going (computer). On the
// emulator the computer is one network namespace, made when the cable
// goes in (attachComputer; else at the first request), which takes its
// address at the first request and keeps it until the cable comes out
// (endComputer), as a computer stays plugged in.
func (c *Core) usbComputer(addr string, f func(do httpDo, dial dialPort, base string) error) error {
	if c.usb.sim {
		if addr == "" {
			return errors.New("nothing is shared")
		}
		client := &http.Client{Timeout: 10 * time.Second}
		host, _, _ := net.SplitHostPort(addr)
		dial := func(port int) (net.Conn, error) {
			return net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 5*time.Second)
		}
		return f(client.Do, dial, "http://"+addr)
	}
	pcMu.Lock()
	if pc == nil {
		p, err := startComputer(c.usb.cfg.Serial)
		if err != nil {
			pcMu.Unlock()
			return err
		}
		pc = p
	}
	p := pc
	pcMu.Unlock()
	gone := errors.New("the cable is out")
	select {
	case p.reqs <- f:
	case <-p.quit:
		return gone
	}
	select {
	case err := <-p.errs:
		return err
	case <-p.quit:
		return gone
	}
}

// The emulator's computer: one locked thread in a network namespace of its
// own, running the requests it is given.
type computer struct {
	reqs chan func(do httpDo, dial dialPort, base string) error
	errs chan error
	quit chan struct{}
}

var (
	pcMu  sync.Mutex
	pc    *computer
	pcGen int // cables pulled
)

// endComputer: the cable is out; the namespace goes with its thread, and
// the host side's interface (gone with the cable) with it.
func endComputer() {
	pcMu.Lock()
	defer pcMu.Unlock()
	pcGen++
	if pc != nil {
		close(pc.quit)
		pc = nil
	}
}

func startComputer(serial string) (*computer, error) {
	_, mac := usbgadget.MACs(serial)
	var ifname string
	for end := time.Now().Add(10 * time.Second); ifname == "" && time.Now().Before(end); {
		if ifname = ifaceByMAC(mac); ifname == "" {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if ifname == "" {
		return nil, errors.New("no host side of the USB link (is the cable in?)")
	}
	p := &computer{reqs: make(chan func(do httpDo, dial dialPort, base string) error), errs: make(chan error, 1), quit: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		// Never unlocked: the thread, and with it the namespace, ends with
		// this goroutine.
		runtime.LockOSThread()
		if err := syscall.Unshare(syscall.CLONE_NEWNET); err != nil {
			ready <- err
			return
		}
		tid := syscall.Gettid()
		moved := make(chan error, 1)
		go func() {
			out, err := exec.Command("ip", "link", "set", "dev", ifname, "netns", strconv.Itoa(tid)).CombinedOutput()
			if err != nil {
				err = fmt.Errorf("%v: %s", err, out)
			}
			moved <- err
		}()
		if err := <-moved; err != nil {
			ready <- fmt.Errorf("moving %s: %w", ifname, err)
			return
		}
		_ = exec.Command("ip", "link", "set", "lo", "up").Run()
		_ = exec.Command("ip", "link", "set", ifname, "up").Run()
		ready <- nil
		leased := false
		do := func(req *http.Request) (*http.Response, error) {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(usbAddr, "80"), 5*time.Second)
			if err != nil {
				return nil, err
			}
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			req.Close = true
			if err := req.Write(conn); err != nil {
				conn.Close()
				return nil, err
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), req)
			if err != nil {
				conn.Close()
				return nil, err
			}
			resp.Body = connBody{resp.Body, conn}
			return resp, nil
		}
		dial := func(port int) (net.Conn, error) {
			return net.DialTimeout("tcp", net.JoinHostPort(usbAddr, strconv.Itoa(port)), 5*time.Second)
		}
		for {
			select {
			case f := <-p.reqs:
				if !leased {
					if err := leaseFromPhone(ifname, serial); err != nil {
						p.errs <- err
						continue
					}
					leased = true
				}
				p.errs <- f(do, dial, "http://"+usbAddr)
			case <-p.quit:
				return
			}
		}
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return p, nil
}

// attachComputer brings the computer's end up in its namespace as soon as
// the cable is in, as a computer's network manager does; the first
// request takes the address. It must be open before the phone's end comes
// up at the trust: usbnet marks an interface open only after starting the
// transfer that brings the device's connection notice, and a notice
// handled in between is lost, the link left without carrier. gen is
// computerGen at the plug: a cable pulled meanwhile leaves it alone.
func attachComputer(serial string, gen int) {
	pcMu.Lock()
	defer pcMu.Unlock()
	if pc != nil || gen != pcGen {
		return
	}
	if p, err := startComputer(serial); err == nil {
		pc = p
	}
}

func computerGen() int {
	pcMu.Lock()
	defer pcMu.Unlock()
	return pcGen
}

// leaseFromPhone takes the computer's address from the phone's DHCP
// server; on the computer's thread, in its namespace.
func leaseFromPhone(ifname, serial string) error {
	out, err := exec.Command("udhcpc", "-i", ifname, "-n", "-q", "-t", "10", "-T", "1").CombinedOutput()
	if err == nil {
		return nil
	}
	here, _ := exec.Command("sh", "-c", "ip addr show "+ifname+"; grep -E 'face|"+ifname+"' /proc/net/dev").CombinedOutput()
	phone := make(chan string, 1)
	go func() { phone <- phoneLinkState(serial) }()
	return fmt.Errorf("no address from the phone (DHCP): %v: %s\nthe computer's side:\n%s\nthe phone's side:\n%s", err, out, here, <-phone)
}

// phoneLinkState is what the phone's side of the link looks like, for a
// computer that got no address; run outside the computer's namespace.
func phoneLinkState(serial string) string {
	ifname := usbgadget.Interface(usbgadget.Default(serial))
	var b strings.Builder
	for _, cmd := range [][]string{
		{"sh", "-c", "ip addr show " + ifname + "; grep -E 'face|" + ifname + "' /proc/net/dev"},
		{"networkctl", "status", "--no-pager", "-n", "20", ifname},
	} {
		out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
		fmt.Fprintf(&b, "$ %s (%v)\n%s", strings.Join(cmd, " "), err, out)
	}
	return b.String()
}

// connBody is a response's body that closes its connection with it.
type connBody struct {
	io.Reader
	conn net.Conn
}

func (b connBody) Close() error { return b.conn.Close() }

func ifaceByMAC(mac string) string {
	dirs, _ := filepath.Glob("/sys/class/net/*/address")
	for _, d := range dirs {
		if b, err := os.ReadFile(d); err == nil && strings.EqualFold(strings.TrimSpace(string(b)), mac) {
			return filepath.Base(filepath.Dir(d))
		}
	}
	return ""
}

// HasUSB: there is a cable to plug (a device controller, or the simulated
// one of the developer machine).
func (c *Core) HasUSB() bool { return c.usb.sim || c.usb.udc != "" }
