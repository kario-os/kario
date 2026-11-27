#!/usr/bin/env python3
"""Test yocto/fajita-flash.sh against a simulated OnePlus 6T: fastboot and
adb stand-ins keep its partitions as files and log every command.

backup copies the partitions through TWRP and writes nothing to the phone,
and never replaces a backup; test and flash refuse a phone without a backup
that checks out (none, changed, another phone's, one without the phone's
own partitions) and touch nothing then; a backup taken by hand without
boot and dtbo, which the phone had lost, is taken with a warning;
restore puts OxygenOS's boot and dtbo back from the backup, and refuses a
backup whose boot is not OxygenOS's, or whose boot or dtbo is missing or
erased, unless --stock names OxygenOS's own, writing nothing then;
restore-efs writes the IMEI and calibration partitions back.
test and flash refuse a boot image without phone-initramfs (the 6T's
until 2026-11-24, whose kernel mounted the bootloader's root=) or whose
ramdisk does not name its layout. Every image goes in downloads of at
most 64M (fastboot -S, --chunk), which a phone dropping a 512M one
takes.
The partitions come from the board file (tools/sku.py partitions): in the
userdata layout flash writes boot and userdata, in the A/B layout boot,
system_a and system_b and erases userdata; each checks every partition's
size before it writes anything and refuses one too small or missing;
neither touches a partition the table keeps, nor one of the phone's own,
even when a table says to; a dry run lists what it would write. flash
names the whole phone's test that comes next, and the system image it
writes is the chroot kit's root file system too
(tools/phone-chroot/unsparse.py).

    fajita-flash-test.py
"""
import gzip
import hashlib
import json
import os
import shutil
import struct
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "fajita-flash.sh")
SKU = os.path.join(os.path.dirname(HERE), "tools", "sku.py")
SERIAL = "a1b2c3d4"
BYNAME = "/dev/block/bootdevice/by-name"
EFS = ["persist", "modemst1", "modemst2", "fsg", "fsc"]
KEPT = ["vbmeta_a", "vbmeta_b", "misc", "modem_a", "modem_b", "vendor_a", "vendor_b", "frp"]
PARTS = EFS + ["boot_a", "boot_b", "dtbo_a", "dtbo_b", "system_a", "system_b", "userdata"] + KEPT
MIB = 1 << 20
# The phone's partition sizes, as its bootloader reports them.
SIZES = {**{p: 64 * MIB for p in PARTS}, "system_a": 3072 * MIB, "system_b": 3072 * MIB, "userdata": 65536 * MIB}

# The stand-ins: one program, called as fastboot or adb. FAKE_USB_MAX: the
# phone's USB drops a download bigger than that, as a 6T's did with 512M
# of the userdata image; the stand-in counts a sparse image by the bytes
# it unpacks to.
FAKE = r'''#!/usr/bin/env python3
import hashlib, json, os, shutil, struct, sys
phone = os.environ["FAKE_PHONE"]
tool = os.path.basename(sys.argv[0])
args = sys.argv[1:]
with open(os.path.join(phone, "log"), "a") as f:
    f.write(tool + " " + " ".join(args) + "\n")
def rd(name):
    with open(os.path.join(phone, name), "rb") as f:
        return f.read()
def wr(name, data):
    with open(os.path.join(phone, name), "wb") as f:
        f.write(data)
mode = rd("mode").decode()
serial = rd("serial").decode()
if args[:1] == ["-s"]:
    if args[1] != serial:
        sys.exit(1)
    args = args[2:]
chunk = None
if args[:1] == ["-S"]:
    chunk = args[1]
    args = args[2:]
def nbytes(s):
    units = {"K": 1 << 10, "M": 1 << 20, "G": 1 << 30}
    return int(s[:-1]) * units[s[-1]] if s[-1] in units else int(s)
def image_size(path):
    with open(path, "rb") as f:
        head = f.read(28)
    if head[:4] == b"\x3a\xff\x26\xed":
        return struct.unpack_from("<I", head, 12)[0] * struct.unpack_from("<I", head, 16)[0]
    return os.path.getsize(path)
def part(path):
    return "parts/" + path.rsplit("/", 1)[1]
if tool == "fastboot":
    if mode != "fastboot":
        sys.exit("< waiting for any device >")
    c = args[0]
    if c == "devices":
        print(serial + "\tfastboot")
    elif c == "getvar":
        v = {"product": "sdm845", "unlocked": rd("unlocked").decode(), "serialno": serial, "current-slot": rd("active").decode()}
        for name, st in json.loads(rd("slots")).items():
            v["slot-unbootable:" + name] = "yes" if st["unbootable"] else "no"
            v["slot-retry-count:" + name] = str(st["retry"])
            v["slot-successful:" + name] = "yes" if st["successful"] else "no"
        if args[1].startswith("partition-size:"):
            sizes = json.loads(rd("sizes"))
            name = args[1].split(":", 1)[1]
            if name not in sizes:
                sys.exit("getvar:%s FAILED (remote: 'GetVar Variable Not found')" % args[1])
            v[args[1]] = "0x%x" % sizes[name]
        print("%s: %s" % (args[1], v.get(args[1], "")), file=sys.stderr)
    elif c.startswith("--set-active=") or c == "set_active":
        # ABL's set_active: the slot active, bootable, all its tries, not
        # yet successful.
        name = c.split("=", 1)[1] if c != "set_active" else args[1]
        slots = json.loads(rd("slots"))
        slots[name] = {"unbootable": False, "retry": 7, "successful": False}
        wr("slots", json.dumps(slots).encode())
        wr("active", name.encode())
    elif c == "erase":
        wr("parts/" + args[1], b"")
    elif c == "flash":
        size = image_size(args[2])
        if "FAKE_USB_MAX" in os.environ and min(size, nbytes(chunk) if chunk else size) > nbytes(os.environ["FAKE_USB_MAX"]):
            sys.exit("fastboot: error: Write to device failed in SendBuffer() (no link)")
        shutil.copyfile(args[2], os.path.join(phone, "parts", args[1]))
    elif c == "-w":
        wr("parts/userdata", b"")
    elif c == "boot":
        if json.loads(rd("slots"))[rd("active").decode()]["unbootable"]:
            sys.exit("FAILED (remote: 'Load Error')")
        with open(args[1], "rb") as f:
            img = f.read()
        wr("mode", b"recovery" if b"TWRP" in img else b"kario")
    elif c == "reboot":
        wr("mode", b"android")
    else:
        sys.exit("fastboot: unknown " + c)
else:
    c = args[0]
    if c == "wait-for-recovery":
        sys.exit(0 if mode == "recovery" else 1)
    if mode != "recovery":
        sys.exit("adb: no device")
    if c == "shell":
        cmd = args[1]
        if cmd.startswith("for d in"):
            print("/dev/block/bootdevice/by-name")
        elif cmd.startswith("sha256sum "):
            p = cmd.split()[1]
            print(hashlib.sha256(rd(part(p))).hexdigest() + "  " + p)
        elif cmd.startswith("dd if=/tmp/"):
            src = cmd.split()[1][len("if=/tmp/"):]
            dst = cmd.split()[2][len("of="):]
            shutil.copyfile(os.path.join(phone, "tmp", src), os.path.join(phone, part(dst)))
            os.remove(os.path.join(phone, "tmp", src))
        else:
            sys.exit("adb shell: unknown " + cmd)
    elif c == "exec-out":
        src = args[1].split()[1][len("if="):]
        data = rd(part(src)) if src.startswith(BYNAME) else rd("luns/" + src.rsplit("/", 1)[1])
        sys.stdout.buffer.write(data)
    elif c == "push":
        shutil.copyfile(args[1], os.path.join(phone, "tmp", os.path.basename(args[2])))
    elif c == "reboot" and args[1:] == ["bootloader"]:
        wr("mode", b"fastboot")
    else:
        sys.exit("adb: unknown " + c)
'''.replace("BYNAME", repr(BYNAME))

failed = 0


def check(ok, what):
    global failed
    print(("ok   " if ok else "FAIL ") + what)
    if not ok:
        failed += 1


class Phone:
    def __init__(self, root, serial=SERIAL):
        self.dir = os.path.join(root, "phone")
        for d in ("parts", "luns", "tmp"):
            os.makedirs(os.path.join(self.dir, d))
        self.write("mode", b"fastboot")
        self.write("serial", serial.encode())
        self.write("unlocked", b"yes")
        self.write("active", b"a")
        self.slots({"a": {"unbootable": False, "retry": 7, "successful": True},
                    "b": {"unbootable": False, "retry": 7, "successful": True}})
        self.sizes(SIZES)
        for p in PARTS:
            data = os.urandom(8192)
            if p.startswith("boot_"):
                data = b"ANDROID!" + b"\0" * 56 + b"androidboot.hardware=qcom " + p.encode() + data
            elif p.startswith("dtbo_"):
                data = DTBO + data
            self.write("parts/" + p, data)
        for lun in "abcdef":
            self.write("luns/sd" + lun, b"EFI PART" + bytes([ord(lun)]) * 100)
        self.original = self.parts()

    def write(self, name, data):
        with open(os.path.join(self.dir, name), "wb") as f:
            f.write(data)

    def sizes(self, sizes):
        self.write("sizes", json.dumps(sizes).encode())

    def slots(self, slots=None):
        """The slots' attributes as ABL reports them (getvar slot-*);
        with an argument, set them."""
        if slots is not None:
            self.write("slots", json.dumps(slots).encode())
        return json.loads(self.read("slots"))

    def read(self, name):
        with open(os.path.join(self.dir, name), "rb") as f:
            return f.read()

    def parts(self):
        return {p: self.read("parts/" + p) for p in PARTS}

    def log(self):
        """The commands, without the -s naming the phone."""
        path = os.path.join(self.dir, "log")
        lines = open(path).read().splitlines() if os.path.exists(path) else []
        return [" ".join(w for i, w in enumerate(l.split()) if not (w == "-s" or (i > 0 and l.split()[i - 1] == "-s")))
                for l in lines]

    def clear_log(self):
        path = os.path.join(self.dir, "log")
        if os.path.exists(path):
            os.remove(path)


def writes(log):
    return [l for l in log if l.split()[:1] == ["fastboot"] and
            any(w in ("erase", "flash", "-w") or w.startswith("--set-active") for w in l.split())]


def sparse(blocks, data=b"kario"):
    """An Android sparse image's header: its blocks of 4096 bytes."""
    return struct.pack("<IHHHHIIII", 0xed26ff3a, 1, 0, 28, 12, 4096, blocks, 0, 0) + data


def newc(files):
    """A cpio archive (newc) of (name, data), padded to 512 bytes as cpio
    pads it."""
    out = bytearray()
    for i, (name, data) in enumerate(files + [("TRAILER!!!", b"")], 1):
        nm = name.encode() + b"\0"
        out += b"070701" + b"".join(b"%08X" % v for v in (i, 0o100644, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(nm), 0))
        out += nm + b"\0" * (-(110 + len(nm)) % 4) + data + b"\0" * (-len(data) % 4)
    return bytes(out) + b"\0" * (-len(out) % 512)


# Android's device tree overlay table starts with it (dt_table_header).
DTBO = bytes.fromhex("d7b7ab1e")
# phone-initramfs, compressed (only its magic is looked at).
INITRAMFS = gzip.compress(b"070701 phone-initramfs" * 64)
# What the userdata layout's boot image carries (phone-bootimg), the A/B
# layout's (phone-abboot), and the 6T's before 2026-11-24: an empty archive.
USERDATA_RAMDISK = newc([("etc/phone/layout", b"layout=userdata\n")]) + INITRAMFS
AB_RAMDISK = newc([("etc/phone/rootfs.verity", b"root_hash=00\n"), ("etc/phone/rootfs.verity.sig", b"sig")]) + INITRAMFS
EMPTY_RAMDISK = newc([])


def main():
    root = tempfile.mkdtemp(prefix="fajita-flash-test.")
    try:
        bin_dir = os.path.join(root, "bin")
        os.makedirs(bin_dir)
        for tool in ("fastboot", "adb"):
            with open(os.path.join(bin_dir, tool), "w") as f:
                f.write(FAKE)
            os.chmod(os.path.join(bin_dir, tool), 0o755)
        def boot_image(cmdline, compatible, ramdisk=USERDATA_RAMDISK):
            """A boot image (header version 0, pages of 4096 bytes): the
            header with the command line at byte 64, a kernel with a device
            tree appended, the ramdisk."""
            kernel = b"\x1f\x8b kernel" + b"\xd0\x0d\xfe\xed\0compatible\0" + compatible + b"\0qcom,sdm845\0"
            head = struct.pack("<8sIIIIIIIIII16s", b"ANDROID!", len(kernel), 0x8000, len(ramdisk), 0x1000000, 0, 0xf00000,
                               0x100, 4096, 0, 0, b"") + cmdline
            page = lambda b: b + b"\0" * (-len(b) % 4096)
            return page(head) + page(kernel) + page(ramdisk)

        def images_dir(name, boot, userdata=sparse(1000, b"kario userdata"), abboot=None, slot=sparse(2000, b"kario slot")):
            d = os.path.join(root, name)
            os.makedirs(d)
            files = {"boot-oneplus-fajita.img": boot, "phone-image-oneplus-fajita.rootfs.ext4.sparse": userdata,
                     "phone-image-oneplus-fajita.rootfs.ext4.verity.sparse": slot}
            if abboot:
                files["phone-image-oneplus-fajita.rootfs.abboot"] = abboot
            for f, data in files.items():
                with open(os.path.join(d, f), "wb") as out:
                    out.write(data)
            return d

        images = images_dir("images", boot_image(b"root=PARTLABEL=userdata phone.gui=1", b"oneplus,fajita"))
        others = {
            "the OnePlus 6's build": (images_dir("enchilada", boot_image(b"root=PARTLABEL=userdata phone.gui=1", b"oneplus,enchilada")),
                                      "its device tree says oneplus,enchilada, not oneplus,fajita"),
            "Nura's boot image": (images_dir("nura", boot_image(b"pmos_root_uuid=1 console=ttyMSM0", b"oneplus,fajita")),
                                  "is not Kario's boot image"),
            "a userdata that is not sparse": (images_dir("raw", boot_image(b"phone.gui=1", b"oneplus,fajita"), b"\x53\xef" + b"\0" * 64),
                                              "is not an Android sparse image"),
            # The 6T's boot image as built before 2026-11-24.
            "a boot image without an initramfs": (
                images_dir("noinitramfs", boot_image(b"root=PARTLABEL=userdata rootfstype=ext4 rootwait rw console=ttyMSM0,115200 phone.gui=1 console=tty0",
                                                     b"oneplus,fajita", EMPTY_RAMDISK)),
                "has no initramfs (its ramdisk is 512 bytes)"),
            "a ramdisk naming no layout": (images_dir("nolayout", boot_image(b"root=PARTLABEL=userdata phone.gui=1", b"oneplus,fajita", AB_RAMDISK)),
                                           "ramdisk does not name the userdata layout"),
        }
        twrp = os.path.join(root, "twrp-fajita.img")
        with open(twrp, "wb") as f:
            f.write(b"ANDROID!" + b"TWRP" * 64)
        home = os.path.join(root, "home")
        os.makedirs(home)
        phone = Phone(root)
        env = {**os.environ, "PATH": bin_dir + os.pathsep + os.environ["PATH"], "HOME": home,
               "FAKE_PHONE": phone.dir, "WORK": os.path.join(root, "work")}
        backup = os.path.join(home, "kario-phone-backup", "oneplus-fajita-" + SERIAL)

        def run(*args):
            r = subprocess.run(["sh", SCRIPT, "-y", "-d", images] + list(args), env=env, capture_output=True, text=True)
            return r.returncode, r.stdout + r.stderr

        def run_in(e, d, *args):
            r = subprocess.run(["sh", SCRIPT, "-y", "-d", d] + list(args), env=e, capture_output=True, text=True)
            return r.returncode, r.stdout + r.stderr

        rc, out = run("flash")
        check(rc != 0 and "no backup" in out and not writes(phone.log()), "flash without a backup is refused, nothing written")
        phone.clear_log()
        rc, out = run("test")
        check(rc != 0 and not writes(phone.log()), "test without a backup is refused, nothing written")

        phone.clear_log()
        rc, out = run("backup", twrp)
        check(rc == 0, "backup runs: " + out.strip().splitlines()[-1] if out.strip() else "backup runs")
        check(not writes(phone.log()) and phone.parts() == phone.original, "backup writes nothing to the phone")
        check(phone.read("mode") == b"fastboot", "backup leaves the phone in fastboot mode")
        sums = open(os.path.join(backup, "SHA256SUMS")).read()
        names = [l.split("  ")[1] for l in sums.splitlines()]
        backed = EFS + ["boot_a", "boot_b", "dtbo_a", "dtbo_b"]
        check(sorted(n for n in names if n.endswith(".img")) == sorted(p + ".img" for p in backed),
              "the backup has persist, the IMEI's four, boot and dtbo of both slots, not userdata or system")
        check(all(open(os.path.join(backup, p + ".img"), "rb").read() == phone.original[p] for p in backed),
              "every partition copied byte for byte")
        check(all(("gpt-sd%s.bin" % l) in names for l in "abcdef"), "each LUN's GPT copied")
        info = open(os.path.join(backup, "phone.txt")).read()
        check("serial=" + SERIAL in info and "stock-boot=yes" in info, "phone.txt names the phone, its boot is OxygenOS's")

        persist = os.path.join(backup, "persist.img")
        os.chmod(persist, 0o644)
        before = os.path.getmtime(persist)
        phone.clear_log()
        rc, out = run("backup", twrp)
        check(rc == 0 and "never replaced" in out and os.path.getmtime(persist) == before,
              "a second backup replaces nothing")
        check(not any(l.startswith("fastboot boot") for l in phone.log()), "a second backup does not even start TWRP")

        for what, (d, why) in others.items():
            for cmd in ("flash", "test"):
                if cmd == "test" and "userdata" in what:
                    continue
                phone.clear_log()
                r = subprocess.run(["sh", SCRIPT, "-y", "-d", d, cmd], env=env, capture_output=True, text=True)
                check(r.returncode != 0 and why in r.stderr and not writes(phone.log()) and phone.parts() == phone.original,
                      "%s refuses %s, nothing written" % (cmd, what))
            r = subprocess.run(["sh", SCRIPT, "-n", "-d", d, "flash"], env=env, capture_output=True, text=True)
            check(r.returncode != 0 and why in r.stderr, "a dry run refuses %s too" % what)

        phone.clear_log()
        rc, out = run("flash")
        check(rc == 0, "flash with the backup runs")
        check("tools/phone-chroot/kario-phone.sh --whole --sku oneplus-fajita-t9 push setup selftest pixel collect" in out,
              "flash names the whole phone's test that comes next")
        w = writes(phone.log())
        check(w == ["fastboot erase dtbo_a", "fastboot erase dtbo_b",
                    "fastboot -S 64M flash boot_a " + os.path.join(images, "boot-oneplus-fajita.img"),
                    "fastboot -S 64M flash boot_b " + os.path.join(images, "boot-oneplus-fajita.img"),
                    "fastboot -S 64M flash userdata " + os.path.join(images, "phone-image-oneplus-fajita.rootfs.ext4.sparse"),
                    "fastboot --set-active=a"],
              "flash erases dtbo, writes boot_a, boot_b and userdata in downloads of at most 64M, makes the slot it booted "
              "active again, and nothing else")
        check(phone.slots()["a"] == {"unbootable": False, "retry": 7, "successful": False}
              and phone.slots()["b"]["successful"], "set_active gave slot a all its tries, slot b is untouched")
        check(all(phone.read("parts/" + p) == phone.original[p] for p in EFS + KEPT + ["system_a", "system_b"]),
              "flash leaves persist, the IMEI's partitions, the system slots and what the table keeps alone")
        check("layout=userdata" in open(os.path.join(backup, "kario.txt")).read(), "flash notes the layout beside the backup")
        check("fastboot getvar partition-size:userdata" in phone.log(), "flash reads userdata's size first")

        phone.write("mode", b"fastboot")
        phone.clear_log()
        rc, out = run("restore")
        check(rc == 0, "restore runs")
        check(all(phone.read("parts/" + p) == phone.original[p] for p in ("boot_a", "boot_b", "dtbo_a", "dtbo_b")),
              "restore puts OxygenOS's boot and dtbo back on both slots")
        check(phone.read("parts/userdata") == b"" and "fastboot -w" in phone.log(), "restore wipes userdata")

        # A slot whose tries failed boots spent: ABL marks it unbootable and
        # refuses even fastboot boot ("Load Error") until set_active, as on
        # the 6T of 2026-11-24 (39-oneplus-6t.md §8.2).
        spent = {"a": {"unbootable": True, "retry": 0, "successful": False}, "b": {"unbootable": True, "retry": 0, "successful": False}}
        stuck = Phone(os.path.join(root, "stuck"))
        stuck.slots(spent)
        envs = {**env, "FAKE_PHONE": stuck.dir}
        r = subprocess.run(["fastboot", "boot", os.path.join(images, "boot-oneplus-fajita.img")], env=envs, capture_output=True, text=True)
        check(r.returncode != 0 and "Load Error" in r.stderr, "the stand-in refuses fastboot boot from an unbootable slot")
        stuck.clear_log()
        rc, out = run_in(envs, images, "test")
        log = stuck.log()
        check(rc == 0 and "fastboot --set-active=a" in log and "fastboot boot " + os.path.join(images, "boot-oneplus-fajita.img") in log
              and log.index("fastboot --set-active=a") < log.index("fastboot boot " + os.path.join(images, "boot-oneplus-fajita.img"))
              and stuck.read("mode") == b"kario", "test makes the unbootable slot active, then boots Kario from it")
        check(stuck.slots()["a"] == {"unbootable": False, "retry": 7, "successful": False} and stuck.slots()["b"] == spent["b"],
              "set_active cleared slot a's unbootable mark and gave it its tries back, slot b untouched")
        stuck.write("mode", b"fastboot")
        stuck.slots(spent)
        rc, out = run_in(envs, images, "flash")
        check(rc == 0 and stuck.slots()["a"]["retry"] == 7 and not stuck.slots()["a"]["unbootable"],
              "flash leaves the slot it wrote bootable, with all its tries")
        stuck.write("mode", b"fastboot")
        stuck.slots(spent)
        rc, out = run_in(envs, images, "restore")
        check(rc == 0 and not stuck.slots()["a"]["unbootable"] and "fastboot -w" in stuck.log(),
              "restore makes the slot bootable again too, or OxygenOS would not start")

        data = bytearray(open(persist, "rb").read())
        data[0] ^= 1
        open(persist, "wb").write(bytes(data))
        phone.write("mode", b"fastboot")
        phone.clear_log()
        rc, out = run("flash")
        check(rc != 0 and not writes(phone.log()), "a changed backup is refused, nothing written")
        data[0] ^= 1
        open(persist, "wb").write(bytes(data))

        other = Phone(os.path.join(root, "other"), serial="ffff0000")
        env2 = {**env, "FAKE_PHONE": other.dir}
        r = subprocess.run(["sh", SCRIPT, "-y", "-d", images, "-b", backup, "flash"], env=env2, capture_output=True, text=True)
        check(r.returncode != 0 and not writes(other.log()), "another phone's backup is refused, nothing written")

        lost = Phone(os.path.join(root, "lost"))
        for p in ("persist", "modemst1", "modemst2", "fsg", "fsc"):
            lost.write("parts/" + p, b"\0" * 8192)
        env3 = {**env, "FAKE_PHONE": lost.dir}
        r = subprocess.run(["sh", SCRIPT, "-y", "-d", images, "restore-efs", twrp], env=env3, capture_output=True, text=True)
        check(r.returncode == 0 and all(lost.read("parts/" + p) == phone.original[p] for p in ("persist", "modemst1", "modemst2", "fsg", "fsc")),
              "restore-efs writes persist and the IMEI's partitions back")
        check(lost.read("parts/boot_a") == lost.original["boot_a"] and lost.read("mode") == b"fastboot",
              "restore-efs touches nothing else and ends in fastboot mode")

        kario = Phone(os.path.join(root, "kario"))
        kario.write("parts/boot_a", open(os.path.join(images, "boot-oneplus-fajita.img"), "rb").read())
        env4 = {**env, "FAKE_PHONE": kario.dir, "HOME": os.path.join(root, "home4")}
        os.makedirs(env4["HOME"])
        r = subprocess.run(["sh", SCRIPT, "-y", "-d", images, "backup", twrp], env=env4, capture_output=True, text=True)
        check(r.returncode == 0 and "--stock" in r.stdout + r.stderr, "a backup taken over Kario says restore wants --stock")
        kario.clear_log()
        r = subprocess.run(["sh", SCRIPT, "-y", "-d", images, "restore"], env=env4, capture_output=True, text=True)
        check(r.returncode != 0 and not writes(kario.log()), "restore refuses a backup whose boot is Kario's")
        stock = os.path.join(root, "stock")
        os.makedirs(stock)
        shutil.copyfile(os.path.join(phone.dir, "parts", "boot_a"), os.path.join(stock, "boot.img"))
        shutil.copyfile(os.path.join(phone.dir, "parts", "dtbo_a"), os.path.join(stock, "dtbo.img"))
        r = subprocess.run(["sh", SCRIPT, "-y", "-d", images, "--stock", stock, "restore"], env=env4, capture_output=True, text=True)
        check(r.returncode == 0 and kario.read("parts/boot_a") == phone.original["boot_a"]
              and kario.read("parts/boot_b") == phone.original["boot_a"], "restore --stock writes OxygenOS's own to both slots")
        with open(os.path.join(stock, "dtbo.img"), "wb") as f:
            f.write(b"\0" * 8192)
        kario.write("mode", b"fastboot")
        kario.clear_log()
        r = subprocess.run(["sh", SCRIPT, "-y", "-d", images, "--stock", stock, "restore"], env=env4, capture_output=True, text=True)
        check(r.returncode != 0 and "is not a device tree overlay image" in r.stderr and not writes(kario.log()),
              "restore --stock refuses a dtbo.img that is not a device tree overlay image, nothing written")
        shutil.copyfile(os.path.join(phone.dir, "parts", "dtbo_a"), os.path.join(stock, "dtbo.img"))

        # A backup taken by hand after the phone had lost boot_a (Nura's boot
        # in it) and dtbo (erased), as on a 6T (39-oneplus-6t.md §9.1).
        def hand_backup(name, phone_, parts, stock_boot):
            home_ = os.path.join(root, name)
            d = os.path.join(home_, "kario-phone-backup", "oneplus-fajita-" + SERIAL)
            os.makedirs(d)
            for p, data in parts.items():
                with open(os.path.join(d, p + ".img"), "wb") as f:
                    f.write(phone_.original[p] if data is None else data)
            with open(os.path.join(d, "phone.txt"), "w") as f:
                f.write("serial=%s\nstock-boot=%s\n" % (SERIAL, stock_boot))
            sums = "".join("%s  %s.img\n" % (hashlib.sha256(open(os.path.join(d, p + ".img"), "rb").read()).hexdigest(), p)
                           for p in parts)
            with open(os.path.join(d, "SHA256SUMS"), "w") as f:
                f.write(sums)
            return {**env, "FAKE_PHONE": phone_.dir, "HOME": home_}

        nura = Phone(os.path.join(root, "nura-phone"))
        envn = hand_backup("home-nura", nura, {**{p: None for p in EFS}, "boot_b": None}, "no")
        rc, out = run_in(envn, images, "flash")
        check(rc == 0 and all("the backup has no %s.img" % p in out for p in ("boot_a", "dtbo_a", "dtbo_b"))
              and "restore will want --stock" in out and "the backup has no boot_b.img" not in out,
              "flash takes a backup without boot_a, dtbo_a and dtbo_b, saying restore will want --stock")
        nura.write("mode", b"fastboot")
        nura.clear_log()
        rc, out = run_in(envn, images, "restore")
        check(rc != 0 and "--stock DIR with OxygenOS's boot.img and dtbo.img" in out and not writes(nura.log()),
              "restore from it without --stock is refused, nothing written")
        claims = Phone(os.path.join(root, "claims-phone"))
        envc = hand_backup("home-claims", claims, {**{p: None for p in EFS}, "boot_a": None, "boot_b": None, "dtbo_b": None}, "yes")
        rc, out = run_in(envc, images, "restore")
        check(rc != 0 and "dtbo_a.img is missing or not a device tree overlay image" in out and "nothing written" in out
              and not writes(claims.log()), "restore refuses a backup without dtbo_a even when it says its boot is OxygenOS's")
        erased = Phone(os.path.join(root, "erased-phone"))
        enve = hand_backup("home-erased", erased, {**{p: None for p in EFS}, "boot_a": None, "boot_b": None,
                                                   "dtbo_a": b"\0" * 8192, "dtbo_b": b"\0" * 8192}, "yes")
        rc, out = run_in(enve, images, "restore")
        check(rc != 0 and "dtbo_a.img is missing or not a device tree overlay image" in out and not writes(erased.log()),
              "restore never writes back a dtbo copied after it was erased")
        rc, out = run_in(envn, images, "--stock", stock, "restore")
        check(rc == 0 and all(nura.read("parts/" + p) == phone.original["boot_a"] for p in ("boot_a", "boot_b"))
              and all(nura.read("parts/" + p) == phone.original["dtbo_a"] for p in ("dtbo_a", "dtbo_b")) and "fastboot -w" in nura.log(),
              "restore --stock puts OxygenOS's boot and dtbo on both slots of that phone")
        noefs = Phone(os.path.join(root, "noefs-phone"))
        envx = hand_backup("home-noefs", noefs, {p: None for p in EFS if p != "fsc"}, "no")
        rc, out = run_in(envx, images, "flash")
        check(rc != 0 and "no backup of phone" in out and not writes(noefs.log()), "a backup without fsc, one of the phone's own, is refused")

        # The A/B layout (43-phone-partitions.md): the same backup, another
        # phone with the same serial.
        abboot_data = boot_image(b"root=/dev/dm-0 phone.gui=1 phone.layout=ab", b"oneplus,fajita", AB_RAMDISK)
        ab = images_dir("ab", boot_image(b"root=PARTLABEL=userdata phone.gui=1", b"oneplus,fajita"), abboot=abboot_data)
        abboot = os.path.join(ab, "phone-image-oneplus-fajita.rootfs.abboot")
        slotimg = os.path.join(ab, "phone-image-oneplus-fajita.rootfs.ext4.verity.sparse")
        want_ab = ["fastboot erase dtbo_a", "fastboot erase dtbo_b", "fastboot -S 64M flash boot_a " + abboot, "fastboot -S 64M flash boot_b " + abboot,
                   "fastboot -S 64M flash system_a " + slotimg, "fastboot -S 64M flash system_b " + slotimg,
                   "fastboot erase userdata", "fastboot --set-active=a"]

        def ab_phone(name, sizes=None):
            p = Phone(os.path.join(root, name))
            if sizes is not None:
                p.sizes(sizes)
            return p, {**env, "FAKE_PHONE": p.dir}

        r = subprocess.run(["sh", SCRIPT, "-n", "-d", ab, "--layout", "ab", "flash"], env={**env, "PATH": os.environ["PATH"]},
                           capture_output=True, text=True)
        fb = [l for l in r.stdout.splitlines() if l.startswith("fastboot ")]
        check(r.returncode == 0 and fb == want_ab + ["fastboot reboot"], "a dry run of the A/B layout lists its fastboot commands: %s" % fb)
        check("# write system_b: phone-image-oneplus-fajita.rootfs.ext4.verity.sparse, 8192000 bytes "
              "(partition-size:system_b must be at least that)\n" in r.stdout and "# erase userdata\n" in r.stdout,
              "the dry run says each partition written, the image's size and what it is checked against")
        kept = [l for l in r.stdout.splitlines() if l.startswith("# kept, never written: ")]
        check(kept and "vbmeta_a" in kept[0] and "vendor_b" in kept[0] and "persist" not in kept[0] and "system_a" not in kept[0]
              and "# the phone's own, never written (in the backup): persist modemst1 modemst2 fsg fsc\n" in r.stdout,
              "the dry run lists what is kept and the phone's own partitions")

        abphone, envab = ab_phone("abphone")
        rc, out = run_in(envab, images, "--layout", "ab", "flash")
        check(rc != 0 and "rootfs.abboot" in out and not writes(abphone.log()), "--layout ab without its images is refused, nothing written")
        mixed = images_dir("mixed", abboot_data)
        rc, out = run_in(envab, mixed, "flash")
        check(rc != 0 and "is the A/B layout's boot image: --layout ab" in out and not writes(abphone.log()),
              "the A/B layout's boot image is refused in the userdata layout")
        plain = images_dir("plain", boot_image(b"phone.gui=1", b"oneplus,fajita"), abboot=boot_image(b"phone.gui=1", b"oneplus,fajita"))
        rc, out = run_in(envab, plain, "--layout", "ab", "flash")
        check(rc != 0 and "not the A/B layout's boot image" in out and not writes(abphone.log()),
              "a boot image without phone.layout=ab is refused in the A/B layout")
        nodesc = images_dir("nodesc", boot_image(b"phone.gui=1", b"oneplus,fajita"),
                            abboot=boot_image(b"root=/dev/dm-0 phone.gui=1 phone.layout=ab", b"oneplus,fajita"))
        rc, out = run_in(envab, nodesc, "--layout", "ab", "flash")
        check(rc != 0 and "ramdisk holds no verity descriptor of the root" in out and not writes(abphone.log()),
              "an A/B boot image whose ramdisk has no root descriptor is refused")

        abphone.clear_log()
        rc, out = run_in(envab, ab, "--layout", "ab", "flash")
        check(rc == 0, "flash in the A/B layout runs: " + out.strip().splitlines()[-1])
        check(writes(abphone.log()) == want_ab, "it erases dtbo, writes boot and system of both slots, erases userdata, "
                                                "makes the slot it booted active again, nothing else: %s" % writes(abphone.log()))
        check(abphone.read("parts/system_b") == open(slotimg, "rb").read() and abphone.read("parts/boot_a") == abboot_data
              and abphone.read("parts/userdata") == b"", "system_b holds the slot image, boot_a the A/B boot image, userdata is empty")
        check(all(abphone.read("parts/" + p) == abphone.original[p] for p in EFS + KEPT),
              "the A/B layout leaves persist, the IMEI's partitions, vbmeta, misc, the modem's, vendor and frp alone")
        sized = [l for l in abphone.log() if "partition-size:" in l]
        first = abphone.log().index(writes(abphone.log())[0])
        check(all(abphone.log().index(l) < first for l in sized) and len(sized) == 7,
              "every partition it changes is sized before the first write")
        check("layout=ab" in open(os.path.join(backup, "kario.txt")).read(), "the A/B layout noted beside the backup")

        abphone.write("mode", b"fastboot")
        abphone.clear_log()
        rc, out = run_in(envab, ab, "--layout", "ab", "test")
        check(rc == 0 and writes(abphone.log()) == want_ab[:2] + ["fastboot --set-active=a"] and "fastboot boot " + abboot in abphone.log(),
              "test in the A/B layout boots its boot image and writes nothing but dtbo and the slot's attributes")

        small, envs = ab_phone("small", {**SIZES, "system_b": 4 * MIB})
        rc, out = run_in(envs, ab, "--layout", "ab", "flash")
        check(rc != 0 and "system_b is 4194304 bytes, phone-image-oneplus-fajita.rootfs.ext4.verity.sparse needs 8192000: nothing written" in out
              and not writes(small.log()) and small.parts() == small.original, "a system slot too small is refused before anything is written")
        missing, envm = ab_phone("missing", {k: v for k, v in SIZES.items() if k != "system_b"})
        rc, out = run_in(envm, ab, "--layout", "ab", "flash")
        check(rc != 0 and "the phone reports no partition system_b" in out and not writes(missing.log()),
              "a phone without system_b is refused, nothing written")
        tiny, envt = ab_phone("tiny", {**SIZES, "userdata": 1 * MIB})
        rc, out = run_in(envt, images, "flash")
        check(rc != 0 and "userdata is 1048576 bytes" in out and not writes(tiny.log()), "a userdata too small for the image is refused")

        # Whatever the table says, the phone's own partitions and the
        # firmware are never written.
        table = subprocess.run([sys.executable, SKU, "partitions", "oneplus-fajita"], capture_output=True, text=True, check=True).stdout
        for what, layout, old, new in (("persist as the data", "userdata", "userdata data write rootfs", "persist data write rootfs"),
                                       ("modem_a as a root slot", "ab", "system_a root write slot", "modem_a root write slot"),
                                       ("fsg erased", "userdata", "dtbo_b dtbo erase -", "fsg dtbo erase -")):
            t = table if layout == "userdata" else subprocess.run([sys.executable, SKU, "partitions", "oneplus-fajita", "ab"],
                                                                 capture_output=True, text=True, check=True).stdout
            path = os.path.join(root, "table-" + what.split()[0])
            with open(path, "w") as f:
                f.write(t.replace(old, new))
            bad, envb = ab_phone("bad-" + what.split()[0])
            for dry in ([], ["-n"]):
                r = subprocess.run(["sh", SCRIPT, "-y", "-d", ab, "--layout", layout] + dry + ["flash"],
                                   env={**envb, "PHONE_PARTITIONS": path}, capture_output=True, text=True)
                check(r.returncode != 0 and "refusing: the partition table lets a flash change " + new.split()[0] in r.stderr
                      and not writes(bad.log()) and bad.parts() == bad.original,
                      "a table with %s is refused%s, nothing written" % (what, " in a dry run" if dry else ""))

        # Back to OxygenOS from the A/B layout: its system slots too, from
        # OxygenOS's package.
        abphone.write("mode", b"fastboot")
        abphone.clear_log()
        rc, out = run_in(envab, ab, "restore")
        check(rc != 0 and "--stock DIR with OxygenOS's boot.img, dtbo.img and system.img" in out and not writes(abphone.log()),
              "restore after the A/B layout wants OxygenOS's system.img, nothing written")
        rc, out = run_in(envab, ab, "--stock", stock, "restore")
        check(rc != 0 and "wants system.img too" in out and not writes(abphone.log()), "--stock without system.img is refused")
        with open(os.path.join(stock, "system.img"), "wb") as f:
            f.write(b"oxygenos system" * 100)
        rc, out = run_in(envab, ab, "--stock", stock, "restore")
        check(rc == 0 and all(abphone.read("parts/" + p) == phone.original["boot_a"] for p in ("boot_a", "boot_b"))
              and all(abphone.read("parts/" + p) == b"oxygenos system" * 100 for p in ("system_a", "system_b"))
              and "fastboot -w" in abphone.log() and not os.path.exists(os.path.join(backup, "kario.txt")),
              "restore --stock writes OxygenOS's boot, dtbo and system to both slots and wipes userdata")
        check(all(abphone.read("parts/" + p) == abphone.original[p] for p in EFS + KEPT), "restore leaves the rest alone")

        r = subprocess.run(["sh", SCRIPT, "-n", "-d", images, "flash"], env={**env, "PATH": os.environ["PATH"]},
                           capture_output=True, text=True)
        check(r.returncode == 0 and "flash boot_a" in r.stdout and r.stdout.splitlines()[-3:-1] == ["fastboot --set-active=a", "fastboot reboot"],
              "a dry run needs no phone and no backup, and lists the set_active")

        # Downloads of at most --chunk; a phone whose USB drops a download
        # over 100M (a 6T dropped the userdata image's first 512M after
        # 471 MB) takes the default, not 512M.
        r = subprocess.run(["sh", SCRIPT, "-n", "-d", images, "--chunk", "16M", "flash"], env={**env, "PATH": os.environ["PATH"]},
                           capture_output=True, text=True)
        fb = [l for l in r.stdout.splitlines() if l.startswith("fastboot ") and " flash " in l]
        check(r.returncode == 0 and len(fb) == 3 and all(l.startswith("fastboot -S 16M flash ") for l in fb), "--chunk 16M: %s" % fb)
        for i, bad_chunk in enumerate(("0", "64MB", "1.5G", "M", "", "64M; reboot")):
            usb, envu = ab_phone("chunk-%d" % i)
            rc, out = run_in(envu, images, "--chunk", bad_chunk, "flash")
            check(rc != 0 and "--chunk is a size such as 64M" in out and not writes(usb.log()), "--chunk %r is refused, nothing written" % bad_chunk)
        big = images_dir("big", boot_image(b"root=PARTLABEL=userdata phone.gui=1", b"oneplus,fajita"), sparse(30000, b"kario userdata"))
        bigdata = os.path.join(big, "phone-image-oneplus-fajita.rootfs.ext4.sparse")
        usb, envu = ab_phone("usb")
        envu["FAKE_USB_MAX"] = "100M"
        rc, out = run_in(envu, big, "flash")
        check(rc == 0 and usb.read("parts/userdata") == open(bigdata, "rb").read(),
              "flash writes a 117 MiB userdata image to a phone that drops downloads over 100M")
        usb, envu = ab_phone("usb512")
        envu["FAKE_USB_MAX"] = "100M"
        rc, out = run_in(envu, big, "--chunk", "512M", "flash")
        check(rc != 0 and "SendBuffer" in out and usb.read("parts/userdata") == usb.original["userdata"],
              "in downloads of 512M the same phone fails at userdata, as the 6T did")

        # The userdata image flash writes (phone-img2simg.py's sparse form of
        # the root file system) is the chroot kit's image as well: unpacked,
        # it is the ext4 again, byte for byte.
        raw = os.path.join(root, "rootfs.ext4")
        ext4 = bytearray(64 * 4096)
        ext4[1080:1082] = b"\x53\xef"
        ext4[4096:3 * 4096] = os.urandom(2 * 4096)
        ext4[8 * 4096:9 * 4096] = b"\x01\x00\x00\x00" * 1024
        with open(raw, "wb") as f:
            f.write(ext4)
        simg = os.path.join(root, "rootfs.ext4.sparse")
        subprocess.run([sys.executable, os.path.join(HERE, "meta-phone", "files", "phone-img2simg.py"), raw, simg], check=True)
        with open(simg, "rb") as f:
            check(f.read(4) == b"\x3a\xff\x26\xed", "phone-img2simg writes the sparse format flash checks for")
        back = subprocess.run([sys.executable, os.path.join(os.path.dirname(HERE), "tools", "phone-chroot", "unsparse.py"), simg],
                              capture_output=True)
        check(back.returncode == 0 and back.stdout == bytes(ext4), "the kit unpacks the userdata image to the root file system")
    finally:
        shutil.rmtree(root, ignore_errors=True)
    print("fajita-flash-test: " + ("all passed" if not failed else "%d failed" % failed))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
