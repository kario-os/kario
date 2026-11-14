package usbshared

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/kario-os/kario/sandbox"
	"github.com/kario-os/kario/usbshare"
	"github.com/kario-os/kario/usbshare/mtp"
)

// pollMTP is how often the folders the computer looks at are listed
// again, for what the phone adds or removes meanwhile.
const pollMTP = 5 * time.Second

// mainMTP is file transfer over MTP (usbshare/mtp). Its descriptors are
// the MTP function's files the Shell opened (FunctionFS's ep0 as fd 3,
// bulk in, bulk out and interrupt as fds 5, 6 and 7), or a simulated
// cable's on a developer machine; fd 4 is the Shell, as for network
// sharing. Everything the computer sends is parsed here.
func mainMTP() {
	files := []*os.File{os.NewFile(3, "ep0"), os.NewFile(5, "ep-in"), os.NewFile(6, "ep-out"), os.NewFile(7, "ep-intr")}
	for _, f := range files {
		if f == nil {
			log.Fatal("the MTP function's files are not on fds 3, 5, 6 and 7")
		}
	}
	nc, err := net.FileConn(os.NewFile(4, "shell"))
	if err != nil {
		log.Fatalf("no connection to the Shell on fd 4: %v", err)
	}
	conn, ok := nc.(*net.UnixConn)
	if !ok {
		log.Fatal("fd 4 is not a Unix socket")
	}
	packet, _ := strconv.Atoi(os.Getenv(mtp.EnvPacket))
	if packet <= 0 {
		packet = mtp.Packet
	}
	var info mtp.DeviceInfo
	if err := json.Unmarshal([]byte(os.Getenv(mtp.EnvDevice)), &info); err != nil {
		log.Printf("device info: %v", err)
	}
	var t mtp.Transport
	if os.Getenv(mtp.EnvTransport) == mtp.TransportSim {
		t = mtp.Sim(mtp.SimFiles{EP0: files[0], In: files[1], Out: files[2], Intr: files[3]}, packet)
	} else {
		t = mtp.FFS(files[0], files[1], files[2], files[3], packet)
	}
	time.Now().Zone()
	if err := sandbox.DescriptorsOnly(); err != nil {
		log.Fatal(err)
	}
	hi := usbshare.Hello{Sandbox: layers(), UID: os.Getuid(), Caps: capabilities()}
	lib, _, err := usbshare.Connect(conn, hi, probe)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		<-lib.Done()
		os.Exit(0)
	}()
	r := mtp.NewResponder(mtp.Config{Lib: lib, Info: info, Log: lib.Logf, Poll: pollMTP})
	if err := r.Serve(t); err != nil {
		lib.Logf("usb sharing: MTP ended: %v", err)
	}
	// The cable's end: the Shell ends the helper.
	<-lib.Done()
	os.Exit(0)
}
