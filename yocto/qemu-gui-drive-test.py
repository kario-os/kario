#!/usr/bin/env python3
"""Test yocto/qemu-gui-drive.py's waiting for a slow renderer's screen to
settle (PHONE_GUI_WAIT above 1) on a made-up console and screendumps, on a
clock of its own: frames of half a minute (softpipe under TCG); a still
screen, a blinking cursor and a clock left out in its box settle after four
frames, changes push that on, a clock not left out runs into the cap, a
screen that changes all over waits ten frames; PHONE_GUI_WAIT 1 waits the
given time. The status bar's rows change all along and never count.

Also its reading of the console: the shell's and the renderer's lines as
gui-console relays them (the image's console-file, phone.gui-console,
which its generator turns on), with kernel lines printed inside
PHONE-GUI-READY, a PHONE-GUI-INPUT line, a power key line and a
PHONE_PAINT_STEPS line, are read whole and the mended ones named
(PHONE-GUI-CONSOLE); a line damaged otherwise or missing is named and
fails the run; an older image's plain lines are read as they are.

    qemu-gui-drive-test.py
"""
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
DRIVE = os.path.join(HERE, "qemu-gui-drive.py")
FW = os.path.join(HERE, "meta-phone", "recipes-phone", "phone-fw", "files")
W, H = 8, 48
BOX = (2, 36, 6, 44)
# What phone-display's shell and renderer said (the journal's messages,
# as gui-console takes them), from a run on 2026-11-25.
SAID = [
    b"PHONE-GUI-READY bar-t9 phone-paint 0.1.0 \xc2\xb7 Apache-2.0 [kms keys images patch anim layout power mouse pointer phase rate touch shm sandboxed landlock seccomp]",
    b"PHONE-GUI-INPUT mouse added QEMU QEMU USB Tablet",
    b"PHONE-GUI-INPUT touchscreen added QEMU Virtio MultiTouch",
    b'{"draw":[147.14849853515625],"flips":[1,0],"gap":[],"present":[59.19776153564453],"transition":[]}',
    b"phone-paint: power key down",
    b"phone-paint: power key up: short press (99 ms)",
    b"phone-shell: power key: from the renderer: short press (held 99 ms)",
    b"phone-shell: screen off (power key)",
    b"phone-shell: power key: short press (held 99 ms): screen off (the panel on; not asked to turn it off yet: the backlight has not faded out)",
    b"PHONE-GUI-INPUT mouse added QEMU QEMU USB Mouse",
    b'{"draw":[5.43993616104126],"flips":[2,1],"gap":[],"present":[52.496177673339844],"transition":[3.1,2.9]}',
    b"phone-paint: motion: 2 draws (p50 5.4 ms, p95 147.1 ms); so far: 2 draws, 2 transition steps, 2 page flips, 1 completed",
]
POWER = [l.decode() for l in SAID if b"power key" in l or b"screen off" in l]
BOOT = [b"[    0.000000] Booting Linux on physical CPU 0x0000000000 [0x000f0510]",
        b"\x1b[0;32m  OK  \x1b[0m] Started \x1b[0;1;39mphone-gui-console.service\x1b[0m."]
USB = b"[  412.118203] usb 1-1: new full-speed USB device number 3 using xhci-hcd"


class Clock:
    def __init__(self):
        self.now = 1000.0

    def time(self):
        return self.now

    def sleep(self, s):
        self.now += s


class Display:
    """The monitor: screendump writes the picture fn gives for the time
    since start, (outside BOX, inside it), the status bar's rows the time."""

    def __init__(self, clock):
        self.clock, self.start, self.fn = clock, clock.now, lambda t: (0, 0)

    def cmd(self, line):
        parts = line.split()
        if parts[0] == "screendump":
            outside, inside = self.fn(self.clock.now - self.start)
            px = bytearray([outside % 256] * (W * H * 3))
            px[:W * 32 * 3] = bytes([int(self.clock.now) % 256]) * (W * 32 * 3)
            x0, y0, x1, y1 = BOX
            for y in range(y0, y1):
                px[(y * W + x0) * 3:(y * W + x1) * 3] = bytes([inside % 256]) * ((x1 - x0) * 3)
            with open(parts[1], "wb") as f:
                f.write(b"P6\n%d %d\n255\n" % (W, H) + bytes(px))
        return b"(qemu) "


def load(tmp):
    """The drive script's functions, before it waits for the boot."""
    log = os.path.join(tmp, "console.log")
    with open(log, "w") as f:
        f.write('[   40.1] phone-paint: {"draw":[3.1],"flips":[9,9],"gap":[],"present":[29000.0],"transition":[]}\n')
        f.write('[   70.2] phone-paint: {"draw":[2.0],"flips":[10,10],"gap":[],"present":[29998.0],"transition":[]}\n')
    with open(DRIVE, encoding="utf-8") as f:
        src = f.read()
    head = src[:src.index('if not wait_for("PHONE-GUI-READY"')]
    argv, sys.argv = sys.argv, ["qemu-gui-drive.py", log, os.path.join(tmp, "monitor"), tmp]
    os.environ["PHONE_GUI_WAIT"] = "10"
    g = {"__name__": "qemu_gui_drive", "__file__": DRIVE}
    try:
        exec(compile(head, DRIVE, "exec"), g)
    finally:
        sys.argv = argv
    clock = Clock()
    g["time"], g["m"] = clock, Display(clock)
    return g


def relayed(tmp, said):
    """said as gui-console prints it: console-file's lines of a file
    growing as they come, no end."""
    path = os.path.join(tmp, "said")
    with open(path, "wb") as f:
        f.write(b"".join(l + b"\n" for l in said))
    p = subprocess.run(["sh", os.path.join(FW, "console-file"), "gui-8c0f0b5e.log", path, "1"], capture_output=True, check=True)
    return p.stdout.split(b"\n")[:-1]


def splice(lines, which, at):
    """lines with a kernel line printed at byte at of line which, its rest
    on the line after it."""
    out = list(lines)
    out[which:which + 1] = [out[which][:at] + USB, out[which][at:]]
    return out


def reading(g, tmp, failures):
    """The drive reading consoles."""
    log = g["log"] = os.path.join(tmp, "relay.log")
    out = os.path.join(tmp, "drive.txt")

    def say(line):
        with open(out, "a") as f:
            f.write(line + "\n")
    g["say"] = say

    def read(what, lines, report, says=(), said=SAID):
        with open(log, "wb") as f:
            f.write(b"".join(l + b"\r\n" for l in lines))
        if os.path.exists(out):
            os.remove(out)
        got = {
            "ready": g["wait_for"]("PHONE-GUI-READY", 5),
            "mice": g["count"]("PHONE-GUI-INPUT mouse added"),
            "touchscreen": g["wait_for"]("PHONE-GUI-INPUT touchscreen added", 5),
            "power": g["power_lines"](),
            "bursts": len(g["bursts"]()),
            "flips": g["flips"](),
            "sums": g["motion_sums"](),
            "report": g["console_report"](),
        }
        want = {
            "ready": SAID[0] in said, "mice": sum(l.startswith(b"PHONE-GUI-INPUT mouse added") for l in said),
            "touchscreen": SAID[2] in said, "power": [p for p in POWER if p.encode() in said],
            "bursts": sum(l.startswith(b'{"draw"') for l in said), "flips": (2, 1) if SAID[10] in said else (1, 0),
            "sums": "2 draws, 2 transition steps, 2 page flips, 1 completed", "report": report,
        }
        for k in want:
            if got[k] != want[k]:
                failures.append(f"console, {what}: {k}: {got[k]!r}, not {want[k]!r}")
        with open(out) as f:
            told = f.read()
        for s in says:
            if s not in told:
                failures.append(f"console, {what}: the report does not say {s!r}:\n{told}")

    relay = BOOT + relayed(tmp, SAID)
    read("relayed", relay, True, ["PHONE-GUI-CONSOLE: gui-8c0f0b5e.log: 12 lines, 0 mended, 0 wrong"])
    cut = relay
    for n, at in ((1, 12), (2, 20), (9, 30), (11, 25)):
        i = next(i for i, l in enumerate(cut) if l.startswith(b"PHONE-FILE gui-8c0f0b5e.log %d " % n))
        cut = splice(cut, i, cut[i].index(SAID[n - 1][:12]) + at)
    read("kernel lines inside the ready, a mouse, a power key and a steps line (the old drive missed them)", cut, True,
         ["12 lines, 4 mended, 0 wrong", "gui-8c0f0b5e.log line 1 mended, 1 kernel line taken out of it: [  412.118203] usb 1-1"])
    bad = [l.replace(b"short press (held 99 ms): screen", b"short press (held 98 ms): screen") for l in relay]
    read("a power key line damaged otherwise", bad, False, ["gui-8c0f0b5e.log line 9 damaged"], SAID[:8] + SAID[9:])
    gone = [l for l in relay if not l.startswith(b"PHONE-FILE gui-8c0f0b5e.log 10 ")]
    read("a line missing", gone, False, ["gui-8c0f0b5e.log: line 10 missing"], SAID[:9] + SAID[10:])
    old = BOOT + [l if l.startswith(b"{") else b"[  360.349428] phone-shell[446]: " + l for l in SAID]
    read("an older image's plain lines", old, True, ["PHONE-GUI-CONSOLE: no gui-console file"])


def generator(failures):
    """phone-gui-console-generator with and without phone.gui-console."""
    cmdline = "root=/dev/vda rw console=ttyAMA0 phone.gui=1 phone.gui-console systemd.setenv=PHONE_PAINT_STEPS=/dev/console"
    with open(os.path.join(FW, "gui-console")) as f:
        steps = next(l.split("=", 1)[1].strip() for l in f if l.startswith("steps="))
    for line, on in ((cmdline, True), (cmdline.replace(" phone.gui-console", ""), False)):
        with tempfile.TemporaryDirectory() as tmp:
            out = [os.path.join(tmp, d) for d in ("normal", "early", "late")]
            r = subprocess.run(["sh", os.path.join(FW, "phone-gui-console-generator")] + out,
                               env=dict(os.environ, SYSTEMD_PROC_CMDLINE=line), capture_output=True, text=True)
            found = sorted(os.path.relpath(os.path.join(root, f), tmp) for root, _, fs in os.walk(tmp) for f in fs)
            want = ["normal/multi-user.target.wants/phone-gui-console.service", "normal/phone-display.service.d/gui-console.conf"] if on else []
            if r.returncode != 0 or found != want:
                failures.append(f"generator {'with' if on else 'without'} phone.gui-console: exit {r.returncode}, wrote {found}, not {want}")
                continue
            if not on:
                continue
            link = os.readlink(os.path.join(tmp, want[0]))
            with open(os.path.join(tmp, want[1])) as f:
                conf = f.read().split("\n")
            if link != "/usr/lib/systemd/system/phone-gui-console.service" or f"Environment=PHONE_PAINT_STEPS={steps}" not in conf \
                    or "StandardOutput=journal" not in conf or "StandardError=journal" not in conf:
                failures.append(f"generator: {link}, {conf}")
    with open(os.path.join(HERE, "qemu-gui.sh")) as f:
        if "bootparams=\"phone.gui=1 phone.gui-console " not in f.read():
            failures.append("qemu-gui.sh does not boot with phone.gui-console")


def main():
    failures = []
    with tempfile.TemporaryDirectory() as tmp:
        g = load(tmp)
        clock, display = g["time"], g["m"]

        def took(what, fn, want, seconds=6, moving=()):
            display.fn, display.start = fn, clock.now
            g["settle"](seconds, moving)
            got = clock.now - display.start
            if got != want:
                failures.append(f"{what}: settled after {got:.0f} s, not {want} s")

        if g["frame_time"](g["bursts"]()) != 30:
            failures.append(f"the frame: {g['frame_time'](g['bursts']())} s, not 30 s")
        # A frame of 30 s: four frames and 10 s quiet (130 s), a sample every 5 s;
        # the cap, 20 frames and ten minutes (1200 s).
        took("still", lambda t: (1, 1), 130)
        took("a blinking cursor", lambda t: (1 + int(t / 30) % 2, 1), 130)
        took("changing until 180 s", lambda t: (10 + min(int(t / 30), 6), 1), 315)
        took("one change at 100 s", lambda t: (1 if t < 100 else 2, 1), 235)
        took("a clock in its box", lambda t: (1, int(t)), 130, moving=BOX)
        took("a clock in its box, the rest changing until 180 s", lambda t: (10 + min(int(t / 30), 6), int(t)), 315, moving=BOX)
        took("a clock not left out", lambda t: (1, int(t)), 1200)
        took("changing all over", lambda t: (int(t), int(t)), 310, moving=None)
        g["WAIT"] = 1
        took("PHONE_GUI_WAIT 1", lambda t: (int(t), int(t)), 6)
        if os.path.exists(os.path.join(tmp, "settle.tmp")):
            failures.append("settle.tmp left behind")
        reading(g, tmp, failures)
    generator(failures)
    for f in failures:
        print("FAIL:", f)
    print("qemu-gui-drive-test: " + ("ok" if not failures else f"{len(failures)} failed"))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
