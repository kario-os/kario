"""The A/B slot logic (R37): RAUC's backend (rauc-boot) and the boot-time
choice of phone-initramfs (slot-choose), run on one state directory the way
an update, boots and the health mark use it; and what phone-initramfs reads
where the bootloader keeps the slot (slot-abl, 43-phone-partitions.md).
RAUC's backend for that slot, rauc-abl, is kario-bsp's (its
tests/rauc-abl-test.py)."""
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sku  # noqa: E402

FW = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FILES = os.path.join(FW, "yocto", "meta-phone", "recipes-phone", "phone-ota", "files")


class Slots(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()

    def rauc(self, *args):
        out = subprocess.run(["sh", os.path.join(FILES, "rauc-boot"), *args], env={"PHONE_SLOTS": self.dir, "PHONE_SLOT_CHOOSE": os.path.join(FILES, "slot-choose"), "PATH": os.environ["PATH"]},
                             capture_output=True, text=True, check=True)
        return out.stdout.strip()

    def boot(self):
        """A boot: phone-initramfs picks the slot."""
        out = subprocess.run(["sh", "-c", f'. "{FILES}/slot-choose"; choose "$0"', self.dir],
                             capture_output=True, text=True, check=True)
        self.assertEqual(self.rauc("get-current"), out.stdout.strip())
        return out.stdout.strip()

    def install(self, slot):
        """What RAUC does around writing a slot."""
        self.rauc("set-state", slot, "bad")
        self.rauc("set-primary", slot)

    def test_update_then_healthy(self):
        self.assertEqual(self.boot(), "a")
        self.install("b")
        self.assertEqual(self.rauc("get-primary"), "b")
        self.assertEqual(self.rauc("get-state", "b"), "bad")
        self.assertEqual(self.boot(), "b")
        self.rauc("set-state", self.rauc("get-current"), "good")  # rauc status mark-good
        self.assertEqual(self.rauc("get-state", "b"), "good")
        for _ in range(5):
            self.assertEqual(self.boot(), "b")

    def test_update_that_never_comes_up_rolls_back(self):
        self.boot()
        self.install("b")
        self.assertEqual([self.boot() for _ in range(3)], ["b", "b", "b"])  # never marked good
        self.assertEqual(self.boot(), "a")
        self.assertEqual(self.rauc("get-primary"), "a")
        self.assertEqual(self.boot(), "a")

    def test_the_good_slot_marked_bad_is_left(self):
        self.boot()
        self.install("b")
        self.boot()
        self.rauc("set-state", "b", "bad")
        self.assertEqual(self.rauc("get-primary"), "a")
        self.assertEqual(self.boot(), "a")

    def test_a_slot_that_fails_verification_is_refused_at_once(self):
        self.boot()
        self.install("b")
        self.assertEqual(self.boot(), "b")
        out = subprocess.run(["sh", "-c", f'. "{FILES}/slot-choose"; refuse "$0"', self.dir],
                             capture_output=True, text=True, check=True)
        self.assertEqual(out.stdout.strip(), "a")
        self.assertIn("slot b failed verification", out.stderr)
        self.assertEqual(self.rauc("get-current"), "a")
        self.assertEqual(self.rauc("get-primary"), "a")
        self.assertEqual(self.boot(), "a")

    def test_a_broken_state_file_boots_slot_a(self):
        with open(os.path.join(self.dir, "boot"), "w") as f:
            f.write("slot=x\ngood=../etc\ntries=lots\n")
        self.assertEqual(self.boot(), "a")


class SlotAbl(unittest.TestCase):
    """What phone-initramfs reads where the bootloader keeps the slot
    (slot-abl): the board's partitions.conf, and whether a data partition
    holds a file system, which it makes only on a blank one."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir)

    def sh(self, script, *args, path=None):
        env = {"PATH": (path + os.pathsep if path else "") + os.environ["PATH"]}
        return subprocess.run(["sh", "-c", f'. "{FILES}/slot-abl"; log() {{ echo "log: $*"; }}; {script}', "sh", *args],
                              capture_output=True, text=True, env=env)

    def conf(self, text):
        path = os.path.join(self.dir, "partitions.conf")
        with open(path, "w") as f:
            f.write(text)
        return path

    def parts(self, path):
        return self.sh('parts_conf "$1"; echo "$slots|$root_part|$data_part|$state_part"', path).stdout

    def test_parts_conf(self):
        board = sku.load("boards", "oneplus-fajita")
        self.assertEqual(self.parts(self.conf(sku.partitions_conf(board))), "abl|system|userdata|\n")
        self.assertEqual(self.parts(self.conf(sku.partitions_conf(sku.load("boards", "qemuarm64")))), "state|rootfs|data|state\n")
        # No file (a board without [partitions]): the emulator's disk.
        self.assertEqual(self.parts(os.path.join(self.dir, "none")), "state|rootfs|data|state\n")
        self.assertEqual(self.parts(self.conf("")), "state|rootfs|data|\n")
        # Parsed, never run.
        bad = self.conf("slots=abl; reboot\nroot=$(reboot)\ndata=../etc\nstate=a b\n")
        self.assertEqual(self.parts(bad), "state|rootfs|data|\n")

    def image(self, name, at=0, data=b""):
        path = os.path.join(self.dir, name)
        with open(path, "wb") as f:
            f.write(b"\0" * 8192)
            f.seek(at)
            f.write(data)
        return path

    def test_fs_kind(self):
        for name, at, data, kind in (("ext4", 1080, b"\x53\xef", "ext4"), ("f2fs", 1024, b"\x10\x20\xf5\xf2", "f2fs"),
                                     ("blank", 0, b"", "none"), ("other", 1080, b"\x53\xee", "none")):
            r = self.sh('fs_kind "$1"', self.image(name, at, data))
            self.assertEqual(r.stdout, kind + "\n", name)

    def test_data_ready(self):
        bindir = os.path.join(self.dir, "bin")
        os.makedirs(bindir)
        made = os.path.join(self.dir, "made")
        with open(os.path.join(bindir, "mkfs.ext4"), "w") as f:
            f.write(f'#!/bin/sh\necho "$*" >> {made}\n')
        os.chmod(os.path.join(bindir, "mkfs.ext4"), 0o755)
        blank = self.image("blank")
        r = self.sh('data_ready "$1" userdata', blank, path=bindir)
        self.assertEqual(r.returncode, 0)
        self.assertIn("log: userdata: no file system on", r.stdout)
        with open(made) as f:
            self.assertEqual(f.read(), f"-F -q -L userdata {blank}\n")
        os.remove(made)
        r = self.sh('data_ready "$1" userdata', self.image("ext4", 1080, b"\x53\xef"), path=bindir)
        self.assertEqual((r.returncode, os.path.exists(made)), (0, False))
        r = self.sh('data_ready "$1" userdata', self.image("f2fs", 1024, b"\x10\x20\xf5\xf2"), path=bindir)
        self.assertEqual((r.returncode, os.path.exists(made)), (1, False))
        self.assertIn("another file system", r.stdout)


class UserdataLayout(unittest.TestCase):
    """phone-initramfs in the userdata layout (slot-abl): the root file
    system is the board's data partition, found by its name in sysfs and
    mounted read-write, whatever root= the bootloader put on the command
    line (the OnePlus 6T booted nothing with its own root= appended,
    39-oneplus-6t.md §1)."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir)
        self.sys = os.path.join(self.dir, "sys")
        self.dev = os.path.join(self.dir, "dev")
        self.bin = os.path.join(self.dir, "bin")
        self.log = os.path.join(self.dir, "mounts")
        for d in (os.path.join(self.sys, "class", "block"), self.dev, self.bin):
            os.makedirs(d)
        # mount copies <device>.tree, the file system's contents; sleep
        # passes no time.
        self.stub("mount", f'echo "mount $*" >> {self.log}\neval "dev=\\${{$(($#-1))}} dir=\\${{$#}}"\n'
                           '[ -d "$dev.tree" ] || exit 32\ncp -R "$dev.tree/." "$dir/"\n')
        self.stub("umount", f'echo "umount $*" >> {self.log}\n')
        self.stub("sleep", "")

    def stub(self, name, body):
        path = os.path.join(self.bin, name)
        with open(path, "w") as f:
            f.write("#!/bin/sh\n" + body)
        os.chmod(path, 0o755)

    def disk(self, devname, partname=None, fs=None, init=False):
        """A block device the kernel names devname, a GPT partition named
        partname, with an ext4 or f2fs superblock and /sbin/init."""
        d = os.path.join(self.sys, "class", "block", devname)
        os.makedirs(d)
        with open(os.path.join(d, "uevent"), "w") as f:
            f.write(f"MAJOR=8\nMINOR=1\nDEVNAME={devname}\nDEVTYPE={'partition' if partname else 'disk'}\n"
                    + (f"PARTN=1\nPARTNAME={partname}\n" if partname else ""))
        img = bytearray(8192)
        if fs == "ext4":
            img[1080:1082] = b"\x53\xef"
        elif fs == "f2fs":
            img[1024:1028] = b"\x10\x20\xf5\xf2"
        with open(os.path.join(self.dev, devname), "wb") as f:
            f.write(img)
        os.makedirs(os.path.join(self.dev, devname + ".tree", "etc"))
        if init:
            os.makedirs(os.path.join(self.dev, devname + ".tree", "sbin"))
            os.symlink("/lib/systemd/systemd", os.path.join(self.dev, devname + ".tree", "sbin", "init"))

    def sh(self, script, *args):
        env = {"PATH": self.bin + os.pathsep + os.environ["PATH"], "PHONE_SYSFS": self.sys, "PHONE_DEV": self.dev}
        return subprocess.run(["sh", "-c", f'. "{FILES}/slot-abl"; log() {{ echo "log: $*"; }}; data_part=userdata; {script}',
                               "sh", *args], capture_output=True, text=True, env=env)

    def mounts(self):
        if not os.path.exists(self.log):
            return []
        with open(self.log) as f:
            return f.read().splitlines()

    def test_boot_layout(self):
        path = os.path.join(self.dir, "layout")
        for text, want in (("layout=userdata\n", "userdata"), ("layout=ab\n", ""), ("layout=userdata; reboot\n", ""), ("", "")):
            with open(path, "w") as f:
                f.write(text)
            self.assertEqual(self.sh('boot_layout "$1"', path).stdout, want + ("\n" if want else ""), text)
        self.assertEqual(self.sh('boot_layout "$1"', os.path.join(self.dir, "none")).stdout, "")

    def test_part_by_name(self):
        self.disk("sda")
        self.disk("sda16", "userdata2")
        self.disk("sda17", "userdata", "ext4")
        self.disk("sde11", "boot_a")
        self.assertEqual(self.sh("part userdata").stdout, os.path.join(self.dev, "sda17") + "\n")
        self.assertEqual(self.sh("part boot_a").stdout, os.path.join(self.dev, "sde11") + "\n")
        self.assertEqual(self.sh("part system_a").stdout, "")
        self.assertEqual(self.sh('part ""').stdout, "")

    def test_wait_part(self):
        self.disk("sda17", "userdata", "ext4")
        r = self.sh('wait_part userdata 30 && echo "found after $waited"')
        self.assertEqual(r.stdout, "found after 0\n")
        # The partition shows up while it waits (the UFS's units come up
        # one after the other).
        self.stub("sleep", f'n=$(cat {self.dir}/slept 2>/dev/null || echo 0); echo $((n + 1)) > {self.dir}/slept\n'
                           f'[ "$n" != 2 ] || {{ mkdir -p {self.sys}/class/block/sdf1; printf "DEVNAME=sdf1\\nPARTNAME=late\\n" > {self.sys}/class/block/sdf1/uevent; }}\n')
        r = self.sh('wait_part late 30 && echo "found after $waited"')
        self.assertEqual(r.stdout, "found after 3\n")
        r = self.sh('wait_part never 5 || echo "gave up after $waited"')
        self.assertEqual(r.stdout, "gave up after 5\n")

    def test_userdata_root(self):
        root = os.path.join(self.dir, "root")
        os.makedirs(root)
        # A root file system on system_a too, as a bootloader's root= names it.
        self.disk("sda17", "userdata", "ext4", init=True)
        self.disk("sde16", "system_a", "ext4", init=True)
        r = self.sh('userdata_root "$1"', root)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(self.mounts(), [f"mount -t ext4 -o rw {self.dev}/sda17 {root}"])
        self.assertIn(f"log: the root file system: {self.dev}/sda17, the partition userdata (found after 0 s)", r.stdout)

    def test_userdata_root_refuses_what_is_not_kario(self):
        root = os.path.join(self.dir, "root")
        os.makedirs(root)
        # Android's data (f2fs), never made anew.
        self.disk("sda17", "userdata", "f2fs", init=True)
        r = self.sh('userdata_root "$1"', root)
        self.assertEqual((r.returncode, self.mounts()), (1, []))
        self.assertIn("no ext4 on", r.stdout)
        shutil.rmtree(self.sys)
        shutil.rmtree(self.dev)
        os.makedirs(os.path.join(self.sys, "class", "block"))
        os.makedirs(self.dev)
        # An ext4 without /sbin/init: not Kario's root file system.
        self.disk("sda17", "userdata", "ext4")
        r = self.sh('userdata_root "$1"', root)
        self.assertEqual(r.returncode, 1)
        self.assertEqual(self.mounts(), [f"mount -t ext4 -o rw {self.dev}/sda17 {root}", f"umount {root}"])
        self.assertIn("has no /sbin/init", r.stdout)
        # No partition named userdata: it waits 30 s, then names what the kernel has.
        r = self.sh('data_part=data; userdata_root "$1"', root)
        self.assertEqual(r.returncode, 1)
        self.assertIn("log: no partition data after 30 s; block devices: sda17", r.stdout)

    def test_the_command_line_in_the_log(self):
        cmdline = os.path.join(self.dir, "cmdline")
        own = "root=PARTLABEL=userdata console=ttyMSM0,115200 phone.gui=1 console=tty0"
        appended = " ".join(f"androidboot.x{i}=" + "y" * 40 for i in range(30)) + " skip_initramfs rootwait ro init=/init root=/dev/dm-0"
        with open(cmdline, "w") as f:
            f.write(own + " " + appended + " *\n")
        r = self.sh('log_cmdline "$1"', cmdline)
        lines = r.stdout.splitlines()
        self.assertGreater(len(lines), 1)
        self.assertTrue(all(len(("phone-initramfs: " + l[len("log: "):]).encode()) < 1024 for l in lines))
        self.assertTrue(lines[0].startswith("log: command line: root=PARTLABEL=userdata "))
        self.assertTrue(all(l.startswith("log: command line, continued: ") for l in lines[1:]))
        words = " ".join(l.split(": ", 2)[2] for l in lines)
        self.assertEqual(words, own + " " + appended + " *")
        r = self.sh('root_words "$1"', cmdline)
        self.assertEqual(r.stdout, "root=PARTLABEL=userdata skip_initramfs rootwait ro init=/init root=/dev/dm-0 ")


if __name__ == "__main__":
    unittest.main()
