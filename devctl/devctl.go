// Package devctl is the developer link to a phone in developer mode
// (29-app-development.md §2, 27-usb-typec.md §4): kario device on the
// computer runs phone-dev on the phone through SSH (the development image's
// dropbear) over the trusted USB cable, and phone-dev hands the request to
// the Shell through a socket in the Shell's run directory. The Shell
// refuses every request unless developer mode is on and the computer on
// the cable is trusted, and installs, reloads and uninstalls through its
// own installer, as the store app does.
//
// On the socket a request is one JSON line (Request); the Shell answers one
// JSON line (Reply) and, for logs and syslog, the log's text after it until
// either side closes; for debug, the bytes of the app's debugger socket
// both ways after it (kario-jsrt's debugger, jsrt/src/debug.c) until either
// side closes; for forward, nothing until the gate closes or the computer
// goes. An install's, a push's or an update's Size bytes go only after a
// first reply that lets them in: a refusal comes before the package, never
// while it is being written. Replies with More set are progress lines with
// another reply after them; a reply with Size is followed by exactly that
// many bytes (pull, screenshot, bugreport).
package devctl

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// Program is the phone's side, on the PATH of an SSH session.
	Program = "phone-dev"
	// Sock is the Shell's socket in its run directory.
	Sock = "dev.sock"
	// RunDirEnv names another run directory (a Shell under the selftest).
	RunDirEnv = "PHONE_RUN_DIR"
	// DefaultRunDir is the device Shell's.
	DefaultRunDir = "/run/phone"
	// USBAddr is the phone's address on the cable once the computer is
	// trusted; SSH reaches it there and nowhere else.
	USBAddr = "10.71.0.1"
	// SSHEnv is what dropbear tells a session about its connection:
	// "client-address client-port server-address server-port".
	SSHEnv = "SSH_CONNECTION"
	// MaxRequest bounds a request's line.
	MaxRequest = 4096
)

// The operations.
const (
	OpInstall   = "install"
	OpTrust     = "trust"
	OpLogs      = "logs"
	OpReload    = "reload"
	OpRestart   = "restart"
	OpUninstall = "uninstall"
	// OpDebug restarts an app installed in developer mode under the
	// debugger and joins its debugger socket to the connection; with
	// Attach, a running app takes the debugger without a restart.
	OpDebug = "debug"
	// OpPush writes Size bytes to Path; OpPull reads Path.
	OpPush = "push"
	OpPull = "pull"
	// OpScreenshot is a PNG of the screen, secure screens masked.
	OpScreenshot = "screenshot"
	// OpKey presses Keys, OpType types Text: input marked as injected,
	// which system trust dialogs refuse.
	OpKey  = "key"
	OpType = "type"
	// OpSyslog is the system's log: its services' and the Shell's.
	OpSyslog = "syslog"
	// OpForward holds the computer's port forwarding over SSH (ssh -L, or
	// -R with Reverse) to Port on the phone's loopback while the gate is
	// open.
	OpForward = "forward"
	// OpReboot restarts the phone, into Target when there is one.
	OpReboot = "reboot"
	// OpUpdate installs the Size bytes of a signed system update bundle
	// through the Shell's update path.
	OpUpdate = "update"
	// OpBugreport is a zip of what a bug report needs, private data left out.
	OpBugreport = "bugreport"
)

// Ops lists them, for usage lines.
var Ops = []string{OpInstall, OpTrust, OpLogs, OpReload, OpRestart, OpUninstall, OpDebug,
	OpPush, OpPull, OpScreenshot, OpKey, OpType, OpSyslog, OpForward, OpReboot, OpUpdate, OpBugreport}

// Reboot targets: the reason the kernel hands the bootloader
// (LINUX_REBOOT_CMD_RESTART2), which reads it at the next start.
const (
	RebootBootloader = "bootloader"
	RebootRecovery   = "recovery"
)

// Limits on what a request names.
const (
	MaxKeys = 64
	MaxPath = 1024
	// MinReversePort: a reverse forward listens on the phone, where ports
	// below it are the system's.
	MinReversePort = 1024
)

type Request struct {
	Op  string `json:"op"`
	App string `json:"app,omitempty"`
	// Key is the development store's root for trust (base64 Ed25519).
	Key string `json:"key,omitempty"`
	// Size is the length of the package, file or bundle that follows an
	// install, a push or an update.
	Size int64 `json:"size,omitempty"`
	// Follow keeps a logs or syslog request open for the lines still to come.
	Follow bool `json:"follow,omitempty"`
	// Via is the SSH session's connection (SSHEnv) phone-dev runs in.
	Via string `json:"via,omitempty"`
	// Path is push's and pull's file on the phone, Mode push's permission
	// bits.
	Path string `json:"path,omitempty"`
	Mode uint32 `json:"mode,omitempty"`
	// Keys are key's names (as the selftest's key step has them), Text
	// what type types.
	Keys []string `json:"keys,omitempty"`
	Text string   `json:"text,omitempty"`
	// Unit narrows syslog to one service ("shell": the Shell's own log);
	// Lines is how many lines it starts with (0: this boot's).
	Unit  string `json:"unit,omitempty"`
	Lines int    `json:"lines,omitempty"`
	// Port is forward's port on the phone's loopback; Reverse: the phone
	// listens there (ssh -R) rather than the computer (ssh -L).
	Port    int  `json:"port,omitempty"`
	Reverse bool `json:"reverse,omitempty"`
	// Target is reboot's: "", RebootBootloader or RebootRecovery.
	Target string `json:"target,omitempty"`
	// Attach is debug's: a debugger for the app as it runs, without a
	// restart when it can take one.
	Attach bool `json:"attach,omitempty"`
}

type Reply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Text  string `json:"text,omitempty"`
	// More: Text is a line of progress, and another reply follows.
	More bool `json:"more,omitempty"`
	// Size bytes follow the reply (a file, a picture, an archive); Note is
	// said beside them, where Text would mix into them.
	Size int64  `json:"size,omitempty"`
	Note string `json:"note,omitempty"`
}

var (
	appID = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	unit  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]{0,63}$`)
)

// ValidApp: an app id as packages have them, safe in a remote command line.
func ValidApp(id string) bool { return len(id) <= 128 && appID.MatchString(id) }

// ValidPath: a file name on the phone as push and pull take it; what it
// may name is the Shell's to decide.
func ValidPath(p string) bool {
	if p == "" || len(p) > MaxPath || !utf8.ValidString(p) {
		return false
	}
	return !strings.ContainsFunc(p, unicode.IsControl)
}

// ValidUnit: a systemd unit's name, or the Shell's ("shell").
func ValidUnit(u string) bool { return unit.MatchString(u) }

// Binary: the op's reply is followed by bytes, not text.
func Binary(op string) bool { return op == OpPull || op == OpScreenshot || op == OpBugreport }

// withBody: the op's request is followed by Size bytes.
func withBody(op string) bool { return op == OpInstall || op == OpPush || op == OpUpdate }

// Check is what a request must carry for its operation.
func (r Request) Check() error {
	switch r.Op {
	case OpInstall:
		if r.Size <= 0 {
			return errors.New("install: no package")
		}
	case OpTrust:
		if r.Key == "" {
			return errors.New("trust: no key")
		}
	case OpLogs, OpReload, OpRestart, OpUninstall, OpDebug:
		if !ValidApp(r.App) {
			return fmt.Errorf("%s: %q is not an app id", r.Op, r.App)
		}
	case OpPush, OpPull:
		if !ValidPath(r.Path) {
			return fmt.Errorf("%s: %q is not a file name", r.Op, r.Path)
		}
		if r.Size < 0 {
			return fmt.Errorf("%s: a negative size", r.Op)
		}
	case OpScreenshot, OpBugreport:
	case OpKey:
		if len(r.Keys) == 0 || len(r.Keys) > MaxKeys {
			return fmt.Errorf("key: 1 to %d keys", MaxKeys)
		}
		for _, k := range r.Keys {
			if k == "" || len(k) > 32 {
				return fmt.Errorf("key: %q is not a key", k)
			}
		}
	case OpType:
		if r.Text == "" || !utf8.ValidString(r.Text) {
			return errors.New("type: no text")
		}
	case OpSyslog:
		if r.Unit != "" && !ValidUnit(r.Unit) {
			return fmt.Errorf("syslog: %q is not a unit", r.Unit)
		}
		if r.Lines < 0 {
			return errors.New("syslog: a negative number of lines")
		}
	case OpForward:
		low := 1
		if r.Reverse {
			low = MinReversePort
		}
		if r.Port < low || r.Port > 65535 {
			return fmt.Errorf("forward: port %d is not between %d and 65535", r.Port, low)
		}
	case OpReboot:
		if r.Target != "" && r.Target != RebootBootloader && r.Target != RebootRecovery {
			return fmt.Errorf("reboot: %q is not %s or %s", r.Target, RebootBootloader, RebootRecovery)
		}
	case OpUpdate:
		if r.Size <= 0 {
			return errors.New("update: no bundle")
		}
	default:
		return fmt.Errorf("unknown operation %q (one of %s)", r.Op, strings.Join(Ops, ", "))
	}
	return nil
}

// ViaAddr is the address an SSH session came in on (the phone's side of
// its connection), "" without one. An IPv4 address comes as itself: on the
// image dropbear gets its connections from systemd's dual-stack socket,
// which reports them as IPv4-mapped IPv6 (::ffff:10.71.0.1).
func ViaAddr(via string) string {
	f := strings.Fields(via)
	if len(f) != 4 {
		return ""
	}
	a, err := netip.ParseAddr(f[2])
	if err != nil {
		return ""
	}
	return a.Unmap().String()
}

// SockPath is the Shell's socket in runDir, or in RunDirEnv's directory
// (DefaultRunDir without either).
func SockPath(runDir string) string {
	if runDir == "" {
		runDir = os.Getenv(RunDirEnv)
	}
	if runDir == "" {
		runDir = DefaultRunDir
	}
	return filepath.Join(runDir, Sock)
}

// ReadRequest reads a request's line.
func ReadRequest(r *bufio.Reader) (Request, error) {
	var req Request
	line, err := readLine(r)
	if err != nil {
		return req, err
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return req, fmt.Errorf("a request is one JSON line: %w", err)
	}
	return req, req.Check()
}

// WriteReply writes a reply's line.
func WriteReply(w io.Writer, rep Reply) error {
	raw, _ := json.Marshal(rep)
	_, err := w.Write(append(raw, '\n'))
	return err
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, more, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, part...)
		if len(line) > MaxRequest {
			return nil, fmt.Errorf("a line longer than %d bytes", MaxRequest)
		}
		if !more {
			return line, nil
		}
	}
}

// readReply reads the Shell's reply line; a refusal is the error.
func readReply(r *bufio.Reader) (Reply, error) {
	var rep Reply
	line, err := readLine(r)
	if err != nil {
		return rep, fmt.Errorf("no answer from the Shell: %w", err)
	}
	if err := json.Unmarshal(line, &rep); err != nil {
		return rep, fmt.Errorf("the Shell's answer: %w", err)
	}
	if !rep.OK {
		return rep, errors.New(rep.Error)
	}
	return rep, nil
}

// Call sends a request to the Shell's socket at path, with body after an
// install, a push or an update, and copies what the Shell answers to out:
// progress lines, then the reply's text, or the bytes that follow it
// (whose note goes to notes); for logs and syslog what follows it; for
// debug, body goes to the debugger socket and what comes from it to out,
// until either ends; for forward, until the Shell or body ends. A refusal
// is the error.
func Call(path string, req Request, body io.Reader, out, notes io.Writer) error {
	if err := req.Check(); err != nil {
		return err
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return fmt.Errorf("the Shell does not answer on %s: %w", path, err)
	}
	defer conn.Close()
	raw, _ := json.Marshal(req)
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		return err
	}
	r := bufio.NewReader(conn)
	if withBody(req.Op) {
		if _, err := readReply(r); err != nil {
			return err
		}
		if req.Size > 0 {
			n, err := io.CopyN(conn, body, req.Size)
			if err != nil {
				return fmt.Errorf("sending %d of %d bytes: %w", n, req.Size, err)
			}
		}
	}
	var rep Reply
	for {
		if rep, err = readReply(r); err != nil {
			return err
		}
		if !rep.More {
			break
		}
		fmt.Fprintln(out, rep.Text)
	}
	if notes == nil {
		notes = io.Discard
	}
	if rep.Note != "" {
		fmt.Fprintln(notes, rep.Note)
	}
	if Binary(req.Op) {
		if rep.Text != "" {
			fmt.Fprintln(notes, rep.Text)
		}
		n, err := io.CopyN(out, r, rep.Size)
		if err != nil {
			return fmt.Errorf("received %d of %d bytes: %w", n, rep.Size, err)
		}
		return nil
	}
	if rep.Text != "" {
		fmt.Fprintln(out, rep.Text)
	}
	switch req.Op {
	case OpLogs, OpSyslog:
		_, err = io.Copy(out, r)
	case OpDebug, OpForward:
		go func() {
			if body != nil {
				_, _ = io.Copy(conn, body)
			}
			if uc, ok := conn.(*net.UnixConn); ok {
				_ = uc.CloseWrite()
			}
		}()
		_, err = io.Copy(out, r)
	}
	return err
}
