#!/usr/bin/env python3
"""Press the emulator's buttons and touch its screen when the selftest
asks for it. A request comes as a line of a console file of the asker's
own whose name ends in .ask (qemu-console.py; the button and touchscreen
steps, shell/ask_headless.go; charger-test.sh),

    PHONE-FILE <kind>-<id>.ask <n> <Adler-32 of the request> <request>

in one write, through the journal, and again, the same line, while the
asker has had no answer (no press, no finger yet); each name and n is
answered once. A kernel line printed inside one (selftest-heartbeat.sh's
every 30 s, any driver's) is taken out as qemu-console.py does, and the
mended request named; one whose sum holds neither so nor as it is, one
said again otherwise, one that asks for nothing known is not answered but
named with the machine's console and its line, and the exit status is 1.
A line that may be a request cut by a kernel line waits SETTLE seconds
for its rest.

"PHONE-SELFTEST-PRESS: <button> <n>" (the button step) is answered from
the QEMU human monitor; "PHONE-SELFTEST-PRESS: <button> <n> x<count>
<gap>ms" (button-burst) with count presses, one every gap milliseconds.
The virt machine's one button is its power button: system_powerdown
raises a line of its PL061 GPIO controller for 100 ms, which gpio-keys
reports as a short press of KEY_POWER (a press asked for while the line
is up only makes it longer).

"PHONE-SELFTEST-TOUCH: <n> tap <x> <y>", "... long <x> <y> <ms>" and
"... swipe <x0> <y0> <x1> <y1> <ms>" (the touchscreen step,
shell/touch_headless.go; points in ten-thousandths of the screen's width
and height) are answered through QMP, a finger on QEMU's
virtio-multitouch-pci (qemu_touch.py).

"PHONE-SELFTEST-PLUG: <n> <usb-kbd|usb-mouse> <on|off>" (the usb-plug
step, shell/usbplug_headless.go) plugs a USB keyboard or mouse into the
machine's xHCI or pulls it, through QMP's device_add and device_del.

A request is a line of the asker's own: at the start of the console's
line or after the journal's "[<seconds>] <name>[<pid>]: ". A kernel line
that quotes one (the heartbeat's "last:", selftest-heartbeat.sh) asks for
nothing: a gesture played again lands in a later step, which takes it for
its own.

Ends when the QEMU process does.

Usage: qemu-buttons.py <console-log> <monitor-socket> <qemu-pid> [<qmp-socket>]
"""
import importlib.util
import os
import re
import socket
import sys
import time

from qemu_touch import QMP, gesture, play

_spec = importlib.util.spec_from_file_location("qemu_console", os.path.join(os.path.dirname(os.path.abspath(__file__)), "qemu-console.py"))
console = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(console)

PRESS = re.compile(rb"PHONE-SELFTEST-PRESS: (\S+) (\d+)(?: x(\d+) (\d+)ms)?\Z")
TOUCH = re.compile(rb"PHONE-SELFTEST-TOUCH: (\d+) (tap|long|swipe)((?: \d+)+)\Z")
PLUG = re.compile(rb"PHONE-SELFTEST-PLUG: (\d+) (usb-kbd|usb-mouse) (on|off)\Z")
COMMANDS = {b"power": "system_powerdown"}
# The rest of a line cut by a kernel line follows that line at once; the
# host polls and the emulator lags behind on a busy machine.
SETTLE = 10


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


class Asks:
    """The requests on a machine's console as its lines come: each name and
    n once; say is told what was mended, asked again, or cannot be read
    (bad counts those)."""

    def __init__(self, log, say):
        self.log, self.say = log, say
        # The bytes read and the console's lines in them.
        self.pos = self.at = 0
        # (name, n): (the request, its console line)
        self.answered = {}
        # console line: when it was first seen maybe cut, its rest to come
        self.since = {}
        self.bad = 0

    def tell(self, at, msg):
        self.say(f"PHONE-SELFTEST-ASK: {self.log}: line {at + 1}: {msg}")

    def wrong(self, at, msg):
        self.bad += 1
        self.tell(at, msg)

    def new(self, final):
        """The requests to answer that came since the last call, in order,
        each answered before the console is read on: (request, console
        line). final: the console has ended, its last line whole without
        its newline."""
        try:
            with open(self.log, "rb") as f:
                f.seek(self.pos)
                raw = f.read()
        except FileNotFoundError:
            return
        if not final:
            raw = raw[:raw.rfind(b"\n") + 1]
        whole = raw.split(b"\n")
        lines = console.console_lines(raw)
        if whole[-1] == b"":
            whole.pop()
            lines.pop()
        now, took = time.monotonic(), 0
        for ev in console.read(lines):
            k = ev[1]
            if ev[0] in ("other", "damaged") and not final and self.unsettled(ev[0], lines[k], self.at + k, now):
                break
            if ev[0] == "other":
                took = k + 1
            elif ev[0] == "damaged":
                self.damaged(ev[2], self.at + k, lines[k])
                took = k + 1
            else:
                p, _, end, taken = ev
                if p[0] == "line" and console.asks(p[1]):
                    yield from self.asked(p, self.at + k, taken)
                took = end
        self.pos += sum(len(w) + 1 for w in whole[:took])
        self.at += took
        self.since = {at: t for at, t in self.since.items() if at >= self.at}

    def unsettled(self, kind, line, at, now):
        """A line not read as a sound one may be one cut by a kernel line
        whose rest has not come yet."""
        if kind == "other" and not any(console.starts(line[:m.start()]) for m in console.KERNEL.finditer(line, 1)):
            return False
        return now - self.since.setdefault(at, now) < SETTLE

    def asked(self, p, at, taken):
        _, name, n, _, text = p
        if taken:
            self.tell(at, f"{name} line {n} mended, {len(taken)} kernel line{'s' if len(taken) > 1 else ''} taken out of it: {console.show(taken[0])}")
        had = self.answered.get((name, n))
        if had is not None:
            if had[0] != text:
                self.wrong(at, f"{name} line {n} again, not as at line {had[1] + 1}: not answered: {console.show(text)}")
            else:
                self.tell(at, f"{name} line {n} asked again (answered at line {had[1] + 1})")
            return []
        self.answered[(name, n)] = (text, at)
        if not PRESS.match(text) and not TOUCH.match(text) and not PLUG.match(text):
            self.wrong(at, f"{name} line {n} asks for nothing known: {console.show(text)}")
            return []
        return [(text, at)]

    def damaged(self, seen, at, line):
        if seen[0] == "line" and console.asks(seen[1]):
            what = f"{seen[1]} line {seen[2]}"
        elif seen[0] is None and (b".ask " in line or b"PHONE-SELFTEST-" in line):
            what = "a request's line"
        else:
            return
        self.wrong(at, f"{what} damaged, its sum holds neither as it is nor with a kernel line taken out: not answered: {console.show(line)}")


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


def plug(qmp, n, driver, on):
    what, dev = f"{driver.decode()} {n.decode()}", f"selftest-{driver.decode()}"
    if qmp is None:
        print(f"PHONE-SELFTEST-PLUGGED: could not plug {what}: no QMP socket given", flush=True)
        return
    if on == b"on":
        done, did = qmp.cmd("device_add", {"driver": driver.decode(), "id": dev}), "plugged in"
    else:
        done, did = qmp.cmd("device_del", {"id": dev}), "pulled out"
    print(f"PHONE-SELFTEST-PLUGGED: {what} {did}" if done else f"PHONE-SELFTEST-PLUGGED: QMP at {qmp.path} refused {what} {on.decode()}", flush=True)


def main(argv):
    if len(argv) not in (4, 5):
        print(__doc__.strip(), file=sys.stderr)
        return 2
    log, mon, pid = argv[1], Monitor(argv[2]), int(argv[3])
    qmp = QMP(argv[4]) if len(argv) == 5 else None
    asks = Asks(log, lambda m: print(m, flush=True))
    while True:
        running = alive(pid)
        for text, _ in asks.new(not running):
            m = PRESS.match(text)
            if m:
                press(mon, *m.groups())
            elif PLUG.match(text):
                plug(qmp, *PLUG.match(text).groups())
            else:
                touch(qmp, *TOUCH.match(text).groups())
        if not running:
            return 1 if asks.bad else 0
        time.sleep(0.5)


if __name__ == "__main__":
    sys.exit(main(sys.argv))
