"""Tests for tools/phone-chroot (24-sku-framework.md §6.2, 39-oneplus-6t.md
§13) without a phone: the dry run plans every step of both paths (the
chroot on Nura, the whole phone flashed with Kario), changes nothing and
keeps Nura's modem stack; the image the kit takes is told by what it holds
(an ext4, its zstd, the Android sparse form the flash writes), and the
board's own is preferred; the sound card set up as the board says, by the
image's alsaucm where it has one; a detached step's status, facts without
the phone's identities, kit.txt with the tested commit; the scripts are
shellcheck-clean."""
import os
import re
import shlex
import shutil
import struct
import subprocess
import tarfile
import tempfile
import time
import unittest
import zlib

FW = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
KIT = os.path.join(FW, "tools", "phone-chroot")
DRIVER = os.path.join(KIT, "kario-phone.sh")
PHONE = os.path.join(KIT, "on-phone.sh")
UNSPARSE = os.path.join(KIT, "unsparse.py")
IMG2SIMG = os.path.join(FW, "yocto", "meta-phone", "files", "phone-img2simg.py")


def run(*args, **kw):
    return subprocess.run(["sh", *args], capture_output=True, text=True, cwd=FW, **kw)


def read(path):
    with open(path) as f:
        return f.read()


def blob(path):
    with open(path, "rb") as f:
        return f.read()


def ext4(path, blocks=4):
    """A file that starts as an ext4 does: its superblock's magic at 1080,
    then blocks the sparse form keeps raw, fills and zeros."""
    data = bytearray(blocks * 4096)
    data[1080:1082] = b"\x53\xef"
    data[4096:8192] = os.urandom(4096)
    if blocks > 2:
        data[8192:12288] = b"\xde\xad\xbe\xef" * 1024
    with open(path, "wb") as f:
        f.write(data)
    return bytes(data)


def sparse(raw_path, path):
    subprocess.run(["python3", IMG2SIMG, raw_path, path], check=True)


class KitTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.image = os.path.join(self.tmp, "phone-image-qemuarm64.rootfs.ext4")
        ext4(self.image)

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def test_the_dry_run_plans_every_step(self):
        out = os.path.join(self.tmp, "out")
        steps = ["all", "shell-start", "mem", "shell-stop", "power-start", "power-stop", "power-sample=30", "teardown"]
        r = run(DRIVER, "--dry-run", "--sku", "oneplus-fajita-t9", "--image", self.image, "--out", out,
                "--stay-on", "--no-haptics", *steps)
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
            "phone-shell --services spawn --gui auto --stay-on --no-haptics",
            "current_now and voltage_now every second for 30 s",
            "nft delete table inet phone",
            "umount every mount under /var/lib/kario-chroot/root",
            "kit.txt: this tree's commit and changes",
            "> /var/lib/kario-chroot/results/<run>/kit.txt",
            "(start Nura's: rc-service modemmanager start)",
            "the serial number, the modem's identities and hardware addresses removed",
            # The QEMU image runs, and what it lacks of the board's is said.
            "qemuarm64's image, the same binaries without the board's own packages",
        ):
            self.assertIn(want, r.stdout)
        # The selftest and pixel runs go on after a failed case.
        self.assertRegex(r.stdout, r"phone-shell --cts \S+ --junit \S+ --timeout-scale 2 --keep-going")
        self.assertEqual(len(re.findall(r"--pixel-dir \S+ --junit \S+ --timeout-scale 2 --keep-going", r.stdout)), 4)
        # setup sets the phone's clock from this machine's.
        m = re.search(r"KARIO_HOST_TIME=(\d+) sh /var/lib/kario-chroot/kit/on-phone.sh setup", r.stdout)
        self.assertTrue(m, r.stdout)
        self.assertLess(abs(int(m.group(1)) - time.time()), 60)
        self.assertIn(f"date -u -s @{m.group(1)}", r.stdout)
        # Our telephonyd is a client of Nura's ModemManager: its modem stack stays.
        for keep in ("ModemManager", "modemmanager", "rmtfs", "pd-mapper", "NetworkManager", "sshd"):
            self.assertNotIn(f"stop {keep}", r.stdout.replace("rc-service " + keep + " stop", "stop " + keep))
            self.assertNotIn(f"pkill -x {keep}", r.stdout)
        # The chroot leaves the whole phone's units and firmware alone.
        for whole in ("KARIO_TARGET=whole", "systemctl stop phone-display", "firmware_class"):
            self.assertNotIn(whole, r.stdout)
        self.assertFalse(os.path.exists(out), "the dry run wrote its output directory")

    # The whole phone, flashed with Kario: the same steps and results, over
    # its bring-up network; nothing of the chroot's (no image, no mounts,
    # no Nura services), the Shell on the panel and the audio service
    # stopped for a step and started again, the step in a delegated scope.
    def test_the_whole_phone_plans_the_same_steps(self):
        out = os.path.join(self.tmp, "out")
        r = run(DRIVER, "--dry-run", "--whole", "--sku", "oneplus-fajita-t9", "--out", out, "all", "mem", "teardown",
                env={**os.environ, "KARIO_INIT": "systemd"})
        self.assertEqual(r.returncode, 0, r.stderr)
        for want in (
            "ssh root@172.16.42.1 ' env KARIO_TARGET=whole KARIO_TIMEOUT_SCALE=2",
            "mkdir -p /var/lib/kario-test/kit && tar -xf - -C /var/lib/kario-test/kit",
            "sh /var/lib/kario-test/kit/on-phone.sh setup",
            "Kario with the SKU oneplus-fajita-t9",
            "phone# systemctl stop phone-display.service\n    phone# >> /run/kario-test/stopped\n"
            "    phone# (up to 10 s until nothing is left in phone-display.service's group; then systemctl kill --kill-whom=all "
            "--signal=SIGKILL phone-display.service, and up to 10 s more)\n",
            "phone# systemctl stop phone-audiod.socket\n",
            "phone# systemctl stop phone-audiod.service\n",
            "phone# systemctl start <each unit stopped for the step>",
            "systemd-run --scope --quiet --collect --no-ask-password -p Delegate=yes sh /var/lib/kario-test/kit/on-phone.sh --job selftest",
            "phone# PHONE_PUSH_TLCP=1 /usr/bin/phone-shell --cts /var/lib/kario-test/results/<run>/cts.json "
            "--junit /var/lib/kario-test/results/<run>/selftest-junit.xml --timeout-scale 2 --keep-going",
            "/usr/bin/phone-shell --selftest --gui auto --device communicator --pixel-dir /var/lib/kario-test/results/<run>/pixel/communicator",
            "> /var/lib/kario-test/results/<run>/kit.txt",
            "> /var/lib/kario-test/results/<run>/facts.txt",
            "tar -C /var/lib/kario-test/results -cf - .' | tar -xf - -C " + out + "/results",
            "kill -TERM <the kit's steps and their processes>",
        ):
            self.assertIn(want, r.stdout)
        for chroot in ("rootfs.ext4", "mount ", "chroot ", "rc-service", "pkill", "alsaucm", "nft delete", "umount", "chvt"):
            self.assertNotIn(chroot, r.stdout)

    # What only the chroot has is refused with --whole, and the phone's side
    # refuses the Shell's own start: the whole phone's is a unit.
    def test_the_whole_phone_refuses_the_chroots_options(self):
        for extra in (["--image", self.image], ["--machine", "qemuarm64"], ["--firmware", "image"]):
            r = run(DRIVER, "--dry-run", "--whole", "--sku", "oneplus-fajita-t9", *extra, "push")
            self.assertEqual(r.returncode, 1, extra)
            self.assertRegex(r.stderr, "the chroot's", extra)
        env = self.kit_env("oneplus-fajita-t9")
        for step in ("shell-start", "shell-stop"):
            r = run(PHONE, "--plan", "--env", env, step, env={**os.environ, "KARIO_TARGET": "whole"})
            self.assertEqual(r.returncode, 1)
            self.assertIn("phone-display.service, on the panel already", r.stderr)
        self.assertEqual(run(PHONE, "--plan", "status", "x", env={**os.environ, "KARIO_TARGET": "other"}).returncode, 2)

    def fake_ssh(self, cases):
        """A phone stood in for by an ssh that logs each command and answers
        by the sh case patterns given; the environment that uses it."""
        bindir = os.path.join(self.tmp, "bin")
        os.makedirs(bindir, exist_ok=True)
        log = os.path.join(self.tmp, "ssh.log")
        with open(os.path.join(bindir, "ssh"), "w") as f:
            f.write('#!/bin/sh\nfor a; do cmd=$a; done\nprintf "%s\\n" "$cmd" >> "$SSH_LOG"\ncase $cmd in\n' + cases + "esac\n")
        os.chmod(os.path.join(bindir, "ssh"), 0o755)
        home = os.path.join(self.tmp, "home")
        os.makedirs(home, exist_ok=True)
        return {**os.environ, "HOME": home, "PATH": bindir + os.pathsep + os.environ["PATH"], "SSH_LOG": log,
                "SSH_TAR": os.path.join(self.tmp, "kit.tar"), "SSH_IMG": os.path.join(self.tmp, "pushed.img")}, log

    PHONE_CASES = ('*/proc/cmdline*) echo a1b2c3d4 ;;\n*"/proc/mounts"*) exit 1 ;;\n'
                   '*"df -k"*) echo 9999999 ;;\n*"tar -xf -"*) cat > "$SSH_TAR" ;;\n*"cat >"*) cat > "$SSH_IMG" ;;\n')

    def push(self, *extra, backup=True, image=None):
        """push to a fake phone; the result, the commands it got."""
        env, log = self.fake_ssh(self.PHONE_CASES)
        if backup:
            d = os.path.join(env["HOME"], "kario-phone-backup", "oneplus-fajita-a1b2c3d4")
            os.makedirs(d, exist_ok=True)
            with open(os.path.join(d, "persist.img"), "wb") as f:
                f.write(b"calibration")
            subprocess.run(["sh", "-c", "sha256sum persist.img > SHA256SUMS"], cwd=d, check=True)
        if "--whole" not in extra:
            extra = ("--image", image or self.image, *extra)
        r = run(DRIVER, "--sku", "oneplus-fajita-t9", "--out", os.path.join(self.tmp, "out"), *extra, "push", env=env)
        cmds = read(log) if os.path.exists(log) else ""
        return r, cmds

    def kit_txt(self):
        with tarfile.open(os.path.join(self.tmp, "kit.tar")) as t:
            return t.extractfile("./kit.txt").read().decode()

    def test_no_push_without_a_backup(self):
        r, cmds = self.push(backup=False)
        self.assertEqual(r.returncode, 1)
        self.assertIn("no backup of phone (serial ending d4)", r.stderr)
        self.assertNotIn("tar -xf -", cmds)
        self.assertNotIn("rootfs.ext4.part", cmds)

    # The kit names what is tested: this tree's commit, the SKU and the
    # image; what the driver prints carries no serial number. The image is
    # kept on the phone only once all of it came through.
    def test_push_with_a_backup(self):
        r, cmds = self.push()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("backup of phone (serial ending d4)", r.stdout)
        self.assertNotIn("a1b2c3d4", r.stdout + r.stderr)
        kit = self.kit_txt()
        head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=FW, capture_output=True, text=True).stdout.strip()
        self.assertRegex(kit, rf"^kit: {head}, (no changes|with changes:)\n")
        self.assertIn("sku: oneplus-fajita-t9\nimage: phone-image-qemuarm64.rootfs.ext4, 16384 bytes\n", kit)
        lines = cmds.splitlines()
        part = next(i for i, l in enumerate(lines) if "cat > /var/lib/kario-chroot/rootfs.ext4.part" in l)
        self.assertIn("mv /var/lib/kario-chroot/rootfs.ext4.part /var/lib/kario-chroot/rootfs.ext4", lines[part + 1])
        self.assertEqual(blob(os.path.join(self.tmp, "pushed.img")), blob(self.image))

    def test_push_forced_without_a_backup(self):
        r, cmds = self.push("--force", backup=False)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("rootfs.ext4.part", cmds)

    # The whole phone gets the kit and no image: it runs what it was flashed with.
    def test_push_to_the_whole_phone(self):
        r, cmds = self.push("--whole")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("mkdir -p /var/lib/kario-test/kit", cmds)
        self.assertNotIn("rootfs", cmds)
        self.assertIn("image: the phone's own: what it was flashed with", self.kit_txt())
        with tarfile.open(os.path.join(self.tmp, "kit.tar")) as t:
            self.assertEqual(t.extractfile("./perf.play").read(), blob(os.path.join(FW, "tools", "perf", "gate.play")))

    # The userdata image the flash writes (Android sparse) is the chroot's
    # image too: unpacked on the way, the phone gets the ext4 byte for byte.
    def test_push_the_flashs_sparse_image(self):
        raw = os.path.join(self.tmp, "raw.ext4")
        want = ext4(raw)
        img = os.path.join(self.tmp, "phone-image-oneplus-fajita.rootfs.ext4.sparse")
        sparse(raw, img)
        r = run(DRIVER, "--dry-run", "--sku", "oneplus-fajita-t9", "--image", img, "push")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn(f"python3 {UNSPARSE} {img} | ssh", r.stdout)
        self.assertIn("the board's own image (oneplus-fajita), as the phone runs it flashed", r.stdout)
        r, cmds = self.push(image=img)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(blob(os.path.join(self.tmp, "pushed.img")), want)
        self.assertIn("mv /var/lib/kario-chroot/rootfs.ext4.part", cmds)

    @unittest.skipUnless(shutil.which("zstd"), "no zstd")
    def test_push_a_zstd_image(self):
        img = self.image + ".zst"
        subprocess.run(["zstd", "-q", self.image, "-o", img], check=True)
        r = run(DRIVER, "--dry-run", "--sku", "oneplus-fajita-t9", "--image", img, "push")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn(f"zstd -dc {img} | ssh", r.stdout)

    # What is not an ext4 in one of the three forms is refused by what it
    # holds, before anything reaches the phone; a sparse image that ends
    # early leaves nothing on the phone.
    def test_images_the_kit_does_not_take(self):
        for name, data, why in (("phone-image-qemuarm64.rootfs.ext4", b"\0" * 8192, "no ext4 superblock"),
                                ("phone-image-qemuarm64.rootfs.wic", b"\0" * 8192, "a .wic is a whole disk"),
                                ("phone-image-qemuarm64.rootfs.tar.zst", b"\x28\xb5\x2f\xfd" + b"\0" * 64, "it does not unpack a tar")):
            img = os.path.join(self.tmp, "bad", name)
            os.makedirs(os.path.dirname(img), exist_ok=True)
            with open(img, "wb") as f:
                f.write(data)
            r, cmds = self.push(image=img)
            self.assertEqual(r.returncode, 1, name)
            self.assertIn("not a root file system the kit takes", r.stderr)
            self.assertIn(why, r.stderr)
            self.assertIn(".ext4.sparse, what the flash writes", r.stderr)
            self.assertNotIn("rootfs.ext4.part", cmds)
        bad = os.path.join(self.tmp, "phone-image-qemuarm64.rootfs.ext4.sparse")
        with open(bad, "wb") as f:
            f.write(b"\x3a\xff\x26\xed" + b"\0" * 64)
        r, cmds = self.push(image=bad)
        self.assertEqual(r.returncode, 1)
        self.assertIn("not an Android sparse image the kit reads", r.stderr)
        # Cut short past its first MiB: unpacking fails half-way through the copy.
        raw = os.path.join(self.tmp, "raw.ext4")
        ext4(raw, blocks=1)
        with open(raw, "ab") as f:
            f.write(os.urandom(300 * 4096))
        short = os.path.join(self.tmp, "phone-image-oneplus-fajita.rootfs.ext4.sparse")
        sparse(raw, short)
        with open(short, "r+b") as f:
            f.truncate(os.path.getsize(short) - 100)
        r, cmds = self.push(image=short)
        self.assertEqual(r.returncode, 1)
        self.assertIn("unpacking phone-image-oneplus-fajita.rootfs.ext4.sparse failed", r.stderr)
        self.assertIn("rm -f /var/lib/kario-chroot/rootfs.ext4.part", cmds)
        self.assertNotIn("mv /var/lib/kario-chroot/rootfs.ext4.part", cmds)

    # Without --image the board's own machine's build is taken (phone.env's
    # KARIO_MACHINE), its sparse form too; another board's image is refused.
    def test_the_board_s_own_image_first(self):
        work = os.path.join(self.tmp, "work")
        deploy = os.path.join(work, "build", "tmp", "deploy", "images")
        os.makedirs(os.path.join(deploy, "oneplus-fajita"))
        raw = os.path.join(self.tmp, "raw.ext4")
        ext4(raw)
        img = os.path.join(deploy, "oneplus-fajita", "phone-image-oneplus-fajita.rootfs.ext4.sparse")
        sparse(raw, img)
        env = {**os.environ, "WORK": work}
        r = run(DRIVER, "--dry-run", "--sku", "oneplus-fajita-t9", "push", env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn(f"python3 {UNSPARSE} {img} | ssh", r.stdout)
        r = run(DRIVER, "--dry-run", "--sku", "oneplus-fajita-t9", "--machine", "qemuarm64", "push", env=env)
        self.assertIn("<no image yet", r.stdout)
        other = os.path.join(self.tmp, "phone-image-pine64-pinephone.rootfs.ext4")
        ext4(other)
        r = run(DRIVER, "--dry-run", "--sku", "oneplus-fajita-t9", "--image", other, "push")
        self.assertEqual(r.returncode, 1)
        self.assertIn("pine64-pinephone's image: neither the board's (oneplus-fajita) nor a generic one (kario-qcom qemuarm64)", r.stderr)
        r = run(DRIVER, "--dry-run", "--force", "--sku", "oneplus-fajita-t9", "--image", other, "push")
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_only_whole_phones(self):
        r = run(DRIVER, "--dry-run", "--sku", "qemu-t9", "--image", self.image, "push")
        self.assertEqual(r.returncode, 1)
        self.assertIn("not a whole phone's SKU", r.stderr)
        self.assertEqual(run(DRIVER, "--dry-run", "push").returncode, 2)

    # --only reaches phone-shell on the phone as one word, whatever it holds.
    def test_only_some_cases(self):
        only = "^(a|b) c's"
        r = run(DRIVER, "--dry-run", "--sku", "oneplus-fajita-t9", "--image", self.image, "--only", only, "selftest", "pixel")
        self.assertEqual(r.returncode, 0, r.stderr)
        line = next(l for l in r.stdout.splitlines() if l.startswith("+ ssh") and "on-phone.sh bg selftest" in l)
        self.assertIn("KARIO_ONLY=" + only, shlex.split(line[len("+ ssh root@172.16.42.1 '"):-1]))
        self.assertIn(f"--keep-going --only {only} >", r.stdout)
        self.assertEqual(r.stdout.count(f"--only {only}"), 1 + 4)

    def kit_env(self, sku="xiaomi-mido-t9"):
        env = os.path.join(self.tmp, "kit-" + sku)
        subprocess.run(["python3", os.path.join(FW, "tools", "sku.py"), "files", sku, env], check=True, capture_output=True)
        return os.path.join(env, "phone.env")

    # Each step of the phone's script plans without a phone, from the
    # build machine, on both paths.
    def test_the_phone_side_plans(self):
        env = self.kit_env()
        for target in ("chroot", "whole"):
            for step in ("check", "facts", "setup", "selftest", "pixel", "bench", "shell-start", "shell-start --stay-on", "shell-stop",
                         "mem", "power-start", "power-stop", "power-sample", "teardown", "bg selftest", "status selftest", "log selftest"):
                if target == "whole" and step.startswith("shell-"):
                    continue
                r = run(PHONE, "--plan", "--env", env, *step.split(), env={**os.environ, "KARIO_TARGET": target})
                self.assertEqual(r.returncode, 0, f"{target} {step}: {r.stdout}{r.stderr}")
        self.assertEqual(run(PHONE, "--plan", "nonsense").returncode, 2)
        r = run(PHONE, "--plan", "--env", env, "shell-start", "--bogus")
        self.assertEqual(r.returncode, 1)
        self.assertIn("the switches are --stay-on and --no-haptics", r.stderr)

    # The hints and the detaching follow the base: systemd's commands on
    # Nura v26.06; without alsaucm anywhere, what that costs and how to
    # install it.
    def test_the_plan_follows_the_base(self):
        env = self.kit_env("oneplus-fajita-t9")
        base = {**os.environ, "KARIO_BASE": "/var/lib/kario-chroot"}
        r = run(PHONE, "--plan", "--env", env, "setup", env={**base, "KARIO_INIT": "systemd", "KARIO_WITHOUT": "image:alsaucm alsaucm"})
        self.assertEqual(r.returncode, 0, r.stderr)
        for want in ("systemctl stop tinydm", "(start Nura's: systemctl start ModemManager)",
                     "no alsaucm in the image or on Nura: the sound card gets no UCM verb HiFi and device Speaker on hw:0, so "
                     "phone-audiod cannot play (plughw:0,2: aplay: Invalid argument); use the board's own image, or install Nura's "
                     "(apk add alsa-utils)"):
            self.assertIn(want, r.stdout)
        self.assertNotIn("set _verb", r.stdout)
        r = run(PHONE, "--plan", "--env", env, "teardown", env={**base, "KARIO_WITHOUT": "image:alsaucm alsaucm"})
        self.assertNotIn("alsaucm", r.stdout)
        for step in ("bg selftest", "shell-start"):
            r = run(PHONE, "--plan", "--env", env, *step.split(), env={**base, "KARIO_INIT": "systemd"})
            self.assertIn("setsid systemd-run --scope --quiet --collect --no-ask-password ", r.stdout)
            self.assertNotIn("Delegate=yes", r.stdout)
            r = run(PHONE, "--plan", "--env", env, *step.split(), env={**base, "KARIO_INIT": "openrc"})
            self.assertNotIn("systemd-run", r.stdout)
        # A flashed image without setsid still detaches the step, in its scope.
        r = run(PHONE, "--plan", "--env", env, "bg", "selftest",
                env={**os.environ, "KARIO_TARGET": "whole", "KARIO_INIT": "systemd", "KARIO_WITHOUT": "setsid"})
        self.assertIn("phone# systemd-run --scope --quiet --collect --no-ask-password -p Delegate=yes sh /var/lib/kario-test/kit/on-phone.sh "
                      "--job selftest > /var/lib/kario-test/results/<run>/selftest.out &\n", r.stdout)

    # The sound card as the board's [audio] says (the kit's phone.env, from
    # the same table as its sku.json): an image with alsaucm (the board's
    # own, as the whole phone) leaves it to the Shell's own audiod, which
    # has the same values from sku.json, and teardown takes down what it
    # set with the image's alsaucm; an image without (the QEMU one) gets the
    # 6T's HiFi verb and Speaker device from Nura's alsaucm, which
    # phone-shell's audiod plays to (plughw:0,2). A board without [audio]
    # gets no set-up at all (it used to get card 0's HiFi verb, a guess of
    # the kit's, not the board's).
    def test_the_sound_card_follows_the_board(self):
        base = {**os.environ, "KARIO_BASE": "/var/lib/kario-chroot", "KARIO_INIT": "openrc"}
        env = self.kit_env("oneplus-fajita-t9")
        r = run(PHONE, "--plan", "--env", env, "setup", env=base)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("kario: the image's alsaucm: phone-audiod sets the UCM verb HiFi and device Speaker on hw:0 up itself, "
                      "as on the whole phone\nphone# > /run/kario-chroot/ucm\n", r.stdout)
        self.assertNotIn("phone# alsaucm", r.stdout)
        r = run(PHONE, "--plan", "--env", env, "teardown", env=base)
        self.assertIn("phone# chroot /var/lib/kario-chroot/root /usr/bin/alsaucm -c hw:0 set _verb HiFi set _enadev Speaker "
                      "set _disdev Speaker set _verb Inactive\n", r.stdout)
        nura = {**base, "KARIO_WITHOUT": "image:alsaucm"}
        r = run(PHONE, "--plan", "--env", env, "setup", env=nura)
        self.assertIn("phone# alsaucm -c hw:0 set _verb HiFi set _enadev Speaker\nphone# > /run/kario-chroot/ucm\n", r.stdout)
        r = run(PHONE, "--plan", "--env", env, "teardown", env=nura)
        self.assertIn("phone# alsaucm -c hw:0 set _verb HiFi set _enadev Speaker set _disdev Speaker set _verb Inactive\n", r.stdout)
        mido = self.kit_env("xiaomi-mido-t9")
        for step in ("setup", "teardown"):
            r = run(PHONE, "--plan", "--env", mido, step, env=nura)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertNotIn("alsaucm", r.stdout)
        self.assertIn("the board names no UCM set-up ([audio])", run(PHONE, "--plan", "--env", mido, "setup", env=nura).stdout)
        # A dry run of teardown on this machine: what setup noted, with a
        # fake alsaucm (a note without its tool is Nura's, as before).
        bindir, state = os.path.join(self.tmp, "bin"), os.path.join(self.tmp, "state")
        os.makedirs(bindir)
        os.makedirs(state)
        with open(os.path.join(bindir, "alsaucm"), "w") as f:
            f.write("#!/bin/sh\nexit 0\n")
        os.chmod(os.path.join(bindir, "alsaucm"), 0o755)
        dry = {**base, "KARIO_BASE": os.path.join(self.tmp, "base"), "KARIO_STATE": state,
               "PATH": bindir + os.pathsep + os.environ["PATH"]}
        r = run(PHONE, "--dry-run", "--env", env, "teardown", env=dry)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertNotIn("alsaucm", r.stdout)
        with open(os.path.join(state, "ucm"), "w") as f:
            f.write("hw:1\nHiFi\nSpeaker\n")
        r = run(PHONE, "--dry-run", "--env", env, "teardown", env=dry)
        self.assertIn("phone# alsaucm -c hw:1 set _verb HiFi set _enadev Speaker set _disdev Speaker set _verb Inactive\n", r.stdout)
        with open(os.path.join(state, "ucm"), "w") as f:
            f.write("hw:0\nHiFi\nSpeaker\nimage\n")
        r = run(PHONE, "--dry-run", "--env", env, "teardown", env=dry)
        self.assertIn(f"phone# chroot {self.tmp}/base/root /usr/bin/alsaucm -c hw:0 set _verb HiFi", r.stdout)

    # --firmware image: Nura's kernel looks in the image's firmware first
    # from setup on, and teardown puts the path it found back; without it
    # the kernel's path is left alone.
    def test_the_image_s_firmware(self):
        env = self.kit_env("oneplus-fajita-t9")
        base = {**os.environ, "KARIO_INIT": "openrc"}
        r = run(PHONE, "--plan", "--env", env, "setup", env={**base, "KARIO_FIRMWARE": "image"})
        self.assertIn("phone# printf %s /var/lib/kario-chroot/root/lib/firmware > /sys/module/firmware_class/parameters/path", r.stdout)
        r = run(PHONE, "--plan", "--env", env, "teardown", env={**base, "KARIO_FIRMWARE": "image"})
        self.assertIn("> /sys/module/firmware_class/parameters/path", r.stdout)
        r = run(PHONE, "--plan", "--env", env, "setup", env=base)
        self.assertNotIn("firmware_class", r.stdout)
        r = run(DRIVER, "--dry-run", "--sku", "oneplus-fajita-t9", "--image", self.image, "--firmware", "image", "setup")
        self.assertIn("KARIO_FIRMWARE=image sh /var/lib/kario-chroot/kit/on-phone.sh setup", r.stdout)
        self.assertEqual(run(DRIVER, "--dry-run", "--sku", "oneplus-fajita-t9", "--firmware", "nuras", "setup").returncode, 2)

    # facts.txt leaves the phone without its serial number (the kernel's
    # command line, its log), the modem's identities and hardware addresses.
    def test_scrub(self):
        cmdline = os.path.join(self.tmp, "cmdline")
        with open(cmdline, "w") as f:
            f.write("console=ttyMSM0,115200 androidboot.serialno=a1b2c3d4 plymouth.ignore-serial-consoles\n")
        facts = "\n".join([
            "console=ttyMSM0,115200 androidboot.serialno=a1b2c3d4 plymouth.ignore-serial-consoles androidboot.vbmeta.digest=55021d098a52f69e",
            "[    0.000000] Kernel command line: msm_drm.dsi_display0=dsi_samsung serialno=a1b2c3d4",
            "gadget: the serial a1b2c3d4",
            "  Hardware |   equipment id: 867530912345678",
            "  3GPP     |           imei: 867530912345678",
            "  Numbers  |            own: +8613800138000",
            "SIM imsi: 460001234567890 iccid: 89860012345678901234",
            "MemTotal:        5764704 kB",
            "[    1.234567] msm_serial: driver initialized; shown: 3",
            "    link/ether 3e:a1:22:9c:04:7b brd ff:ff:ff:ff:ff:ff",
            "Local time: 12:34:56, inet6 fe80::3ca1:22ff:fe9c:47b/64",
        ]) + "\n"
        r = run(PHONE, "scrub", input=facts, env={**os.environ, "KARIO_CMDLINE": cmdline})
        self.assertEqual(r.returncode, 0, r.stderr)
        for gone in ("a1b2c3d4", "867530912345678", "8613800138000", "460001234567890", "89860012345678901234", "3e:a1:22:9c:04:7b"):
            self.assertNotIn(gone, r.stdout)
        for kept in ("androidboot.serialno=<removed>", "plymouth.ignore-serial-consoles", "55021d098a52f69e", "dsi_display0=dsi_samsung",
                     "gadget: the serial <serial>", "equipment id: <removed>", "MemTotal:        5764704 kB",
                     "msm_serial: driver initialized; shown: 3", "link/ether <mac> brd <mac>", "12:34:56", "fe80::3ca1:22ff:fe9c:47b/64"):
            self.assertIn(kept, r.stdout)

    # The phone's partition map with sizes and the board's roles: what the
    # A/B layout needs checked on the phone (43-phone-partitions.md).
    def test_partitions(self):
        sys_dir = os.path.join(self.tmp, "sys")
        for dev, name, sectors in (("sda2", "persist", 65536), ("sda20", "system_a", 6291456), ("sde11", "boot_a", 131072),
                                   ("sde", None, 0), ("sdf2", "modemst1", 4096), ("sdf9", "fw_4j1ed_a", 2048)):
            d = os.path.join(sys_dir, "class", "block", dev)
            os.makedirs(d)
            with open(os.path.join(d, "uevent"), "w") as f:
                f.write(f"MAJOR=8\nDEVNAME={dev}\n" + (f"PARTNAME={name}\n" if name else "DEVTYPE=disk\n"))
            with open(os.path.join(d, "size"), "w") as f:
                f.write(f"{sectors}\n")
        r = run(PHONE, "--env", self.kit_env("oneplus-fajita-t9"), "partitions", env={**os.environ, "KARIO_SYS": sys_dir})
        self.assertEqual(r.returncode, 0, r.stderr)
        lines = r.stdout.splitlines()
        for want in ("persist sda2 33554432 efs", "system_a sda20 3221225472 root", "boot_a sde11 67108864 boot",
                     "modemst1 sdf2 2097152 efs", "fw_4j1ed_a sdf9 1048576", "system_b: in the board file, not on this phone"):
            self.assertIn(want, lines)
        self.assertNotIn("sde", [l.split()[1] for l in lines if " " in l and not l.endswith("phone")])

    def phone_dirs(self):
        base, state = os.path.join(self.tmp, "base"), os.path.join(self.tmp, "state")
        return base, state, {**os.environ, "KARIO_BASE": base, "KARIO_STATE": state, "KARIO_INIT": "openrc"}

    def status(self, env):
        r = run(PHONE, "status", "selftest", env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        return r.stdout

    # A detached step notes its own pid, and leaves its exit code even when
    # it dies (here: no chroot set up), where status used to say "gone".
    def test_a_job_leaves_its_exit_code(self):
        base, state, env = self.phone_dirs()
        r = run(PHONE, "bg", "selftest", env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        st = ""
        for _ in range(100):
            st = self.status(env)
            if st.startswith("done"):
                break
            time.sleep(0.1)
        self.assertEqual(st, "done 1\n")
        res = os.path.join(base, "results", read(os.path.join(state, "run")).strip())
        pid = read(os.path.join(res, "selftest.pid"))
        self.assertEqual(pid, read(os.path.join(state, "selftest.pid")))
        self.assertIn("the chroot is not set up", read(os.path.join(res, "selftest.out")))
        self.assertIn("the chroot is not set up", run(PHONE, "log", "selftest", env=env).stdout)

    def whole_phone(self, sku_id="oneplus-fajita-t9", source="0123abcd, no changes\n"):
        """A whole phone's system files as the steps read them, its kit
        (kit.txt naming this tree), and the environment of its script."""
        base, state, env = self.phone_dirs()
        root = os.path.join(self.tmp, "root")
        for d in ("usr/bin", "usr/share/phone", "etc"):
            os.makedirs(os.path.join(root, d), exist_ok=True)
        with open(os.path.join(root, "usr/bin/phone-shell"), "w") as f:
            f.write("#!/bin/sh\n")
        os.chmod(os.path.join(root, "usr/bin/phone-shell"), 0o755)
        with open(os.path.join(root, "usr/share/phone/sku.json"), "w") as f:
            f.write('{\n  "id": "%s",\n  "board": "oneplus-fajita",\n  "display": {\n    "id": "s6e3fc2x01"\n  }\n}\n' % sku_id)
        if source:
            with open(os.path.join(root, "usr/share/phone/source.txt"), "w") as f:
                f.write(source)
        with open(os.path.join(root, "etc/os-release"), "w") as f:
            f.write('NAME="Kario"\nVERSION_ID=0.6\n')
        kit = os.path.join(base, "kit")
        os.makedirs(kit, exist_ok=True)
        shutil.copy(self.kit_env("oneplus-fajita-t9"), os.path.join(kit, "phone.env"))
        with open(os.path.join(kit, "kit.txt"), "w") as f:
            f.write("kit: 4567cdef, no changes\nsku: oneplus-fajita-t9\nimage: the phone's own\n")
        return base, state, {**env, "KARIO_TARGET": "whole", "KARIO_ROOT": root, "KARIO_QUIESCE": ""}

    # The whole phone's results are the chroot's: a run directory with
    # kit.txt (the kit's tree, the path, the commit the image was built
    # from, a note when the two differ), facts.txt without the phone's
    # identities, each step's time and exit code; on this machine the
    # Shell is not there, so the selftest itself fails.
    def test_the_whole_phone_s_results(self):
        base, state, env = self.whole_phone()
        r = run(PHONE, "facts", env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        r = run(PHONE, "selftest", env={**env, "SSH_CONNECTION": "172.16.42.11 50000 172.16.42.1 22"})
        self.assertNotEqual(r.returncode, 0)
        res = os.path.join(base, "results", read(os.path.join(state, "run")).strip())
        kit = read(os.path.join(res, "kit.txt"))
        for want in ("kit: 4567cdef, no changes\n", "path: the whole phone (Kario's kernel and image)\n",
                     "image built from: 0123abcd, no changes\n", "note: the kit's tree is not the commit the image was built from",
                     "image os-release NAME=\"Kario\"\n"):
            self.assertIn(want, kit)
        facts = read(os.path.join(res, "facts.txt"))
        for want in ("facts of oneplus-fajita-t9 (oneplus-fajita), whole, ", "== image\nthe whole phone: the system is the image\n",
                     "image built from: 0123abcd", "== sound\n", "== dmesg\n"):
            self.assertIn(want, facts)
        self.assertNotIn("== chroot", facts)
        self.assertRegex(read(os.path.join(res, "times.txt")), r"^selftest: exit [1-9]\d* in \d+ s\n")
        self.assertEqual(read(os.path.join(res, "selftest.rc")).strip(), str(r.returncode))
        for d in ("base", "state", "root"):
            shutil.rmtree(os.path.join(self.tmp, d))
        base2, state2, env2 = self.whole_phone(source="")
        r = run(PHONE, "facts", env=env2)
        self.assertIn("image built from: (not recorded: an image built before source.txt)",
                      read(os.path.join(base2, "results", read(os.path.join(state2, "run")).strip(), "facts.txt")))

    # The selftest's media cases play quietly: each mixer control the
    # board's [audio] names is turned down to its level for the run and set
    # back to the value it had (the amixer here notes what it is asked and
    # reads each as the 6T's codec gain at -20 dB); --quiet sets another
    # level, or with off leaves the mixer alone; teardown sets back what a
    # run stopped half-way left noted. A board without them gets nothing.
    def test_the_speaker_is_quiet_for_a_run(self):
        base, state, env = self.whole_phone()
        bindir, log = os.path.join(self.tmp, "bin"), os.path.join(self.tmp, "amixer.log")
        os.makedirs(bindir)
        with open(os.path.join(bindir, "amixer"), "w") as f:
            f.write(f'#!/bin/sh\necho "$*" >> {log}\n'
                    'case $3 in sget) printf "Simple mixer control \'%s\',0\\n  Capabilities: volume volume-joined\\n'
                    '  Playback channels: Mono\\n  Limits: -84 - 40\\n  Mono: -20 [52%%] [-20.00dB]\\n" "$4" ;; esac\n')
        os.chmod(os.path.join(bindir, "amixer"), 0o755)
        env = {**env, "PATH": bindir + os.pathsep + os.environ["PATH"], "SSH_CONNECTION": "172.16.42.11 50000 172.16.42.1 22"}
        controls = [f"RX{i} Digital" for i in range(5)]

        def calls(extra=None):
            if os.path.exists(log):
                os.remove(log)
            r = run(PHONE, "selftest", env={**env, **(extra or {})})
            self.assertIn("kario:", r.stdout)
            return read(log).splitlines() if os.path.exists(log) else []

        got = calls()
        self.assertEqual(got, [x for c in controls for x in (f"-c 0 sget {c}", f"-c 0 -q sset {c} 15%")] +
                         [f"-c 0 -q sset {c} -- -20" for c in controls])
        self.assertFalse(os.path.exists(os.path.join(state, "quiet")))
        self.assertEqual([c for c in calls({"KARIO_QUIET": "30%"}) if "15%" in c or "30%" in c],
                         [f"-c 0 -q sset {c} 30%" for c in controls])
        self.assertEqual(calls({"KARIO_QUIET": "off"}), [])
        with open(os.path.join(state, "quiet"), "w") as f:
            f.write("RX0 Digital\t-30\nRX1 Digital\t-31,-32\n")
        r = run(PHONE, "teardown", env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(read(log).splitlines(), ["-c 0 -q sset RX0 Digital -- -30", "-c 0 -q sset RX1 Digital -- -31,-32"])
        plan = run(PHONE, "--plan", "--env", self.kit_env("oneplus-fajita-t9"), "pixel", env={**os.environ, "KARIO_TARGET": "whole"})
        self.assertIn("phone# amixer -c 0 -q sset RX4 Digital 15%\nphone# >> /run/kario-test/quiet\n", plan.stdout)
        self.assertIn("phone# amixer -c 0 -q sset <each control quiet turned down> -- <the value it had>", plan.stdout)
        mido = run(PHONE, "--plan", "--env", self.kit_env("xiaomi-mido-t9"), "selftest", env={**os.environ, "KARIO_TARGET": "whole"})
        self.assertNotIn("amixer", mido.stdout)
        r = run(DRIVER, "--dry-run", "--whole", "--sku", "oneplus-fajita-t9", "--quiet", "loud", "selftest")
        self.assertEqual(r.returncode, 2)

    # The input devices' capability bitmaps leave the phone whole: the power
    # key's alone (10000000000000) is a run of digits the scrub took for an
    # identity, which it removed from the report of 2026-11-23.
    def test_facts_keep_the_key_bitmaps(self):
        base, state, env = self.whole_phone()
        sys_dir = os.path.join(self.tmp, "sys")
        for name, ev, key in (("pm8941_pwrkey", "3", "10000000000000 0"), ("Volume keys", "100003", "c000000000000 0")):
            d = os.path.join(sys_dir, "class", "input", f"event{name[0]}", "device")
            os.makedirs(os.path.join(d, "capabilities"))
            for f, v in (("name", name), ("capabilities/ev", ev), ("capabilities/key", key), ("capabilities/abs", "0"), ("capabilities/ff", "0")):
                with open(os.path.join(d, f), "w") as fh:
                    fh.write(v + "\n")
        r = run(PHONE, "facts", env={**env, "KARIO_SYS": sys_dir})
        self.assertEqual(r.returncode, 0, r.stderr)
        facts = read(os.path.join(base, "results", read(os.path.join(state, "run")).strip(), "facts.txt"))
        self.assertIn("eventp: pm8941_pwrkey | ev 0x3 | key 0x10000000000000 0x0 | abs 0x0 | ff 0x0\n", facts)
        self.assertIn("eventV: Volume keys | ev 0x100003 | key 0xc000000000000 0x0 |", facts)
        self.assertIn("== watchdog and the last boot's end\n", facts)

    # The whole phone is checked to run Kario built for the SKU; a login
    # over the developer link (the trusted cable) cannot carry a selftest,
    # whose own Shell takes the cable's link over.
    def test_the_whole_phone_s_checks(self):
        base, state, env = self.whole_phone(sku_id="qemu-t9")
        r = run(PHONE, "check", env=env)
        self.assertIn("built for the SKU 'qemu-t9', not oneplus-fajita-t9", r.stdout)
        self.assertEqual(r.returncode, 1)
        shutil.rmtree(os.path.join(self.tmp, "root"))
        r = run(PHONE, "check", env=env)
        self.assertIn("this phone does not run Kario", r.stdout)
        r = run(PHONE, "selftest", env=env)
        self.assertEqual(r.returncode, 1)
        self.assertIn("this phone does not run Kario", r.stderr)
        shutil.rmtree(self.tmp + "/base")
        base, state, env = self.whole_phone()
        for at in ("10.71.0.1", "::ffff:10.71.0.1"):
            for step in ("selftest", "pixel"):
                r = run(PHONE, step, env={**env, "SSH_CONNECTION": f"10.71.0.2 41000 {at} 22"})
                self.assertEqual(r.returncode, 1)
                self.assertIn("came over the developer link (10.71.0.1, the trusted cable)", r.stderr)
                self.assertIn("root@172.16.42.1", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(state, "stopped")))

    # gone comes with what the phone shows: the state, each pid file and
    # whether its process is there, the results.
    def test_gone_says_what_the_phone_shows(self):
        base, state, env = self.phone_dirs()
        os.makedirs(state)
        os.makedirs(os.path.join(base, "results", "r1"))
        with open(os.path.join(state, "run"), "w") as f:
            f.write("r1\n")
        p = subprocess.Popen(["true"])
        p.wait()
        with open(os.path.join(state, "selftest.pid"), "w") as f:
            f.write(f"{p.pid}\n")
        out = self.status(env)
        self.assertEqual(out.splitlines()[0], "gone")
        for want in (f"{state}/run: r1", f"{state}/selftest.pid: {p.pid} (no /proc/{p.pid})", "results, newest first:\nr1\n", "processes ("):
            self.assertIn(want, out)

    # status never starts a run: one that finds none says so (it made a new
    # run before, and then looked for the job in it).
    def test_status_starts_no_run(self):
        base, state, env = self.phone_dirs()
        os.makedirs(state)
        self.assertTrue(self.status(env).startswith("gone\n"))
        self.assertFalse(os.path.exists(os.path.join(state, "run")))
        self.assertFalse(os.path.exists(os.path.join(base, "results")))

    # A job whose pid file names another process is still found running.
    def test_status_finds_the_job_by_its_command(self):
        base, state, env = self.phone_dirs()
        os.makedirs(state)
        job = subprocess.Popen(["sh", "-c", "sleep 30; :", "on-phone.sh", "--job", "selftest"])
        try:
            self.assertEqual(self.status(env), "running\n")
        finally:
            job.kill()
            job.wait()

    # The driver passes gone on with what the phone showed and the end of
    # the step's output, for the report; the same polling for both paths.
    def test_the_driver_reports_a_gone_job(self):
        env, log = self.fake_ssh('*"on-phone.sh bg"*) echo "kario: selftest running" ;;\n'
                                 '*"on-phone.sh status"*) printf "gone\\nwhat the phone shows (systemd):\\n/run/kario-chroot/run: r1\\n" ;;\n'
                                 '*"on-phone.sh log"*) echo "the end of the output" ;;\n')
        for whole in ([], ["--whole"]):
            r = run(DRIVER, "--sku", "oneplus-fajita-t9", "--out", os.path.join(self.tmp, "out"), *whole, "selftest",
                    env={**env, "KARIO_POLL": "0"})
            self.assertEqual(r.returncode, 1)
            for want in ("what the phone shows (systemd):\n/run/kario-chroot/run: r1", "the end of the output", "ended without its exit code"):
                self.assertIn(want, r.stderr)
        self.assertIn("KARIO_TARGET=whole KARIO_TIMEOUT_SCALE=2 KARIO_FORCE=0 KARIO_HOST_TIME=", read(log).splitlines()[-1])

    # The clock is not the Shell under test's, in the chroot (Nura's) nor on
    # the whole phone (its own services'): every command entered is told so
    # (hal/rtc's OwnerEnv) on both paths, and facts reads the RTC's count
    # for the board's rtc values.
    def test_the_clock_is_the_hosts(self):
        env = re.search(r'^const OwnerEnv = "(\w+)"', read(os.path.join(FW, "hal", "rtc", "rtc.go")), re.M).group(1)
        src = read(PHONE)
        enter = src[src.index("step_enter() {"):]
        enter = enter[:enter.index("\n}\n")]
        lines = enter.splitlines()
        first = next(i for i, l in enumerate(lines) if "exec " in l)
        self.assertRegex("\n".join(lines[:first]), rf'set -- /usr/bin/env -i [^\n]* {env}=host "\$@"')
        self.assertIn('exec "$@"', lines[first])
        self.assertIn('exec chroot "$ROOT" "$@"', enter)
        self.assertIn('FIELDS="name since_epoch hctosys wakealarm" each "$sys"/class/rtc/rtc*', src)
        self.assertIn("rtc.*)", src)

    # An attribute whose read blocks is given up and said so, the reader
    # killed: /sys/power/wakeup_count waits while a wake-up event is in
    # progress, as long as phone-bringup-awake holds its wake lock, and
    # facts hung there with setup on the 6T (2026-11-24). Here a FIFO
    # stands in for it (its open blocks without a writer).
    def test_facts_go_on_past_a_blocking_attribute(self):
        base, state, env = self.whole_phone()
        sys_dir = os.path.join(self.tmp, "sys")
        power = os.path.join(sys_dir, "power")
        src = os.path.join(sys_dir, "class", "wakeup", "wakeup7")
        for d in (power, src):
            os.makedirs(d)
        charger = os.path.join(sys_dir, "class", "wakeup", "wakeup12", "device", "power")
        os.makedirs(charger)
        for f, v in (("power/state", "freeze mem"), ("power/mem_sleep", "[s2idle]"), ("power/pm_wakeup_irq", "90"),
                     ("power/wake_lock", "phone-bringup"), ("class/wakeup/wakeup7/name", "phone-bringup"),
                     ("class/wakeup/wakeup7/active_time_ms", "81234"), ("class/wakeup/wakeup7/wakeup_count", "0"),
                     ("class/wakeup/wakeup12/name", "c440000.spmi:pmic@2:charger@1000"),
                     ("class/wakeup/wakeup12/active_time_ms", "0"), ("class/wakeup/wakeup12/wakeup_count", "3"),
                     ("class/wakeup/wakeup12/device/power/wakeup", "enabled")):
            with open(os.path.join(sys_dir, f), "w") as fh:
                fh.write(v + "\n")
        count = os.path.join(power, "wakeup_count")
        os.mkfifo(count)
        # The bring-up image's watcher of its wake lock, which says why.
        awake = os.path.join(self.tmp, "awake")
        with open(awake, "w") as f:
            f.write('#!/bin/sh\n[ "$1" = check ] && echo "held: a computer has configured kario-bringup on a600000.usb"\n')
        os.chmod(awake, 0o755)
        try:
            r = subprocess.run(["sh", PHONE, "facts"], capture_output=True, text=True, cwd=FW, timeout=60,
                               env={**env, "KARIO_SYS": sys_dir, "KARIO_READ_TIMEOUT": "1", "KARIO_BRINGUP_AWAKE": awake})
        finally:
            # A reader left over (blocked in its open) would take this writer.
            try:
                os.close(os.open(count, os.O_WRONLY | os.O_NONBLOCK))
                left = True
            except OSError:
                left = False
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertFalse(left, "a reader of wakeup_count is left over")
        facts = read(os.path.join(base, "results", read(os.path.join(state, "run")).strip(), "facts.txt"))
        self.assertIn(f"{power}: state=freeze mem mem_sleep=[s2idle] wakeup_count=<no answer in 1 s> pm_wakeup_irq=90\n", facts)
        self.assertIn("wake locks: phone-bringup\nphone-bringup-awake: held: a computer has configured kario-bringup on a600000.usb\n"
                      "wake-up sources active: phone-bringup\n", facts)
        self.assertIn("wake-up source phone-bringup: wakeup= wakeup_count=0\n", facts)
        self.assertIn("wake-up source c440000.spmi:pmic@2:charger@1000: wakeup=enabled wakeup_count=3\n", facts)
        self.assertIn("== watchdog and the last boot's end\n", facts)
        self.assertEqual(os.listdir(state), ["run"])

    def fake_systemctl(self, kill_works=True):
        """A systemctl that logs each call, has the units active, and whose
        kill removes the fake /proc's processes; the /proc with phone-display's
        renderer left in its group, as an image with KillMode=process leaves
        it."""
        bindir, log, proc = os.path.join(self.tmp, "bin"), os.path.join(self.tmp, "systemctl.log"), os.path.join(self.tmp, "proc")
        os.makedirs(bindir)
        left = os.path.join(proc, "1004")
        os.makedirs(left)
        with open(os.path.join(left, "cgroup"), "w") as f:
            f.write("0::/system.slice/phone-display.service\n")
        with open(os.path.join(left, "comm"), "w") as f:
            f.write("renderer\n")
        os.makedirs(os.path.join(proc, "1"))
        with open(os.path.join(proc, "1", "cgroup"), "w") as f:
            f.write("0::/init.scope\n")
        kill = f'rm -rf {left}' if kill_works else ':'
        with open(os.path.join(bindir, "systemctl"), "w") as f:
            f.write(f'#!/bin/sh\necho "$* (left: $(ls {proc} | tr \'\\n\' \' \'))" >> {log}\n'
                    f'case $1 in kill) {kill} ;; esac\nexit 0\n')
        os.chmod(os.path.join(bindir, "systemctl"), 0o755)
        return {"PATH": bindir + os.pathsep + os.environ["PATH"], "KARIO_PROC": proc, "KARIO_INIT": "systemd",
                "KARIO_QUIESCE": "phone-display.service phone-audiod.socket", "KARIO_SETTLE": "1",
                "SSH_CONNECTION": "172.16.42.11 50000 172.16.42.1 22"}, log

    # What phone-display leaves in its group when the selftest stops it goes
    # before the run, and phone-display starts again only once nothing is
    # left: on the 6T (2026-11-24) the panel renderer's input opener stayed
    # (KillMode=process), kept the display, and phone-display failed five
    # times over with Permission denied.
    def test_the_whole_phone_s_units_leave_nothing_running(self):
        base, state, env = self.whole_phone()
        extra, log = self.fake_systemctl()
        r = run(PHONE, "selftest", env={**env, **extra})
        calls = read(log).splitlines()
        self.assertIn("phone-display.service left 1004 (renderer) running after it stopped", r.stdout)
        self.assertEqual(calls, [
            "is-active -q phone-display.service (left: 1 1004 )",
            "stop phone-display.service (left: 1 1004 )",
            "kill --kill-whom=all --signal=SIGKILL phone-display.service (left: 1 1004 )",
            "is-active -q phone-audiod.socket (left: 1 )",
            "stop phone-audiod.socket (left: 1 )",
            "start phone-display.service phone-audiod.socket (left: 1 )",
        ])
        self.assertNotIn("still has", r.stdout)

    # A process that a kill does not end is said, with what to do.
    def test_a_unit_s_process_that_stays_is_said(self):
        base, state, env = self.whole_phone()
        extra, log = self.fake_systemctl(kill_works=False)
        r = run(PHONE, "selftest", env={**env, **extra})
        self.assertIn("phone-display.service still has 1004 (renderer) after systemctl kill: it cannot start again until they are gone", r.stdout)
        self.assertIn("start phone-display.service phone-audiod.socket (left: 1 1004 )", read(log).splitlines())

    # The gate's run (39-oneplus-6t.md §8.1) plans the runs --perf names on
    # both paths: on the whole phone phone-display with a drop-in giving
    # the renderer the run's GPU path, size and the kit's sequence; in the
    # chroot the Shell started with them. One table of the runs at the end.
    def test_the_gate_s_run_plans(self):
        r = run(DRIVER, "--dry-run", "--whole", "--sku", "oneplus-fajita-t9", "--perf", "cpu-full,raster-target", "perf",
                env={**os.environ, "KARIO_INIT": "systemd"})
        self.assertEqual(r.returncode, 0, r.stderr)
        line = next(l for l in r.stdout.splitlines() if l.startswith("+ ssh") and "on-phone.sh bg perf" in l)
        self.assertIn("KARIO_PERF=cpu-full raster-target", shlex.split(line[len("+ ssh root@172.16.42.1 '"):-1]))
        res = "/var/lib/kario-test/results/<run>"
        for want in (
            "phone# (the drop-in: Environment=PHONE_PAINT_GPU=0 PHONE_PAINT_TARGET= "
            f"PHONE_PAINT_PLAY={res}/perf.play PHONE_PAINT_TRACE=1)\n"
            "    phone# > /run/systemd/system/phone-display.service.d/kario-perf.conf\n"
            "    phone# systemctl daemon-reload\n    phone# systemctl stop phone-display.service\n",
            "PHONE_PAINT_GPU=raster PHONE_PAINT_TARGET=480x640 ",
            "phone# rm -f /run/systemd/system/phone-display.service.d/kario-perf.conf\n    phone# systemctl daemon-reload\n"
            "    phone# systemctl restart phone-display.service\n",
            f"phone# /usr/lib/phone/bin/phone-paint --stats {res}/perf-cpu-full.json {res}/perf-raster-target.json > {res}/perf.txt",
        ):
            self.assertIn(want, r.stdout)
        self.assertNotIn("compose", r.stdout)
        r = run(PHONE, "--plan", "perf", env={**os.environ, "KARIO_PERF": "compose-target"})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("on-phone.sh enter PHONE_PAINT_GPU=1 PHONE_PAINT_TARGET=480x640 PHONE_PAINT_PLAY=/var/lib/kario-results/<run>/perf.play "
                      "PHONE_PAINT_TRACE=1 /usr/bin/phone-shell --services spawn --gui auto --stay-on", r.stdout)
        self.assertNotIn("systemctl", r.stdout)
        r = run(PHONE, "--plan", "perf", env={**os.environ, "KARIO_PERF": "gpu-full"})
        self.assertEqual(r.returncode, 1)
        self.assertIn("the GPU path is cpu, compose or raster", r.stderr)

    # On the whole phone each run's numbers are what the renderer wrote when
    # its sequence ended (here a systemctl that writes them as phone-display
    # starts, from the drop-in's environment); a run whose sequence stopped
    # fails the step but stays in the table; the drop-in goes after and
    # phone-display starts again as it was.
    def test_the_gate_s_run_on_the_whole_phone(self):
        base, state, env = self.whole_phone()
        root = env["KARIO_ROOT"]
        shutil.copy(os.path.join(FW, "tools", "perf", "gate.play"), os.path.join(base, "kit", "perf.play"))
        bindir, log = os.path.join(self.tmp, "bin"), os.path.join(self.tmp, "systemctl.log")
        os.makedirs(bindir)
        dropin = os.path.join(root, "run/systemd/system/phone-display.service.d/kario-perf.conf")
        stats = os.path.join(root, "run/phone/paint-stats.json")
        with open(os.path.join(bindir, "systemctl"), "w") as f:
            f.write("#!/bin/sh\necho \"$*\" >> LOG\ncase \"$*\" in\n"
                    "\"start phone-display.service\")\n\tmkdir -p $(dirname STATS)\n\te=$(sed -n 's/^Environment=//p' DROPIN)\n"
                    "\tcase $e in *raster*) s=failed ;; *) s=done ;; esac\n"
                    "\tprintf '{\"env\": \"%s\", \"play\": {\"state\": \"%s\"}}\\n' \"$e\" \"$s\" > STATS ;;\n"
                    "is-failed*) exit 1 ;;\nesac\nexit 0\n".replace("LOG", log).replace("STATS", stats).replace("DROPIN", dropin))
        with open(os.path.join(bindir, "journalctl"), "w") as f:
            f.write("#!/bin/sh\necho \"journal $*\"\n")
        paint = os.path.join(root, "usr/lib/phone/bin/phone-paint")
        os.makedirs(os.path.dirname(paint))
        with open(paint, "w") as f:
            f.write("#!/bin/sh\necho \"table of $*\"\n")
        for p in (os.path.join(bindir, "systemctl"), os.path.join(bindir, "journalctl"), paint):
            os.chmod(p, 0o755)
        proc = os.path.join(self.tmp, "proc")
        os.makedirs(proc)
        r = run(PHONE, "perf", env={**env, "PATH": bindir + os.pathsep + os.environ["PATH"], "KARIO_INIT": "systemd",
                                    "KARIO_PROC": proc, "KARIO_SETTLE": "1", "KARIO_PERF": "cpu-full raster-target"})
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("perf raster-target: the sequence did not get to its end", r.stdout)
        res = os.path.join(base, "results", read(os.path.join(state, "run")).strip())
        self.assertEqual(read(os.path.join(res, "perf-cpu-full.json")),
                         '{"env": "PHONE_PAINT_GPU=0 PHONE_PAINT_TARGET= PHONE_PAINT_PLAY=%s/perf.play PHONE_PAINT_TRACE=1", '
                         '"play": {"state": "done"}}\n' % res)
        self.assertIn("PHONE_PAINT_GPU=raster PHONE_PAINT_TARGET=480x640 ", read(os.path.join(res, "perf-raster-target.json")))
        self.assertEqual(read(os.path.join(res, "perf.play")), read(os.path.join(base, "kit", "perf.play")))
        self.assertIn("journal -b -u phone-display --since @", read(os.path.join(res, "perf-cpu-full.log")))
        self.assertEqual(read(os.path.join(res, "perf.txt")),
                         f"table of --stats {res}/perf-cpu-full.json {res}/perf-raster-target.json\n")
        self.assertEqual(read(os.path.join(res, "perf.rc")).strip(), "1")
        self.assertFalse(os.path.exists(dropin))
        run_calls = ["daemon-reload", "stop phone-display.service", "start phone-display.service"]
        self.assertEqual(read(log).splitlines(), ["is-active -q phone-display.service", *run_calls, *run_calls,
                                                  "daemon-reload", "restart phone-display.service"])

    def test_shellcheck(self):
        if not shutil.which("shellcheck"):
            self.skipTest("no shellcheck")
        r = subprocess.run(["shellcheck", "-s", "sh", DRIVER, PHONE, os.path.join(KIT, "tree-note.sh"), os.path.join(FW, "tools", "perf", "host.sh")],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout)


class UnsparseTest(unittest.TestCase):
    """unsparse.py gives back the raw image an Android sparse image stands for."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp()

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def unsparse(self, path, *args):
        return subprocess.run(["python3", UNSPARSE, *args, path], capture_output=True)

    # What the build's phone-img2simg.py writes (raw and fill chunks) comes
    # back byte for byte, its size too.
    def test_the_builds_sparse_image(self):
        raw = os.path.join(self.tmp, "raw")
        want = ext4(raw, blocks=9)
        img = os.path.join(self.tmp, "img.sparse")
        sparse(raw, img)
        r = self.unsparse(img)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout, want)
        self.assertEqual(self.unsparse(img, "--size").stdout, b"%d\n" % len(want))

    # libsparse's other chunks: "don't care" (zeros), CRC32 over what came
    # before, the header's checksum.
    def test_dont_care_and_crc(self):
        block = 4096
        data = os.urandom(block)
        out = data + bytes(2 * block) + b"\x01\x02\x03\x04" * 1024
        crc = zlib.crc32(out)
        chunks = [struct.pack("<HHII", 0xCAC1, 0, 1, 12 + block) + data,
                  struct.pack("<HHII", 0xCAC3, 0, 2, 12),
                  struct.pack("<HHII", 0xCAC2, 0, 1, 16) + b"\x01\x02\x03\x04",
                  struct.pack("<HHII", 0xCAC4, 0, 0, 16) + struct.pack("<I", crc)]
        img = os.path.join(self.tmp, "img")
        with open(img, "wb") as f:
            f.write(struct.pack("<IHHHHIIII", 0xED26FF3A, 1, 0, 28, 12, block, 4, len(chunks), crc) + b"".join(chunks))
        r = self.unsparse(img)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout, out)
        with open(img, "r+b") as f:
            f.seek(-1, os.SEEK_END)
            f.write(b"\xff")
        r = self.unsparse(img)
        self.assertEqual(r.returncode, 1)
        self.assertIn(b"CRC", r.stderr)

    # Not a sparse image, one cut short, or one whose chunks do not add up:
    # status 1 and why.
    def test_what_it_refuses(self):
        raw = os.path.join(self.tmp, "raw")
        ext4(raw)
        img = os.path.join(self.tmp, "img")
        sparse(raw, img)
        whole = blob(img)
        for name, data, why in (("ext4", blob(raw), b"not an Android sparse image"),
                                ("short", whole[:-10], b"bytes early"),
                                ("blocks", whole[:16] + struct.pack("<I", 99) + whole[20:], b"its header says 99")):
            path = os.path.join(self.tmp, name)
            with open(path, "wb") as f:
                f.write(data)
            r = self.unsparse(path)
            self.assertEqual(r.returncode, 1, name)
            self.assertIn(why, r.stderr, name)


if __name__ == "__main__":
    unittest.main()
