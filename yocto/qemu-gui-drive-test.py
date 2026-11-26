#!/usr/bin/env python3
"""Test yocto/qemu-gui-drive.py's waiting for a slow renderer's screen to
settle (PHONE_GUI_WAIT above 1) on a made-up console and screendumps, on a
clock of its own: frames of half a minute (softpipe under TCG); a still
screen, a blinking cursor and a clock left out in its box settle after four
frames, changes push that on, a clock not left out runs into the cap, a
screen that changes all over waits ten frames; PHONE_GUI_WAIT 1 waits the
given time. The status bar's rows change all along and never count.

    qemu-gui-drive-test.py
"""
import os
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
DRIVE = os.path.join(HERE, "qemu-gui-drive.py")
W, H = 8, 48
BOX = (2, 36, 6, 44)


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
    g = {"__name__": "qemu_gui_drive"}
    try:
        exec(compile(head, DRIVE, "exec"), g)
    finally:
        sys.argv = argv
    clock = Clock()
    g["time"], g["m"] = clock, Display(clock)
    return g


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
    for f in failures:
        print("FAIL:", f)
    print("qemu-gui-drive-test: " + ("ok" if not failures else f"{len(failures)} failed"))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
