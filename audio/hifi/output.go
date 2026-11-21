package hifi

import (
	"bufio"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"

	"github.com/kario-os/kario/audio/dsd"
)

// Format is what a track decodes to.
type Format struct {
	Rate     int  // frames a second; DSD: 1-bit samples a second per channel
	Channels int  // as decoded
	Bits     int  // significant bits of a PCM sample
	Float    bool // the samples were floats (converted to integers here)
	DSD      bool // 1-bit: byte frames, see audio/dsd
}

func (f Format) String() string {
	if f.DSD {
		return fmt.Sprintf("%s %dch", dsd.Name(f.Rate), f.Channels)
	}
	return fmt.Sprintf("%d/%g kHz %dch", f.Bits, float64(f.Rate)/1000, f.Channels)
}

// Output modes: how a track reaches the sound card.
const (
	ModeMixer = "mixer" // resampled to the mixer's 48 kHz, mixed with other sounds
	ModePCM   = "pcm"   // at the track's own rate (bit-perfect when Plan.BitPerfect)
	ModeDoP   = "dop"   // DSD over PCM
	ModeDSD   = "dsd"   // ALSA's native DSD formats
)

// DSD output settings (what a DAC can take is not always detectable: a
// DAC without DoP plays DoP as loud noise, so DoP is the user's choice).
const (
	DSDAuto = "auto" // native DSD when ALSA offers it, otherwise PCM
	DSDDoP  = "dop"  // DoP when the card takes 24-bit PCM at the DoP rate
	DSDPCM  = "pcm"  // always convert to PCM
)

// Plan is the sound card set-up for a track.
type Plan struct {
	Mode       string
	Format     string // ALSA sample format
	Rate       int
	Channels   int
	BitPerfect bool
	// PCMRate: a DSD track converted to PCM is decimated to this rate.
	PCMRate int
	// Source is the format the plan was made for.
	Source Format
}

func (p Plan) String() string {
	if p.Mode == ModeMixer {
		return "mixer 48 kHz"
	}
	s := fmt.Sprintf("%s %s %d Hz %dch", p.Mode, p.Format, p.Rate, p.Channels)
	if p.BitPerfect {
		s += " bit-perfect"
	}
	return s
}

// Alt is one set of formats, channels and rates a card takes together (a
// USB interface's alternate setting, or the whole of an ALSA device).
type Alt struct {
	Formats          []string
	MinCh, MaxCh     int
	Rates            []int // discrete rates; empty: MinRate..MaxRate
	MinRate, MaxRate int
}

// Caps is what a sound card takes. Any is the null sink, which takes
// everything (the developer machine).
type Caps struct {
	Any  bool
	Alts []Alt
}

func (c Caps) Supports(format string, ch, rate int) bool {
	if c.Any {
		return true
	}
	for _, a := range c.Alts {
		if !contains(a.Formats, format) || ch < a.MinCh || ch > a.MaxCh {
			continue
		}
		if len(a.Rates) > 0 {
			for _, r := range a.Rates {
				if r == rate {
					return true
				}
			}
			continue
		}
		if rate >= a.MinRate && rate <= a.MaxRate {
			return true
		}
	}
	return false
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// pcmFormats in the order to try for a source of bits: the smallest
// container that holds the samples unchanged first. Under digital volume
// the widest goes first, so the attenuation loses nothing.
func pcmFormats(bits int, volume bool) []string {
	switch {
	case volume:
		return []string{"S32_LE", "S24_3LE", "S24_LE", "S16_LE"}
	case bits <= 16:
		return []string{"S16_LE", "S24_3LE", "S24_LE", "S32_LE"}
	case bits <= 24:
		return []string{"S24_3LE", "S32_LE", "S24_LE"}
	}
	return []string{"S32_LE"}
}

func containerBits(format string) int {
	switch format {
	case "S16_LE":
		return 16
	case "S24_3LE", "S24_LE":
		return 24
	}
	return 32
}

// Negotiate picks the set-up for a track of format src played at gain
// (volume and ReplayGain together; 1 leaves the samples untouched).
func Negotiate(src Format, c Caps, gain float64, dsdMode string) Plan {
	p := negotiate(src, c, gain, dsdMode)
	p.Source = src
	return p
}

func negotiate(src Format, c Caps, gain float64, dsdMode string) Plan {
	unity := math.Abs(gain-1) < 1e-9
	if src.DSD {
		if unity && src.Channels <= 2 {
			if dsdMode == DSDAuto || dsdMode == "" {
				for _, f := range []string{dsd.FormatU32BE, dsd.FormatU32LE, dsd.FormatU16BE, dsd.FormatU16LE, dsd.FormatU8} {
					r := src.Rate / 8 / dsd.NativeWidth(f)
					if c.Supports(f, src.Channels, r) {
						return Plan{Mode: ModeDSD, Format: f, Rate: r, Channels: src.Channels, BitPerfect: true}
					}
				}
			}
			if dsdMode == DSDDoP {
				for _, f := range []string{"S24_3LE", "S32_LE", "S24_LE"} {
					if r := dsd.DoPRate(src.Rate); c.Supports(f, src.Channels, r) {
						return Plan{Mode: ModeDoP, Format: f, Rate: r, Channels: src.Channels, BitPerfect: true}
					}
				}
			}
		}
		ch := min(src.Channels, 2)
		for _, rate := range []int{176400, 88200} {
			if src.Rate%rate != 0 || src.Rate/rate%8 != 0 {
				continue
			}
			for _, f := range pcmFormats(24, true) {
				if c.Supports(f, ch, rate) {
					return Plan{Mode: ModePCM, Format: f, Rate: rate, Channels: ch, PCMRate: rate}
				}
			}
		}
		return Plan{Mode: ModeMixer, PCMRate: 88200}
	}
	chs := []int{src.Channels}
	if src.Channels == 1 {
		chs = append(chs, 2)
	}
	if src.Channels > 2 {
		chs = append(chs, 2)
	}
	for _, ch := range chs {
		for _, f := range pcmFormats(src.Bits, !unity) {
			if c.Supports(f, ch, src.Rate) {
				perfect := unity && !src.Float && containerBits(f) >= src.Bits && ch >= src.Channels && (ch == src.Channels || src.Channels == 1)
				return Plan{Mode: ModePCM, Format: f, Rate: src.Rate, Channels: ch, BitPerfect: perfect}
			}
		}
	}
	return Plan{Mode: ModeMixer}
}

// BytesPerFrame of an ALSA frame of the plan.
func (p Plan) BytesPerFrame() int {
	if w := dsd.NativeWidth(p.Format); w > 0 {
		return w * p.Channels
	}
	switch p.Format {
	case "S16_LE":
		return 2 * p.Channels
	case "S24_3LE":
		return 3 * p.Channels
	}
	return 4 * p.Channels
}

// pack writes left-justified samples in an ALSA PCM format.
func pack(format string, in []int32, out []byte) int {
	n := 0
	switch format {
	case "S16_LE":
		for _, v := range in {
			u := uint32(v) >> 16
			out[n], out[n+1] = byte(u), byte(u>>8)
			n += 2
		}
	case "S24_3LE":
		for _, v := range in {
			u := uint32(v) >> 8
			out[n], out[n+1], out[n+2] = byte(u), byte(u>>8), byte(u>>16)
			n += 3
		}
	case "S24_LE":
		for _, v := range in {
			u := uint32(v >> 8)
			out[n], out[n+1], out[n+2], out[n+3] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
			n += 4
		}
	default:
		for _, v := range in {
			u := uint32(v)
			out[n], out[n+1], out[n+2], out[n+3] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
			n += 4
		}
	}
	return n
}

// scale applies a digital volume to left-justified samples, with TPDF
// dither at the output's last bit.
func scale(s []int32, gain float64, bits int) {
	lsb := math.Ldexp(1, 32-bits)
	for i, v := range s {
		x := float64(v)*gain + (rand.Float64()-rand.Float64())*lsb
		x = math.Round(x/lsb) * lsb
		s[i] = int32(math.Max(math.MinInt32, math.Min(math.MaxInt32, x)))
	}
}

var (
	reRange = regexp.MustCompile(`^[\[(]\s*(\d+)\s+(\d+)\s*[\])]$`)
	reNum   = regexp.MustCompile(`^\d+$`)
)

// ParseHWParams reads what `aplay -D hw:N,M --dump-hw-params` prints about
// a device (the block between the dashed lines).
func ParseHWParams(out string) (Caps, error) {
	var a Alt
	seen := false
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		lo, hi, ok := interval(v)
		switch strings.TrimSpace(k) {
		case "FORMAT":
			a.Formats, seen = strings.Fields(v), true
		case "CHANNELS":
			if ok {
				a.MinCh, a.MaxCh = lo, hi
			}
		case "RATE":
			if ok {
				a.MinRate, a.MaxRate = lo, hi
			}
		}
	}
	if !seen || a.MaxCh == 0 || a.MaxRate == 0 {
		return Caps{}, fmt.Errorf("no hardware parameters in %q", out)
	}
	return Caps{Alts: []Alt{a}}, nil
}

// interval reads "48000", "[8000 192000]" or "(83 1486078)".
func interval(v string) (int, int, bool) {
	if reNum.MatchString(v) {
		n, _ := strconv.Atoi(v)
		return n, n, true
	}
	if m := reRange.FindStringSubmatch(v); m != nil {
		lo, _ := strconv.Atoi(m[1])
		hi, _ := strconv.Atoi(m[2])
		return lo, hi, true
	}
	return 0, 0, false
}

// ParseUSBStream reads /proc/asound/cardN/stream0 of a USB audio device:
// the playback interface's alternate settings, each with its format,
// channels and exact rates (where DSD-capable DACs list DSD_U32_BE).
func ParseUSBStream(text string) Caps {
	var c Caps
	playback := false
	var cur *Alt
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "Playback:":
			playback = true
		case line == "Capture:":
			playback = false
		case !playback:
		case strings.HasPrefix(line, "Altset"):
			c.Alts = append(c.Alts, Alt{})
			cur = &c.Alts[len(c.Alts)-1]
		case cur == nil:
		case strings.HasPrefix(line, "Format:"):
			cur.Formats = strings.Fields(strings.TrimPrefix(line, "Format:"))
		case strings.HasPrefix(line, "Channels:"):
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Channels:")))
			cur.MinCh, cur.MaxCh = n, n
		case strings.HasPrefix(line, "Rates:"):
			r := strings.TrimSpace(strings.TrimPrefix(line, "Rates:"))
			if lo, hi, ok := strings.Cut(r, " - "); ok {
				cur.MinRate, _ = strconv.Atoi(strings.TrimSpace(lo))
				cur.MaxRate, _ = strconv.Atoi(strings.Fields(hi)[0])
				continue
			}
			for _, f := range strings.Split(r, ",") {
				if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
					cur.Rates = append(cur.Rates, n)
				}
			}
		}
	}
	return c
}
