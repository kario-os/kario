#!/usr/bin/env python3
"""Test phone-bringup's wake lock (meta-phone's phone-bringup) against a fake sysfs, without a phone.

On the OnePlus 6T (39-oneplus-6t.md §13.5, report 007) phone-bringup-awake
held the wake lock phone-bringup from boot on, so the phone never
suspended and its battery was empty after a night off the cable. Now the
lock is held only while a computer has the bring-up gadget configured.
Checked, with KARIO_SYS pointing the script at a tree that plays the
gadget's configfs directory, the controller's state and the power
supplies: which states hold it (configured; suspended while the cable
powers the phone) and which do not (a charger only, no cable, a
controller left "configured" with no USB power supply online, the gadget
unbound); that the watcher takes it with a timeout and renews it, lets it
go when the cable is pulled, takes it again for the next computer, and
lets it go when stopped; that the unit runs the watcher and the recipe
installs it where the unit looks.

    bringup-awake-test.py [files-dir]
"""
import os
import re
import signal
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
FILES = os.path.join(HERE, "meta-phone", "recipes-bsp", "phone-bringup", "files")
RECIPE = os.path.join(HERE, "meta-phone", "recipes-bsp", "phone-bringup", "phone-bringup_0.1.bb")
UDC = "a600000.usb"
POLL = 1


class Sys:
    """The parts of sysfs the watcher reads; the kernel's wake_lock and
    wake_unlock as files that keep the last write."""

    def __init__(self, root):
        self.root = root
        for d in ("kernel/config/usb_gadget/kario-bringup", f"class/udc/{UDC}", "power",
                  "class/power_supply/pmi8998-charger", "class/power_supply/bq27411-0"):
            os.makedirs(os.path.join(root, d), exist_ok=True)
        self.put("class/power_supply/pmi8998-charger/type", "USB")
        self.put("class/power_supply/bq27411-0/type", "Battery")
        self.put("class/power_supply/bq27411-0/online", "1")
        self.put("power/wake_lock", "")
        self.put("power/wake_unlock", "")
        self.cable(UDC, "not attached", 0)

    def put(self, name, value):
        with open(os.path.join(self.root, name), "w") as f:
            f.write(value + "\n" if value else "")

    def get(self, name):
        with open(os.path.join(self.root, name)) as f:
            return f.read().strip()

    def cable(self, bound, state, online):
        self.put("kernel/config/usb_gadget/kario-bringup/UDC", bound)
        self.put(f"class/udc/{UDC}/state", state)
        self.put("class/power_supply/pmi8998-charger/online", str(online))


def script(files):
    return os.path.join(files, "bringup-awake")


def check(files, root):
    r = subprocess.run(["sh", script(files), "check"], capture_output=True, text=True, timeout=10,
                       env={**os.environ, "KARIO_SYS": root})
    return r.returncode, r.stdout.strip()


def wait_for(cond, what, within=4 * POLL + 2):
    end = time.monotonic() + within
    while time.monotonic() < end:
        if cond():
            return
        time.sleep(0.05)
    raise AssertionError(f"not within {within} s: {what}")


def test_decisions(files):
    cases = [
        # (bound, state, charger online, no charger at all, held, words in the reason)
        (UDC, "configured", 1, False, True, "a computer has configured kario-bringup on " + UDC),
        (UDC, "configured", 0, False, False, "the cable is out"),
        (UDC, "suspended", 1, False, True, "the cable still powers the phone"),
        (UDC, "suspended", 0, False, False, "no USB power supply is online"),
        (UDC, "powered", 1, False, False, UDC + " is powered (no computer has configured it)"),
        (UDC, "default", 1, False, False, "no computer has configured it"),
        (UDC, "not attached", 0, False, False, UDC + " is not attached"),
        (UDC, "configured", 0, True, True, "a computer has configured"),
        (UDC, "suspended", 0, True, False, "no USB power supply is online"),
        ("", "configured", 1, False, False, "bound to no USB controller"),
    ]
    for bound, state, online, nocharger, held, words in cases:
        with tempfile.TemporaryDirectory() as root:
            s = Sys(root)
            s.cable(bound, state, online)
            if nocharger:
                os.unlink(os.path.join(root, "class/power_supply/pmi8998-charger/type"))
            rc, out = check(files, root)
            label = f"{state!r}, bound {bound!r}, charger {'none' if nocharger else online}"
            assert (rc == 0) == held, f"{label}: exit {rc}, {out!r}"
            assert out.startswith("held: " if held else "not held: "), f"{label}: {out!r}"
            assert words in out, f"{label}: {out!r} lacks {words!r}"
    with tempfile.TemporaryDirectory() as root:
        rc, out = check(files, root)
        assert rc == 1 and "bound to no USB controller" in out, f"no gadget at all: {rc} {out!r}"


def test_watcher(files):
    with tempfile.TemporaryDirectory() as root:
        s = Sys(root)
        p = subprocess.Popen(["sh", script(files)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                             env={**os.environ, "KARIO_SYS": root, "PHONE_BRINGUP_POLL": str(POLL)})
        try:
            # On its battery: let go at the start, never taken.
            wait_for(lambda: s.get("power/wake_unlock") == "phone-bringup", "released at the start")
            time.sleep(2.5 * POLL)
            assert s.get("power/wake_lock") == "", "taken with no cable: " + s.get("power/wake_lock")
            # A computer configures the gadget: taken, with a timeout of three polls.
            s.cable(UDC, "configured", 1)
            want = f"phone-bringup {3 * POLL * 1000000000}"
            wait_for(lambda: s.get("power/wake_lock") == want, "taken for the computer")
            # Renewed while it stays.
            s.put("power/wake_lock", "")
            wait_for(lambda: s.get("power/wake_lock") == want, "renewed")
            # The cable pulled, the controller still saying configured: let go.
            s.put("power/wake_unlock", "")
            s.cable(UDC, "configured", 0)
            wait_for(lambda: s.get("power/wake_unlock") == "phone-bringup", "released when the cable went")
            # A charger: not taken.
            s.put("power/wake_lock", "")
            s.cable(UDC, "powered", 1)
            time.sleep(2.5 * POLL)
            assert s.get("power/wake_lock") == "", "taken for a charger"
            # The next computer: taken again.
            s.cable(UDC, "configured", 1)
            wait_for(lambda: s.get("power/wake_lock") == want, "taken for the next computer")
            # Stopped: let go at once.
            s.put("power/wake_unlock", "")
            p.send_signal(signal.SIGTERM)
            assert p.wait(timeout=5) == 0, f"exit {p.returncode}"
            assert s.get("power/wake_unlock") == "phone-bringup", "kept when stopped"
        finally:
            if p.poll() is None:
                p.kill()
            out, err = p.communicate()
        assert err == "", err
        lines = out.splitlines()
        expect = ["not holding the wake lock phone-bringup, the phone may sleep: " + UDC + " is not attached",
                  "holding the wake lock phone-bringup: a computer has configured",
                  "not holding the wake lock phone-bringup, the phone may sleep: " + UDC + " says configured",
                  "holding the wake lock phone-bringup: a computer has configured"]
        assert len(lines) == len(expect), f"one line per change: {lines}"
        for line, start in zip(lines, expect):
            assert line.startswith("phone-bringup-awake: " + start), f"{line!r}, want {start!r}"


def test_unit_and_recipe(files):
    with open(os.path.join(files, "phone-bringup-awake.service")) as f:
        unit = f.read()
    with open(RECIPE) as f:
        recipe = f.read()
    start = re.findall(r"^ExecStart=(.*)$", unit, re.M)
    assert start == ["/usr/libexec/phone-bringup/awake"], f"ExecStart {start}: the watcher, not a lock for good"
    assert not re.search(r"^Type=oneshot", unit, re.M), "a oneshot unit holds the lock for good"
    assert re.search(r"^ExecStopPost=-/usr/libexec/phone-bringup/awake release$", unit, re.M), "stopped, it lets go"
    assert "install -m 0755 ${S}/bringup-awake ${D}${libexecdir}/phone-bringup/awake" in recipe
    assert "file://bringup-awake" in recipe


def main():
    files = sys.argv[1] if len(sys.argv) > 1 else FILES
    for t in (test_unit_and_recipe, test_decisions, test_watcher):
        t(files)
        print(f"ok {t.__name__}")


if __name__ == "__main__":
    main()
