#!/usr/bin/env python3
"""Whether an image whose phone-paint takes its GPU path by default has
what that path loads and a Mesa driver for the board's GPU.

    phone-gpu-check.py ROOTFS [DRIVER]

The image's share/phone/sku.json gives the path (paint_gpu, absent for
off; 42-gpu-rendering.md §3) and, where the display's device cannot
render, the GPU's kernel driver (gpu_render); DRIVER is that driver
otherwise (the board's gpu_driver, PHONE_SKU_GPU_DRIVER). With paint_gpu
off there is nothing to check.

What phone-paint loads (gpu.rs, gbm.rs): libEGL.so.1, libGLESv2.so.2 and
libgbm.so.1, with the GBM backend libgbm names a directory for (Mesa's
dri_gbm.so, Mesa 24.3 on); with libglvnd, Mesa's EGL vendor file. Since
Mesa 24.2 the drivers are all in libgallium-*.so, and a driver's name
there proves nothing (the kmsro and stub entries carry every name), so
the driver must leave a string of its own code there (MESA); before
that, the loader's lib/dri/<driver>_dri.so.

Exit status 1 when something is missing.
"""
import glob
import json
import mmap
import os
import re
import sys

LIBDIRS = ("/usr/lib", "/usr/lib64", "/lib", "/lib64")
SKU = "/usr/share/phone/sku.json"
# The kernel's DRM driver: Mesa's gallium driver for it, and a string only
# that driver's code has (its debug variable's name).
MESA = {
    "msm": ("freedreno", b"FD_MESA_DEBUG\0"),
    "panfrost": ("panfrost", b"PAN_MESA_DEBUG\0"),
    "etnaviv": ("etnaviv", b"ETNA_MESA_DEBUG\0"),
    "lima": ("lima", b"LIMA_DEBUG\0"),
    "vc4": ("vc4", b"VC4_DEBUG\0"),
    "virtio_gpu": ("virgl", b"VIRGL_DEBUG\0"),
}
GBM_DIR = re.compile(rb"\0(/[A-Za-z0-9_./-]*/gbm)\0")


def real(root, path):
    """The path an absolute path of the image names, symlinks followed
    inside the image (absolute ones too); None for a loop."""
    parts = [p for p in path.split("/") if p]
    out = []
    hops = 0
    while parts:
        name = parts.pop(0)
        if name == ".":
            continue
        if name == "..":
            if out:
                out.pop()
            continue
        full = os.path.join(root, *out, name)
        if os.path.islink(full):
            hops += 1
            if hops > 40:
                return None
            target = os.readlink(full)
            if target.startswith("/"):
                out = []
            parts = [p for p in target.split("/") if p] + parts
            continue
        out.append(name)
    return "/" + "/".join(out)


def file(root, path):
    """The host path of the image's regular file at path, or None."""
    p = real(root, path)
    if p is None or not os.path.isfile(os.path.join(root, p.lstrip("/"))):
        return None
    return os.path.join(root, p.lstrip("/"))


def library(root, name):
    for d in LIBDIRS:
        f = file(root, d + "/" + name)
        if f:
            return f
    return None


def files(root, d, pattern):
    """The image's files in its directory d matching pattern, as paths
    of the image."""
    p = real(root, d)
    if p is None or not os.path.isdir(os.path.join(root, p.lstrip("/"))):
        return []
    names = glob.glob(os.path.join(glob.escape(os.path.join(root, p.lstrip("/"))), pattern))
    return [p.rstrip("/") + "/" + os.path.basename(n) for n in names if file(root, p.rstrip("/") + "/" + os.path.basename(n))]


def has(path, needle):
    if os.path.getsize(path) == 0:
        return False
    with open(path, "rb") as f, mmap.mmap(f.fileno(), 0, access=mmap.ACCESS_READ) as m:
        return m.find(needle) >= 0


def check(root, driver=""):
    """(problems, what the image has)."""
    f = file(root, SKU)
    if not f:
        return [], f"no {SKU}: nothing to check"
    with open(f, encoding="utf-8") as fp:
        sku = json.load(fp)
    paint = sku.get("paint_gpu", "off")
    if paint == "off":
        return [], "paint_gpu is off: nothing to check"
    driver = sku.get("gpu_render") or driver
    problems = []
    for name, pkg in (("libEGL.so.1", "libegl-mesa"), ("libGLESv2.so.2", "libgles2-mesa"), ("libgbm.so.1", "libgbm")):
        if not library(root, name):
            problems.append(f"{name} is not in the image (Mesa's {pkg})")
    gbm = library(root, "libgbm.so.1")
    if gbm:
        with open(gbm, "rb") as fp:
            m = GBM_DIR.search(fp.read())
        if m and not file(root, m[1].decode() + "/dri_gbm.so"):
            problems.append(f"libgbm.so.1 loads its backend from {m[1].decode()}, which has no dri_gbm.so (Mesa's libgbm)")
    vendors = files(root, "/usr/share/glvnd/egl_vendor.d", "*.json")
    if library(root, "libEGL_mesa.so.0") and not any(has(file(root, j), b"libEGL_mesa") for j in vendors):
        problems.append("libglvnd's libEGL.so.1 has no vendor file for Mesa's libEGL_mesa.so.0 (share/glvnd/egl_vendor.d)")
    if not driver:
        problems.append(f"paint_gpu is {paint}, but the GPU's kernel driver is not known: "
                        "the board names it (gpu_driver, or gpu_render; 24-sku-framework.md)")
        return problems, ""
    if driver not in MESA:
        problems.append(f"no Mesa driver is known for the kernel driver {driver} (phone-gpu-check.py's MESA)")
        return problems, ""
    name, needle = MESA[driver]
    gallium = sorted({real(root, g) for d in LIBDIRS for g in files(root, d, "libgallium*.so*")})
    found = [g for g in gallium if has(file(root, g), needle)]
    if found:
        return problems, f"{name} for {driver} in {', '.join(found)}"
    if file(root, f"/usr/lib/dri/{driver}_dri.so"):
        return problems, f"{name} for {driver}: /usr/lib/dri/{driver}_dri.so"
    where = ", ".join(gallium) or "no libgallium"
    problems.append(f"paint_gpu is {paint}, but Mesa has no {name} driver for the GPU ({driver}): "
                    f"not in {where}, no /usr/lib/dri/{driver}_dri.so "
                    f"(PACKAGECONFIG:append:pn-mesa = \" {name}\")")
    return problems, ""


def main():
    if len(sys.argv) not in (2, 3):
        print(__doc__.strip().splitlines()[3].strip(), file=sys.stderr)
        return 2
    problems, got = check(sys.argv[1], sys.argv[2] if len(sys.argv) == 3 else "")
    for p in problems:
        print(p)
    if not problems:
        print(got)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
