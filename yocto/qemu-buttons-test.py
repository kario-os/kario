#!/usr/bin/env python3
"""Test yocto/qemu-buttons.py against made-up consoles, monitors and QMP
sockets. Each request (a line of an .ask console file, as the selftest's
steps say it through the journal and charger-test.sh with console-file)
is answered once, in order: power with system_powerdown, a burst with its
presses the gap apart, a button the emulator lacks reported and not
pressed; a touch through QMP, a tap a finger down in slot 0 at the point
(in QEMU's 0..32767) and up again, a swipe moving it on the way, a long
press holding it. Requests that come while it runs are answered too, and
it ends with the QEMU process. A request said again is answered once, one
another line quotes (the heartbeat's last line of the run) not at all; a
new asker (a Shell that took the script over) counts from 1 again.

A kernel line printed inside a request (selftest-heartbeat.sh's, as in
the night chain's d94, quoting the request) at its start, in its middle
and at its end, two in a row, in a press and in a touch, in two requests
one after the other: each answered as asked, the mended line named.
Before, such a request went unanswered or was answered with its numbers
cut short (one press for a burst, a tap elsewhere). A request whose rest
has not come yet, or that is still being written, waits for it. One
damaged past mending, said again otherwise or asking for nothing known:
not answered but named with the machine's console and its line, exit
status 1; its sound repeat answered.

    qemu-buttons-test.py
"""
import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
BUTTONS = os.path.join(HERE, "qemu-buttons.py")
CONSOLE_FILE = os.path.join(HERE, "meta-phone", "recipes-phone", "phone-fw", "files", "console-file")
JOURNAL = b"[   80.100000] selftest-boot.sh[400]: "
HEARTBEAT = b"[  329.315920] phone-heartbeat: up 329.21 s, MemAvailable: 1690748 kB, last: "
SYSRQ = b"[  331.002113] sysrq: Show Blocked State"
ASKER = "press-0a1b2c3d.ask"
TOUCHES = "touch-0a1b2c3d.ask"


def monitor(path, got, at):
    """A human monitor that keeps every command it is sent, and when."""
    srv = socket.socket(socket.AF_UNIX)
    srv.bind(path)
    srv.listen(1)

    def serve():
        conn, _ = srv.accept()
        conn.sendall(b"QEMU 10.0 monitor - type 'help' for more information\n(qemu) ")
        buf = b""
        while True:
            chunk = conn.recv(4096)
            if not chunk:
                return
            buf += chunk
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                got.append(line.decode())
                at.append(time.monotonic())
                conn.sendall(line + b"\r\n(qemu) ")

    threading.Thread(target=serve, daemon=True).start()
    return srv


def qmp(path, got):
    """A QMP server: its greeting, an event now and then, and an answer to
    every command, which it keeps (with when it came)."""
    srv = socket.socket(socket.AF_UNIX)
    srv.bind(path)
    srv.listen(1)

    def serve():
        conn, _ = srv.accept()
        conn.sendall(b'{"QMP": {"version": {"qemu": {"major": 10, "minor": 2, "micro": 0}}, "capabilities": []}}\n')
        buf = b""
        while True:
            chunk = conn.recv(4096)
            if not chunk:
                return
            buf += chunk
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                got.append((time.monotonic(), json.loads(line)))
                conn.sendall(b'{"event": "RTC_CHANGE", "data": {}}\n{"return": {}}\n')

    threading.Thread(target=serve, daemon=True).start()
    return srv


def frame(name, n, text):
    """A request as its asker says it: line n of its console file."""
    return b"PHONE-FILE %s %d %08x %s" % (name.encode(), n, zlib.adler32(text), text)


def press(n, rest=b"", button=b"power", asker=ASKER):
    return JOURNAL + frame(asker, n, b"PHONE-SELFTEST-%s: %s %d%s" % (b"PRESS", button, n, rest))


def touch(n, gesture):
    return JOURNAL + frame(TOUCHES, n, b"PHONE-SELFTEST-TOUCH: %d %s" % (n, gesture))


def quoting(line):
    """The heartbeat's line with the run's last line, as it is in its
    output (selftest.out), cut at 160 characters."""
    return HEARTBEAT + line[len(JOURNAL):][:160]


def splice(lines, which, at, kernel=None):
    """lines with the kernel's lines (the heartbeat quoting the line by
    default) printed at byte at of line which (negative: from its end),
    its rest on a line after them."""
    out = list(lines)
    line = out[which]
    kernel = kernel or (quoting(line),)
    k = at if at >= 0 else len(line) + at
    out[which:which + 1] = [line[:k] + kernel[0]] + list(kernel[1:]) + [line[k:]]
    return out


def start(line):
    """A few bytes into the line's own text, after the journal's prefix."""
    return len(JOURNAL) + 3


def inside(part, k):
    """Byte k of part in the request's text."""
    return lambda line: line.index(part, line.index(b"PHONE-SELFTEST-")) + k


def end(line):
    return len(line) - 2


def console(lines):
    return b"".join(line + b"\r\n" for line in lines)


def q(v):
    return round(v * 0x7FFF / 10000)


class Run:
    """qemu-buttons.py on vm3's console, a monitor and a QMP socket, while a
    made-up QEMU process lasts that many seconds."""

    def __init__(self, tmp, lines, seconds):
        d = tempfile.mkdtemp(dir=tmp)
        os.makedirs(os.path.join(d, "vm3"))
        self.log, mon, qsock = os.path.join(d, "vm3", "console.log"), os.path.join(d, "monitor"), os.path.join(d, "qmp")
        self.write(console(lines))
        self.got, self.at, self.sent = [], [], []
        self.srv, self.qsrv = monitor(mon, self.got, self.at), qmp(qsock, self.sent)
        self.qemu = subprocess.Popen(["sleep", str(seconds)])
        self.proc = subprocess.Popen([sys.executable, BUTTONS, self.log, mon, str(self.qemu.pid), qsock], stdout=subprocess.PIPE, text=True)
        self.late = False

    def write(self, data):
        with open(self.log, "ab") as f:
            f.write(data)

    def end(self):
        self.qemu.wait()
        try:
            out, _ = self.proc.communicate(timeout=15)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            out, _ = self.proc.communicate()
            self.late = True
        self.srv.close()
        self.qsrv.close()
        self.out, self.rc = out.splitlines(), self.proc.returncode
        return self

    def said(self, prefix):
        return [line for line in self.out if line.startswith(prefix)]

    def events(self):
        return [m["arguments"]["events"] for _, m in self.sent if m.get("execute") == "input-send-event"]

    def taps(self):
        """Where each gesture's finger came down, in QEMU's units."""
        return [(r[1]["data"]["value"], r[2]["data"]["value"]) for r in self.events() if r[0]["data"]["type"] == "begin"]


def main():
    failures = []

    def check(what, cond, out=""):
        if not cond:
            failures.append(f"{what}\n{out}")

    with tempfile.TemporaryDirectory() as tmp:
        # charger-test.sh asks with console-file itself.
        path = os.path.join(tmp, "asks")
        with open(path, "wb") as f:
            f.write(b"PHONE-SELFTEST-PRESS: power 1\n")
        charger = subprocess.run(["sh", CONSOLE_FILE, "charger-1f2e.ask", path, "1"], capture_output=True).stdout
        check("console-file says a request as the selftest's steps do",
              charger == frame("charger-1f2e.ask", 1, b"PHONE-SELFTEST-PRESS: power 1") + b"\n", charger)

        first = [press(1), press(2, button=b"home"), touch(1, b"tap 5000 9800")]
        first.append(quoting(first[-1]))
        r = Run(tmp, first, 5)
        time.sleep(1.5)
        p3 = press(3)
        r.write(console([p3, quoting(p3), p3, press(4, b" x5 150ms"), touch(2, b"swipe 100 5000 4000 5000 300"),
                         touch(3, b"long 10000 0 700"), press(1, asker="press-5e6f7a8b.ask"),
                         b"[  95.000000] charger-test.sh[300]: " + charger.rstrip(b"\n")]))
        r.end()
        check("it ended with the QEMU process", not r.late)
        check("all read: exit status 0", r.rc == 0, f"exit status {r.rc}")
        if r.got != ["system_powerdown"] * 9:
            failures.append(f"the monitor got {r.got}, want nine system_powerdown")
        gaps = [b - a for a, b in zip(r.at[2:6], r.at[3:7])]
        if len(gaps) != 4 or any(not 0.13 <= g <= 0.3 for g in gaps):
            failures.append(f"the burst's presses came {[round(g, 3) for g in gaps]} s apart, want 0.15")
        want = ["PHONE-SELFTEST-BUTTON: pressed power 1 (system_powerdown)",
                "PHONE-SELFTEST-BUTTON: the emulator has no home button",
                "PHONE-SELFTEST-TOUCH-DONE: touched tap 1 (2 reports through QMP)",
                "PHONE-SELFTEST-BUTTON: pressed power 3 (system_powerdown)",
                f"PHONE-SELFTEST-ASK: {r.log}: line 7: {ASKER} line 3 asked again (answered at line 5)",
                "PHONE-SELFTEST-BUTTON: pressed power 4 5 times 150 ms apart (system_powerdown)",
                "PHONE-SELFTEST-TOUCH-DONE: touched swipe 2 (12 reports through QMP)",
                "PHONE-SELFTEST-TOUCH-DONE: touched long 3 (2 reports through QMP)",
                "PHONE-SELFTEST-BUTTON: pressed power 1 (system_powerdown)",
                "PHONE-SELFTEST-BUTTON: pressed power 1 (system_powerdown)"]
        if r.out != want:
            failures.append(f"it printed {r.out}, want {want}")
        cmds = [m for _, m in r.sent]
        if not cmds or cmds[0] != {"execute": "qmp_capabilities"}:
            failures.append(f"QMP began with {cmds[:1]}, not qmp_capabilities")
        events = r.events()
        data = lambda e: (e["data"]["type"], e["data"]["slot"], e["data"]["tracking-id"]) + ((e["data"]["axis"], e["data"]["value"]) if e["data"]["type"] == "data" else ())
        tap = [[data(e) for e in ev] for ev in events[:2]]
        if tap != [[("begin", 0, 1), ("data", 0, 1, "x", 16384), ("data", 0, 1, "y", 32112)], [("end", 0, -1)]]:
            failures.append(f"the tap was {tap}")
        swipe = [[data(e) for e in ev] for ev in events[2:14]]
        xs = [ev[0][4] for ev in swipe[1:11] if ev and len(ev[0]) == 5]
        if len(swipe) != 12 or swipe[0][0] != ("begin", 0, 2) or swipe[11] != [("end", 0, -1)] or xs != sorted(xs) or xs[-1:] != [13107]:
            failures.append(f"the swipe was {swipe}")
        held = [t for t, m in r.sent if m.get("execute") == "input-send-event"][14:16]
        if len(held) != 2 or not 0.65 <= held[1] - held[0] <= 1.0:
            failures.append(f"the long press was held {[round(b - a, 3) for a, b in zip(held, held[1:])]} s, want 0.7")

        # The usb-plug step: a keyboard and a mouse plugged in and pulled
        # through QMP; a device it does not plug is not answered.
        plugs = "plug-0a1b2c3d.ask"
        asks = [JOURNAL + frame(plugs, i + 1, b"PHONE-SELFTEST-PLUG: %d %s" % (i + 1, a)) for i, a in enumerate(
            [b"usb-kbd on", b"usb-mouse on", b"usb-mouse off", b"usb-kbd off", b"usb-storage on"])]
        r = Run(tmp, asks, 3)
        r.end()
        got = [m for _, m in r.sent if m.get("execute") in ("device_add", "device_del")]
        check("usb-plug: device_add and device_del as asked", got == [
            {"execute": "device_add", "arguments": {"driver": "usb-kbd", "id": "selftest-usb-kbd"}},
            {"execute": "device_add", "arguments": {"driver": "usb-mouse", "id": "selftest-usb-mouse"}},
            {"execute": "device_del", "arguments": {"id": "selftest-usb-mouse"}},
            {"execute": "device_del", "arguments": {"id": "selftest-usb-kbd"}}], str(got))
        check("usb-plug: said what it did", r.said("PHONE-SELFTEST-PLUGGED:") == [
            "PHONE-SELFTEST-PLUGGED: usb-kbd 1 plugged in", "PHONE-SELFTEST-PLUGGED: usb-mouse 2 plugged in",
            "PHONE-SELFTEST-PLUGGED: usb-mouse 3 pulled out", "PHONE-SELFTEST-PLUGGED: usb-kbd 4 pulled out"], "\n".join(r.out))
        check("usb-plug: a device it does not plug is named and fails the run",
              r.rc == 1 and any("asks for nothing known" in l and "usb-storage" in l for l in r.out), "\n".join(r.out))

        # A kernel line inside requests, each its own case: the presses
        # bursts and the touches taps, so a number cut short shows.
        two = (None, SYSRQ)
        cases = [
            ("at the start of a press", press(5, b" x3 20ms"), start, None, 3),
            ("in the middle of a touch", touch(4, b"tap 1234 5678"), inside(b"1234", 2), None, (1234, 5678)),
            ("at the end of a press", press(6, b" x4 20ms"), end, None, 4),
            ("at the start of a touch", touch(5, b"tap 2500 7500"), start, None, (2500, 7500)),
            ("in the middle of a press", press(7, b" x2 20ms"), inside(b"power", 2), None, 2),
            ("at the end of a touch", touch(6, b"tap 4321 8765"), end, None, (4321, 8765)),
            ("two in a row in a press", press(8, b" x5 20ms"), inside(b"x5", 1), two, 5),
            ("two in a row in a touch", touch(7, b"tap 6000 3000"), inside(b"6000", 2), two, (6000, 3000)),
            ("one after another, the press", press(9, b" x2 20ms"), inside(b"x2", 1), None, 2),
            ("one after another, the touch", touch(8, b"tap 7000 2000"), inside(b"7000", 1), None, (7000, 2000)),
        ]
        lines, mended = [b"[   70.000000] selftest-boot.sh[400]: phone-shell selftest: 3 cases"], []
        for _, line, at, kernel, _ in cases:
            mended.append(len(lines))
            lines = splice(lines + [line], len(lines), at(line), kernel and (quoting(line),) + kernel[1:])
        r = Run(tmp, lines, 3).end()
        for (what, line, _, _, want), at in zip(cases, mended):
            if isinstance(want, int):
                n = int(line.split(b"power ")[1].split(b" ")[0])
                said = f"PHONE-SELFTEST-BUTTON: pressed power {n} {want} times 20 ms apart (system_powerdown)"
                check(f"a kernel line {what}: pressed as asked", said in r.out, "\n".join(r.out))
            else:
                check(f"a kernel line {what}: touched where asked", (q(want[0]), q(want[1])) in r.taps(), f"taps {r.taps()}\n" + "\n".join(r.out))
            named = f"PHONE-SELFTEST-ASK: {r.log}: line {at + 1}: "
            check(f"a kernel line {what}: the mended line named", any(s.startswith(named) and " mended, " in s for s in r.out), "\n".join(r.out))
        presses = sum(want for _, _, _, _, want in cases if isinstance(want, int))
        check("kernel lines inside requests: nothing else pressed", len(r.got) == presses, f"{len(r.got)} presses, want {presses}")
        check("kernel lines inside requests: nothing else touched", len(r.taps()) == 5, f"taps {r.taps()}")
        check("kernel lines inside requests: exit status 0", r.rc == 0, "\n".join(r.out))

        # Damaged past mending, said again otherwise, asking for nothing
        # known, cut with its rest gone: not answered, named; the sound
        # repeat of the damaged one answered.
        hurt = press(10, b" x2 20ms").replace(b"x2 20ms", b"x3 20ms")
        gone = touch(9, b"tap 1000 1000")
        lines = [hurt, press(10, b" x2 20ms"), press(11), press(11, b" x9 20ms"),
                 JOURNAL + frame(ASKER, 12, b"PHONE-SELFTEST-SHAKE: 3"), splice([gone], 0, inside(b"1000", 2)(gone))[0]]
        r = Run(tmp, lines, 2).end()
        wrong = r.said("PHONE-SELFTEST-ASK")
        check("a request that cannot be read: exit status 1", r.rc == 1, f"exit status {r.rc}")
        check("damaged past mending: not answered, its sound repeat answered",
              r.said("PHONE-SELFTEST-BUTTON") == ["PHONE-SELFTEST-BUTTON: pressed power 10 2 times 20 ms apart (system_powerdown)",
                                                  "PHONE-SELFTEST-BUTTON: pressed power 11 (system_powerdown)"] and len(r.got) == 3, "\n".join(r.out))
        for what, says in [("damaged past mending", f"{r.log}: line 1: {ASKER} line 10 damaged, its sum holds neither as it is nor with a kernel line taken out: not answered: "),
                           ("said again otherwise", f"{r.log}: line 4: {ASKER} line 11 again, not as at line 3: not answered: "),
                           ("asking for nothing known", f"{r.log}: line 5: {ASKER} line 12 asks for nothing known: PHONE-SELFTEST-SHAKE: 3"),
                           ("a kernel line inside, its rest gone", f"{r.log}: line 6: {TOUCHES} line 9 damaged")]:
            check(f"{what}: named with the machine's console and line", any(says in s for s in wrong), "\n".join(r.out))
        check("a request that cannot be read: nothing touched", r.taps() == [], f"taps {r.taps()}")

        # As the console grows: a request still being written (QEMU writes
        # the serial port's bytes as they come), one cut by a kernel line
        # whose rest comes later.
        r = Run(tmp, [b"[   70.000000] selftest-boot.sh[400]: phone-shell selftest: 1 case"], 6)
        time.sleep(1)
        slow = touch(10, b"tap 5000 9800")
        r.write(slow[:-2])
        time.sleep(1.5)
        r.write(slow[-2:] + b"\r\n")
        time.sleep(1)
        late = press(13, b" x3 20ms")
        k = inside(b"x3", 1)(late)
        r.write(late[:k] + quoting(late) + b"\r\n")
        time.sleep(1.5)
        r.write(late[k:] + b"\r\n")
        r.end()
        check("a request still being written: touched where asked, once", r.taps() == [(q(5000), q(9800))], f"taps {r.taps()}")
        check("a request whose rest came later: pressed as asked",
              r.said("PHONE-SELFTEST-BUTTON") == ["PHONE-SELFTEST-BUTTON: pressed power 13 3 times 20 ms apart (system_powerdown)"], "\n".join(r.out))
        check("as the console grows: nothing damaged, exit status 0", r.rc == 0 and not [s for s in r.out if "damaged" in s], "\n".join(r.out))
    for f in failures:
        print("FAIL:", f)
    print("qemu-buttons-test:", "FAIL" if failures else "PASS")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
