#!/usr/bin/env python3
"""Press the emulator's buttons and touch its screen when the selftest
asks for it: each new "PHONE-SELFTEST-PRESS: <button> <n>" line on the
console (the button step, shell/buttons_headless.go) is answered once from
the QEMU human monitor; "PHONE-SELFTEST-PRESS: <button> <n> x<count>
<gap>ms" (button-burst) with count presses, one every gap milliseconds.
The virt machine's one button is its power button: system_powerdown raises
a line of its PL061 GPIO controller for 100 ms, which gpio-keys reports as
a short press of KEY_POWER (a press asked for while the line is up only
makes it longer).

"PHONE-SELFTEST-TOUCH: <n> tap <x> <y>", "... long <x> <y> <ms>" and
"... swipe <x0> <y0> <x1> <y1> <ms>" (the touchscreen step,
shell/touch_headless.go; points in ten-thousandths of the screen's width
and height) are answered once through QMP, a finger on QEMU's
virtio-multitouch-pci (qemu_touch.py).

A request is a line of the selftest's own: it starts the line, after the
kernel's timestamp and the journal's name[pid]. A line that quotes one
(the heartbeat's "last:", selftest-heartbeat.sh) asks for nothing: a
gesture played again lands in a later step, which takes it for its own.

Ends when the QEMU process does.

Usage: qemu-buttons.py <console-log> <monitor-socket> <qemu-pid> [<qmp-socket>]
"""
import os
import re
import socket
import sys
import time

from qemu_touch import QMP, gesture, play

OWN = rb"(?m)^(?:\[[ \d.]*\] )?(?:[^\s\[\]:]+\[\d+\]: )?"
PRESS = re.compile(OWN + rb"PHONE-SELFTEST-PRESS: (\S+) (\d+)(?: x(\d+) (\d+)ms)?")
TOUCH = re.compile(OWN + rb"PHONE-SELFTEST-TOUCH: (\d+) (tap|long|swipe)((?: \d+)+)")
COMMANDS = {b"power": "system_powerdown"}


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        pass
    return True


class Monitor:
    def __init__(self, path):
        self.path, self.s = path, None

    def cmd(self, line):
        """Sends a command, connecting first (the socket appears once QEMU
        has started); False when there is no monitor to send it to."""
        for _ in range(60):
            if self.s is None:
                try:
                    s = socket.socket(socket.AF_UNIX)
                    s.settimeout(5)
                    s.connect(self.path)
                    self.s = s
                    self.read()
                except OSError:
                    self.s = None
                    time.sleep(1)
                    continue
            try:
                self.s.sendall(line.encode() + b"\n")
                self.read()
                return True
            except OSError:
                self.s = None
        return False

    def read(self):
        buf = b""
        try:
            while not buf.endswith(b"(qemu) "):
                chunk = self.s.recv(4096)
                if not chunk:
                    break
                buf += chunk
        except socket.timeout:
            pass
        return buf


def requests(log, pattern):
    """The requests of pattern on the console so far, in order."""
    try:
        with open(log, "rb") as f:
            return pattern.findall(f.read())
    except FileNotFoundError:
        return []


def press(mon, name, n, count, gap):
    what = f"{name.decode(errors='replace')} {n.decode()}"
    times, gap = int(count or 1), int(gap or 0) / 1000
    cmd = COMMANDS.get(name)
    if cmd is None:
        print(f"PHONE-SELFTEST-BUTTON: the emulator has no {what.split()[0]} button", flush=True)
        return
    pressed, start = 0, time.monotonic()
    for i in range(times):
        time.sleep(max(0, start + i * gap - time.monotonic()))
        if not mon.cmd(cmd):
            break
        pressed += 1
    if pressed == times:
        many = f" {times} times {round(gap * 1000)} ms apart" if times > 1 else ""
        print(f"PHONE-SELFTEST-BUTTON: pressed {what}{many} ({cmd})", flush=True)
    else:
        print(f"PHONE-SELFTEST-BUTTON: could not press {what}: no monitor at {mon.path}", flush=True)


def touch(qmp, n, kind, nums):
    what = f"{kind.decode()} {n.decode()}"
    reports = gesture(kind.decode(), [int(v) for v in nums.split()], int(n))
    if reports is None:
        print(f"PHONE-SELFTEST-TOUCH-DONE: cannot make {what} of {nums.decode().strip()}", flush=True)
        return
    if qmp is None:
        print(f"PHONE-SELFTEST-TOUCH-DONE: could not touch {what}: no QMP socket given", flush=True)
        return
    if not play(qmp, reports):
        print(f"PHONE-SELFTEST-TOUCH-DONE: could not touch {what}: QMP at {qmp.path} refused it", flush=True)
        return
    print(f"PHONE-SELFTEST-TOUCH-DONE: touched {what} ({len(reports)} reports through QMP)", flush=True)


def main(argv):
    if len(argv) not in (4, 5):
        print(__doc__.strip(), file=sys.stderr)
        return 2
    log, mon, pid = argv[1], Monitor(argv[2]), int(argv[3])
    qmp = QMP(argv[4]) if len(argv) == 5 else None
    pressed = touched = 0
    while True:
        running = alive(pid)
        asked = requests(log, PRESS)
        for name, n, count, gap in asked[pressed:]:
            press(mon, name, n, count, gap)
        pressed = len(asked)
        asked = requests(log, TOUCH)
        for n, kind, nums in asked[touched:]:
            touch(qmp, n, kind, nums)
        touched = len(asked)
        if not running:
            return 0
        time.sleep(0.5)


if __name__ == "__main__":
    sys.exit(main(sys.argv))
