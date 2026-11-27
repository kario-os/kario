#!/usr/bin/env python3
"""Test phone-initramfs.bb's firmware for built-in drivers
(PHONE_INITRAMFS_FIRMWARE): its function run here on a root file system
and package data made up as bitbake leaves them, without an image build.

The OnePlus 6T's case: linux-firmware-qcom-adreno-a630 (a630_sqe.fw,
a630_gmu.bin; it needs linux-firmware-qcom-license) and
firmware-oneplus-sdm845 (the zap shader among the remote processors'
firmware; it needs the Wi-Fi and Bluetooth firmware), next to
wireless-regdb-static's regulatory.db. Only the three files, the license
of the package that has the first two and regulatory.db may stay, with no
empty directory left, even where a firmware package needs
wireless-regdb-static; nothing changes with neither variable set; a file
no such package has, a file gone from the image and one variable without
the other stop the build. yocto/kas/phone-fajita.yml sets the GPU's files,
and kario-bsp's firmware-oneplus-sdm845 installs the zap shader where they
say (KARIO_BSP_LAYERS names its layers: make test gives them).

    initramfs-firmware-test.py
"""
import json
import os
import re
import shutil
import sys
import tempfile
import textwrap
import types
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
FW = os.path.dirname(HERE)
RECIPE = os.path.join(HERE, "meta-phone", "recipes-core", "images", "phone-initramfs.bb")
sys.path.insert(0, os.path.join(FW, "tools"))
import sku  # noqa: E402

try:
    SDM845 = sku.layer_file("recipes-bsp", "firmware-oneplus-sdm845", "firmware-oneplus-sdm845_19.bb")
except sku.Bad as e:
    sys.exit(f"initramfs-firmware-test: {e}")

ZAP = "qcom/sdm845/OnePlus/enchilada/a630_zap.mbn"
GPU = ["qcom/a630_sqe.fw", "qcom/a630_gmu.bin", ZAP]
PACKAGES = ["linux-firmware-qcom-adreno-a630", "firmware-oneplus-sdm845"]
BASE = "busybox phone-ota-init wireless-regdb-static phone-sku-partitions phone-abslot"
# Package -> (its files, its RDEPENDS as pkgdata has them).
PKGDATA = {
    "busybox": (["/usr/bin/busybox"], "libc6 (>= 2.42)"),
    "libc6": (["/usr/lib/libc.so.6"], ""),
    "phone-ota-init": (["/init"], "busybox"),
    "wireless-regdb-static": (["/usr/lib/firmware/regulatory.db", "/usr/lib/firmware/regulatory.db.p7s"], ""),
    "phone-sku-partitions": (["/etc/phone/partitions.conf"], ""),
    "phone-qcom-abslot": (["/usr/bin/phone-qcom-abslot"], "busybox"),
    "linux-firmware-qcom-adreno-a630": (["/usr/lib/firmware/qcom/a630_sqe.fw", "/usr/lib/firmware/qcom/a630_gmu.bin"],
                                        "linux-firmware-qcom-license"),
    "linux-firmware-qcom-license": (["/usr/lib/firmware/LICENSE.qcom"], ""),
    "firmware-oneplus-sdm845": ([f"/usr/lib/firmware/qcom/sdm845/OnePlus/enchilada/{f}" for f in
                                 ("a630_zap.mbn", "adsp.mbn", "cdsp.mbn", "modem.mbn", "venus.mbn")] +
                                ["/usr/lib/firmware/qca/OnePlus/enchilada/crnv21.bin",
                                 "/usr/lib/firmware/updates/qca/crbtfw21.tlv", "/usr/lib/firmware/updates/tfa98xx.cnt"],
                                "linux-firmware-ath10k-wcn3990 linux-firmware-qca-wcn399x linux-firmware-qcom-adreno-a630"),
    "linux-firmware-ath10k-wcn3990": (["/usr/lib/firmware/ath10k/WCN3990/hw1.0/wlanmdsp.mbn"], "linux-firmware-ath10k-license"),
    "linux-firmware-qca-wcn399x": (["/usr/lib/firmware/qca/crbtfw21.tlv"], "linux-firmware-ath10k-license"),
    "linux-firmware-ath10k-license": (["/usr/lib/firmware/LICENCE.atheros_firmware"], ""),
}
RPROVIDES = {"phone-abslot": ["phone-qcom-abslot"]}


def read(*p):
    with open(os.path.join(*p), encoding="utf-8") as f:
        return f.read()


class Fatal(Exception):
    pass


def fatal(msg):
    raise Fatal(msg)


def explode_deps(s):
    out, skip = [], False
    for t in s.split():
        skip = skip or t.startswith("(")
        if not skip:
            out.append(t)
        if t.endswith(")"):
            skip = False
    return out


def packagedata(pkgdata):
    """oe.packagedata's functions the recipe uses, on pkgdata."""
    pd = types.ModuleType("oe.packagedata")

    def providers(d, rdep, include_rdep=False):
        for p in RPROVIDES.get(rdep, []) + ([rdep] if include_rdep else []):
            yield p, {}
    pd.foreach_runtime_provider_pkgdata = providers
    pd.has_subpkgdata = lambda pkg, d: pkg in pkgdata
    pd.read_subpkgdata_dict = lambda pkg, d: {"FILES_INFO": json.dumps({f: 1 for f in pkgdata[pkg][0]}),
                                              "RDEPENDS": pkgdata[pkg][1]}
    return pd


class D:
    def __init__(self, **v):
        self.v = v

    def getVar(self, name):
        return self.v.get(name, "")


def function():
    """The recipe's function, as bitbake runs it: a function of d."""
    m = re.search(r"^python phone_initramfs_firmware\(\) \{\n(.*?)^\}", read(RECIPE), re.M | re.S)
    return "def phone_initramfs_firmware(d):\n" + textwrap.indent(textwrap.dedent(m.group(1)), "    ")


class Firmware(unittest.TestCase):
    def image(self, pkgdata=PKGDATA):
        root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, root)
        for files, _ in pkgdata.values():
            for f in files:
                os.makedirs(os.path.dirname(root + f), exist_ok=True)
                with open(root + f, "w") as fp:
                    fp.write(f)
        return root

    def run_it(self, root, keep=GPU, pkgs=PACKAGES, pkgdata=PKGDATA):
        oe = types.ModuleType("oe")
        oe.packagedata = packagedata(pkgdata)
        bb = types.SimpleNamespace(fatal=fatal, utils=types.SimpleNamespace(explode_deps=explode_deps))
        saved = {k: sys.modules.get(k) for k in ("oe", "oe.packagedata")}
        sys.modules.update({"oe": oe, "oe.packagedata": oe.packagedata})
        try:
            d = D(PHONE_INITRAMFS_FIRMWARE=" ".join(keep), PHONE_INITRAMFS_FIRMWARE_PACKAGES=" ".join(pkgs),
                  nonarch_base_libdir="/usr/lib", IMAGE_ROOTFS=root, PACKAGE_INSTALL=" ".join([BASE] + list(pkgs)))
            g = {"bb": bb, "os": os}
            exec(function(), g)
            g["phone_initramfs_firmware"](d)
        finally:
            for k, v in saved.items():
                if v is None:
                    sys.modules.pop(k, None)
                else:
                    sys.modules[k] = v

    def tree(self, root):
        out = set()
        for top, dirs, names in os.walk(root):
            out.update(os.path.relpath(os.path.join(top, n), root) for n in names)
            out.update(os.path.relpath(os.path.join(top, n), root) + "/" for n in dirs if not os.listdir(os.path.join(top, n)))
        return out

    def test_the_6t_gpu(self):
        root = self.image()
        self.run_it(root)
        fw = {f[len("usr/lib/firmware/"):] for f in self.tree(root) if f.startswith("usr/lib/firmware/")}
        self.assertEqual(fw, set(GPU) | {"LICENSE.qcom", "regulatory.db", "regulatory.db.p7s"})
        self.assertFalse(os.path.exists(os.path.join(root, "usr/lib/firmware/updates")), "an empty directory is left")
        self.assertLessEqual({"usr/bin/busybox", "init", "usr/bin/phone-qcom-abslot"}, self.tree(root))

    def test_what_other_packages_need_stays(self):
        pkgdata = dict(PKGDATA, **{"linux-firmware-ath10k-wcn3990": (PKGDATA["linux-firmware-ath10k-wcn3990"][0],
                                                                      "linux-firmware-ath10k-license wireless-regdb-static")})
        root = self.image(pkgdata)
        self.run_it(root, pkgdata=pkgdata)
        self.assertIn("usr/lib/firmware/regulatory.db", self.tree(root))
        self.assertNotIn("usr/lib/firmware/ath10k/WCN3990/hw1.0/wlanmdsp.mbn", self.tree(root))

    def test_none_changes_nothing(self):
        root = self.image()
        before = self.tree(root)
        self.run_it(root, keep=[], pkgs=[])
        self.assertEqual(self.tree(root), before)

    def test_refused(self):
        for keep, pkgs, gone, why in (
            (GPU, [], None, "go together"),
            ([], PACKAGES, None, "go together"),
            (GPU + ["qcom/a640_sqe.fw"], PACKAGES, None, "no package .* has /usr/lib/firmware/qcom/a640_sqe.fw"),
            (GPU, ["linux-firmware-qcom-adreno-a630"], None, f"has /usr/lib/firmware/{ZAP}"),
            (GPU, PACKAGES, "usr/lib/firmware/qcom/a630_gmu.bin", "a630_gmu.bin is not in the initramfs"),
        ):
            with self.subTest(why=why, keep=keep, pkgs=pkgs):
                root = self.image()
                if gone:
                    os.remove(os.path.join(root, gone))
                with self.assertRaisesRegex(Fatal, why):
                    self.run_it(root, keep=keep, pkgs=pkgs)


class Wiring(unittest.TestCase):
    def test_the_recipe(self):
        recipe = read(RECIPE)
        self.assertIn('PACKAGE_INSTALL += "${PHONE_INITRAMFS_FIRMWARE_PACKAGES}"', recipe)
        self.assertIn('ROOTFS_POSTPROCESS_COMMAND += "phone_initramfs_firmware"', recipe)

    # The 6T's GPU: what msm asked for at 5.5 s (a630_sqe.fw, then
    # a630_gmu.bin) and the zap shader of its device tree, which kario-bsp's
    # firmware package installs.
    def test_the_6t(self):
        kas = read(FW, "yocto", "kas", "phone-fajita.yml")
        keep = re.search(r'^    PHONE_INITRAMFS_FIRMWARE = "([^"]*)"$', kas, re.M).group(1).split()
        pkgs = re.search(r'^    PHONE_INITRAMFS_FIRMWARE_PACKAGES = "([^"]*)"$', kas, re.M).group(1).split()
        self.assertEqual(keep, GPU)
        self.assertEqual(pkgs, PACKAGES)
        recipe = read(SDM845)
        self.assertIn("a630_zap.mbn", re.search(r'^FW_QCOM = "([^"]*)"', recipe, re.M | re.S).group(1).split())
        self.assertIn("$fw/qcom/sdm845/OnePlus/enchilada/", recipe)


if __name__ == "__main__":
    unittest.main()
