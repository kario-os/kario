package main

import (
	"bytes"
	"debug/elf"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/jsrt/nativetest"
)

// kario symbolize maps a backtrace's offsets to functions and source lines
// with the unstripped library it names by build-id: from a log, a crash
// record, the store's list of them, or frames pasted alone; a library
// found in a directory (a project's native/) counts as its module; one of
// another build is not taken for it.
func TestSymbolize(t *testing.T) {
	if jsrt.HostArch() == "" {
		t.Skip("modules are built for arm64 and x86_64 only")
	}
	probe := nativetest.Build(t)
	f, err := elf.NewFile(bytes.NewReader(probe))
	if err != nil {
		t.Fatal(err)
	}
	syms, _ := f.Symbols()
	var upper uint64
	for _, s := range syms {
		if s.Name == "upper" {
			upper = s.Value
		}
	}
	id := jsrt.BuildID(f)
	if upper == 0 || id == "" {
		t.Fatalf("probe: upper at %#x, build-id %q", upper, id)
	}
	dir := filepath.Join(t.TempDir(), "native", jsrt.HostArch())
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "probe.so"), probe, 0o644); err != nil {
		t.Fatal(err)
	}
	head := "kario-jsrt: crashed in native:probe.upper: SIGSEGV at 0x0"
	bt := []string{head,
		fmt.Sprintf("kario-jsrt: #0 native:probe +0x%x build-id %s", upper+4, id),
		"kario-jsrt: #1 kario-jsrt +0x2a1b0 build-id 77ac",
		"kario-jsrt: #2 0x7f0000001000",
		jsrt.BacktraceEnd}
	record, _ := json.Marshal(map[string]any{"reason": "signal: segmentation fault", "backtrace": bt})
	list, _ := json.Marshal([]map[string]any{{"log": append([]string{"earlier"}, bt...)}, {"log": []string{"no crash here"}}})
	for name, in := range map[string]string{
		"log":    "starting\n" + strings.Join(bt, "\n") + "\n",
		"record": string(record),
		"list":   string(list),
		"pasted": strings.Join(bt[1:4], "\n"),
	} {
		var out bytes.Buffer
		if err := symbolize(strings.NewReader(in), &out, []string{filepath.Dir(dir)}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := out.String()
		if !strings.Contains(got, fmt.Sprintf("#0 native:probe +0x%x upper (probe.c:", upper+4)) || !strings.Contains(got, "#1 kario-jsrt +0x2a1b0\n") ||
			!strings.Contains(got, "#2 0x7f0000001000") || strings.Contains(got, "no symbols") {
			t.Errorf("%s:\n%s", name, got)
		}
	}
	other := filepath.Join(t.TempDir(), "probe.so")
	_ = os.WriteFile(other, nativetest.Build(t, "PROBE_INIT_FAILS"), 0o644)
	var out bytes.Buffer
	if err := symbolize(strings.NewReader(strings.Join(bt, "\n")), &out, []string{other}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "upper (") || !strings.Contains(got, "no symbols for native:probe (build-id "+id+")") {
		t.Errorf("another build:\n%s", got)
	}
	if err := symbolize(strings.NewReader("nothing native"), &out, nil); err == nil {
		t.Error("no backtrace: no error")
	}
}
