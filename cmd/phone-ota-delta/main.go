// phone-ota-delta makes and applies incremental system updates
// (25-system-update.md, package ota/delta).
//
// On the build host, from an earlier slot image (or the A/B disk of an
// earlier release, gzip-compressed or not) to this build's:
//
//	phone-ota-delta generate --source OLD --target NEW.ext4.verity --out rootfs.kdelta [--report report.json]
//
// On the phone, from the incremental bundle's install hook: the running
// slot through its dm-verity device, the inactive slot written, with a
// checkpoint that lets the next install of the same payload resume, the
// payload read from the mounted bundle's own device (-direct=false: as a
// file), and a file whose appearance stops it at a checkpoint (the shell
// pausing an automatic install):
//
//	phone-ota-delta apply --payload rootfs.kdelta --source /dev/mapper/root_a --source-desc rootfs_a.verity \
//	    --target /dev/disk/by-partlabel/rootfs_b --target-desc NEW.verity.desc --checkpoint rootfs_b.progress \
//	    --stop /run/phone-ota-delta.stop
//
//	phone-ota-delta info rootfs.kdelta
//
// The payload formats it applies, one a line (for the release tool), and
// whether it applies a payload's (the install hook asks the system's own
// applier, /usr/bin/phone-ota-delta, before it falls back to the one in the
// bundle), and whether it takes --checkpoint and --stop (an older one does
// not; one line each it takes):
//
//	phone-ota-delta formats
//	phone-ota-delta can-apply rootfs.kdelta
//	phone-ota-delta can-resume
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
	"github.com/kario-os/kario/ota/delta/squashfs"
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
	case "can-resume":
		fmt.Println("checkpoint")
		fmt.Println("stop")
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "phone-ota-delta:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: phone-ota-delta generate|apply|info ... (-h for each) | formats | can-apply PAYLOAD | can-resume")
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
	// In a mounted bundle the header comes from the bundle's device, a
	// block of it: read through the mount, the kernel would fetch the
	// whole first block, which the applier then fetches again.
	var r io.Reader = f
	if b, err := bundlePayload(f, squashfs.Align); err == nil {
		defer b.Close()
		r = b
	}
	format, err := delta.PayloadFormat(r)
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
	format := fl.Int("format", delta.Format, "the payload format: 1 for appliers that read only it, 2 with patches in pieces")
	piece := fl.Int("piece", delta.DefaultPiece, "bytes of a file each piece of its patch makes (format 2)")
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
		Workers: *workers, MaxDiff: *maxDiff, NoVerify: *noVerify, Format: *format, Piece: *piece,
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

// directBuffer is what one read of the bundle's device asks for while the
// payload is applied.
const directBuffer = 1 << 20

func apply(args []string) error {
	fl := flag.NewFlagSet("apply", flag.ExitOnError)
	payload := fl.String("payload", "", "the payload (- for standard input)")
	source := fl.String("source", "", "the running slot (its dm-verity device on a phone)")
	sourceDesc := fl.String("source-desc", "", "the running slot's verity descriptor (otherwise the source is hashed whole)")
	target := fl.String("target", "", "the slot to write")
	targetDesc := fl.String("target-desc", "", "the target's signed verity descriptor")
	checkpoint := fl.String("checkpoint", "", "record the progress here, and resume from it (the state partition's rootfs_<slot>.progress)")
	checkpointBytes := fl.Uint64("checkpoint-bytes", 0, "bytes written between two checkpoints (default 32 MiB)")
	stop := fl.String("stop", "", "once this file exists, stop before the next operation or piece, with a checkpoint there")
	direct := fl.Bool("direct", true, "read a payload in a mounted bundle straight from the bundle's device, each block once")
	_ = fl.Parse(args)
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "phone-ota-delta: "+format+"\n", a...) }
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
	var in io.Reader = bufio.NewReaderSize(os.Stdin, 1<<20)
	var bundle *bundleStream
	if *payload != "-" {
		f, err := os.Open(*payload)
		if err != nil {
			return err
		}
		defer f.Close()
		in = &seekBuffer{f: f, r: bufio.NewReaderSize(f, 1<<20)}
		if *direct {
			if bundle, err = bundlePayload(f, directBuffer); err != nil {
				logf("reading the payload as a file: %v", err)
			} else {
				defer bundle.Close()
				in = bundle
				logf("reading the payload from the bundle's device %s: %d bytes there for its %d (%d blocks compressed)", bundle.path, bundle.onDev, bundle.size, bundle.compressed)
			}
		}
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
	var stopped func() bool
	if *stop != "" {
		stopped = func() bool {
			_, err := os.Stat(*stop)
			return err == nil
		}
	}
	res, err := delta.Apply(in, delta.ApplyConfig{Source: src, SourceSize: srcSize, SourceDesc: have,
		Target: dst, TargetSize: dstSize, TargetDesc: *want, Checkpoint: *checkpoint, CheckpointBytes: *checkpointBytes, Stop: stopped, Log: logf})
	if bundle != nil {
		logf("payload read: %d bytes from the bundle's device, %d through the file", bundle.DevRead, bundle.FileRead)
	}
	if err != nil {
		return err
	}
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	resumed := ""
	if res.Resumed > 0 {
		resumed = fmt.Sprintf(", resumed at operation %d of %d (%s of payload data not read)", res.Resumed, res.Ops, mb(int64(res.Skipped)))
	}
	// Maxrss is in KiB on Linux.
	fmt.Printf("PHONE-OTA-DELTA: applied %d operations, %s of payload data, %s written in %s, peak memory %s%s\n", res.Ops, mb(int64(res.Data)), mb(int64(res.Written)), res.Duration.Round(1e6), mb(ru.Maxrss*1024), resumed)
	return nil
}

// seekBuffer reads a payload file through a buffer and seeks forward in it
// (delta.Apply skips the data of the operations a resumed install did):
// within what is buffered, or past it on the file.
type seekBuffer struct {
	f *os.File
	r *bufio.Reader
}

func (s *seekBuffer) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *seekBuffer) Seek(off int64, whence int) (int64, error) {
	if whence != io.SeekCurrent || off < 0 {
		return 0, errors.New("seekBuffer: only forward from the current position")
	}
	if off <= int64(s.r.Buffered()) {
		if _, err := io.CopyN(io.Discard, s.r, off); err != nil {
			return 0, err
		}
		pos, err := s.f.Seek(0, io.SeekCurrent)
		return pos - int64(s.r.Buffered()), err
	}
	pos, err := s.f.Seek(off-int64(s.r.Buffered()), io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	s.r.Reset(s.f)
	return pos, nil
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
	pieces := 0
	for _, op := range m.Ops {
		counts[op.Type]++
		pieces += len(op.Pieces)
	}
	raw, _ := json.MarshalIndent(map[string]any{"format": m.Format, "source": m.Source, "target": m.Target, "verity": m.Verity,
		"operations": counts, "pieces": pieces, "data_bytes": m.DataSize()}, "", "  ")
	fmt.Println(string(raw))
	return nil
}
