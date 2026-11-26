#!/usr/bin/env python3
"""The Android sparse image (fastboot's format, version 1.0) of a raw image.

    phone-img2simg.py RAW SPARSE

A 4096-byte block that is one 4-byte word repeated, zeros included, goes
into a fill chunk; any other block into a raw chunk; runs of either are
merged. No block becomes "don't care": the partition is written in full,
so nothing it held before shows through in what the file system takes for
zeros. A last partial block is padded with zeros.
"""
import struct
import sys

BLOCK = 4096
RAW, FILL = 0xCAC1, 0xCAC2
HEADER = struct.Struct("<IHHHHIIII")
CHUNK = struct.Struct("<HHII")
# Raw chunks are kept to 16 MiB, so that one is held in memory at a time.
RAW_MAX = 4096


def chunks(f):
    """(kind, fill word or raw data, blocks) runs of the image."""
    run_kind, run_value, run_raw, run_n = None, None, [], 0
    while True:
        block = f.read(BLOCK)
        if not block:
            break
        block = block.ljust(BLOCK, b"\0")
        word = block[:4]
        if block == word * (BLOCK // 4):
            kind, value = FILL, word
        else:
            kind, value = RAW, None
        same = kind == run_kind and (kind == RAW and run_n < RAW_MAX or kind == FILL and value == run_value)
        if run_n and not same:
            yield run_kind, run_value if run_kind == FILL else b"".join(run_raw), run_n
            run_raw, run_n = [], 0
        run_kind, run_value = kind, value
        if kind == RAW:
            run_raw.append(block)
        run_n += 1
    if run_n:
        yield run_kind, run_value if run_kind == FILL else b"".join(run_raw), run_n


def main(src, dst):
    total_blocks = total_chunks = 0
    with open(src, "rb") as f, open(dst, "wb") as out:
        out.write(bytes(HEADER.size))
        for kind, data, n in chunks(f):
            out.write(CHUNK.pack(kind, 0, n, CHUNK.size + len(data)))
            out.write(data)
            total_blocks += n
            total_chunks += 1
        out.seek(0)
        out.write(HEADER.pack(0xED26FF3A, 1, 0, HEADER.size, CHUNK.size, BLOCK, total_blocks, total_chunks, 0))
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__.strip().splitlines()[2].strip())
    sys.exit(main(sys.argv[1], sys.argv[2]))
