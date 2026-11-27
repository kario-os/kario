#!/usr/bin/env python3
"""The disk and the ramdisk of the userdata layout's test on QEMU
(qemu-userdata.sh).

disk: a GPT disk with 4096-byte sectors, as the OnePlus 6T's UFS has, with
the phone's partition names around the image's root file system, which
fills "userdata" up to <extra> MiB short of its end (systemd grows it, as
on the phone). system_a and system_b hold no file system: a root= naming
one of them, as the 6T's bootloader appends one naming its Android system
partition by PARTUUID, finds nothing to start; each partition's PARTUUID
is printed.

ramdisk: what the 6T's boot image carries (phone-bootimg): a small
uncompressed cpio archive naming the layout, then phone-initramfs; the
board's partitions.conf, which the 6T's initramfs has from
phone-sku-partitions, goes into the small archive.

conf: the image's qemuboot.conf for the disk, which runqemu attaches as a
SCSI disk (as the 6T's UFS is) with 4096-byte sectors; its paths made
absolute, as it is kept beside the disk.

    qemu-userdata.py disk <out> <rootfs.ext4> <extra MiB>
    qemu-userdata.py ramdisk <out> <phone-initramfs.cpio.gz>
    qemu-userdata.py conf <out> <qemuboot.conf>
"""
import configparser
import os
import shutil
import struct
import sys
import uuid
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "tools"))
import sku  # noqa: E402

SECTOR = 4096
MIB = 1 << 20
ENTRIES = 128
ENTRY = 128
LINUX_DATA = uuid.UUID("0fc63daf-8483-4772-8e79-3d69d8477de4")
# The 6T's names (sku/boards/oneplus-fajita.toml), in MiB; userdata's is
# the root file system's and the extra.
PARTS = [("boot_a", 4), ("boot_b", 4), ("system_a", 16), ("system_b", 16), ("userdata", None)]


def disk(out, rootfs, extra_mib):
    size = os.path.getsize(rootfs)
    elbas = ENTRIES * ENTRY // SECTOR
    first = MIB // SECTOR
    ents = bytearray(ENTRIES * ENTRY)
    lba = first
    spans, guids = {}, {}
    for i, (name, mib) in enumerate(PARTS):
        count = (mib * MIB if mib else (size + MIB - 1) // MIB * MIB + extra_mib * MIB) // SECTOR
        e = memoryview(ents)[i * ENTRY:(i + 1) * ENTRY]
        e[0:16] = LINUX_DATA.bytes_le
        guids[name] = uuid.uuid4()
        e[16:32] = guids[name].bytes_le
        struct.pack_into("<QQQ", e, 32, lba, lba + count - 1, 0)
        nm = name.encode("utf-16-le")
        e[56:56 + len(nm)] = nm
        spans[name] = lba
        lba += count
    total = lba + elbas + 1
    crc = zlib.crc32(ents) & 0xffffffff
    guid = uuid.uuid4().bytes_le

    def header(me, alt, at):
        h = bytearray(92)
        struct.pack_into("<8sII", h, 0, b"EFI PART", 0x00010000, 92)
        struct.pack_into("<QQQQ", h, 24, me, alt, first, lba - 1)
        h[56:72] = guid
        struct.pack_into("<QIII", h, 72, at, ENTRIES, ENTRY, crc)
        struct.pack_into("<I", h, 16, zlib.crc32(h) & 0xffffffff)
        return bytes(h)

    mbr = bytearray(512)
    mbr[446:462] = struct.pack("<BBBBBBBBII", 0, 0, 2, 0, 0xee, 0xff, 0xff, 0xff, 1, min(total - 1, 0xffffffff))
    mbr[510:512] = b"\x55\xaa"
    with open(out, "wb") as f:
        f.truncate(total * SECTOR)
        f.write(mbr)
        f.seek(SECTOR)
        f.write(header(1, total - 1, 2))
        f.seek(2 * SECTOR)
        f.write(ents)
        f.seek((total - 1 - elbas) * SECTOR)
        f.write(ents)
        f.seek((total - 1) * SECTOR)
        f.write(header(total - 1, 1, total - 1 - elbas))
        f.seek(spans["userdata"] * SECTOR)
        with open(rootfs, "rb") as r:
            shutil.copyfileobj(r, f, 16 * MIB)
    for i, (name, _) in enumerate(PARTS):
        print(f"{name}: partition {i + 1}, sector {spans[name]}, partuuid {guids[name]}")


def cpio(files):
    """A newc cpio archive of (name, mode, data), padded to 512 bytes as
    GNU cpio pads it."""
    out = bytearray()
    for ino, (name, mode, data) in enumerate(files + [("TRAILER!!!", 0, b"")], 1):
        nm = name.encode() + b"\0"
        out += b"070701" + b"".join(b"%08X" % v for v in (ino if mode else 0, mode, 0, 0, 2 if mode & 0o40000 else 1,
                                                          0, len(data), 0, 0, 0, 0, len(nm), 0))
        out += nm + b"\0" * (-(110 + len(nm)) % 4)
        out += data + b"\0" * (-len(data) % 4)
    return bytes(out) + b"\0" * (-len(out) % 512)


def ramdisk(out, initramfs):
    conf = sku.partitions_conf(sku.load("boards", "oneplus-fajita")).encode()
    head = cpio([("etc", 0o40755, b""), ("etc/phone", 0o40755, b""),
                 ("etc/phone/layout", 0o100644, b"layout=userdata\n"),
                 ("etc/phone/partitions.conf", 0o100644, conf)])
    with open(out, "wb") as f, open(initramfs, "rb") as i:
        f.write(head)
        shutil.copyfileobj(i, f)
    sys.stdout.write(conf.decode())


def conf(out, src):
    cf = configparser.ConfigParser(interpolation=None)
    cf.read(src)
    base = os.path.dirname(os.path.abspath(src))
    for k, v in cf.items("config_bsp"):
        if v == "." or v.startswith("../"):
            cf.set("config_bsp", k, os.path.normpath(os.path.join(base, v)))
    cf.set("config_bsp", "qb_rootfs_opt", "-drive id=disk0,file=@ROOTFS@,if=none,format=raw -device virtio-scsi-pci,id=scsi0 "
           "-device scsi-hd,drive=disk0,logical_block_size=%d,physical_block_size=%d" % (SECTOR, SECTOR))
    with open(out, "w") as f:
        cf.write(f)


def main(argv):
    if len(argv) == 4 and argv[0] == "disk":
        disk(argv[1], argv[2], int(argv[3]))
    elif len(argv) == 3 and argv[0] == "ramdisk":
        ramdisk(argv[1], argv[2])
    elif len(argv) == 3 and argv[0] == "conf":
        conf(argv[1], argv[2])
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main(sys.argv[1:])
