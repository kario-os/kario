#!/usr/bin/env python3
"""Test yocto/fajita-flash.sh against a simulated OnePlus 6T: fastboot and
adb stand-ins keep its partitions as files and log every command.

backup copies the partitions through TWRP and writes nothing to the phone,
and never replaces a backup; test and flash refuse a phone without a backup
that checks out (none, changed, another phone's) and touch nothing then;
restore puts OxygenOS's boot and dtbo back from the backup, and refuses a
backup whose boot is not OxygenOS's unless --stock names OxygenOS's own;
restore-efs writes the IMEI and calibration partitions back.

    fajita-flash-test.py
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "fajita-flash.sh")
SERIAL = "a1b2c3d4"
BYNAME = "/dev/block/bootdevice/by-name"
PARTS = ["persist", "modemst1", "modemst2", "fsg", "fsc", "boot_a", "boot_b", "dtbo_a", "dtbo_b", "userdata"]

# The stand-ins: one program, called as fastboot or adb.
FAKE = r'''#!/usr/bin/env python3
import hashlib, os, shutil, sys
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
if args[:1] == ["-S"]:
    args = args[2:]
def part(path):
    return "parts/" + path.rsplit("/", 1)[1]
if tool == "fastboot":
    if mode != "fastboot":
        sys.exit("< waiting for any device >")
    c = args[0]
    if c == "devices":
        print(serial + "\tfastboot")
    elif c == "getvar":
        v = {"product": "sdm845", "unlocked": rd("unlocked").decode(), "serialno": serial, "current-slot": "a"}
        print("%s: %s" % (args[1], v.get(args[1], "")), file=sys.stderr)
    elif c == "erase":
        wr("parts/" + args[1], b"")
    elif c == "flash":
        shutil.copyfile(args[2], os.path.join(phone, "parts", args[1]))
    elif c == "-w":
        wr("parts/userdata", b"")
    elif c == "boot":
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
        for p in PARTS:
            data = os.urandom(8192)
            if p.startswith("boot_"):
                data = b"ANDROID!" + b"\0" * 56 + b"androidboot.hardware=qcom " + p.encode() + data
            self.write("parts/" + p, data)
        for lun in "abcdef":
            self.write("luns/sd" + lun, b"EFI PART" + bytes([ord(lun)]) * 100)
        self.original = self.parts()

    def write(self, name, data):
        with open(os.path.join(self.dir, name), "wb") as f:
            f.write(data)

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
            any(w in l.split() for w in ("erase", "flash", "-w"))]


def main():
    root = tempfile.mkdtemp(prefix="fajita-flash-test.")
    try:
        bin_dir = os.path.join(root, "bin")
        os.makedirs(bin_dir)
        for tool in ("fastboot", "adb"):
            with open(os.path.join(bin_dir, tool), "w") as f:
                f.write(FAKE)
            os.chmod(os.path.join(bin_dir, tool), 0o755)
        images = os.path.join(root, "images")
        os.makedirs(images)
        with open(os.path.join(images, "boot-oneplus-fajita.img"), "wb") as f:
            f.write(b"ANDROID!" + b"\0" * 56 + b"root=PARTLABEL=userdata phone.gui=1")
        with open(os.path.join(images, "phone-image-oneplus-fajita.rootfs.ext4.sparse"), "wb") as f:
            f.write(b"\x3a\xff\x26\xed" + b"kario userdata")
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
        check(all(p + ".img" in names for p in PARTS if p != "userdata") and "userdata.img" not in names,
              "the backup has persist, the IMEI's four, boot and dtbo of both slots, not userdata")
        check(all(open(os.path.join(backup, p + ".img"), "rb").read() == phone.original[p] for p in PARTS if p != "userdata"),
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

        phone.clear_log()
        rc, out = run("flash")
        check(rc == 0, "flash with the backup runs")
        w = writes(phone.log())
        check(w == ["fastboot erase dtbo_a", "fastboot erase dtbo_b",
                    "fastboot flash boot_a " + os.path.join(images, "boot-oneplus-fajita.img"),
                    "fastboot flash boot_b " + os.path.join(images, "boot-oneplus-fajita.img"),
                    "fastboot -S 512M flash userdata " + os.path.join(images, "phone-image-oneplus-fajita.rootfs.ext4.sparse")],
              "flash erases dtbo, writes boot_a, boot_b and userdata, and nothing else")
        check(all(phone.read("parts/" + p) == phone.original[p] for p in ("persist", "modemst1", "modemst2", "fsg", "fsc")),
              "flash leaves persist and the IMEI's partitions alone")

        phone.write("mode", b"fastboot")
        phone.clear_log()
        rc, out = run("restore")
        check(rc == 0, "restore runs")
        check(all(phone.read("parts/" + p) == phone.original[p] for p in ("boot_a", "boot_b", "dtbo_a", "dtbo_b")),
              "restore puts OxygenOS's boot and dtbo back on both slots")
        check(phone.read("parts/userdata") == b"" and "fastboot -w" in phone.log(), "restore wipes userdata")

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

        r = subprocess.run(["sh", SCRIPT, "-n", "-d", images, "flash"], env={**env, "PATH": os.environ["PATH"]},
                           capture_output=True, text=True)
        check(r.returncode == 0 and "fastboot flash boot_a" in r.stdout, "a dry run needs no phone and no backup")
    finally:
        shutil.rmtree(root, ignore_errors=True)
    print("fajita-flash-test: " + ("all passed" if not failed else "%d failed" % failed))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
