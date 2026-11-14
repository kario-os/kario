// phone-ota-delta makes and applies incremental system updates
// (25-system-update.md, package ota/delta).
//
// On the build host, from an earlier slot image (or the A/B disk of an
// earlier release, gzip-compressed or not) to this build's:
//
//	phone-ota-delta generate --source OLD --target NEW.ext4.verity --out rootfs.kdelta [--report report.json]
//
// On the phone, from the incremental bundle's install hook: the running
// slot through its dm-verity device, the inactive slot written:
//
//	phone-ota-delta apply --payload rootfs.kdelta --source /dev/mapper/root_a --source-desc rootfs_a.verity \
//	    --target /dev/disk/by-partlabel/rootfs_b --target-desc NEW.verity.desc
//
//	phone-ota-delta info rootfs.kdelta
//
// The payload formats it applies, one a line (for the release tool), and
// whether it applies a payload's (the install hook asks the system's own
// applier, /usr/bin/phone-ota-delta, before it falls back to the one in the
// bundle):
//
//	phone-ota-delta formats
//	phone-ota-delta can-apply rootfs.kdelta
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"syscall"

	"github.com/kario-os/kario/ota/delta"
	"github.com/kario-os/kario/ota/verity"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "generate":
		err = generate(os.Args[2:])
	case "apply":
		err = apply(os.Args[2:])
	case "info":
		err = info(os.Args[2:])
	case "formats":
		for _, f := range delta.Formats {
			fmt.Println(f)
		}
	case "can-apply":
		err = canApply(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "phone-ota-delta:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: phone-ota-delta generate|apply|info ... (-h for each) | formats | can-apply PAYLOAD")
	os.Exit(2)
}

// canApply prints the payload's format and exits 0 when this program
// applies it, 3 when it does not: the install hook asks the system's
// applier, so the hook needs no tool of its own to read the header.
func canApply(args []string) error {
	if len(args) != 1 {
		usage()
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	format, err := delta.PayloadFormat(f)
	if err != nil {
		return err
	}
	if !slices.Contains(delta.Formats, format) {
		fmt.Fprintf(os.Stderr, "phone-ota-delta: payload format %d; this program applies %v\n", format, delta.Formats)
		os.Exit(3)
	}
	fmt.Println(format)
	return nil
}

func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/1e6) }

func generate(args []string) error {
	fl := flag.NewFlagSet("generate", flag.ExitOnError)
	source := fl.String("source", "", "the earlier slot image, or an A/B disk image (.wic, .wic.gz)")
	sourceDesc := fl.String("source-desc", "", "the earlier slot's verity descriptor (default: <source>.desc, or the disk's state partition)")
	slot := fl.String("source-slot", "a", "the slot of an A/B disk image to start from")
	sourceVersion := fl.String("source-version", "", "the earlier version, for the record")
	target := fl.String("target", "", "this build's slot image (ext4.verity)")
	targetDesc := fl.String("target-desc", "", "its verity descriptor (default: <target>.desc)")
	targetVersion := fl.String("target-version", "", "this build's version, for the record")
	out := fl.String("out", "", "the payload to write")
	reportPath := fl.String("report", "", "write the report (JSON) here as well")
	workers := fl.Int("workers", 0, "files diffed at once (default: up to 8)")
	maxDiff := fl.Int64("max-diff", 0, "files bigger than this are not diffed (default 64 MiB)")
	noVerify := fl.Bool("no-verify", false, "do not apply the payload in memory before writing it")
	_ = fl.Parse(args)
	if *source == "" || *target == "" || *out == "" {
		fl.Usage()
		os.Exit(2)
	}
	src, err := delta.LoadSlot(*source, *sourceDesc, *slot)
	if err != nil {
		return fmt.Errorf("%s: %w", *source, err)
	}
	dst, err := delta.LoadSlot(*target, *targetDesc, "a")
	if err != nil {
		return fmt.Errorf("%s: %w", *target, err)
	}
	if dst.Desc == nil {
		return errors.New("the target's verity descriptor is needed (--target-desc)")
	}
	f, err := os.Create(*out + ".tmp")
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	rep, err := delta.Generate(delta.GenerateConfig{Source: src, Target: dst, SourceVersion: *sourceVersion, TargetVersion: *targetVersion,
		Workers: *workers, MaxDiff: *maxDiff, NoVerify: *noVerify,
		Log: func(format string, a ...any) { fmt.Fprintf(os.Stderr, "phone-ota-delta: "+format+"\n", a...) }}, w)
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(*out + ".tmp")
		return err
	}
	if err := os.Rename(*out+".tmp", *out); err != nil {
		return err
	}
	size := int64(rep.Target.Size)
	fmt.Printf("PHONE-OTA-DELTA: payload %s, %.1f%% of the %s slot image; an adaptive install of the full bundle downloads more than about %s (index %s, %d blocks not in the source: %s uncompressed)\n",
		mb(rep.Payload), float64(rep.Payload)*100/float64(size), mb(size), mb(rep.Adaptive.Estimate), mb(rep.Adaptive.IndexBytes),
		rep.Adaptive.MissingBlocks, mb(rep.Adaptive.MissingBytes))
	var types []string
	for t := range rep.Ops {
		types = append(types, t)
	}
	sort.Strings(types)
	line := "PHONE-OTA-DELTA: operations:"
	for _, t := range types {
		s := rep.Ops[t]
		line += fmt.Sprintf(" %s %d (%d blocks, %s)", t, s.Count, s.Blocks, mb(int64(s.Data)))
	}
	fmt.Println(line)
	fmt.Printf("PHONE-OTA-DELTA: %d files changed or added\n", len(rep.Files))
	for i, fs := range rep.Files {
		if i == 15 {
			break
		}
		from := ""
		if fs.From != "" && fs.From != fs.Path {
			from = " from " + fs.From
		}
		cost := "-"
		if fs.Bytes >= 0 {
			cost = mb(fs.Bytes)
		}
		fmt.Printf("PHONE-OTA-DELTA:   %9s  %-7s %s (%s)%s\n", cost, fs.Method, fs.Path, mb(fs.Size), from)
	}
	if *reportPath != "" {
		raw, _ := json.MarshalIndent(rep, "", "  ")
		return os.WriteFile(*reportPath, append(raw, '\n'), 0o644)
	}
	return nil
}

func size(f *os.File) (int64, error) {
	n, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	_, err = f.Seek(0, io.SeekStart)
	return n, err
}

func readDesc(path string) (*verity.Descriptor, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d, err := verity.ParseDescriptor(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &d, nil
}

func apply(args []string) error {
	fl := flag.NewFlagSet("apply", flag.ExitOnError)
	payload := fl.String("payload", "", "the payload (- for standard input)")
	source := fl.String("source", "", "the running slot (its dm-verity device on a phone)")
	sourceDesc := fl.String("source-desc", "", "the running slot's verity descriptor (otherwise the source is hashed whole)")
	target := fl.String("target", "", "the slot to write")
	targetDesc := fl.String("target-desc", "", "the target's signed verity descriptor")
	_ = fl.Parse(args)
	if *payload == "" || *source == "" || *target == "" || *targetDesc == "" {
		fl.Usage()
		os.Exit(2)
	}
	want, err := readDesc(*targetDesc)
	if err != nil {
		return err
	}
	var have *verity.Descriptor
	if *sourceDesc != "" {
		if have, err = readDesc(*sourceDesc); err != nil {
			return err
		}
	}
	var in io.Reader = os.Stdin
	if *payload != "-" {
		f, err := os.Open(*payload)
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	src, err := os.Open(*source)
	if err != nil {
		return err
	}
	defer src.Close()
	srcSize, err := size(src)
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(*target, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer dst.Close()
	dstSize, err := size(dst)
	if err != nil {
		return err
	}
	res, err := delta.Apply(bufio.NewReaderSize(in, 1<<20), delta.ApplyConfig{Source: src, SourceSize: srcSize, SourceDesc: have,
		Target: dst, TargetSize: dstSize, TargetDesc: *want,
		Log: func(format string, a ...any) { fmt.Fprintf(os.Stderr, "phone-ota-delta: "+format+"\n", a...) }})
	if err != nil {
		return err
	}
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	// Maxrss is in KiB on Linux.
	fmt.Printf("PHONE-OTA-DELTA: applied %d operations, %s of payload data, %s written in %s, peak memory %s\n", res.Ops, mb(int64(res.Data)), mb(int64(res.Written)), res.Duration.Round(1e6), mb(ru.Maxrss*1024))
	return nil
}

func info(args []string) error {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: phone-ota-delta info PAYLOAD")
		os.Exit(2)
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	m, err := delta.ReadManifest(bufio.NewReader(f))
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, op := range m.Ops {
		counts[op.Type]++
	}
	raw, _ := json.MarshalIndent(map[string]any{"format": m.Format, "source": m.Source, "target": m.Target, "verity": m.Verity,
		"operations": counts, "data_bytes": m.DataSize()}, "", "  ")
	fmt.Println(string(raw))
	return nil
}
