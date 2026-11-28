#!/usr/bin/env python3
"""Compare the screendumps of two phone.gui=1 runs with the same keys
(qemu-gui-drive.py), e.g. phone-paint drawing on the CPU and composing on
the GPU (42-gpu-rendering.md §7): for each picture, the pixels that differ
by more than LEVELS in a channel (gpu.rs's LEVELS: the GPU path lays the
top layer over the page where the CPU path draws it on the page, which
rounds translucent edges differently), and where.

The status bar's clock and battery follow the time of each run, a text
cursor blinks, other clocks and times on a screen and the scan line follow the
time: differences in the status bar's rows, in a picture named volatile,
within the box named with it or, outside that box, in a cursor-sized box
are reported and do not fail; any other difference fails.

Usage: gui-compare.py <reference-dir> <dir> [--status ROWS] [--volatile NAME[:X0,Y0-X1,Y1]]...
"""
import fnmatch
import os
import sys

LEVELS = 2
# A text cursor at the largest scale: 3 px wide, a line of text high.
CURSOR = (8, 64)
# Screendumps taken while a page slides in (each a different step).
SKIP = ["18-push-*"]


def ppm(path):
    with open(path, "rb") as f:
        data = f.read()
    fields, pos = [], 0
    while len(fields) < 4:
        while data[pos:pos + 1].isspace():
            pos += 1
        if data[pos:pos + 1] == b"#":
            pos = data.index(b"\n", pos)
            continue
        end = pos
        while not data[end:end + 1].isspace():
            end += 1
        fields.append(data[pos:end])
        pos = end
    if fields[0] != b"P6" or fields[3] != b"255":
        raise ValueError(f"{path}: not an 8-bit P6 picture")
    w, h = int(fields[1]), int(fields[2])
    return w, h, data[pos + 1:pos + 1 + w * h * 3]


def differ(a, b, w, h, top, leave=None):
    """The pixels differing by more than LEVELS: (count, box) in the rows
    from top on but for the box leave, and the count above them."""
    n, above, box = 0, 0, None
    row = w * 3
    for y in range(h):
        ra, rb = a[y * row:(y + 1) * row], b[y * row:(y + 1) * row]
        if ra == rb:
            continue
        for x in range(w):
            i = x * 3
            if max(abs(ra[i] - rb[i]), abs(ra[i + 1] - rb[i + 1]), abs(ra[i + 2] - rb[i + 2])) <= LEVELS:
                continue
            if y < top:
                above += 1
                continue
            if leave and leave[0] <= x < leave[2] and leave[1] <= y < leave[3]:
                continue
            n += 1
            box = (x, y, x + 1, y + 1) if box is None else (min(box[0], x), box[1], max(box[2], x + 1), y + 1)
    return n, box, above


def volatile_box(arg):
    """--volatile's NAME[:X0,Y0-X1,Y1]: the name and the box, or None for
    the whole picture."""
    name, _, box = arg.partition(":")
    if not box:
        return name, None
    a, b = box.split("-")
    return name, tuple(int(v) for v in a.split(",") + b.split(","))


def main(argv):
    args, status, volatile = [], 32, {}
    it = iter(argv)
    for a in it:
        if a == "--status":
            status = int(next(it))
        elif a == "--volatile":
            name, box = volatile_box(next(it))
            volatile[name] = box
        else:
            args.append(a)
    if len(args) != 2:
        sys.exit(__doc__)
    ref, other = args
    names = sorted(f[:-4] for f in os.listdir(ref) if f.endswith(".ppm") and not any(fnmatch.fnmatch(f[:-4], p) for p in SKIP))
    if not names:
        sys.exit(f"no screendumps in {ref}")
    failed = []
    for name in names:
        path = os.path.join(other, name + ".ppm")
        if not os.path.exists(path):
            print(f"PHONE-GUI-COMPARE: {name}: missing", flush=True)
            failed.append(name)
            continue
        wa, ha, a = ppm(os.path.join(ref, name + ".ppm"))
        wb, hb, b = ppm(path)
        if (wa, ha) != (wb, hb):
            print(f"PHONE-GUI-COMPARE: {name}: {wa}x{ha} against {wb}x{hb}", flush=True)
            failed.append(name)
            continue
        n, box, above = differ(a, b, wa, ha, status)
        bar = f"; the status bar: {above} pixels" if above else ""
        if n == 0:
            print(f"PHONE-GUI-COMPARE: {name}: the same{bar}", flush=True)
            continue
        vbox = volatile.get(name)
        out, obox, _ = differ(a, b, wa, ha, status, vbox) if vbox else (n, box, above)
        where = f"{obox[0]},{obox[1]}-{obox[2]},{obox[3]}" if obox else ""
        small = obox is not None and obox[2] - obox[0] <= CURSOR[0] and obox[3] - obox[1] <= CURSOR[1]
        if name in volatile and vbox is None:
            why = "volatile"
        elif out == 0:
            why = "within its volatile box"
        elif small:
            why = "a cursor-sized box" if out == n else f"a cursor-sized box, {where}, and its volatile box"
        else:
            why = "FAIL" if out == n else f"FAIL: {out} pixels out of its volatile box, in {where}"
        print(f"PHONE-GUI-COMPARE: {name}: {n} pixels differ in {box[0]},{box[1]}-{box[2]},{box[3]} ({why}){bar}", flush=True)
        if why.startswith("FAIL"):
            failed.append(name)
    print(f"PHONE-GUI-COMPARE: {len(names) - len(failed)} of {len(names)} as the reference" + (f"; differing: {' '.join(failed)}" if failed else ""), flush=True)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
