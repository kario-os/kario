#!/usr/bin/env python3
"""Test files/phone-modsig-check.py (phone-modsig.bbclass's image check)
without a kernel build.

Keys come from yocto/ota-keys.sh (two build directories' kernel module
keys); a kernel image is a release banner and the build's certificate
amid other bytes, raw, gzip-compressed and in an Android boot image;
modules are signed as the kernel's sign-file signs them (openssl cms:
detached, no certificates, no attributes; by issuer and serial number or
by key identifier) with struct module_signature and the marker appended.
The check must pass such modules and refuse unsigned ones, those signed by
the other build's key (the 6T's first whole-phone image), those changed
after signing, those of another release, and a kernel without the given
certificate.

    modsig-test.py
"""
import gzip
import os
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
CHECK = os.path.join(HERE, "meta-phone", "files", "phone-modsig-check.py")
RELEASE = "7.1.0-rc1-sdm845"
MARKER = b"~Module signature appended~\n"


def keys(d):
    subprocess.run(["sh", os.path.join(HERE, "ota-keys.sh"), d], check=True, stdout=subprocess.DEVNULL)
    return os.path.join(d, "kernel-modules.pem")


def cert_der(pem):
    return subprocess.run(["openssl", "x509", "-in", pem, "-outform", "DER"], check=True, capture_output=True).stdout


def sign(body, pem, tmp, keyid=False, attrs=False):
    """body with a signature appended as sign-file appends one."""
    src, sig = os.path.join(tmp, "body"), os.path.join(tmp, "sig")
    with open(src, "wb") as f:
        f.write(body)
    args = ["openssl", "cms", "-sign", "-binary", "-nocerts", "-nosmimecap", "-md", "sha512",
            "-signer", pem, "-inkey", pem, "-in", src, "-outform", "DER", "-out", sig]
    if not attrs:
        args.append("-noattr")
    if keyid:
        args.append("-keyid")
    subprocess.run(args, check=True)
    with open(sig, "rb") as f:
        s = f.read()
    return body + s + struct.pack(">BBBBB3xI", 0, 0, 2, 0, 0, len(s)) + MARKER


def module_body(name):
    return b"\x7fELF\x02\x01\x01" + os.urandom(3000) + f"\0vermagic={RELEASE} SMP mod_unload aarch64\0name={name}\0".encode()


class Check(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp(prefix="modsig-test-")
        cls.ours = keys(os.path.join(cls.dir, "ours"))
        cls.theirs = keys(os.path.join(cls.dir, "theirs"))
        image = (b"\0" * 4096 + f"Linux version {RELEASE} (kario@build) (gcc 15) #1 SMP PREEMPT\n".encode()
                 + os.urandom(2048) + cert_der(cls.ours) + os.urandom(2048))
        cls.image = cls.write("Image", image)
        cls.gz = cls.write("Image.gz", gzip.compress(image))
        # Header version 0, page size 4096: the kernel (Image.gz and the
        # device tree) at the second page.
        dtb = b"\xd0\x0d\xfe\xed" + os.urandom(500)
        kernel = gzip.compress(image) + dtb
        header = b"ANDROID!" + struct.pack("<10I", len(kernel), 0x8000, 0, 0x1000000, 0, 0xf00000, 0x100, 4096, 0, 0)
        cls.boot = cls.write("boot.img", header.ljust(4096, b"\0") + kernel.ljust(-(-len(kernel) // 4096) * 4096, b"\0"))
        cls.other = cls.write("Image-other", image.replace(cert_der(cls.ours), cert_der(cls.theirs)))

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.dir)

    @classmethod
    def write(cls, name, data):
        path = os.path.join(cls.dir, name)
        with open(path, "wb") as f:
            f.write(data)
        return path

    def modules(self, mods, release=RELEASE):
        root = tempfile.mkdtemp(dir=self.dir)
        for rel, data in mods.items():
            path = os.path.join(root, release, "kernel", rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "wb") as f:
                f.write(data)
        return root

    def run_check(self, root, *kernels, cert=None):
        args = [sys.executable, CHECK]
        for k in kernels or (self.image,):
            args += ["--kernel", k]
        if cert:
            args += ["--cert", cert]
        r = subprocess.run(args + [root], capture_output=True, text=True)
        return r.returncode, r.stdout

    def signed(self, name, pem=None, **kw):
        return sign(module_body(name), pem or self.ours, self.dir, **kw)

    def test_signed_with_the_kernel_key(self):
        root = self.modules({"drivers/rtc/rtc-pm8xxx.ko": self.signed("rtc_pm8xxx"),
                             "net/qrtr/qrtr.ko": self.signed("qrtr", keyid=True),
                             "net/netfilter/nft_ct.ko": self.signed("nft_ct", attrs=True)})
        for kernels in ((self.image,), (self.gz,), (self.boot,), (self.gz, self.boot)):
            rc, out = self.run_check(root, *kernels, cert=self.ours)
            self.assertEqual(rc, 0, out)
            self.assertIn("3 modules for Linux " + RELEASE, out)
            self.assertIn("Kario kernel modules (development)", out)
            self.assertIn("PASS", out)
        rc, out = self.run_check(root)
        self.assertEqual(rc, 0, out)

    def test_signed_by_another_build(self):
        # What the 6T refused: every module signed with the other build's key.
        root = self.modules({"drivers/rtc/rtc-pm8xxx.ko": self.signed("rtc_pm8xxx", self.theirs),
                             "drivers/power/supply/bq27xxx_battery.ko": self.signed("bq27xxx_battery", self.theirs)})
        rc, out = self.run_check(root, self.boot)
        self.assertEqual(rc, 1, out)
        self.assertIn("rtc-pm8xxx.ko: signed by a key the kernel does not have: Kario kernel modules (development) (issuer), serial", out)
        self.assertIn("FAIL", out)
        rc, out = self.run_check(self.modules({"a.ko": self.signed("a", self.theirs, keyid=True)}))
        self.assertEqual(rc, 1, out)
        self.assertIn("signed by a key the kernel does not have: key identifier", out)

    def test_unsigned_and_changed(self):
        good = self.signed("rtc_pm8xxx")
        flipped = bytearray(good)
        flipped[100] ^= 1
        root = self.modules({"unsigned.ko": module_body("unsigned"), "changed.ko": bytes(flipped),
                             "appended.ko": good + b"\n", "ok.ko": good})
        rc, out = self.run_check(root, cert=self.ours)
        self.assertEqual(rc, 1, out)
        self.assertIn("unsigned.ko: unsigned", out)
        self.assertIn("appended.ko: unsigned", out)
        self.assertIn("changed.ko: signature does not verify", out)
        self.assertNotIn("ok.ko", out)
        self.assertIn("(3 problems)", out)

    def test_signed_attributes_changed(self):
        good = bytearray(self.signed("nft_ct", attrs=True))
        good[100] ^= 1
        rc, out = self.run_check(self.modules({"nft_ct.ko": bytes(good)}))
        self.assertEqual(rc, 1, out)
        self.assertIn("nft_ct.ko: signature does not verify", out)

    def test_another_release(self):
        root = self.modules({"a.ko": self.signed("a")}, release="6.18.54-yocto-standard")
        rc, out = self.run_check(root)
        self.assertEqual(rc, 1, out)
        self.assertIn("for Linux 6.18.54-yocto-standard, the kernel is " + RELEASE, out)

    def test_a_kernel_without_the_certificate(self):
        root = self.modules({"a.ko": self.signed("a")})
        rc, out = self.run_check(root, self.other, cert=self.ours)
        self.assertEqual(rc, 1, out)
        self.assertIn("the kernel does not carry the certificate of " + self.ours, out)
        # Two kernels that trust different keys: no module verifies against both.
        rc, out = self.run_check(root, self.image, self.other)
        self.assertEqual(rc, 1, out)
        self.assertIn("no certificate", out)

    def test_no_modules(self):
        rc, out = self.run_check(os.path.join(self.dir, "none"), cert=self.ours)
        self.assertEqual(rc, 0, out)
        self.assertIn("no modules to check", out)


if __name__ == "__main__":
    unittest.main()
