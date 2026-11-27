package displayd

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

func drmIOWR(nr, size uintptr) uintptr { return 3<<30 | size<<16 | 'd'<<8 | nr }

var (
	ioctlGetResources = drmIOWR(0xA0, unsafe.Sizeof(drmCardRes{}))
	ioctlGetCRTC      = drmIOWR(0xA1, unsafe.Sizeof(drmCRTC{}))
	ioctlGetEncoder   = drmIOWR(0xA6, unsafe.Sizeof(drmGetEncoder{}))
	ioctlGetConnector = drmIOWR(0xA7, unsafe.Sizeof(drmGetConnector{}))
)

const ioctlDropMaster = 'd'<<8 | 0x1f

func drmIoctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
		if e == syscall.EINTR || e == syscall.EAGAIN {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}

func ptr[T any](s []T) uint64 {
	if len(s) == 0 {
		return 0
	}
	return uint64(uintptr(unsafe.Pointer(&s[0])))
}

// readKMS reads the first connected connector of the card at path, its
// modes and its CRTC's mode. The card is opened only once the renderer
// holds it: with no master, the first process to open a card becomes its
// master; should this one have, it gives that up at once (the renderer
// would fail to set its mode and the Shell start it again).
func readKMS(path string) (kmsInfo, error) {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return kmsInfo{}, err
	}
	defer f.Close()
	fd := f.Fd()
	_ = drmIoctl(fd, ioctlDropMaster, nil)
	var res drmCardRes
	var conns, crtcs []uint32
	for {
		res = drmCardRes{}
		if err := drmIoctl(fd, ioctlGetResources, unsafe.Pointer(&res)); err != nil {
			return kmsInfo{}, err
		}
		conns, crtcs = make([]uint32, res.CountConnectors), make([]uint32, res.CountCRTCs)
		want := res
		res.FBs, res.Encoders, res.CountFBs, res.CountEncod = 0, 0, 0, 0
		res.Connectors, res.CRTCs = ptr(conns), ptr(crtcs)
		err := drmIoctl(fd, ioctlGetResources, unsafe.Pointer(&res))
		runtime.KeepAlive(conns)
		runtime.KeepAlive(crtcs)
		if err != nil {
			return kmsInfo{}, err
		}
		if res.CountConnectors == want.CountConnectors && res.CountCRTCs == want.CountCRTCs {
			break
		}
	}
	for _, id := range conns {
		c, modes, err := getConnector(fd, id)
		if err != nil || c.Connection != drmConnected || len(modes) == 0 {
			continue
		}
		var cur *drmModeInfo
		if c.EncoderID != 0 {
			enc := drmGetEncoder{EncoderID: c.EncoderID}
			if drmIoctl(fd, ioctlGetEncoder, unsafe.Pointer(&enc)) == nil && enc.CRTCID != 0 {
				crtc := drmCRTC{CRTCID: enc.CRTCID}
				if drmIoctl(fd, ioctlGetCRTC, unsafe.Pointer(&crtc)) == nil && crtc.ModeValid != 0 {
					cur = &crtc.Mode
				}
			}
		}
		rates, hz := panelRates(modes, cur)
		return kmsInfo{connector: connectorName(c.ConnectorType, c.ConnectorTypeID), rates: rates, hz: hz}, nil
	}
	return kmsInfo{}, errors.New("no connected display with modes")
}

// getConnector: a connector and its modes, asked for until the count
// holds (a monitor plugged in between the two calls).
func getConnector(fd uintptr, id uint32) (drmGetConnector, []drmModeInfo, error) {
	for {
		c := drmGetConnector{ConnectorID: id}
		if err := drmIoctl(fd, ioctlGetConnector, unsafe.Pointer(&c)); err != nil {
			return c, nil, err
		}
		n := c.CountModes
		modes := make([]drmModeInfo, n)
		c = drmGetConnector{ConnectorID: id, CountModes: n, Modes: ptr(modes)}
		err := drmIoctl(fd, ioctlGetConnector, unsafe.Pointer(&c))
		runtime.KeepAlive(modes)
		if err != nil {
			return c, nil, err
		}
		if c.CountModes <= n {
			return c, modes[:c.CountModes], nil
		}
	}
}
