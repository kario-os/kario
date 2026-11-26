#!/usr/bin/env python3
"""Test what the OnePlus 6T's screen shows while it boots (39-oneplus-6t.md
§16.13), without a phone or bitbake.

Expands the machine's boot image command lines (the userdata and the A/B
layout's) the way bitbake would, with PHONE_BOOT_LOG unset and set to "1"
(yocto/kas/boot-log.yml): by default no console=tty0 (fbcon would paint
the kernel's and systemd's messages on the panel) and no cursor; with the
boot log, console=tty0 back; either way the serial console stays and
nothing quietens the log. Also checked: every Kario kernel carries
phone-console.cfg's deferred fbcon takeover (without it fbcon blanks the
bootloader's picture as soon as a frame buffer appears), and no other
machine file puts console=tty0 on its command line.

    boot-console-test.py
"""
import glob
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
MACHINES = os.path.join(HERE, "meta-phone", "conf", "machine")
LINUX = os.path.join(HERE, "meta-phone", "recipes-kernel", "linux")

failures = []


def check(name, ok, detail=""):
    print(("ok   " if ok else "FAIL ") + name + (f": {detail}" if detail and not ok else ""))
    if not ok:
        failures.append(name)


def assignments(path):
    """The machine file's NAME = "value" and NAME ??= "value" lines, the
    later of the two kinds only where NAME is not set otherwise."""
    hard, weak = {}, {}
    with open(path) as f:
        for line in f:
            m = re.match(r'^([A-Z_]+)\s*(\?\?=|=)\s*"(.*)"\s*$', line)
            if m:
                (weak if m.group(2) == "??=" else hard)[m.group(1)] = m.group(3)
    return {**weak, **hard}


class Data:
    def __init__(self, values):
        self.values = values

    def getVar(self, name):
        return self.values.get(name)


def expand(value, values, depth=0):
    """${NAME} and ${@python} as bitbake expands them, for what this file uses."""
    if depth > 10:
        raise ValueError("recursive expansion: " + value)

    def one(m):
        body = m.group(1)
        if body.startswith("@"):
            return str(eval(body[1:], {"d": Data(values)}))
        return expand(values.get(body, m.group(0)), values, depth + 1)

    return re.sub(r"\$\{(@[^}]*|[A-Z_]+)\}", one, value)


def cmdlines(boot_log):
    values = assignments(os.path.join(MACHINES, "oneplus-fajita.conf"))
    if boot_log is not None:
        values["PHONE_BOOT_LOG"] = boot_log
    return {name: expand(values.get(name, ""), values).split() for name in ("PHONE_BOOTIMG_CMDLINE", "PHONE_ABBOOT_CMDLINE")}


for name, words in cmdlines(None).items():
    check(f"{name} by default: no console=tty0", "console=tty0" not in words, " ".join(words))
    check(f"{name} by default: no cursor", "vt.global_cursor_default=0" in words, " ".join(words))
    check(f"{name} by default: the serial console", "console=ttyMSM0,115200" in words, " ".join(words))
    check(f"{name} by default: phone.gui=1", "phone.gui=1" in words, " ".join(words))
    check(f"{name} by default: the log not quietened", not {"quiet", "loglevel=0", "loglevel=1"} & set(words), " ".join(words))
for name, words in cmdlines("1").items():
    check(f"{name} with PHONE_BOOT_LOG = 1: console=tty0", "console=tty0" in words, " ".join(words))
    check(f"{name} with PHONE_BOOT_LOG = 1: the serial console", "console=ttyMSM0,115200" in words, " ".join(words))

with open(os.path.join(HERE, "kas", "boot-log.yml")) as f:
    overlay = f.read()
check("boot-log.yml sets PHONE_BOOT_LOG = \"1\"", re.search(r'^\s+PHONE_BOOT_LOG = "1"\s*$', overlay, re.M) is not None)

try:
    with open(os.path.join(LINUX, "files", "phone-console.cfg")) as f:
        cfg = f.read().splitlines()
except FileNotFoundError:
    cfg = []
check("phone-console.cfg defers fbcon's takeover", "CONFIG_FRAMEBUFFER_CONSOLE_DEFERRED_TAKEOVER=y" in cfg)
with open(os.path.join(LINUX, "phone-kernel.inc")) as f:
    inc = f.read()
check("phone-kernel.inc adds phone-console.cfg", re.search(r'^SRC_URI \+= "file://phone-console\.cfg"$', inc, re.M) is not None)
check("phone-kernel.inc checks the kernel's configuration for it", "addtask phone_console_kconfig" in inc)
for recipe in sorted(glob.glob(os.path.join(LINUX, "linux-*.bb*"))):
    with open(recipe) as f:
        text = f.read()
    check(f"{os.path.basename(recipe)} requires phone-kernel.inc", "require phone-kernel.inc" in text)

for machine in sorted(glob.glob(os.path.join(MACHINES, "*.conf"))):
    with open(machine) as f:
        code = [line for line in f if not line.lstrip().startswith("#")]
    hit = [line.strip() for line in code if re.search(r"""console=tty0['" ]""", line) and "PHONE_BOOT_LOG" not in line]
    check(f"{os.path.basename(machine)}: console=tty0 only behind PHONE_BOOT_LOG", not hit, "; ".join(hit))

if failures:
    print(f"boot-console-test: {len(failures)} failed")
    sys.exit(1)
print("boot-console-test: all passed")
