#!/usr/bin/env python3
"""What an update across versions has to move (25-system-update.md §3).

    ota-delta.py files <rootfs dir> > files.tsv
        path, size and SHA-256 of every regular file (symlinks: their target)
    ota-delta.py report <base slot image> <new slot image> <base files.tsv> <new rootfs dir>
        the 4 KiB blocks of the new slot image found in neither slot of a
        phone running the base (both slots hold the base image there): no
        block-level delta can send less; and the files that changed, the
        biggest first
    ota-delta.py ranges <ota-www.py log> <url path> <bundle> [<payload>]
        how much of a bundle a streamed install requested, how much of it
        twice, and in which part of the bundle (RAUC's verity format: the
        squashfs image, its hash tree, the signature); with the payload,
        where it sits in the squashfs image (stored as it is, being
        compressed already)

RAUC's adaptive install looks blocks up by hash, wherever they sit in the
local slots, so a file that only moved costs nothing; a changed file costs
the blocks it changed, which for a rebuilt binary is usually all of them.
"""
import hashlib
import os
import sys

BLOCK = 4096


def walk(root):
    out = {}
    for dirpath, dirs, files in os.walk(root):
        dirs.sort()
        for name in sorted(files):
            p = os.path.join(dirpath, name)
            rel = "/" + os.path.relpath(p, root)
            if os.path.islink(p):
                out[rel] = (0, "link:" + os.readlink(p))
            elif os.path.isfile(p):
                h = hashlib.sha256()
                with open(p, "rb") as f:
                    for chunk in iter(lambda: f.read(1 << 20), b""):
                        h.update(chunk)
                out[rel] = (os.path.getsize(p), h.hexdigest())
    return out


def blocks(path):
    with open(path, "rb") as f:
        while True:
            b = f.read(BLOCK)
            if not b:
                return
            yield hashlib.sha256(b.ljust(BLOCK, b"\0")).digest()


def mb(n):
    return f"{n / (1 << 20):.1f} MB"


def report(base_img, new_img, base_tsv, new_root):
    have = set(blocks(base_img))
    total = missing = 0
    seen = set()
    for h in blocks(new_img):
        total += 1
        if h not in have and h not in seen:
            missing += 1
            seen.add(h)
    print(f"PHONE-OTA-DELTA: slot image {mb(total * BLOCK)}, {missing} of {total} blocks not on the phone: {mb(missing * BLOCK)}")
    base = {}
    with open(base_tsv) as f:
        for line in f:
            path, size, digest = line.rstrip("\n").split("\t")
            base[path] = (int(size), digest)
    new = walk(new_root)
    changed = [(size, path) for path, (size, digest) in new.items() if base.get(path, (0, ""))[1] != digest]
    removed = [p for p in base if p not in new]
    changed.sort(reverse=True)
    print(f"PHONE-OTA-DELTA: files changed or added {len(changed)} ({mb(sum(s for s, _ in changed))}), removed {len(removed)}")
    for size, path in changed[:15]:
        print(f"PHONE-OTA-DELTA:   {mb(size):>9}  {path}{'' if path in base else ' (new)'}")


def parts(bundle, payload):
    """(start, end, name) of a RAUC verity bundle's parts."""
    size = os.path.getsize(bundle)
    with open(bundle, "rb") as f:
        sb = f.read(96)
        f.seek(size - 8)
        sig = int.from_bytes(f.read(8), "big")
        f.seek(0)
        image = f.read()
    squash = int.from_bytes(sb[40:48], "little") if sb[:4] == b"hsqs" else 0
    sig_start = size - 8 - sig
    out = []
    if payload:
        with open(payload, "rb") as f:
            data = f.read()
        # The first 128 KiB block holds the (compressible) manifest; later
        # blocks are stored as they are, so one of them places the payload.
        at = image.find(data[1 << 17:(1 << 17) + 4096], 0, squash) if len(data) > (1 << 17) + 4096 else -1
        if at >= 0:
            out.append((max(0, at - (1 << 17)), min(squash, at - (1 << 17) + len(data)), "payload"))
    out.append((0, squash, "squashfs image (applier, hook, descriptor, metadata)"))
    out.append((squash, sig_start, "dm-verity hash tree"))
    out.append((sig_start, size, "signature"))
    return out, size


def ranges(log, path, bundle, payload=None):
    spans = []
    with open(log, errors="replace") as f:
        for line in f:
            w = line.split()
            if len(w) >= 4 and w[0] == "SENT" and w[1] == path:
                spans.append((int(w[3]), int(w[3]) + int(w[2])))
    named, size = parts(bundle, payload)
    edges = sorted({0, size} | {s for s, _ in spans} | {e for _, e in spans} | {p for a, b, _ in named for p in (a, b)})
    total = sum(e - s for s, e in spans)
    once = again = 0
    by_part = {}
    for lo, hi in zip(edges, edges[1:]):
        n = sum(1 for s, e in spans if s <= lo and hi <= e)
        if not n:
            continue
        once += hi - lo
        if n > 1:
            again += (n - 1) * (hi - lo)
            name = next((nm for a, b, nm in named if a <= lo and hi <= b), "?")
            by_part[name] = by_part.get(name, 0) + (n - 1) * (hi - lo)
    print(f"PHONE-OTA-DELTA: {path}: {len(spans)} requests, {mb(total)} requested; {mb(once)} of the {mb(size)} bundle distinct, {mb(again)} read again")
    for a, b, nm in named:
        print(f"PHONE-OTA-DELTA:   {nm}: {mb(b - a)} at {a}, read again {mb(by_part.get(nm, 0))}")


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "files":
        for path, (size, digest) in sorted(walk(sys.argv[2]).items()):
            print(f"{path}\t{size}\t{digest}")
    elif len(sys.argv) == 6 and sys.argv[1] == "report":
        report(*sys.argv[2:])
    elif len(sys.argv) in (5, 6) and sys.argv[1] == "ranges":
        ranges(*sys.argv[2:])
    else:
        sys.exit(__doc__)
