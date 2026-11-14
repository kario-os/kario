package dsd

// DoP is DSD over PCM (the DoP standard, v1.1): each 24-bit PCM sample
// carries 16 DSD samples of its channel in its lower two bytes, earliest
// first, under a marker byte that alternates 0x05 / 0xFA from one PCM
// frame to the next. A DAC that knows DoP plays the DSD; at DSD64 the PCM
// rate is 176.4 kHz, at DSD128 352.8 kHz. Any change to the samples
// (volume, dither, mixing) destroys the markers, so DoP is only for a
// stream that reaches the card untouched.
type DoP struct {
	odd bool
}

const (
	dopMarker1 = 0x05
	dopMarker2 = 0xfa
)

// DoPRate is the PCM rate that carries DSD at dsdRate.
func DoPRate(dsdRate int) int { return dsdRate / 16 }

// Pack turns byte frames (two per PCM frame) into DoP samples,
// left-justified in int32 (the 24 bits in the top three bytes, as an S32
// or S24_3LE card takes them). It returns the PCM samples written.
func (p *DoP) Pack(in []byte, channels int, out []int32) int {
	n := 0
	for i := 0; i+2*channels <= len(in) && n+channels <= len(out); i += 2 * channels {
		m := uint32(dopMarker1)
		if p.odd {
			m = dopMarker2
		}
		p.odd = !p.odd
		for c := 0; c < channels; c++ {
			v := m<<16 | uint32(in[i+c])<<8 | uint32(in[i+channels+c])
			out[n] = int32(v << 8)
			n++
		}
	}
	return n
}

// The ALSA sample formats for native DSD.
const (
	FormatU8    = "DSD_U8"
	FormatU16LE = "DSD_U16_LE"
	FormatU16BE = "DSD_U16_BE"
	FormatU32LE = "DSD_U32_LE"
	FormatU32BE = "DSD_U32_BE"
)

// NativeWidth is the bytes per channel per ALSA frame of a native DSD
// format (0 for other names).
func NativeWidth(format string) int {
	switch format {
	case FormatU8:
		return 1
	case FormatU16LE, FormatU16BE:
		return 2
	case FormatU32LE, FormatU32BE:
		return 4
	}
	return 0
}

// PackNative lays byte frames out in a native ALSA DSD format: each ALSA
// frame holds width consecutive bytes of every channel, the earliest in
// the most significant byte of the word (stored big- or little-endian as
// the format says). It returns the bytes written; whole ALSA frames only.
func PackNative(format string, in []byte, channels int, out []byte) int {
	w := NativeWidth(format)
	if w == 0 {
		return 0
	}
	little := format == FormatU16LE || format == FormatU32LE
	n := 0
	for i := 0; i+w*channels <= len(in) && n+w*channels <= len(out); i += w * channels {
		for c := 0; c < channels; c++ {
			for k := 0; k < w; k++ {
				b := in[i+k*channels+c]
				if little {
					out[n+w-1-k] = b
				} else {
					out[n+k] = b
				}
			}
			n += w
		}
	}
	return n
}
