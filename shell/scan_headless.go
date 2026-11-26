package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/kario-os/kario/svc"
)

// The scanner's selftest steps. phone-mediad decodes what its camera sees
// (programs/mediad/scan.go); scan-code holds a QR code up to the camera (the
// synthetic one, or the emulator's vivid: a camera of the kernel's test
// driver), scan-tries waits until the open scan has decoded frames, and
// scan-report prints the decoder's numbers.

type scanStats struct {
	Tries   int     `json:"tries"`
	Found   int     `json:"found"`
	TotalMS float64 `json:"total_ms"`
	MaxMS   float64 `json:"max_ms"`
	W       int     `json:"w"`
	H       int     `json:"h"`
	Open    int     `json:"open_tries"`
}

func (r *Runner) scanStats() (string, scanStats, error) {
	var info struct {
		Camera string    `json:"camera"`
		Scan   scanStats `json:"scan"`
	}
	err := r.C.broker.rpc(svc.Mediad, svc.MediaInfo, nil, &info)
	return info.Camera, info.Scan, err
}

func (r *Runner) scanStep(cmd, arg string) error {
	switch cmd {
	case "scan-code":
		text := strings.TrimSpace(arg)
		if text == "" {
			return fmt.Errorf("usage: scan-code <text>|none")
		}
		if text == "none" {
			text = ""
		}
		return r.C.broker.rpc(svc.Mediad, svc.MediaSimCode, map[string]string{"text": text}, nil)
	case "scan-tries":
		rest, ms := lastNum(arg, 5000)
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return fmt.Errorf("usage: scan-tries <n> [ms]")
		}
		got := -1
		if _, ok := r.waitFor(func(string) bool {
			if _, st, err := r.scanStats(); err == nil {
				got = st.Open
			}
			return got >= n
		}, ms); !ok {
			return fmt.Errorf("the open scan has decoded %d frames, want %d (-1: no scan open)", got, n)
		}
	case "scan-report":
		camera, st, err := r.scanStats()
		if err != nil {
			return err
		}
		avg := 0.0
		if st.Tries > 0 {
			avg = st.TotalMS / float64(st.Tries)
		}
		fmt.Fprintf(r.Out, "PHONE-SCAN: camera=%s frame=%dx%d tries=%d read=%d decode_avg_ms=%.1f decode_max_ms=%.1f\n",
			camera, st.W, st.H, st.Tries, st.Found, avg, st.MaxMS)
	}
	return nil
}
