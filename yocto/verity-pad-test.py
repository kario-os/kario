#!/usr/bin/env python3
"""Test phone-bundle-delta's files/phone-verity-pad.py without bitbake or
RAUC, by what a streamed install of the bundles it pads downloads.

A bundle is made as RAUC 1.15 makes a verity bundle (rauc bundle, which
phone-verity-pad.py runs again: this file with rauc-bundle): mksquashfs with
RAUC's options, the hash tree right after the image (salted SHA-256, 4 KiB
blocks, the top level first), a signature and its size. The install is
modelled at the blocks the network block device is asked for, each one
downloaded, nothing cached below it: dm-verity, as dm-verity-target.c does,
reads the hash blocks that check a data block through dm-bufio, and the
leaves ahead in 256 KiB clusters aligned on the device; the kernel mounting
the image reads its superblock and tables; the applier reads the payload's
blocks once, from the device. The payload does not compress, so mksquashfs
stores it whole and in order.

The update test's payload of 2026-11-25 (1.0 MB, under the 1 MiB from which
the recipe used to add 256 KiB of padding) left the read-ahead in its tail,
which an install then downloaded twice; padded here, the read-ahead takes
none of the image, and the install downloads that much less.

    verity-pad-test.py
"""
import collections
import hashlib
import importlib.util
import os
import random
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
BUNDLE_FILES = os.path.join(HERE, "meta-phone", "dynamic-layers", "rauc", "recipes-phone", "phone-bundle", "files")
PAD = os.path.join(BUNDLE_FILES, "phone-verity-pad.py")
BLOCK = 4096
HASHES = BLOCK // 32
CLUSTER = (256 << 10) // BLOCK
DESC = "phone-image-qemuarm64.rootfs.ext4.verity.desc"


def tree(data, salt):
    level = [hashlib.sha256(salt + data[i:i + BLOCK]).digest() for i in range(0, len(data), BLOCK)]
    out = []
    while True:
        blocks = [b"".join(level[i:i + HASHES]).ljust(BLOCK, b"\0") for i in range(0, len(level), HASHES)]
        out.insert(0, b"".join(blocks))
        if len(blocks) == 1:
            return b"".join(out)
        level = [hashlib.sha256(salt + b).digest() for b in blocks]


def rauc_bundle(content, out):
    """rauc bundle of a verity bundle (bundle.c: mksquashfs, then
    generate_verity and the signature). RAUC also passes -quiet, which
    older mksquashfs lacks and which changes nothing in the image."""
    if os.path.exists(out):
        sys.exit(f"bundle {out} already exists")
    subprocess.run(["mksquashfs", content, out, "-noappend", "-no-progress", "-all-root", "-no-xattrs"],
                   check=True, stdout=subprocess.DEVNULL)
    with open(out, "r+b") as f:
        data = f.read()
        if len(data) % BLOCK:
            sys.exit(f"squashfs size ({len(data)}) is not a multiple of 4096 bytes")
        f.write(tree(data, os.urandom(32)))
        sig = os.urandom(1900)
        f.write(sig + struct.pack(">Q", len(sig)))


def image_blocks(bundle):
    with open(bundle, "rb") as f:
        return -(-struct.unpack_from("<Q", f.read(96), 40)[0] // BLOCK)


class Install:
    """The blocks a streamed install asks the network block device for, by
    how many times (got), under dm-verity over an image of n blocks."""

    def __init__(self, n):
        # verity_ctr: hash_per_block_bits 7, the tree right after the data.
        self.levels = 0
        while 7 * self.levels < 64 and (n - 1) >> (7 * self.levels):
            self.levels += 1
        self.level_block = [0] * self.levels
        at = n
        for i in range(self.levels - 1, -1, -1):
            self.level_block[i] = at
            at += (n + (1 << (7 * (i + 1))) - 1) >> (7 * (i + 1))
        self.hash_end = at
        self.n = n
        self.bufio = set()
        self.got = collections.Counter()

    def hash_block(self, block, level):
        return self.level_block[level] + (block >> (7 * (level + 1)))

    def buffered(self, blocks):
        new = [b for b in blocks if b not in self.bufio]
        self.bufio.update(new)
        self.got.update(new)

    def read(self, first, count):
        """Data blocks read through dm-verity: verity_submit_prefetch's
        read-ahead, the data, and verity_verify_io's hash blocks."""
        for i in range(self.levels - 2, -1, -1):
            start = self.hash_block(first, i)
            end = self.hash_block(first + count - 1, i)
            if i == 0:
                start &= ~(CLUSTER - 1)
                end = min(end | (CLUSTER - 1), self.hash_end - 1)
            self.buffered(range(start, end + 1))
        self.got.update(range(first, first + count))
        for b in range(first, first + count):
            self.buffered([self.hash_block(b, i) for i in range(self.levels)])

    def ahead(self):
        """The image's blocks the read-ahead took."""
        return sorted(b for b in self.bufio if b < self.n)

    def downloaded(self):
        return sum(self.got.values()) * BLOCK


def install(bundle, payload):
    """A streamed install of bundle, and the payload's device blocks that
    it downloaded twice: the read-ahead's, which the applier then reads."""
    with open(bundle, "rb") as f:
        img = f.read()
    used, inodes = struct.unpack_from("<Q", img, 40)[0], struct.unpack_from("<Q", img, 64)[0]
    n = -(-used // BLOCK)
    at = img.find(payload[:BLOCK])
    assert at >= 0 and img[at:at + len(payload)] == payload, "the payload is not stored whole"
    m = Install(n)
    m.read(0, 1)
    m.read(inodes // BLOCK, n - inodes // BLOCK)
    first, end = at // BLOCK, -(-(at + len(payload)) // BLOCK)
    for b in range(first, end, 256):
        m.read(b, min(256, end - b))
    return m, [b for b in range(first, end) if b in m.bufio]


def content(d, payload, applier=None):
    """The bundle directory as phone-bundle-delta's do_configure leaves it."""
    os.makedirs(d)
    shutil.copy(os.path.join(BUNDLE_FILES, "delta-hook"), d)
    files = {
        "manifest.raucm": b"[update]\ncompatible=kario-qemuarm64\nversion=0.8.0\n\n[bundle]\nformat=verity\n\n"
                          b"[hooks]\nfilename=delta-hook\n\n[image.rootfs]\nfilename=rootfs.kdelta\nhooks=install\n",
        DESC: b"data_blocks=137216\nhash_offset=562036736\nsalt=" + b"ab" * 32 + b"\nroot_hash=" + b"cd" * 32 + b"\n",
        DESC + ".sig": random.Random(2).randbytes(1700),
        "rootfs.kdelta": payload,
    }
    if applier:
        files["phone-ota-delta"] = applier
    for name, data in files.items():
        with open(os.path.join(d, name), "wb") as f:
            f.write(data)


def payload_of(size, seed=1):
    return b"KDLT" + random.Random(seed).randbytes(size - 4)


def old_pad():
    """The 256 KiB file phone-bundle-delta.bb used to add to a payload of
    1 MiB or more."""
    return b"".join(hashlib.sha256(b"kario verity pad %d" % i).digest() for i in range((256 << 10) // 32))


def pad_run(out, pad, d):
    return subprocess.run([sys.executable, PAD, out, pad, "--", sys.executable, __file__, "rauc-bundle", d, out],
                          capture_output=True, text=True)


def load_pad():
    spec = importlib.util.spec_from_file_location("phone_verity_pad", PAD)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


@unittest.skipUnless(shutil.which("mksquashfs"), "no mksquashfs")
class VerityPadTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="verity-pad-test.")
        self.addCleanup(shutil.rmtree, self.tmp)
        self.k = 0

    def bundle(self, payload, applier=None):
        """A bundle directory and the bundle rauc made of it."""
        self.k += 1
        d = os.path.join(self.tmp, f"b{self.k}")
        content(d, payload, applier)
        rauc_bundle(d, d + ".raucb")
        return d, d + ".raucb"

    def padded(self, payload, applier=None):
        """The bundle as the recipe makes it now (phone-verity-pad.py after
        rauc bundle), and a copy of it before padding: what the recipe made
        of a payload under 1 MiB before."""
        d, out = self.bundle(payload, applier)
        shutil.copy(out, d + ".unpadded")
        r = pad_run(out, os.path.join(d, "zz-verity-pad"), d)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        return d, out, d + ".unpadded", r.stdout

    def test_small_payload(self):
        """The payload of 2026-11-25 (1.0 MB), its image ending on every
        fourth block of a cluster."""
        hit = cases = 0
        for k in range(0, CLUSTER, 4):
            cases += 1
            payload = payload_of(1_000_000 - k * BLOCK, seed=k)
            d, out, before, said = self.padded(payload)
            old, old_twice = install(before, payload)
            new, new_twice = install(out, payload)
            hit += bool(old_twice)
            self.assertEqual(new.ahead(), [], f"{len(payload)}: {said}")
            self.assertEqual(new_twice, [], f"{len(payload)}: {said}")
            self.assertLessEqual(new.downloaded(), old.downloaded() - len(old.ahead()) * BLOCK + BLOCK,
                                 f"{len(payload)}: downloaded {new.downloaded()} bytes, {old.downloaded()} unpadded")
            grew = os.path.getsize(out) - os.path.getsize(before)
            self.assertLess(grew, (CLUSTER - 1) * BLOCK + 1024, f"{len(payload)}: {said}")
        # Unpadded, the read-ahead took the payload's tail in most layouts.
        self.assertGreater(hit, cases * 3 // 4)

    def test_big_payload(self):
        """A payload of 1 MiB or more had 256 KiB of padding, which took the
        read-ahead in place of the payload's tail: downloaded all the same."""
        payload = payload_of(5_400_000)
        d, out, _, said = self.padded(payload)
        with open(os.path.join(d, "zz-verity-pad"), "wb") as f:
            f.write(old_pad())
        rauc_bundle(d, d + ".old.raucb")
        old, old_twice = install(d + ".old.raucb", payload)
        new, new_twice = install(out, payload)
        self.assertEqual(old_twice, [])
        self.assertEqual(new_twice, [])
        self.assertEqual(new.ahead(), [], said)
        self.assertLessEqual(new.downloaded(), old.downloaded() - len(old.ahead()) * BLOCK + BLOCK)
        self.assertLess(os.path.getsize(out), os.path.getsize(d + ".old.raucb"))

    def test_applier_in_bundle(self):
        payload = payload_of(1_000_000)
        applier = b"".join(b"\x7fELF kario applier %d\n" % i for i in range(120000))
        _, out, _, said = self.padded(payload, applier)
        new, new_twice = install(out, payload)
        self.assertEqual(new.ahead(), [], said)
        self.assertEqual(new_twice, [])

    def test_small_image(self):
        """An image of up to 128 blocks has a tree of one level, never read
        ahead: no padding, no second build."""
        payload = payload_of(300_000)
        d, out = self.bundle(payload)
        with open(out, "rb") as f:
            built = f.read()
        r = subprocess.run([sys.executable, PAD, out, os.path.join(d, "zz-verity-pad"), "--", "false"],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("stays in its hash tree", r.stdout)
        self.assertFalse(os.path.exists(os.path.join(d, "zz-verity-pad")))
        with open(out, "rb") as f:
            self.assertEqual(f.read(), built)
        self.assertEqual(install(out, payload)[0].ahead(), [])

    def test_padding_from_before(self):
        """do_bundle run again finds the last run's padding: taken out, and
        the image measured afresh (the directory's new time may change its
        tables by a byte or two)."""
        payload = payload_of(1_000_000)
        d, out, before, _ = self.padded(payload)
        grew = os.path.getsize(out) - os.path.getsize(before)
        pad = os.path.join(d, "zz-verity-pad")
        with open(pad, "wb") as f:
            f.write(old_pad())
        r = pad_run(out, pad, d)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(os.path.getsize(out) - os.path.getsize(before), grew, r.stdout)
        self.assertEqual(install(out, payload)[0].ahead(), [])

    def test_fails(self):
        """A build that never takes the padding, and a file that is not a
        bundle, fail with a reason."""
        payload = payload_of(1_000_000)
        d, out = self.bundle(payload)
        if not load_pad().reach(image_blocks(out)):
            self.skipTest("this mksquashfs's image ends where the read-ahead stays in the tree")
        r = pad_run(out, os.path.join(self.tmp, "elsewhere"), d)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("did not come to", r.stderr)
        r = subprocess.run([sys.executable, PAD, PAD, os.path.join(self.tmp, "pad"), "--", "true"],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("not a bundle", r.stderr)


class ArithmeticTest(unittest.TestCase):
    def test_reach_is_the_kernels(self):
        """reach() is what the kernel's first read-ahead takes of an image,
        up to three levels of tree; goal() grows an image by less than a
        cluster to the middle of block counts where it takes none."""
        pad = load_pad()
        for n in list(range(2, 2200)) + list(range(16300, 16500)) + list(range(32700, 32900)):
            m = Install(n)
            m.read(0, 1)
            self.assertEqual(pad.reach(n), len(m.ahead()), n)
            if pad.reach(n):
                g = pad.goal(n)
                self.assertLess(-(-g // BLOCK) - n, CLUSTER, n)
                for at in (g - BLOCK // 2, g, g + BLOCK // 2):
                    self.assertEqual(pad.reach(-(-at // BLOCK)), 0, (n, at))


if __name__ == "__main__":
    if len(sys.argv) == 4 and sys.argv[1] == "rauc-bundle":
        rauc_bundle(sys.argv[2], sys.argv[3])
    else:
        unittest.main()
