package hifi

import "math"

// resampler converts stereo float PCM between rates by a rational factor
// l/m with a polyphase Kaiser-windowed sinc (pass band to 45 % of the
// lower rate, about 80 dB stop band): the mixer's path, for tracks that
// play together with other sounds at 48 kHz.
type resampler struct {
	l, m  int64
	half  int
	coef  [][]float32 // per phase
	buf   [2][]float32
	start int64 // input index of buf[*][0]
	num   int64 // next output's input position times l
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func newResampler(in, out int) *resampler {
	g := gcd(int64(in), int64(out))
	r := &resampler{l: int64(out) / g, m: int64(in) / g}
	ratio := math.Min(1, float64(out)/float64(in))
	r.half = int(math.Ceil(16 / ratio))
	fc := 0.45 * ratio
	taps := 2 * r.half
	r.coef = make([][]float32, r.l)
	for ph := range r.coef {
		c := make([]float32, taps)
		frac := float64(ph) / float64(r.l)
		sum := 0.0
		w := make([]float64, taps)
		for j := range w {
			d := float64(j-r.half+1) - frac
			s := 2 * fc
			if d != 0 {
				s = math.Sin(2*math.Pi*fc*d) / (math.Pi * d)
			}
			x := d / float64(r.half)
			win := 0.0
			if x > -1 && x < 1 {
				win = bessel(8*math.Sqrt(1-x*x)) / bessel(8)
			}
			w[j] = s * win
			sum += w[j]
		}
		for j := range c {
			c[j] = float32(w[j] / sum)
		}
		r.coef[ph] = c
	}
	r.reset()
	return r
}

func bessel(x float64) float64 {
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

// reset forgets the input (a seek, a new track): the next output lines
// up with the next input.
func (r *resampler) reset() {
	for c := range r.buf {
		r.buf[c] = make([]float32, r.half-1, 8192)
	}
	r.start, r.num = -int64(r.half-1), 0
}

func (r *resampler) push(left, right []float32) {
	r.buf[0] = append(r.buf[0], left...)
	r.buf[1] = append(r.buf[1], right...)
}

// pull writes as many output frames as the input allows.
func (r *resampler) pull(left, right []float64) int {
	n := 0
	taps := 2 * r.half
	for n < len(left) {
		i := r.num / r.l
		lo := int(i - int64(r.half) + 1 - r.start)
		if lo+taps > len(r.buf[0]) {
			break
		}
		c := r.coef[r.num%r.l]
		a, b := r.buf[0][lo:lo+taps], r.buf[1][lo:lo+taps]
		var x, y float32
		for j, w := range c {
			x += w * a[j]
			y += w * b[j]
		}
		left[n], right[n] = float64(x), float64(y)
		n++
		r.num += r.m
	}
	if drop := int(r.num/r.l-int64(r.half)+1-r.start) - 1; drop > 4096 {
		for c := range r.buf {
			r.buf[c] = append(r.buf[c][:0], r.buf[c][drop:]...)
		}
		r.start += int64(drop)
	}
	return n
}

// pending says how many input frames wait beyond those consumed.
func (r *resampler) pending() int {
	return len(r.buf[0]) - int(r.num/r.l-r.start)
}
