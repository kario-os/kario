package displayd

import (
	"bytes"
	"fmt"
	"slices"
)

// The DRM uAPI's structures (include/uapi/drm/drm_mode.h), as the kernel
// lays them out; drm_linux.go asks for them.

type drmModeInfo struct {
	Clock                                         uint32
	HDisplay, HSyncStart, HSyncEnd, HTotal, HSkew uint16
	VDisplay, VSyncStart, VSyncEnd, VTotal, VScan uint16
	VRefresh, Flags, Type                         uint32
	Name                                          [32]byte
}

type drmCardRes struct {
	FBs, CRTCs, Connectors, Encoders                  uint64
	CountFBs, CountCRTCs, CountConnectors, CountEncod uint32
	MinWidth, MaxWidth, MinHeight, MaxHeight          uint32
}

type drmGetConnector struct {
	Encoders, Modes, Props, PropValues uint64
	CountModes, CountProps, CountEncod uint32
	EncoderID, ConnectorID             uint32
	ConnectorType, ConnectorTypeID     uint32
	Connection, MMWidth, MMHeight      uint32
	Subpixel, Pad                      uint32
}

type drmGetEncoder struct {
	EncoderID, EncoderType, CRTCID, PossibleCRTCs, PossibleClones uint32
}

type drmCRTC struct {
	SetConnectors                       uint64
	CountConnectors, CRTCID, FBID, X, Y uint32
	GammaSize, ModeValid                uint32
	Mode                                drmModeInfo
}

const (
	drmModeFlagInterlace = 1 << 4
	drmModeFlagDblscan   = 1 << 5
	drmModeTypePreferred = 1 << 3
	drmConnected         = 1
)

// connectorTypes are DRM_MODE_CONNECTOR_*'s names, as the kernel prints
// them (drm_connector_enum_list).
var connectorTypes = []string{"Unknown", "VGA", "DVI-I", "DVI-D", "DVI-A", "Composite", "SVIDEO", "LVDS", "Component", "DIN",
	"DP", "HDMI-A", "HDMI-B", "TV", "eDP", "Virtual", "DSI", "DPI", "Writeback", "SPI", "USB"}

func connectorName(typ, id uint32) string {
	name := "Unknown"
	if int(typ) < len(connectorTypes) {
		name = connectorTypes[typ]
	}
	return fmt.Sprintf("%s-%d", name, id)
}

// modeRate is a mode's refresh rate in Hz, rounded as the kernel's
// drm_mode_vrefresh rounds it.
func modeRate(m drmModeInfo) int {
	if m.HTotal == 0 || m.VTotal == 0 {
		return 0
	}
	num, den := uint64(m.Clock)*1000, uint64(m.HTotal)*uint64(m.VTotal)
	if m.Flags&drmModeFlagInterlace != 0 {
		num *= 2
	}
	if m.Flags&drmModeFlagDblscan != 0 {
		den *= 2
	}
	if m.VScan > 1 {
		den *= uint64(m.VScan)
	}
	return int((num + den/2) / den)
}

func modeName(m drmModeInfo) string {
	n, _, _ := bytes.Cut(m.Name[:], []byte{0})
	return string(n)
}

// panelRates are the rates of the modes of the size the panel runs at
// (cur, when the CRTC has a mode; else the preferred mode's, else the
// first's), progressive only, lowest first, and the rate it runs at.
func panelRates(modes []drmModeInfo, cur *drmModeInfo) (rates []int, hz int) {
	if len(modes) == 0 {
		return nil, 0
	}
	size := modes[0]
	for _, m := range modes {
		if m.Type&drmModeTypePreferred != 0 {
			size = m
			break
		}
	}
	if cur != nil {
		size = *cur
	}
	for _, m := range modes {
		r := modeRate(m)
		if m.HDisplay != size.HDisplay || m.VDisplay != size.VDisplay || m.Flags&drmModeFlagInterlace != 0 || r <= 0 || slices.Contains(rates, r) {
			continue
		}
		rates = append(rates, r)
	}
	slices.Sort(rates)
	hz = modeRate(size)
	if !slices.Contains(rates, hz) && len(rates) > 0 {
		hz = rates[0]
	}
	return rates, hz
}
