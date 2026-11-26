package main

import (
	"bytes"
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/pkg"
)

// kario device works on a phone in developer mode over the trusted cable
// (29-app-development.md §2, 27-usb-typec.md §4): it runs ssh to the
// phone's address on the cable (the development image's dropbear) and
// phone-dev there, which hands the request to the Shell (package devctl).
// The phone refuses unless developer mode is on and it trusts the computer
// on the cable; nothing here gets around that, it only says what to do.
//
//	kario device [-ssh CMD] [-to USER@HOST] install [-devstore DIR [-name NAME]] [-native DIR] [-open] FILE|DIR
//	kario device ... logs [-f] APP
//	kario device ... reload|restart|uninstall APP
//	kario device ... shell [COMMAND...]
//	kario device ... push LOCAL REMOTE | pull REMOTE LOCAL
//	kario device ... screenshot [-o FILE.png] | bugreport [-o FILE.zip]
//	kario device ... key NAME... | type TEXT...
//	kario device ... syslog [-f] [-u UNIT] [-n N]
//	kario device ... forward LOCALPORT REMOTEPORT | reverse REMOTEPORT LOCALPORT
//	kario device ... reboot [bootloader|recovery] | update FILE.raucb
//
// install takes a signed .phoneapp, or a project or app directory, which it
// signs with a development store first (-devstore, by default one kept in
// the user's configuration directory); with a development store it first
// sends the store's root, which developer mode then trusts in place of
// another development store's. Files go through the SSH session's command
// (no scp or sftp server on the phone); forward and reverse are the
// session's own port forwarding (ssh -L, -R), held by phone-dev while the
// phone's gate is open (27-usb-typec.md §4.1).

const deviceUsage = `usage:
  kario device [-ssh CMD] [-to USER@HOST] install [-devstore DIR [-name NAME]] [-native DIR] [-open] FILE|DIR
  kario device [-ssh CMD] [-to USER@HOST] logs [-f] APP
  kario device [-ssh CMD] [-to USER@HOST] reload|restart|uninstall APP
  kario device [-ssh CMD] [-to USER@HOST] shell [COMMAND...]
  kario device [-ssh CMD] [-to USER@HOST] push LOCAL REMOTE
  kario device [-ssh CMD] [-to USER@HOST] pull REMOTE LOCAL
  kario device [-ssh CMD] [-to USER@HOST] screenshot [-o FILE.png]
  kario device [-ssh CMD] [-to USER@HOST] key NAME...
  kario device [-ssh CMD] [-to USER@HOST] type TEXT...
  kario device [-ssh CMD] [-to USER@HOST] syslog [-f] [-u UNIT] [-n N]
  kario device [-ssh CMD] [-to USER@HOST] forward LOCALPORT REMOTEPORT
  kario device [-ssh CMD] [-to USER@HOST] reverse REMOTEPORT LOCALPORT
  kario device [-ssh CMD] [-to USER@HOST] reboot [bootloader|recovery]
  kario device [-ssh CMD] [-to USER@HOST] update FILE.raucb
  kario device [-ssh CMD] [-to USER@HOST] bugreport [-o FILE.zip]
REMOTE: a path in the phone's developer directory, or APP-ID:PATH in the data
of an app installed in developer mode.`

type device struct {
	ssh            []string
	to             string
	stdin          io.Reader
	stdout, stderr io.Writer
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// defaultDevStore is the development store kario device signs with.
func defaultDevStore() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "kario", "devstore")
}

func deviceCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("kario device", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ssh := fs.String("ssh", envOr("KARIO_SSH", "ssh"), "the ssh command, with its options (default $KARIO_SSH or ssh)")
	to := fs.String("to", envOr("KARIO_DEVICE", "root@"+devctl.USBAddr), "the phone (default $KARIO_DEVICE or the cable's address)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 || len(strings.Fields(*ssh)) == 0 {
		return errors.New(deviceUsage)
	}
	d := device{ssh: strings.Fields(*ssh), to: *to, stdin: stdin, stdout: stdout, stderr: stderr}
	op, rest := fs.Arg(0), fs.Args()[1:]
	switch op {
	case devctl.OpInstall:
		return d.install(rest)
	case devctl.OpLogs:
		sub := flag.NewFlagSet("kario device logs", flag.ContinueOnError)
		sub.SetOutput(stderr)
		follow := sub.Bool("f", false, "keep printing what the app writes")
		if err := sub.Parse(rest); err != nil {
			return err
		}
		if sub.NArg() != 1 {
			return errors.New(deviceUsage)
		}
		if !devctl.ValidApp(sub.Arg(0)) {
			return fmt.Errorf("%q: not an app id", sub.Arg(0))
		}
		cmd := []string{devctl.OpLogs}
		if *follow {
			cmd = append(cmd, "-f")
		}
		return d.phoneDev(nil, d.stdout, append(cmd, sub.Arg(0))...)
	case devctl.OpReload, devctl.OpRestart, devctl.OpUninstall:
		if len(rest) != 1 {
			return errors.New(deviceUsage)
		}
		if !devctl.ValidApp(rest[0]) {
			return fmt.Errorf("%q: not an app id", rest[0])
		}
		return d.phoneDev(nil, d.stdout, op, rest[0])
	case "shell":
		argv := append(append([]string{}, d.ssh[1:]...), "-t", d.to)
		cmd := exec.Command(d.ssh[0], append(argv, rest...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = d.stdin, d.stdout, d.stderr
		return sshErr(cmd.Run(), d.to)
	case devctl.OpPush:
		if len(rest) != 2 {
			return errors.New(deviceUsage)
		}
		return d.push(rest[0], rest[1])
	case devctl.OpPull:
		if len(rest) != 2 {
			return errors.New(deviceUsage)
		}
		dst := rest[1]
		if st, err := os.Stat(dst); err == nil && st.IsDir() {
			_, inside, _ := strings.Cut(rest[0], ":")
			dst = filepath.Join(dst, path.Base(cmp.Or(inside, rest[0])))
		}
		return d.fetch(dst, devctl.OpPull, rest[0])
	case devctl.OpScreenshot, devctl.OpBugreport:
		sub := flag.NewFlagSet("kario device "+op, flag.ContinueOnError)
		sub.SetOutput(stderr)
		ext := map[string]string{devctl.OpScreenshot: ".png", devctl.OpBugreport: ".zip"}[op]
		out := sub.String("o", op+"-"+time.Now().Format("20060102-150405")+ext, "the file to write")
		if err := sub.Parse(rest); err != nil {
			return err
		}
		if sub.NArg() != 0 {
			return errors.New(deviceUsage)
		}
		return d.fetch(*out, op)
	case devctl.OpKey:
		if len(rest) == 0 {
			return errors.New(deviceUsage)
		}
		return d.phoneDev(nil, d.stdout, append([]string{op}, rest...)...)
	case devctl.OpType:
		if len(rest) == 0 {
			return errors.New(deviceUsage)
		}
		return d.phoneDev(nil, d.stdout, op, strings.Join(rest, " "))
	case devctl.OpSyslog:
		sub := flag.NewFlagSet("kario device syslog", flag.ContinueOnError)
		sub.SetOutput(stderr)
		follow := sub.Bool("f", false, "keep printing what comes next")
		unit := sub.String("u", "", "one service (a systemd unit; shell: the Shell's own log)")
		lines := sub.Int("n", 0, "start with the last N lines (default: this boot's)")
		if err := sub.Parse(rest); err != nil {
			return err
		}
		if sub.NArg() != 0 || *lines < 0 || *unit != "" && !devctl.ValidUnit(*unit) {
			return errors.New(deviceUsage)
		}
		cmd := []string{op}
		if *follow {
			cmd = append(cmd, "-f")
		}
		if *unit != "" {
			cmd = append(cmd, "-u", *unit)
		}
		if *lines > 0 {
			cmd = append(cmd, "-n", strconv.Itoa(*lines))
		}
		return d.phoneDev(nil, d.stdout, cmd...)
	case devctl.OpForward, "reverse":
		if len(rest) != 2 {
			return errors.New(deviceUsage)
		}
		return d.forward(op == "reverse", rest[0], rest[1])
	case devctl.OpReboot:
		if len(rest) > 1 {
			return errors.New(deviceUsage)
		}
		return d.phoneDev(nil, d.stdout, append([]string{op}, rest...)...)
	case devctl.OpUpdate:
		if len(rest) != 1 {
			return errors.New(deviceUsage)
		}
		f, err := os.Open(rest[0])
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Size() == 0 {
			return fmt.Errorf("%s: not a bundle", rest[0])
		}
		return d.phoneDev(f, d.stdout, op, strconv.FormatInt(st.Size(), 10))
	}
	return fmt.Errorf("unknown device command %q\n%s", op, deviceUsage)
}

// push sends a file; phone-dev reads exactly its size, the Shell writes it.
func (d device) push(local, remote string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s: not a file (push takes files one by one)", local)
	}
	return d.phoneDev(f, d.stdout, devctl.OpPush, remote, strconv.FormatInt(st.Size(), 10), fmt.Sprintf("%o", st.Mode().Perm()))
}

// fetch writes what phone-dev puts out (a pulled file, a screenshot, a bug
// report) to dst, whole or not at all.
func (d device) fetch(dst string, args ...string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".kario-device-*")
	if err != nil {
		return err
	}
	err = d.phoneDev(nil, tmp, args...)
	st, serr := tmp.Stat()
	if cerr := tmp.Close(); err == nil {
		err = cmp.Or(serr, cerr)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	fmt.Fprintf(d.stdout, "%s (%d bytes)\n", dst, st.Size())
	return nil
}

// forward runs ssh with the port forwarding and phone-dev holding it: it
// lasts until this command is stopped (ssh's standard input, a pipe only
// this process holds, ends with it) or the phone ends it (developer mode
// off, the cable out).
func (d device) forward(reverse bool, a, b string) error {
	ports := [2]int{}
	for i, s := range []string{a, b} {
		p, err := strconv.Atoi(s)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("%q: not a port", s)
		}
		ports[i] = p
	}
	var opt, text string
	args := []string{devctl.OpForward}
	if reverse {
		if ports[0] < devctl.MinReversePort {
			return fmt.Errorf("reverse: the phone's port must be %d or above", devctl.MinReversePort)
		}
		opt = fmt.Sprintf("-R127.0.0.1:%d:127.0.0.1:%d", ports[0], ports[1])
		text = fmt.Sprintf("the phone's 127.0.0.1:%d reaches this computer's port %d", ports[0], ports[1])
		args = append(args, "-reverse", a)
	} else {
		opt = fmt.Sprintf("-L%d:127.0.0.1:%d", ports[0], ports[1])
		text = fmt.Sprintf("this computer's localhost:%d reaches the phone's port %d", ports[0], ports[1])
		args = append(args, b)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	defer pw.Close()
	fmt.Fprintf(d.stderr, "%s (stop with Ctrl-C)\n", text)
	err = d.phoneDevSSH([]string{"-oExitOnForwardFailure=yes", opt}, pr, d.stdout, args...)
	pr.Close()
	return err
}

// remoteSafe are the bytes a remote command line's word may have bare.
const remoteSafe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789@%+=:,./_-"

// remoteWord quotes a word for the shell that runs the SSH session's
// command line (ssh joins the words with spaces).
func remoteWord(s string) string {
	if s != "" && strings.Trim(s, remoteSafe) == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// phoneDev runs phone-dev on the phone with the arguments.
func (d device) phoneDev(stdin io.Reader, out io.Writer, args ...string) error {
	return d.phoneDevSSH(nil, stdin, out, args...)
}

// phoneDevSSH is phoneDev with more ssh options; each argument reaches
// phone-dev as it is, whatever the remote shell makes of the line.
func (d device) phoneDevSSH(opts []string, stdin io.Reader, out io.Writer, args ...string) error {
	for _, a := range args {
		if strings.ContainsFunc(a, unicode.IsControl) {
			return fmt.Errorf("%q: control characters do not go to the phone", a)
		}
	}
	argv := append(append(append([]string{}, d.ssh[1:]...), opts...), "-T", d.to, devctl.Program)
	for _, a := range args {
		argv = append(argv, remoteWord(a))
	}
	cmd := exec.Command(d.ssh[0], argv...)
	cmd.Stdin, cmd.Stdout = stdin, out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if _, why, refused := strings.Cut(stderr.String(), devctl.Program+": "); err != nil && refused {
		return fmt.Errorf("the phone: %s", strings.TrimSpace(why))
	}
	_, _ = d.stderr.Write(stderr.Bytes())
	return sshErr(err, d.to)
}

func sshErr(err error, to string) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 255 {
		return fmt.Errorf("ssh could not reach %s: is the cable in, the computer trusted on the phone and developer mode on?", to)
	}
	return err
}

func (d device) install(args []string) error {
	fs := flag.NewFlagSet("kario device install", flag.ContinueOnError)
	fs.SetOutput(d.stderr)
	store := fs.String("devstore", "", "sign with the development store kept in this directory, and make the phone trust it (default for a directory: "+defaultDevStore()+")")
	name := fs.String("name", "Example Developer", "-devstore: the developer's name")
	native := fs.String("native", "", "the prebuilt native modules, <arch>/<name>.so (default DIR/native)")
	open := fs.Bool("open", false, "open the app afterwards (restarting it if it runs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(deviceUsage)
	}
	src := fs.Arg(0)
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if st.IsDir() && *store == "" {
		*store = defaultDevStore()
	}
	var raw []byte
	if st.IsDir() {
		if raw, err = devPackageFile(src, packageOptions{devStore: *store, name: *name, native: *native}); err != nil {
			return err
		}
	} else if raw, err = os.ReadFile(src); err != nil {
		return err
	}
	if len(raw) > pkg.MaxSize {
		return fmt.Errorf("%s: larger than a package may be (%d MB)", src, pkg.MaxSize>>20)
	}
	id, err := d.send(raw, *store)
	if err != nil || !*open {
		return err
	}
	return d.phoneDev(nil, d.stdout, devctl.OpRestart, id)
}

// devPackageFile signs a project or an app directory as packageApp does
// (o.out and o.symbols aside) and returns the package.
func devPackageFile(src string, o packageOptions) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "kario-device-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	o.out, o.symbols = filepath.Join(tmp, "app.phoneapp"), "-"
	if _, err := packageApp(src, o); err != nil {
		return nil, err
	}
	return os.ReadFile(o.out)
}

// send installs a package on the phone, with a development store's root
// first (store "" none), and returns the app's id as the phone says it.
func (d device) send(raw []byte, store string) (string, error) {
	if store != "" {
		if _, err := pkg.DevStore(store, filepath.Join(store, "trust")); err != nil {
			return "", err
		}
		key, err := os.ReadFile(filepath.Join(store, "trust", "dev.pub"))
		if err != nil {
			return "", err
		}
		if err := d.phoneDev(nil, d.stdout, devctl.OpTrust, strings.TrimSpace(string(key))); err != nil {
			return "", err
		}
	}
	var said bytes.Buffer
	if err := d.phoneDev(bytes.NewReader(raw), io.MultiWriter(d.stdout, &said), devctl.OpInstall, "-"); err != nil {
		return "", err
	}
	f := strings.Fields(said.String())
	if len(f) < 2 || f[0] != "installed" {
		return "", fmt.Errorf("the phone did not say what it installed: %q", said.String())
	}
	return f[1], nil
}
