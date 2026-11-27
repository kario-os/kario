#!/usr/bin/env python3
"""Test btvirt, the emulator's virtual Bluetooth controllers, with the
layer's BlueZ patches (meta-phone's bluez5 append), on the build machine.

Unpacks the BlueZ release the image builds, applies the patches the append
lists, in its order, and builds btvirt's controller and main loop with
AddressSanitizer around btvirt-test.c, which runs an inquiry to its end, the
same with its end and the next result in one epoll batch (what crashed
btvirt on QEMU, 13-prototype.md), a page timeout, and two controllers
pairing twice, each to be asked its IO capability before its Simple
Pairing Complete (the kernel's connection reference). Any use after free
fails it. --unpatched builds the release as it is, which fails.

    btvirt-test.py [--unpatched] [bluez-VERSION.tar.xz] [cc]

Without a tarball it takes the one in $DL_DIR, ~/yocto/cache/downloads or
build/, and downloads it from kernel.org into build/ when there is none.
"""
import hashlib
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
APPEND_DIR = os.path.join(HERE, "meta-phone", "recipes-connectivity", "bluez5")
APPEND = os.path.join(APPEND_DIR, "bluez5_%.bbappend")
HARNESS = os.path.join(HERE, "btvirt-test.c")
# oe-core's bluez5 at the commit the kas files pin (bluez5_5.86.bb).
VERSION = "5.86"
SHA256 = "99f144540c6070591e4c53bcb977eb42664c62b7b36cb35a29cf72ded339621d"
URL = f"https://www.kernel.org/pub/linux/bluetooth/bluez-{VERSION}.tar.xz"
SOURCES = ["emulator/btdev.c", "src/shared/util.c", "src/shared/queue.c", "src/shared/crypto.c",
           "src/shared/ecc.c", "src/shared/timeout-mainloop.c", "src/shared/mainloop.c",
           "src/shared/mainloop-notify.c", "src/shared/io-mainloop.c", "lib/bluetooth/bluetooth.c"]
SCENARIOS = ["inquiry", "batch", "page", "repair"]


def patches():
    """The patches the append adds, in its order."""
    with open(APPEND) as f:
        text = f.read()
    return [os.path.join(APPEND_DIR, "files", p) for p in re.findall(r"file://([^\s\"\\]+\.patch)", text)]


def tarball(given):
    if given:
        return given
    name = f"bluez-{VERSION}.tar.xz"
    for d in [os.environ.get("DL_DIR", ""), os.path.expanduser("~/yocto/cache/downloads"), os.path.join(REPO, "build")]:
        if d and os.path.exists(os.path.join(d, name)):
            return os.path.join(d, name)
    path = os.path.join(REPO, "build", name)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    print(f"downloading {URL}")
    urllib.request.urlretrieve(URL, path + ".part")
    os.rename(path + ".part", path)
    return path


def main():
    args = sys.argv[1:]
    unpatched = "--unpatched" in args
    args = [a for a in args if a != "--unpatched"]
    src = tarball(next((a for a in args if a.endswith(".tar.xz")), None))
    cc = next((a for a in args if not a.endswith(".tar.xz")), os.environ.get("CC", "cc"))
    with open(src, "rb") as f:
        if hashlib.sha256(f.read()).hexdigest() != SHA256:
            sys.exit(f"{src}: not BlueZ {VERSION} as oe-core fetches it (sha256)")
    with tempfile.TemporaryDirectory() as tmp:
        with tarfile.open(src) as t:
            t.extractall(tmp, filter="data")
        tree = os.path.join(tmp, f"bluez-{VERSION}")
        for p in [] if unpatched else patches():
            subprocess.run(["patch", "-s", "-p1", "-d", tree, "-i", p], check=True)
            print(f"applied {os.path.basename(p)}")
        # 5.86's btdev.c includes glib.h for nothing but MIN; no config.h
        # without configure.
        inc = os.path.join(tmp, "include")
        os.makedirs(inc)
        with open(os.path.join(inc, "glib.h"), "w") as f:
            f.write("#define MIN(a, b) ((a) < (b) ? (a) : (b))\n")
        with open(os.path.join(inc, "config.h"), "w") as f:
            f.write(f'#define VERSION "{VERSION}"\n')
        exe = os.path.join(tmp, "btvirt-test")
        subprocess.run([cc, "-g", "-O1", "-fsanitize=address", "-fno-omit-frame-pointer", "-DHAVE_CONFIG_H",
                        "-w", "-I", inc, "-I", tree, "-I", os.path.join(tree, "lib"), "-o", exe, HARNESS]
                       + [os.path.join(tree, s) for s in SOURCES], check=True)
        failed = []
        env = dict(os.environ, ASAN_OPTIONS="detect_leaks=0")
        for s in SCENARIOS:
            r = subprocess.run([exe, s], capture_output=True, text=True, env=env, timeout=60)
            if r.returncode != 0:
                failed.append(s)
                err = [l for l in r.stderr.splitlines() if "ERROR: AddressSanitizer" in l or re.match(r"\s+#[0-3] ", l)]
                print(f"FAIL {s}: exit {r.returncode}\n  " + "\n  ".join(err[:5] or r.stdout.strip().splitlines()))
            else:
                print("ok   " + "\n     ".join(r.stdout.strip().splitlines()))
    if failed:
        sys.exit(f"btvirt ({'unpatched' if unpatched else 'with the layer patches'}): {' '.join(failed)} failed")
    print(f"btvirt with the layer patches: {len(SCENARIOS)} scenarios ok")


if __name__ == "__main__":
    if shutil.which("patch") is None:
        sys.exit("btvirt-test.py needs patch")
    main()
