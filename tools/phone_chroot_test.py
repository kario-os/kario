"""Tests for tools/phone-chroot (24-sku-framework.md §6.2) without a phone:
the dry run plans every step, changes nothing and keeps Nura's modem stack;
the scripts are shellcheck-clean."""
import os
import shutil
import subprocess
import tempfile
import unittest

FW = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
KIT = os.path.join(FW, "tools", "phone-chroot")
DRIVER = os.path.join(KIT, "kario-phone.sh")
PHONE = os.path.join(KIT, "on-phone.sh")


def run(*args, **kw):
    return subprocess.run(["sh", *args], capture_output=True, text=True, cwd=FW, **kw)


class KitTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.image = os.path.join(self.tmp, "phone-image-qemuarm64.rootfs.ext4")
        open(self.image, "wb").close()

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def test_the_dry_run_plans_every_step(self):
        out = os.path.join(self.tmp, "out")
        steps = ["all", "shell-start", "mem", "shell-stop", "power-start", "power-stop", "power-sample=30", "teardown"]
        r = run(DRIVER, "--dry-run", "--sku", "oneplus-fajita-t9", "--image", self.image, "--out", out, *steps)
        self.assertEqual(r.returncode, 0, r.stderr)
        for want in (
            "ssh root@172.16.42.1",
            f"cat {self.image} | ssh",
            "mount -t ext4 -o loop /var/lib/kario-chroot/rootfs.ext4 /var/lib/kario-chroot/root",
            "mount --rbind /run /var/lib/kario-chroot/root/run",
            "mount --make-rslave /var/lib/kario-chroot/root/sys",
            "install -m 0644 /var/lib/kario-chroot/kit/sku.json /var/lib/kario-chroot/root/usr/share/phone/sku.json",
            "rc-service tinydm stop",
            "setfattr -n trusted.delegate -v 1 /sys/fs/cgroup/kario",
            "phone-shell --cts",
            "phone-shell --selftest --gui auto --device bar-qwerty",
            "elogind-inhibit --what=handle-power-key",
            "phone-shell --services spawn --gui auto",
            "current_now and voltage_now every second for 30 s",
            "nft delete table inet phone",
            "umount every mount under /var/lib/kario-chroot/root",
        ):
            self.assertIn(want, r.stdout)
        # Our telephonyd is a client of Nura's ModemManager: its modem stack stays.
        for keep in ("ModemManager", "modemmanager", "rmtfs", "pd-mapper", "NetworkManager", "sshd"):
            self.assertNotIn(f"stop {keep}", r.stdout.replace("rc-service " + keep + " stop", "stop " + keep))
            self.assertNotIn(f"pkill -x {keep}", r.stdout)
        self.assertFalse(os.path.exists(out), "the dry run wrote its output directory")

    def push(self, *extra, backup=True):
        """push to a phone stood in for by a fake ssh; the commands it got."""
        home = os.path.join(self.tmp, "home")
        bindir = os.path.join(self.tmp, "bin")
        os.makedirs(bindir, exist_ok=True)
        log = os.path.join(self.tmp, "ssh.log")
        with open(os.path.join(bindir, "ssh"), "w") as f:
            f.write('#!/bin/sh\nfor a; do cmd=$a; done\nprintf "%s\\n" "$cmd" >> "$SSH_LOG"\n'
                    'case $cmd in\n*/proc/cmdline*) echo a1b2c3d4 ;;\n*"/proc/mounts"*) exit 1 ;;\n'
                    '*"df -k"*) echo 9999999 ;;\n*"tar -xf -"* | *"cat >"*) cat > /dev/null ;;\nesac\n')
        os.chmod(os.path.join(bindir, "ssh"), 0o755)
        if backup:
            d = os.path.join(home, "kario-phone-backup", "oneplus-fajita-a1b2c3d4")
            os.makedirs(d, exist_ok=True)
            with open(os.path.join(d, "persist.img"), "wb") as f:
                f.write(b"calibration")
            subprocess.run(["sh", "-c", "sha256sum persist.img > SHA256SUMS"], cwd=d, check=True)
        env = {**os.environ, "HOME": home, "PATH": bindir + os.pathsep + os.environ["PATH"], "SSH_LOG": log}
        r = run(DRIVER, "--sku", "oneplus-fajita-t9", "--image", self.image, "--out", os.path.join(self.tmp, "out"),
                *extra, "push", env=env)
        cmds = open(log).read() if os.path.exists(log) else ""
        return r, cmds

    def test_no_push_without_a_backup(self):
        r, cmds = self.push(backup=False)
        self.assertEqual(r.returncode, 1)
        self.assertIn("no backup of phone a1b2c3d4", r.stderr)
        self.assertNotIn("tar -xf -", cmds)
        self.assertNotIn("rootfs.ext4.part", cmds)

    def test_push_with_a_backup(self):
        r, cmds = self.push()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("backup of phone a1b2c3d4", r.stdout)
        self.assertIn("rootfs.ext4.part", cmds)

    def test_push_forced_without_a_backup(self):
        r, cmds = self.push("--force", backup=False)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("rootfs.ext4.part", cmds)

    def test_only_whole_phones(self):
        r = run(DRIVER, "--dry-run", "--sku", "qemu-t9", "--image", self.image, "push")
        self.assertEqual(r.returncode, 1)
        self.assertIn("not a whole phone's SKU", r.stderr)
        self.assertEqual(run(DRIVER, "--dry-run", "push").returncode, 2)

    # Each step of the phone's script plans without a phone, from the
    # build machine.
    def test_the_phone_side_plans(self):
        env = os.path.join(self.tmp, "kit")
        subprocess.run(["python3", os.path.join(FW, "tools", "sku.py"), "files", "xiaomi-mido-t9", env], check=True, capture_output=True)
        for step in ("check", "facts", "setup", "selftest", "pixel", "bench", "shell-start", "shell-stop", "mem",
                     "power-start", "power-stop", "power-sample", "teardown", "bg selftest", "status selftest"):
            r = run(PHONE, "--plan", "--env", os.path.join(env, "phone.env"), *step.split(), env={**os.environ, "KARIO_BASE": "/var/lib/kario-chroot"})
            self.assertEqual(r.returncode, 0, f"{step}: {r.stdout}{r.stderr}")
        self.assertEqual(run(PHONE, "--plan", "nonsense").returncode, 2)

    def test_shellcheck(self):
        if not shutil.which("shellcheck"):
            self.skipTest("no shellcheck")
        r = subprocess.run(["shellcheck", "-s", "sh", DRIVER, PHONE], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout)


if __name__ == "__main__":
    unittest.main()
