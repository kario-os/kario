package dsd

import (
	"fmt"
	"math"
)

// Decimator converts DSD to PCM with one FIR low-pass filter (a
// Kaiser-windowed sinc, about 80 dB stop band) evaluated only at the
// output rate. The input being one bit per sample, each byte of the
// filter's history is one table lookup: the table holds the sum of the
// eight taps it meets for each of the 256 byte values.
//
//	DSD64 → 176.4 kHz: 512 taps, -6 dB at 37 kHz (pass band to ~23 kHz)
//	DSD64 → 88.2 kHz:  1024 taps, -6 dB at 28 kHz
//
// Higher DSD rates scale the taps with the rate, so the filter has the
// same shape in Hz. The output is at the signal's level: DSD's 0 dB (50 %
// modulation) comes out at -6 dBFS, the format's +6 dB peak at full scale.
type Decimator struct {
	ch     int
	step   int // input bytes per output sample
	nbytes int // filter length in bytes
	tables [][256]float32
	// rtables is tables oldest byte first, the order the window is read in.
	rtables [][256]float32
	hist    [][]byte // per channel, nbytes twice over (no wrap on read)
	at      int      // next write position in hist
	pend    int      // input bytes since the last output
}

// NewDecimator makes a converter from DSD at dsdRate to PCM at pcmRate;
// dsdRate/pcmRate must be a multiple of 8.
func NewDecimator(dsdRate, channels, pcmRate int) (*Decimator, error) {
	if pcmRate <= 0 || dsdRate%pcmRate != 0 || dsdRate/pcmRate%8 != 0 || channels < 1 {
		return nil, fmt.Errorf("DSD %d Hz to PCM %d Hz: not a whole number of bytes per sample", dsdRate, pcmRate)
	}
	scale := float64(dsdRate) / Base
	taps, cutoff := 512, 37000.0
	if pcmRate < 150000 {
		taps, cutoff = 1024, 28000
	}
	taps = int(float64(taps)*scale+7) / 8 * 8
	h := kaiserSinc(taps, cutoff/float64(dsdRate), 8)
	d := &Decimator{ch: channels, step: dsdRate / pcmRate / 8, nbytes: taps / 8}
	d.tables = make([][256]float32, d.nbytes)
	for j := range d.tables {
		for v := 0; v < 256; v++ {
			s := 0.0
			for p := 0; p < 8; p++ {
				// Byte j back in time; its LSB is the latest of its samples.
				k := 8*j + (7 - p)
				if v>>(7-p)&1 != 0 {
					s += h[k]
				} else {
					s -= h[k]
				}
			}
			d.tables[j][v] = float32(s)
		}
	}
	d.rtables = make([][256]float32, d.nbytes)
	for j := range d.tables {
		d.rtables[d.nbytes-1-j] = d.tables[j]
	}
	d.hist = make([][]byte, channels)
	for c := range d.hist {
		d.hist[c] = make([]byte, 2*d.nbytes)
		for i := range d.hist[c] {
			d.hist[c][i] = Silence
		}
	}
	return d, nil
}

// kaiserSinc is a low-pass FIR of n taps with cutoff fc (cycles per
// sample), normalised to unity gain at DC.
func kaiserSinc(n int, fc, beta float64) []float64 {
	h := make([]float64, n)
	m := float64(n-1) / 2
	sum := 0.0
	for i := range h {
		x := float64(i) - m
		s := 2 * fc
		if x != 0 {
			s = math.Sin(2*math.Pi*fc*x) / (math.Pi * x)
		}
		r := x / m
		w := bessel0(beta*math.Sqrt(max(0, 1-r*r))) / bessel0(beta)
		h[i] = s * w
		sum += h[i]
	}
	for i := range h {
		h[i] /= sum
	}
	return h
}

func bessel0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 50; k++ {
		term *= (x / (2 * float64(k))) * (x / (2 * float64(k)))
		sum += term
		if term < 1e-12*sum {
			break
		}
	}
	return sum
}

// Ratio is how many input byte frames make one output frame.
func (d *Decimator) Ratio() int { return d.step }

// Process filters byte frames from in and writes interleaved PCM frames
// to out (values in [-1, 1]); it returns how many frames it wrote. out
// needs room for len(in)/channels/Ratio()+1 frames.
func (d *Decimator) Process(in []byte, out []float32) int {
	ch, nb := d.ch, d.nbytes
	n := 0
	for i := 0; i+ch <= len(in); i += ch {
		for c := 0; c < ch; c++ {
			h := d.hist[c]
			h[d.at] = in[i+c]
			h[d.at+nb] = in[i+c]
		}
		d.at++
		if d.at == nb {
			d.at = 0
		}
		d.pend++
		if d.pend < d.step {
			continue
		}
		d.pend = 0
		if (n+1)*ch > len(out) {
			break
		}
		// The window is the last nb bytes, oldest first.
		for c := 0; c < ch; c++ {
			w := d.hist[c][d.at : d.at+nb]
			tb := d.rtables[:len(w)]
			var s float32
			for k, v := range w {
				s += tb[k][v]
			}
			out[n*ch+c] = s
		}
		n++
	}
	return n
}

// Reset forgets the history (after a seek).
func (d *Decimator) Reset() {
	for c := range d.hist {
		for i := range d.hist[c] {
			d.hist[c][i] = Silence
		}
	}
	d.at, d.pend = 0, 0
}
