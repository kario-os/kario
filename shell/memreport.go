package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type procMem struct {
	pid          int
	name         string
	pss, huge    int // kB
	anon, file   int // kB, where the kernel reports them (Pss_Anon, Pss_File)
	privateDirty int
}

func readProcMem(pid int) (procMem, bool) {
	m := procMem{pid: pid}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return m, false
	}
	m.name = strings.TrimSpace(string(comm))
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		return m, false
	}
	for _, l := range strings.Split(string(raw), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.Atoi(f[1])
		switch f[0] {
		case "Pss:":
			m.pss = v
		case "AnonHugePages:":
			m.huge = v
		case "Pss_Anon:":
			m.anon = v
		case "Pss_File:":
			m.file = v
		case "Private_Dirty:":
			m.privateDirty = v
		}
	}
	return m, true
}

// descendants are the processes below pid: the services and apps this Shell
// started, through the sandbox launcher too.
func descendants(pid int) []int {
	children := map[int][]int{}
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, d := range dirs {
		p, err := strconv.Atoi(filepath.Base(d))
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(d + "/stat")
		if err != nil {
			continue
		}
		s := string(raw)
		f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		if len(f) > 1 {
			pp, _ := strconv.Atoi(f[1])
			children[pp] = append(children[pp], p)
		}
	}
	var out []int
	queue := []int{pid}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			out = append(out, c)
			queue = append(queue, c)
		}
	}
	return out
}

// MemReport lists the PSS of the Shell and everything it started, with the
// part in transparent huge pages, and the kernel's THP mode.
func MemReport() string {
	var ms []procMem
	if m, ok := readProcMem(os.Getpid()); ok {
		ms = append(ms, m)
	}
	for _, p := range descendants(os.Getpid()) {
		if m, ok := readProcMem(p); ok && m.pss > 0 {
			ms = append(ms, m)
		}
	}
	sort.SliceStable(ms[min(1, len(ms)):], func(a, b int) bool { return ms[1+a].pss > ms[1+b].pss })
	thp, err := os.ReadFile("/sys/kernel/mm/transparent_hugepage/enabled")
	if err != nil {
		thp = []byte("not in this kernel")
	}
	var b strings.Builder
	total, huge := 0, 0
	fmt.Fprintf(&b, "PHONE-MEM: page size %d KB, transparent huge pages %s\n", os.Getpagesize()/1024, strings.TrimSpace(string(thp)))
	mb := func(kb int) float64 { return float64(kb) / 1024 }
	for _, m := range ms {
		fmt.Fprintf(&b, "PHONE-MEM: %-16s %6.1f MB PSS (anon %.1f, file %.1f, private dirty %.1f, huge %.1f)\n",
			m.name, mb(m.pss), mb(m.anon), mb(m.file), mb(m.privateDirty), mb(m.huge))
		total += m.pss
		huge += m.huge
	}
	fmt.Fprintf(&b, "PHONE-MEM: %d processes, %.1f MB PSS, %.1f MB in huge pages", len(ms), mb(total), mb(huge))
	if len(ms) > 1 {
		small := ms[len(ms)-1]
		for _, m := range ms[1:] {
			if strings.HasPrefix(m.name, "phone-app-") && m.pss < small.pss || !strings.HasPrefix(small.name, "phone-app-") {
				small = m
			}
		}
		for _, l := range mappings(small.pid, 6) {
			fmt.Fprintf(&b, "\nPHONE-MEM: %s mapping %s", small.name, l)
		}
	}
	return b.String()
}

// mappings lists a process's largest mappings by PSS, grouped by name.
func mappings(pid, n int) []string {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps", pid))
	if err != nil {
		return nil
	}
	type agg struct{ pss, rss, dirty int }
	by := map[string]*agg{}
	cur := ""
	for _, l := range strings.Split(string(raw), "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if strings.Contains(f[0], "-") && len(f) >= 5 {
			cur = "[anon]"
			if len(f) >= 6 {
				cur = filepath.Base(f[5])
			}
			if by[cur] == nil {
				by[cur] = &agg{}
			}
			continue
		}
		if len(f) < 2 || by[cur] == nil {
			continue
		}
		v, _ := strconv.Atoi(f[1])
		switch f[0] {
		case "Pss:":
			by[cur].pss += v
		case "Rss:":
			by[cur].rss += v
		case "Private_Dirty:":
			by[cur].dirty += v
		}
	}
	names := make([]string, 0, len(by))
	for k := range by {
		names = append(names, k)
	}
	sort.Slice(names, func(a, b int) bool { return by[names[a]].pss > by[names[b]].pss })
	var out []string
	for _, k := range names[:min(n, len(names))] {
		a := by[k]
		out = append(out, fmt.Sprintf("%-24s PSS %6.1f MB, RSS %6.1f MB, private dirty %6.1f MB", k, float64(a.pss)/1024, float64(a.rss)/1024, float64(a.dirty)/1024))
	}
	return out
}
