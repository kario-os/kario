// Package usbshared is phone-usbshared: file sharing with a computer over
// the USB cable (usbshare.Server, HTTP and WebDAV) in a process of its
// own, the way Android runs MTP in the MediaProvider process and not in
// system_server. Everything a computer sends is parsed here, never in the
// Shell, which starts this helper once the user trusts the computer.
//
// It inherits two descriptors and opens nothing else: fd 3, the listening
// socket on the link's address (the Shell made it: port 80 needs a
// privilege this process does not have), and fd 4, its end of a socket
// pair to the Shell, which lends it the library (usbshare.Remote). The
// launcher has confined it already (its own uid, an empty network
// namespace, Landlock allowing nothing but this program, seccomp, no
// capabilities, no_new_privs; on a developer machine what needs no root);
// it adds sandbox.DescriptorsOnly itself. It ends when the Shell's end
// closes. For file transfer over MTP it serves usbshare/mtp instead, on
// the MTP function's files the Shell opened (mtp.go).
package usbshared

import (
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/sandbox"
	"github.com/kario-os/kario/usbshare"
	"github.com/kario-os/kario/usbshare/mtp"
)

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	log.SetPrefix("phone-usbshared: ")
	if os.Getenv(mtp.EnvMode) == mtp.ModeMTP {
		mainMTP()
		return
	}
	lf, sf := os.NewFile(3, "listener"), os.NewFile(4, "shell")
	ln, err := net.FileListener(lf)
	if err != nil {
		log.Fatalf("no listening socket on fd 3: %v", err)
	}
	lf.Close()
	nc, err := net.FileConn(sf)
	if err != nil {
		log.Fatalf("no connection to the Shell on fd 4: %v", err)
	}
	sf.Close()
	conn, ok := nc.(*net.UnixConn)
	if !ok {
		log.Fatal("fd 4 is not a Unix socket")
	}
	// Read once what would need a path later: the system's file types
	// and time zone.
	mime.TypeByExtension(".html")
	time.Now().Zone()
	if err := sandbox.DescriptorsOnly(); err != nil {
		log.Fatal(err)
	}
	hi := usbshare.Hello{Sandbox: layers(), UID: os.Getuid(), Caps: capabilities()}
	lib, pass, err := usbshare.Connect(conn, hi, probe)
	if err != nil {
		log.Fatal(err)
	}
	srv := usbshare.New(lib, pass)
	srv.Log = lib.Logf
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-lib.Done()
		hs.Close()
		os.Exit(0)
	}()
	err = hs.Serve(ln)
	select {
	case <-lib.Done():
		os.Exit(0)
	default:
	}
	log.Fatal(err)
}

func layers() []string {
	var out []string
	for _, l := range strings.Split(os.Getenv(sandbox.Env), ",") {
		if l != "" {
			out = append(out, l)
		}
	}
	return append(out, sandbox.FDOnly)
}

// capabilities is the effective set (0: none).
func capabilities() uint64 {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return ^uint64(0)
	}
	return uint64(data[0].Effective) | uint64(data[1].Effective)<<32
}
