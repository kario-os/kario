package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The soak run: PHONE_SOAK (a duration) of soak.script rounds on the full
// stack, sampling the Shell and the system services after each round.
const (
	soakEnv    = "PHONE_SOAK"
	soakOutEnv = "PHONE_SOAK_OUT"
	soakScript = "soak.script"
)

// How much a resource may grow over a soak run before it counts as a leak:
// the lowest sample of the last third against the highest of the first
// (after the first round), so a sawtooth (the garbage collector, caches
// filling once) passes and only sustained growth fails.
var soakLimits = map[string]float64{
	"shell_fds":        16,
	"shell_goroutines": 64,
	"shell_heap_mb":    16,
	"shell_rss_mb":     48,
	"svc_fds":          8,
	"svc_rss_mb":       16,
}

// The system services sampled (the simulated cloud servers keep what they
// are sent, by design).
var soakServices = []string{"phone-mediad", "phone-keystored", "phone-audiod", "phone-imed", "phone-nfcd", "phone-smsd",
	"phone-pushd", "phone-telephonyd"}

type soakSample struct {
	round   int
	at      time.Duration
	values  map[string]float64
	svcPIDs map[string]int
}

func procStatusKB(pid, field string) float64 {
	raw, err := os.ReadFile(filepath.Join("/proc", pid, "status"))
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(l, field+":"); ok {
			kb, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 64)
			return kb
		}
	}
	return 0
}

func procFDs(pid string) float64 {
	ents, err := os.ReadDir(filepath.Join("/proc", pid, "fd"))
	if err != nil {
		return 0
	}
	return float64(len(ents))
}

func soakSampleNow(round int, start time.Time, svcs *e2eServices) soakSample {
	s := soakSample{round: round, at: time.Since(start), values: map[string]float64{}, svcPIDs: svcs.pids()}
	s.values["shell_fds"] = procFDs("self")
	s.values["shell_goroutines"] = float64(runtime.NumGoroutine())
	s.values["shell_heap_mb"] = float64(heapInUse()) / (1 << 20)
	s.values["shell_rss_mb"] = procStatusKB("self", "VmRSS") / 1024
	for _, name := range soakServices {
		pid, ok := s.svcPIDs[name]
		if !ok {
			continue
		}
		p := strconv.Itoa(pid)
		s.values[name+"_fds"] = procFDs(p)
		s.values[name+"_rss_mb"] = procStatusKB(p, "VmRSS") / 1024
	}
	return s
}

// soakGrowth is how much a series grew (see soakLimits); samples after a
// restart of the service are a new process and are left out.
func soakGrowth(samples []soakSample, key, svc string) (float64, bool) {
	var vals []float64
	pid := 0
	for _, s := range samples[1:] {
		if svc != "" {
			if pid == 0 {
				pid = s.svcPIDs[svc]
			}
			if s.svcPIDs[svc] != pid {
				break
			}
		}
		if v, ok := s.values[key]; ok {
			vals = append(vals, v)
		}
	}
	if len(vals) < 6 {
		return 0, false
	}
	third := len(vals) / 3
	return slices.Min(vals[len(vals)-third:]) - slices.Max(vals[:third]), true
}

func TestSoak(t *testing.T) {
	d, err := time.ParseDuration(os.Getenv(soakEnv))
	if err != nil {
		t.Skip("soak run: set " + soakEnv + " to a duration (make soak)")
	}
	script, err := os.ReadFile(soakScript)
	if err != nil {
		t.Fatal(err)
	}
	c, svcs := e2eStack(t)
	out := os.Getenv(soakOutEnv)
	if out == "" {
		out = filepath.Join(t.TempDir(), "soak.csv")
	}
	csv, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer csv.Close()
	keys := []string{"shell_fds", "shell_goroutines", "shell_heap_mb", "shell_rss_mb"}
	for _, name := range soakServices {
		keys = append(keys, name+"_fds", name+"_rss_mb")
	}
	fmt.Fprintf(csv, "round,seconds,failed,%s\n", strings.Join(keys, ","))
	start := time.Now()
	var samples []soakSample
	failed := 0
	r := &Runner{C: c}
	for round := 0; time.Since(start) < d || round < 2; round++ {
		var log strings.Builder
		r.Out = &log
		ok := r.Run(string(script))
		if !ok {
			failed++
			t.Errorf("round %d failed:\n%s", round, log.String())
		}
		r.pump(time.Second)
		s := soakSampleNow(round, start, svcs)
		samples = append(samples, s)
		row := []string{strconv.Itoa(round), strconv.Itoa(int(s.at.Seconds())), strconv.FormatBool(!ok)}
		for _, k := range keys {
			row = append(row, strconv.FormatFloat(s.values[k], 'f', 1, 64))
		}
		fmt.Fprintln(csv, strings.Join(row, ","))
		if failed >= 3 {
			t.Fatal("three rounds failed; stopping")
		}
	}
	first, last := samples[0], samples[len(samples)-1]
	t.Logf("%d rounds in %v (%d failed); samples in %s", len(samples), time.Since(start).Round(time.Second), failed, out)
	svcs.mu.Lock()
	t.Logf("service restarts: %v", svcs.restarts)
	svcs.mu.Unlock()
	for _, k := range keys {
		svc, limit := "", soakLimits[k]
		for _, name := range soakServices {
			if strings.HasPrefix(k, name+"_") {
				svc, limit = name, soakLimits["svc_"+strings.TrimPrefix(k, name+"_")]
			}
		}
		g, ok := soakGrowth(samples, k, svc)
		t.Logf("%-28s first %8.1f  last %8.1f  sustained growth %+.1f (limit %v)", k, first.values[k], last.values[k], g, limit)
		if ok && g > limit {
			t.Errorf("%s grew by %.1f over the run (limit %v): a leak", k, g, limit)
		}
	}
}
