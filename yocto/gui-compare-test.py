#!/usr/bin/env python3
"""Test yocto/gui-compare.py on made-up screendumps: the same picture, one
off by gpu.rs's LEVELS (rounding) and the status bar's clock pass; a
blinking cursor, a picture named volatile, a difference within the box
named with it and a cursor besides are reported and pass; a difference out
of that box, a changed screen, a missing or a resized picture fail.

    gui-compare-test.py
"""
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "gui-compare.py")
W, H = 48, 80


def picture(path, w=W, h=H, paint=()):
    """A grey picture, with (x0, y0, x1, y1, delta) boxes made lighter."""
    px = bytearray([100] * (w * h * 3))
    for x0, y0, x1, y1, d in paint:
        for y in range(y0, y1):
            for x in range(x0, x1):
                i = (y * w + x) * 3
                px[i:i + 3] = bytes([100 + d] * 3)
    with open(path, "wb") as f:
        f.write(b"P6\n%d %d\n255\n" % (w, h) + px)


def compare(ref, other, *extra):
    p = subprocess.run([sys.executable, SCRIPT, ref, other, "--status", "8", *extra], capture_output=True, text=True)
    return p.returncode, p.stdout


def main():
    failures = []

    def check(what, cond, out):
        if not cond:
            failures.append(f"{what}:\n{out}")

    with tempfile.TemporaryDirectory() as tmp:
        ref, other = os.path.join(tmp, "cpu"), os.path.join(tmp, "gpu")
        os.mkdir(ref)
        os.mkdir(other)
        for name in ["01-idle", "02-menu", "06-sms", "17-scan-line", "18-push-00"]:
            picture(os.path.join(ref, name + ".ppm"))
        picture(os.path.join(other, "01-idle.ppm"), paint=[(0, 0, W, 8, 60), (10, 20, 30, 40, 2)])
        picture(os.path.join(other, "02-menu.ppm"))
        picture(os.path.join(other, "06-sms.ppm"), paint=[(20, 30, 23, 60, 90)])
        picture(os.path.join(other, "17-scan-line.ppm"), paint=[(0, 40, W, 44, 90)])
        rc, out = compare(ref, other, "--volatile", "17-scan-line")
        check("passes", rc == 0, out)
        check("the status bar is reported", "01-idle: the same; the status bar: 384 pixels" in out, out)
        check("the cursor is reported", "06-sms: 90 pixels differ in 20,30-23,60 (a cursor-sized box)" in out, out)
        check("volatile", "17-scan-line: 192 pixels differ in 0,40-48,44 (volatile)" in out, out)
        check("the slide's steps are skipped", "18-push" not in out, out)
        check("the sum", "PHONE-GUI-COMPARE: 4 of 4 as the reference" in out, out)

        rc, out = compare(ref, other)
        check("not volatile fails", rc == 1 and "17-scan-line: 192 pixels differ in 0,40-48,44 (FAIL)" in out, out)

        rc, out = compare(ref, other, "--volatile", "17-scan-line:0,36-48,48")
        check("within its box", rc == 0 and "17-scan-line: 192 pixels differ in 0,40-48,44 (within its volatile box)" in out, out)
        rc, out = compare(ref, other, "--volatile", "17-scan-line:0,36-48,42")
        check("out of its box fails", rc == 1 and "17-scan-line: 192 pixels differ in 0,40-48,44 (FAIL: 96 pixels out of its volatile box, in 0,42-48,44)" in out, out)

        picture(os.path.join(other, "06-sms.ppm"), paint=[(20, 30, 23, 60, 90), (32, 12, 44, 18, 90)])
        rc, out = compare(ref, other, "--volatile", "17-scan-line", "--volatile", "06-sms:30,10-46,20")
        check("a cursor and its box", rc == 0 and "06-sms: 162 pixels differ in 20,12-44,60 (a cursor-sized box, 20,30-23,60, and its volatile box)" in out, out)
        rc, out = compare(ref, other, "--volatile", "17-scan-line")
        check("a cursor and a change fail", rc == 1 and "06-sms: 162 pixels differ in 20,12-44,60 (FAIL)" in out, out)

        picture(os.path.join(other, "02-menu.ppm"), paint=[(5, 10, 40, 70, 3)])
        os.remove(os.path.join(other, "06-sms.ppm"))
        picture(os.path.join(other, "01-idle.ppm"), w=W + 2)
        rc, out = compare(ref, other, "--volatile", "17-scan-line")
        check("a changed screen fails", rc == 1 and "02-menu: 2100 pixels differ in 5,10-40,70 (FAIL)" in out, out)
        check("missing", "06-sms: missing" in out, out)
        check("resized", "01-idle: 48x80 against 50x80" in out, out)
        check("the failures named", "1 of 4 as the reference; differing: 01-idle 02-menu 06-sms" in out, out)
    for f in failures:
        print("FAIL:", f)
    print("gui-compare-test: " + ("ok" if not failures else f"{len(failures)} failed"))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
