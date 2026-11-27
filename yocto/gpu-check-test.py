#!/usr/bin/env python3
"""Test files/phone-gpu-check.py (phone-gpu.bbclass's image check) on root
file systems made up here, without an image build.

The libraries stand in for Mesa 26's as the OnePlus 6T's image 009 has
them: EGL, GLES and GBM on libgallium-26.0.5.so, libgbm's backend in
/usr/lib/gbm, no lib/dri, no libglvnd. With paint_gpu on, the check must
take that image with freedreno in libgallium and refuse it without
(only the names of drivers left, as kmsro and the stubs leave them), with
a library or the GBM backend missing, with libglvnd but no vendor file
for Mesa, and when the GPU's kernel driver is not known or has no Mesa
driver it knows; it goes by sku.json's gpu_render over the board's
driver, takes Mesa's older lib/dri/<driver>_dri.so, and checks nothing
with paint_gpu off. The class runs it on every phone-image with the
SKU's PHONE_SKU_GPU_DRIVER, which tools/sku.py writes from the board, and
the 6T's Mesa is built with the driver the check wants for its board.

    gpu-check-test.py
"""
import importlib.util
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
FW = os.path.dirname(HERE)
LAYER = os.path.join(HERE, "meta-phone")
CHECK = os.path.join(LAYER, "files", "phone-gpu-check.py")
sys.path.insert(0, os.path.join(FW, "tools"))
import sku  # noqa: E402

spec = importlib.util.spec_from_file_location("phone_gpu_check", CHECK)
gpucheck = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gpucheck)

FREEDRENO = b"\0FD_MESA_DEBUG\0FD630\0"
# What a libgallium without freedreno still has of it: the kernel
# driver's name (kmsro's list, the stub's descriptor).
NAMES_ONLY = b"\0msm\0msm: driver missing\n\0kmsro\0LP_DEBUG\0"


def read(*p):
    with open(os.path.join(*p), encoding="utf-8") as f:
        return f.read()


class Image:
    """A root file system made up in a temporary directory."""

    def __init__(self, test, paint="compose", gallium=FREEDRENO, **sku_json):
        self.root = tempfile.mkdtemp()
        test.addCleanup(subprocess.run, ["rm", "-rf", self.root])
        os.symlink("usr/lib", os.path.join(self.root, "lib"))
        j = {"id": "oneplus-fajita-t9", **sku_json}
        if paint != "off":
            j["paint_gpu"] = paint
        self.put("usr/share/phone/sku.json", json.dumps(j).encode())
        self.put("usr/lib/libgallium-26.0.5.so", b"\x7fELF" + gallium)
        for lib in ("libEGL.so.1.0.0", "libGLESv2.so.2.0.0"):
            self.put("usr/lib/" + lib, b"\x7fELF\0libgallium-26.0.5.so\0")
        self.link("usr/lib/libEGL.so.1", "libEGL.so.1.0.0")
        self.link("usr/lib/libGLESv2.so.2", "libGLESv2.so.2.0.0")
        self.put("usr/lib/libgbm.so.1.0.0", b"\x7fELF\0GBM_BACKENDS_PATH\0/usr/lib/gbm\0gbmint_get_backend\0")
        self.link("usr/lib/libgbm.so.1", "libgbm.so.1.0.0")
        self.put("usr/lib/gbm/dri_gbm.so", b"\x7fELF\0libgallium-26.0.5.so\0")

    def put(self, path, data):
        p = os.path.join(self.root, path)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "wb") as f:
            f.write(data)

    def link(self, path, target):
        os.symlink(target, os.path.join(self.root, path))

    def rm(self, path):
        os.remove(os.path.join(self.root, path))

    def check(self, driver="msm"):
        r = subprocess.run([sys.executable, CHECK, self.root, driver], stdout=subprocess.PIPE,
                           stderr=subprocess.STDOUT, text=True)
        return r.returncode, r.stdout


class Check(unittest.TestCase):
    def ok(self, img, driver="msm", want=""):
        code, out = img.check(driver)
        self.assertEqual(code, 0, out)
        self.assertIn(want, out)

    def refused(self, img, why, driver="msm"):
        code, out = img.check(driver)
        self.assertEqual(code, 1, out)
        self.assertRegex(out, why)

    def test_mesa_26_with_freedreno(self):
        self.ok(Image(self), want="freedreno for msm in /usr/lib/libgallium-26.0.5.so")

    def test_without_freedreno(self):
        img = Image(self, gallium=NAMES_ONLY)
        self.refused(img, r"Mesa has no freedreno driver for the GPU \(msm\): not in /usr/lib/libgallium-26.0.5.so")
        self.refused(img, re.escape('PACKAGECONFIG:append:pn-mesa = " freedreno"'))
        img.rm("usr/lib/libgallium-26.0.5.so")
        self.refused(img, "not in no libgallium")

    def test_a_library_or_the_gbm_backend_missing(self):
        for path, why in (("usr/lib/libEGL.so.1.0.0", "libEGL.so.1 is not in the image"),
                          ("usr/lib/libGLESv2.so.2.0.0", "libGLESv2.so.2 is not in the image"),
                          ("usr/lib/libgbm.so.1.0.0", "libgbm.so.1 is not in the image"),
                          ("usr/lib/gbm/dri_gbm.so", "loads its backend from /usr/lib/gbm, which has no dri_gbm.so")):
            with self.subTest(path=path):
                img = Image(self)
                img.rm(path)
                self.refused(img, why)

    def test_glvnd_needs_mesas_vendor_file(self):
        img = Image(self)
        img.put("usr/lib/libEGL_mesa.so.0", b"\x7fELF")
        self.refused(img, "no vendor file for Mesa's libEGL_mesa.so.0")
        img.put("usr/share/glvnd/egl_vendor.d/50_mesa.json",
                b'{"file_format_version": "1.0.0", "ICD": {"library_path": "libEGL_mesa.so.0"}}')
        self.ok(img)

    def test_the_gpus_driver(self):
        self.refused(Image(self), "the GPU's kernel driver is not known", driver="")
        self.refused(Image(self), "no Mesa driver is known for the kernel driver powervr", driver="powervr")
        # The render node's driver where the display's device cannot render.
        split = Image(self, gallium=FREEDRENO, gpu_render="panfrost")
        self.refused(split, r"no panfrost driver for the GPU \(panfrost\)", driver="")
        self.ok(Image(self, gallium=b"\0PAN_MESA_DEBUG\0", gpu_render="panfrost"), driver="", want="panfrost for panfrost")
        self.ok(Image(self, gallium=b"\0PAN_MESA_DEBUG\0", gpu_render="panfrost"), driver="msm", want="panfrost")

    def test_older_mesas_dri_driver(self):
        img = Image(self, gallium=NAMES_ONLY)
        img.put("usr/lib/dri/msm_dri.so", b"\x7fELF")
        self.ok(img, want="/usr/lib/dri/msm_dri.so")

    def test_off_checks_nothing(self):
        img = Image(self, paint="off", gallium=NAMES_ONLY)
        img.rm("usr/lib/libEGL.so.1.0.0")
        self.ok(img, driver="", want="paint_gpu is off: nothing to check")
        img.rm("usr/share/phone/sku.json")
        self.ok(img, driver="", want="nothing to check")

    def test_raster_as_compose(self):
        self.refused(Image(self, paint="raster", gallium=NAMES_ONLY), "paint_gpu is raster")

    # /lib -> /usr/lib as an absolute link: inside the image, not the host's.
    def test_absolute_links_stay_in_the_image(self):
        img = Image(self)
        os.remove(os.path.join(img.root, "lib"))
        img.link("lib", "/usr/lib")
        self.assertEqual(gpucheck.files(img.root, "/lib", "libgallium*"), ["/usr/lib/libgallium-26.0.5.so"])
        self.ok(img)


class Wiring(unittest.TestCase):
    def test_the_image_runs_it(self):
        image = read(LAYER, "recipes-core", "images", "phone-image.bb")
        self.assertRegex(image, r"(?m)^inherit .*\bphone-gpu\b")
        cls = read(LAYER, "classes-recipe", "phone-gpu.bbclass")
        self.assertIn("files/phone-gpu-check.py", cls)
        self.assertIn('ERROR_QA:append = " gpu-driver"', cls)
        self.assertIn('IMAGE_QA_COMMANDS += "phone_gpu_check"', cls)
        self.assertIn("d.getVar('PHONE_SKU_GPU_DRIVER')", cls)
        self.assertIn('oe.qa.handle_error("gpu-driver"', cls)

    # Each board's GPU driver has a Mesa driver the check knows; the 6T's
    # Mesa is built with it (yocto/kas/phone-fajita.yml).
    def test_the_boards(self):
        for b in sku.names("boards"):
            board = sku.load("boards", b)
            driver = board.get("gpu_render") or board.get("gpu_driver")
            if driver:
                self.assertIn(driver, gpucheck.MESA, b)
        fajita = sku.load("boards", "oneplus-fajita")
        mesa = re.findall(r'PACKAGECONFIG:append:pn-mesa = "([^"]*)"', read(FW, "yocto", "kas", "phone-fajita.yml"))
        self.assertIn(gpucheck.MESA[fajita["gpu_driver"]][0], " ".join(mesa).split())
        self.assertIn('PHONE_SKU_GPU_DRIVER = "msm"\n', sku.kas(sku.resolve("oneplus-fajita-t9")))


if __name__ == "__main__":
    unittest.main()
