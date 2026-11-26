#!/usr/bin/env python3
"""Extract the PNGs a QEMU boot printed on the serial console (base64 between
PHONE-PNG-BEGIN / PHONE-PNG-END, see phone-shell --print-png). Only lines from
the selftest service are read, so interleaved console output does not matter.

Usage: extract-png.py <console-log> <out-dir>
"""
import base64
import os
import re
import sys

log, out = sys.argv[1:3]
os.makedirs(out, exist_ok=True)
name, chunks = None, []
for line in open(log, "rb").read().decode("utf-8", "replace").splitlines():
    m = re.search(r"selftest-boot\.sh\[\d+\]: (.*?)\s*$", line)
    if not m:
        continue
    text = m.group(1)
    if text.startswith("PHONE-PNG-BEGIN "):
        name, chunks = text.split()[1], []
    elif text.startswith("PHONE-PNG-END ") and name:
        with open(os.path.join(out, name + ".png"), "wb") as f:
            f.write(base64.b64decode("".join(chunks)))
        print("device pixel snapshot:", name)
        name = None
    elif name and re.fullmatch(r"[A-Za-z0-9+/=]+", text):
        chunks.append(text)
