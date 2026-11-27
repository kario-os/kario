#!/usr/bin/env python3
"""The raw image an Android sparse image (fastboot's format, version 1.x)
stands for, on standard output: what the chroot kit pushes when it is
given the userdata image the flash writes (phone-image-<machine>.rootfs.
ext4.sparse, yocto/meta-phone/files/phone-img2simg.py's).

    unsparse.py SPARSE          the raw image on stdout
    unsparse.py --size SPARSE   only its size in bytes

Raw chunks are copied, fill chunks repeat their word, "don't care" chunks
are zeros (the flash writes the whole partition, so the file system takes
nothing from them), CRC chunks are checked when the header asks. Exit
status 1, with what is wrong, for anything else.
"""
import struct
import sys
import zlib

MAGIC = 0xED26FF3A
RAW, FILL, DONT_CARE, CRC32 = 0xCAC1, 0xCAC2, 0xCAC3, 0xCAC4
HEADER = struct.Struct("<IHHHHIIII")
CHUNK = struct.Struct("<HHII")
PIECE = 1 << 20


class Bad(Exception):
    pass


def read(f, n):
    data = f.read(n)
    if len(data) != n:
        raise Bad(f"ends {n - len(data)} bytes early")
    return data


def header(f):
    if len(head := f.read(HEADER.size)) != HEADER.size:
        raise Bad("too short for an Android sparse image")
    magic, major, _minor, hdr_size, chunk_size, block, blocks, chunks, checksum = HEADER.unpack(head)
    if magic != MAGIC:
        raise Bad("not an Android sparse image (its magic is %08x)" % magic)
    if major != 1 or hdr_size < HEADER.size or chunk_size < CHUNK.size or block == 0 or block % 4:
        raise Bad(f"a sparse format this does not read (version {major}, header {hdr_size}, chunk header {chunk_size}, block {block})")
    read(f, hdr_size - HEADER.size)
    return block, blocks, chunks, checksum, chunk_size


def unsparse(f, out):
    """Writes the raw image to out (None: only counts); its size."""
    block, blocks, chunks, checksum, chunk_size = header(f)
    crc, done = 0, 0
    for i in range(chunks):
        kind, _, n, total = CHUNK.unpack(read(f, CHUNK.size))
        read(f, chunk_size - CHUNK.size)
        body = total - chunk_size
        if kind == RAW:
            if body != n * block:
                raise Bad(f"chunk {i}: a raw chunk of {n} blocks carries {body} bytes")
            left = body
            while left:
                data = read(f, min(left, PIECE))
                crc = zlib.crc32(data, crc)
                if out:
                    out.write(data)
                left -= len(data)
        elif kind in (FILL, DONT_CARE):
            if kind == FILL and body != 4 or kind == DONT_CARE and body != 0:
                raise Bad(f"chunk {i}: a {'fill' if kind == FILL else 'dont-care'} chunk carries {body} bytes")
            word = read(f, 4) if kind == FILL else bytes(4)
            piece = word * (PIECE // 4)
            left = n * block
            while left:
                data = piece[:min(left, PIECE)]
                crc = zlib.crc32(data, crc)
                if out:
                    out.write(data)
                left -= len(data)
        elif kind == CRC32:
            if body != 4:
                raise Bad(f"chunk {i}: a CRC chunk carries {body} bytes")
            want = struct.unpack("<I", read(f, 4))[0]
            if want != crc:
                raise Bad(f"chunk {i}: CRC {crc:08x}, the image says {want:08x}")
            continue
        else:
            raise Bad(f"chunk {i}: unknown kind {kind:04x}")
        done += n
    if done != blocks:
        raise Bad(f"its chunks hold {done} blocks, its header says {blocks}")
    if checksum and checksum != crc:
        raise Bad(f"CRC {crc:08x}, the header says {checksum:08x}")
    return done * block


def main(argv):
    size_only = argv[:1] == ["--size"]
    if size_only:
        argv = argv[1:]
    if len(argv) != 1:
        print(__doc__.strip().split("\n\n")[1], file=sys.stderr)
        return 2
    try:
        with open(argv[0], "rb") as f:
            if size_only:
                block, blocks, _, _, _ = header(f)
                print(block * blocks)
            else:
                unsparse(f, sys.stdout.buffer)
    except (OSError, Bad) as e:
        print(f"unsparse: {argv[0]}: {e}", file=sys.stderr)
        return 1
    except BrokenPipeError:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
