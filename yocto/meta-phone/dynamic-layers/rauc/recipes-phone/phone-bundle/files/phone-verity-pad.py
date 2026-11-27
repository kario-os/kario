#!/usr/bin/env python3
"""Make a RAUC verity bundle's squashfs image end where dm-verity's
read-ahead of its hash tree stays in the tree (phone-bundle-delta.bb,
25-system-update.md §5.5).

    phone-verity-pad.py BUNDLE PAD -- COMMAND...

A verity bundle is the squashfs image, its hash tree right after it on the
same device, and the signature. To check a data block dm-verity reads the
hash blocks above it, and those of the leaf level ahead in clusters of
dm_verity.prefetch_cluster (256 KiB, 64 blocks) aligned on the device's
block numbers, not on the tree's start (drivers/md/dm-verity-target.c,
verity_prefetch_io; a tree of one level, over an image of up to 128 blocks,
is not read ahead). Unless the leaves' first cluster starts in the tree, it
reaches back into the image's tail, up to 63 blocks, and fetches them into
dm-bufio, not the page cache: a streamed install downloads every one of
them once more, and the payload's tail, which lies there, twice.

The read-ahead stays in the tree when the image's block count is one of the
u + 1 counts up to a multiple of the cluster, u being the tree's blocks
above the leaves (the levels are written top first; u = 1 up to 64 MiB).
When it does not, PAD, a file in the directory BUNDLE was built from (after
the payload by name), gets the bytes that grow the image to the middle of
the next such run (4 KiB to spare either way at u = 1), and COMMAND (rauc
bundle, as do_bundle runs it) builds BUNDLE again; then again with PAD
corrected by what the image missed by, until the read-ahead stays in the
tree. PAD holds bytes that do not compress, then zeros to a whole squashfs
block: mksquashfs stores it as data blocks, the last one compressed, and not
in a fragment, which the bundle's small files share and the hook reads.
Nothing reads PAD. A PAD there before is taken out first (do_bundle run
again). Exit status 1 when the read-ahead does not come to stay in the tree.
"""
import hashlib
import os
import struct
import subprocess
import sys

BLOCK = 4096
HASHES = BLOCK // 32
# dm-verity's default, which neither kernel of the layer changes.
CLUSTER = (256 << 10) // BLOCK
TRIES = 4


def levels(blocks):
    """The block counts of the hash levels over blocks of data, the leaves
    first, as veritysetup and the kernel lay them out."""
    sizes = []
    span = HASHES
    while blocks > span // HASHES:
        sizes.append(-(-blocks // span))
        span *= HASHES
    return sizes


def reach(blocks):
    """The image's blocks the leaves' first read-ahead cluster takes."""
    sizes = levels(blocks)
    if len(sizes) < 2:
        return 0
    first = blocks + sum(sizes[1:])
    return max(0, blocks - first // CLUSTER * CLUSTER)


def goal(blocks):
    """The bytes the image should take, grown from blocks: the middle of the
    next run of block counts that the read-ahead stays out of."""
    lo = blocks + 1
    while reach(lo):
        lo += 1
    hi = lo
    while hi < lo + CLUSTER and not reach(hi + 1):
        hi += 1
    return (lo - 1 + hi) * BLOCK // 2


def image(bundle):
    """The squashfs image's bytes (bytes_used) and block size."""
    with open(bundle, "rb") as f:
        sb = f.read(96)
    if len(sb) < 96 or sb[:4] != b"hsqs":
        raise ValueError(f"{bundle}: not a bundle with a squashfs image")
    return struct.unpack_from("<Q", sb, 40)[0], struct.unpack_from("<I", sb, 12)[0]


def blocks_of(used):
    return -(-used // BLOCK)


def write_pad(path, n, block_size):
    data = bytearray()
    i = 0
    while len(data) < n:
        data += hashlib.sha256(b"kario verity pad %d" % i).digest()
        i += 1
    del data[n:]
    data += bytes(-n % block_size)
    with open(path, "wb") as f:
        f.write(data)


def build(bundle, command):
    os.remove(bundle)
    subprocess.run(command, check=True)


def pad_bundle(bundle, pad, command):
    name = os.path.basename(bundle)
    if os.path.exists(pad):
        os.remove(pad)
        build(bundle, command)
    used, block_size = image(bundle)
    if not reach(blocks_of(used)):
        print(f"phone-verity-pad: {name}: an image of {blocks_of(used)} blocks, dm-verity's read-ahead stays in its hash tree")
        return True
    want = goal(blocks_of(used))
    print(f"phone-verity-pad: {name}: dm-verity's read-ahead takes the last {reach(blocks_of(used))} of the image's {blocks_of(used)} blocks; padding it from {used} to {want} bytes")
    n = want - used
    for _ in range(TRIES):
        write_pad(pad, n, block_size)
        build(bundle, command)
        got, _ = image(bundle)
        if not reach(blocks_of(got)):
            print(f"phone-verity-pad: {name}: an image of {blocks_of(got)} blocks ({got - used} bytes more, {n} of them padding), the read-ahead in the hash tree")
            return True
        n = max(1, n + want - got)
    print(f"phone-verity-pad: {name}: the image of {used} bytes did not come to {want} with padding ({got})", file=sys.stderr)
    return False


if __name__ == "__main__":
    if len(sys.argv) < 5 or sys.argv[3] != "--":
        sys.exit(__doc__)
    try:
        ok = pad_bundle(sys.argv[1], sys.argv[2], sys.argv[4:])
    except (OSError, ValueError, subprocess.CalledProcessError) as e:
        sys.exit(f"phone-verity-pad: {e}")
    sys.exit(0 if ok else 1)
