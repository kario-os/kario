#!/usr/bin/env python3
"""Test phone-abslot (meta-phone's phone-qcom) on disk images, without a phone.

Builds the tool with the machine's C compiler, writes GPT disks with 512 and
4096-byte sectors (the OnePlus 6T's UFS has 4096) holding boot_a and boot_b
with Qualcomm's A/B attributes, and checks that mark-good sets "successful"
on the slot that booted in the primary and backup GPT, keeps both GPTs'
CRCs valid, changes no other byte, writes nothing in a dry run or a second
time, and refuses a damaged GPT untouched; and what RAUC's backend
(rauc-abl) and phone-initramfs ask of it: the booted slot, the slots as
phone-ota's boot state, set-active as ABL's fastboot set_active,
mark-unbootable, get-state; on the command line a 6T's ABL gives Kario
(no androidboot.slot_suffix), booted and mark-good take the active slot.
sgdisk, where installed, checks the 512-byte disk too.

    abslot-test.py [cc]
"""
import os
import shutil
import struct
import subprocess
import sys
import tempfile
import uuid
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.path.join(HERE, "meta-phone", "recipes-bsp", "phone-qcom", "files", "abslot.c")
# Byte 6 of the attributes: priority 3, active, 7 tries left.
ACTIVE = 0x3f
SUCCESSFUL = 0x40
# An attribute bit outside Qualcomm's byte, which must survive.
OTHER = 1 << 60
# The command line OnePlus's ABL gave a 6T's Kario boot image on
# 2026-11-24 (39-oneplus-6t.md §1; the serial number scrubbed): with root=
# in the boot image's own line it adds no androidboot.slot_suffix, so the
# booted slot is the one marked active.
FAJITA_CMDLINE = (
    "root=PARTLABEL=userdata rootfstype=ext4 rootwait rw console=ttyMSM0,115200 phone.gui=1 console=tty0 "
    "androidboot.verifiedbootstate=orange androidboot.keymaster=1 "
    "root=PARTUUID=92049492-a5ea-df35-d8ed-7572de28983a androidboot.bootdevice=1d84000.ufshc "
    "androidboot.fstab_suffix=default androidboot.serialno=<serial> androidboot.baseband=msm "
    "msm_drm.dsi_display0=dsi_samsung_s6e3fc2x01_cmd_display: androidboot.dtb_idx=-1347440721 "
    "panel_type=black androidboot.mode=fastboot androidboot.recoveryreason=000 "
    "androidboot.project_name=18801 androidboot.project_codename=fajita ddr_manufacture_info=Samsung "
    "ddr_row0_info=16 androidboot.hw_version=41 androidboot.rf_version=13 androidboot.prj_version=0 "
    "androidboot.platform_id=321 androidboot.platform_name=SDM845 androidboot.startupmode=pon1 "
    "androidboot.enable_dm_verity=1 androidboot.at_location=factory androidboot.power_cut_test=0 "
    "androidboot.secboot=enabled androidboot.battery.absent=false androidboot.rpmb_enable=true "
    "androidboot.type=normal androidboot.product.hardware.sku=3 androidboot.logcat_log_level=49 "
    "androidboot.init_log_level=49 androidboot.cust=0 androidboot.prmec=true androidboot.opcarrier=none "
    "androidboot.bootcount=1919247457"
)


def make_disk(path, ss, parts, n_entries=128):
    size = 128
    elbas = (n_entries * size + ss - 1) // ss
    total = 2 + elbas + 64 + elbas + 1
    first, last = 2 + elbas, total - elbas - 2
    ents = bytearray(n_entries * size)
    lba = first
    for i, (name, a6) in enumerate(parts):
        e = memoryview(ents)[i * size:(i + 1) * size]
        e[0:16] = uuid.uuid4().bytes_le
        e[16:32] = uuid.uuid4().bytes_le
        struct.pack_into("<QQQ", e, 32, lba, lba + 3, a6 << 48 | OTHER)
        nm = name.encode("utf-16-le")
        e[56:56 + len(nm)] = nm
        lba += 4
    crc = zlib.crc32(ents) & 0xffffffff
    guid = uuid.uuid4().bytes_le

    def header(me, alt, at):
        h = bytearray(92)
        struct.pack_into("<8sII", h, 0, b"EFI PART", 0x00010000, 92)
        struct.pack_into("<QQQQ", h, 24, me, alt, first, last)
        h[56:72] = guid
        struct.pack_into("<QIII", h, 72, at, n_entries, size, crc)
        struct.pack_into("<I", h, 16, zlib.crc32(h) & 0xffffffff)
        return bytes(h) + bytes(ss - 92)

    img = bytearray(total * ss)
    img[446:462] = struct.pack("<BBBBBBBBII", 0, 0, 2, 0, 0xee, 0xff, 0xff, 0xff, 1, total - 1)
    img[510:512] = b"\x55\xaa"
    img[ss:2 * ss] = header(1, total - 1, 2)
    img[2 * ss:2 * ss + len(ents)] = ents
    back = total - 1 - elbas
    img[back * ss:back * ss + len(ents)] = ents
    img[(total - 1) * ss:] = header(total - 1, 1, back)
    with open(path, "wb") as f:
        f.write(img)


def read_gpts(path, ss):
    """The attributes by name of the primary and the backup GPT, CRCs checked."""
    with open(path, "rb") as f:
        data = f.read()
    out = []
    for lba in (1, struct.unpack_from("<Q", data, ss + 32)[0]):
        h = bytearray(data[lba * ss:lba * ss + 92])
        assert h[:8] == b"EFI PART", f"no GPT header at LBA {lba}"
        want = struct.unpack_from("<I", h, 16)[0]
        struct.pack_into("<I", h, 16, 0)
        assert zlib.crc32(h) & 0xffffffff == want, f"header CRC at LBA {lba}"
        at, n, size, ecrc = struct.unpack_from("<QIII", h, 72)
        ents = data[at * ss:at * ss + n * size]
        assert zlib.crc32(ents) & 0xffffffff == ecrc, f"entries CRC at LBA {lba}"
        attrs = {}
        for i in range(n):
            e = ents[i * size:(i + 1) * size]
            name = e[56:128].decode("utf-16-le").rstrip("\0")
            if name:
                attrs[name] = struct.unpack_from("<Q", e, 48)[0]
        out.append(attrs)
    return out


def main():
    cc = sys.argv[1] if len(sys.argv) > 1 else os.environ.get("CC", "cc")
    tmp = tempfile.mkdtemp(prefix="abslot-test-")
    tool = os.path.join(tmp, "phone-abslot")
    subprocess.run([cc, "-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", "-o", tool, SRC], check=True)
    failed = []

    def check(ok, what):
        print(("ok   " if ok else "FAIL ") + what)
        if not ok:
            failed.append(what)

    def run(img, ss, *args):
        r = subprocess.run([tool, "--disk", img, "--sector-size", str(ss), *args], capture_output=True, text=True)
        return r.returncode, r.stdout + r.stderr

    parts = [("xbl_a", ACTIVE), ("boot_a", ACTIVE), ("boot_b", 0x1b), ("boot_aa", 0), ("userdata", 0)]
    for ss in (512, 4096):
        img = os.path.join(tmp, f"disk{ss}.img")
        make_disk(img, ss, parts)
        before = open(img, "rb").read()
        rc, out = run(img, ss, "status")
        check(rc == 0 and "booted: a" in out and "boot_a: active, priority 3, tries left 7" in out, f"{ss}: status")
        rc, out = run(img, ss, "mark-good", "--dry-run")
        check(rc == 0 and open(img, "rb").read() == before, f"{ss}: a dry run writes nothing")
        rc, out = run(img, ss, "mark-good")
        prim, back = read_gpts(img, ss)
        want = {"xbl_a": ACTIVE, "boot_a": ACTIVE | SUCCESSFUL, "boot_b": 0x1b, "boot_aa": 0, "userdata": 0}
        check(rc == 0 and all(prim[n] == back[n] == a << 48 | OTHER for n, a in want.items()),
              f"{ss}: boot_a marked successful in both GPTs, nothing else, CRCs valid")
        after = open(img, "rb").read()
        changed = sum(1 for x, y in zip(before, after) if x != y)
        check(changed <= 2 * (1 + 4 + 4), f"{ss}: {changed} bytes changed (attribute byte and CRCs only)")
        rc, out = run(img, ss, "mark-good")
        check(rc == 0 and "already" in out and open(img, "rb").read() == after, f"{ss}: a second mark-good writes nothing")
        if ss == 512 and shutil.which("sgdisk"):
            v = subprocess.run(["sgdisk", "-v", img], capture_output=True, text=True).stdout
            check("No problems found" in v, "512: sgdisk -v finds no problems")
        make_disk(img, ss, [("boot_a", 0x03), ("boot_b", ACTIVE)])
        rc, out = run(img, ss, "mark-good")
        prim, back = read_gpts(img, ss)
        check(rc == 0 and prim["boot_b"] >> 48 & 0xff == ACTIVE | SUCCESSFUL and back["boot_b"] == prim["boot_b"]
              and prim["boot_a"] >> 48 & 0xff == 0x03, f"{ss}: the active slot b is the one marked")
        make_disk(img, ss, parts)
        with open(img, "r+b") as f:
            f.seek(2 * ss + 200)
            f.write(b"\xff")
        before = open(img, "rb").read()
        rc, out = run(img, ss, "mark-good")
        check(rc != 0 and "CRC" in out and open(img, "rb").read() == before, f"{ss}: a damaged GPT is refused untouched")
        make_disk(img, ss, [("userdata", 0)])
        rc, out = run(img, ss, "status")
        check(rc != 0 and "no boot_a" in out, f"{ss}: a disk without boot_a and boot_b is refused")

        # What RAUC's backend (rauc-abl) and phone-initramfs ask of it.
        cmdline = os.path.join(tmp, "cmdline")
        with open(cmdline, "w") as f:
            f.write("root=/dev/dm-0 phone.layout=ab androidboot.slot_suffix=_b\n")
        make_disk(img, ss, parts)
        rc, out = run(img, ss, "--cmdline", cmdline, "booted")
        check(rc == 0 and out == "b\n", f"{ss}: booted is the bootloader's androidboot.slot_suffix")
        rc, out = run(img, ss, "--cmdline", os.devnull, "booted")
        check(rc == 0 and out == "a\n", f"{ss}: without it booted is the active slot")
        # The 6T's own: slot a as fajita-flash.sh's set_active leaves it,
        # b inactive (priority 2, its successful mark kept); then b, which
        # ABL made active (one try spent) when a's tries ran out.
        with open(cmdline, "w") as f:
            f.write(FAJITA_CMDLINE + "\n")
        for act, ina, slot in ((0x3f, 0x7a, "a"), (0x37, 0x82, "b")):
            make_disk(img, ss, [("boot_a", act if slot == "a" else ina), ("boot_b", ina if slot == "a" else act)])
            rc, out = run(img, ss, "--cmdline", cmdline, "booted")
            check(rc == 0 and out == slot + "\n", f"{ss}: the 6T's command line (no slot suffix): booted is the active slot {slot}")
            rc, out = run(img, ss, "--cmdline", cmdline, "mark-good")
            prim, back = read_gpts(img, ss)
            mine, other = ("boot_" + slot, "boot_" + ("b" if slot == "a" else "a"))
            check(rc == 0 and prim[mine] >> 48 & 0xff == (act | SUCCESSFUL) and back[mine] == prim[mine]
                  and prim[other] >> 48 & 0xff == ina, f"{ss}: the 6T's command line: mark-good marks slot {slot}, nothing else")
        make_disk(img, ss, parts)
        rc, out = run(img, ss, "--cmdline", os.devnull, "slots")
        check(rc == 0 and out == "slot=a\ngood=a\ntries=0\nbooted=a\n", f"{ss}: slots before any is marked: {out!r}")
        before = open(img, "rb").read()
        rc, out = run(img, ss, "set-active", "b", "--dry-run")
        check(rc == 0 and open(img, "rb").read() == before, f"{ss}: a dry run of set-active writes nothing")
        run(img, ss, "--cmdline", os.devnull, "mark-good")
        rc, out = run(img, ss, "set-active", "b")
        prim, back = read_gpts(img, ss)
        # b: active, priority 3, 7 tries, not successful; a: inactive,
        # priority 2, still successful.
        want = {"xbl_a": ACTIVE, "boot_a": 0x7a, "boot_b": 0x3f, "boot_aa": 0, "userdata": 0}
        check(rc == 0 and all(prim[n] == back[n] == a << 48 | OTHER for n, a in want.items()),
              f"{ss}: set-active b as ABL's set_active, in both GPTs, nothing else")
        rc, out = run(img, ss, "--booted", "a", "slots")
        check(rc == 0 and out == "slot=b\ngood=a\ntries=7\nbooted=a\n", f"{ss}: slots after set-active b: {out!r}")
        check([run(img, ss, "get-state", s)[1] for s in "ab"] == ["good\n", "bad\n"], f"{ss}: get-state: a good, b bad")
        after = open(img, "rb").read()
        rc, out = run(img, ss, "set-active", "b")
        check(rc == 0 and "already" in out and open(img, "rb").read() == after, f"{ss}: set-active again writes nothing")
        rc, out = run(img, ss, "mark-unbootable", "b")
        prim, back = read_gpts(img, ss)
        check(rc == 0 and prim["boot_b"] >> 48 & 0xff == 0xbf and back["boot_b"] == prim["boot_b"]
              and prim["boot_a"] >> 48 & 0xff == 0x7a, f"{ss}: mark-unbootable b, nothing else")
        rc, out = run(img, ss, "--booted", "a", "slots")
        check(rc == 0 and out.startswith("slot=a\ngood=a\n"), f"{ss}: an unbootable active slot is not the next: {out!r}")
        rc, out = run(img, ss, "mark-good", "b")
        prim, _ = read_gpts(img, ss)
        check(rc == 0 and prim["boot_b"] >> 48 & 0xff == 0x7f and run(img, ss, "get-state", "b")[1] == "good\n",
              f"{ss}: mark-good b makes it bootable and successful")
        for args in (["set-active"], ["booted", "a"], ["get-state"], ["mark-unbootable", "c"], ["status", "--booted", "a"], ["frob"]):
            rc, out = run(img, ss, *args)
            check(rc == 2 and "usage" in out, f"{ss}: {' '.join(args)} is refused")
    shutil.rmtree(tmp)
    print("abslot-test: " + (f"{len(failed)} failed" if failed else "all passed"))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
