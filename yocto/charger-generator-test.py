#!/usr/bin/env python3
"""Test charger mode's detection (28-battery.md §9) without a phone.

Runs phone-charger-generator (meta-phone's phone-fw) as systemd would, its
three output directories given, with the kernel command line in
SYSTEMD_PROC_CMDLINE. The OnePlus 6T's bootloader appends
androidboot.mode=normal to a boot from the power key (report 007) and
androidboot.mode=charger to one its charger started (39-oneplus-6t.md
§16.9): only the latter makes phone-charger.target the default target and
masks Bluetooth's hardware activation; the emulator's command lines carry
no androidboot.mode and change nothing. Also checked: the units keep the
system out of charger mode (no multi-user.target, the radios' drivers
blacklisted before udev's coldplug, phone-qcom-btaddr skipped), and the
modules the blacklist names are modules in the 6T's kernel (built in, a
blacklist could not keep them off).

    charger-generator-test.py
"""
import os
import re
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
FW = os.path.join(HERE, "meta-phone", "recipes-phone", "phone-fw", "files")
QCOM = os.path.join(HERE, "meta-phone", "recipes-bsp", "phone-qcom", "files")
GENERATOR = os.path.join(FW, "phone-charger-generator")

# Kario's boot image line and what the 6T's ABL appended on 2026-11-24
# (report 007), shortened, without the serial number.
OWN = "root=PARTLABEL=userdata console=ttyMSM0,115200 phone.gui=1 vt.global_cursor_default=0"
ABL = ("androidboot.verifiedbootstate=orange androidboot.keymaster=1 root=PARTUUID=92049492-a5ea-df35-d8ed-7572de28983a "
       "androidboot.bootdevice=1d84000.ufshc androidboot.baseband=msm "
       "msm_drm.dsi_display0=dsi_samsung_s6e3fc2x01_cmd_display: androidboot.mode={mode} "
       "androidboot.project_codename=fajita androidboot.startupmode=pon1 androidboot.platform_name=SDM845")
QEMU = "root=/dev/vda rw  mem=2048M ip=dhcp console=ttyAMA0 swiotlb=0 phone.selftest=1 mac80211_hwsim.radios=3"

failures = []


def check(name, ok, detail=""):
    print(("ok   " if ok else "FAIL ") + name + (f": {detail}" if detail and not ok else ""))
    if not ok:
        failures.append(name)


def generate(cmdline, dirs=True):
    """Runs the generator; returns {name in the early directory: link target}
    and whatever it wrote elsewhere."""
    with tempfile.TemporaryDirectory() as tmp:
        out = [os.path.join(tmp, d) for d in ("normal", "early", "late")]
        env = dict(os.environ, SYSTEMD_PROC_CMDLINE=cmdline)
        r = subprocess.run(["sh", GENERATOR] + (out if dirs else []), env=env, capture_output=True, text=True)
        if r.returncode != 0:
            return None, r.stderr
        found = {}
        for root, _, files in os.walk(tmp):
            for f in files:
                p = os.path.join(root, f)
                found[os.path.relpath(p, tmp)] = os.readlink(p) if os.path.islink(p) else open(p).read()
        return found, ""


CHARGER = {"early/default.target": "/usr/lib/systemd/system/phone-charger.target", "early/bluetooth.target": "/dev/null"}

for name, cmdline, want in [
    ("6T power key boot", OWN + " " + ABL.format(mode="normal"), {}),
    ("6T charger boot", OWN + " " + ABL.format(mode="charger"), CHARGER),
    ("emulator selftest", QEMU, {}),
    ("emulator charger test", QEMU + " androidboot.mode=charger phone.charger-test=1 modules-load=test_power", CHARGER),
    ("the last androidboot.mode counts: normal", "androidboot.mode=charger x=1 androidboot.mode=normal", {}),
    ("the last androidboot.mode counts: charger", "androidboot.mode=normal androidboot.mode=charger", CHARGER),
    ("another mode", "androidboot.mode=chargerx", {}),
    ("another word", "xandroidboot.mode=charger phone.androidboot.mode=charger", {}),
    ("empty command line", "", {}),
]:
    got, err = generate(cmdline)
    check(name, got == want, f"{got} {err}".strip())

got, err = generate(OWN + " " + ABL.format(mode="charger"), dirs=False)
check("no output directories: nothing", got == {}, f"{got} {err}".strip())


def unit(path):
    """A unit file's settings: {key: [values]} over all sections, without comments."""
    keys = {}
    for line in open(path):
        line = line.strip()
        if not line or line.startswith("#") or line.startswith("["):
            continue
        k, _, v = line.partition("=")
        keys.setdefault(k.strip(), []).extend(v.split())
    return keys


target = unit(os.path.join(FW, "phone-charger.target"))
check("the target needs the basic system", target.get("Requires") == ["basic.target"] and "basic.target" in target.get("After", []))
check("the target pulls the charger's screen and the radios' blacklist",
      {"phone-charger.service", "phone-charger-radios.service"} <= set(target.get("Wants", [])))
for f in ("phone-charger.target", "phone-charger.service", "phone-charger-radios.service", "phone-charger-test.service"):
    u = unit(os.path.join(FW, f))
    pulled = [v for k in ("Requires", "Wants", "BindsTo", "Requisite", "Upholds") for v in u.get(k, [])]
    check(f"{f} pulls nothing of the system", not any(p in ("multi-user.target", "graphical.target", "phone-display.service") for p in pulled), str(pulled))
    check(f"{f} is not enabled for another target", "WantedBy" not in u and "RequiredBy" not in u)
radios = unit(os.path.join(FW, "phone-charger-radios.service"))
check("the blacklist is in place before udev's coldplug", "systemd-udev-trigger.service" in radios.get("Before", [])
      and radios.get("DefaultDependencies") == ["no"])
check("the test unit runs only on the emulator's test",
      unit(os.path.join(FW, "phone-charger-test.service")).get("ConditionKernelCommandLine") == ["phone.charger-test"])
check("phone-qcom-btaddr is skipped in charger mode",
      unit(os.path.join(QCOM, "phone-qcom-btaddr.service")).get("ConditionKernelCommandLine") == ["!androidboot.mode=charger"])

blacklisted = re.findall(r"(?m)^blacklist (\S+)$", open(os.path.join(QCOM, "charger-modprobe.conf")).read())
check("the modem and Wi-Fi are blacklisted", blacklisted == ["qcom_q6v5_mss", "ath10k_snoc"], str(blacklisted))
kconfig = open(os.path.join(HERE, "meta-phone", "recipes-kernel", "linux", "files", "phone-fajita.cfg")).read()
for module, option in (("qcom_q6v5_mss", "CONFIG_QCOM_Q6V5_MSS"), ("ath10k_snoc", "CONFIG_ATH10K_SNOC")):
    check(f"{module} is a module in the 6T's kernel", re.search(rf"(?m)^{option}=m$", kconfig) is not None)

if failures:
    print(f"{len(failures)} failed: {', '.join(failures)}")
    sys.exit(1)
print("charger-generator-test: all passed")
