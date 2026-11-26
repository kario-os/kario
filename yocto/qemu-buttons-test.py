#!/usr/bin/env python3
"""Test yocto/qemu-buttons.py against a made-up console, monitor and QMP
socket: each press request on the console is answered once, in order,
power with system_powerdown; a burst with its presses, the gap apart; a
button the emulator lacks is reported and not pressed; requests that come
while it runs are answered too, and it ends with the QEMU process. Each
touch request is answered once through QMP: a tap is a finger down in
slot 0 at the point (in QEMU's 0..32767) and up again, a swipe moves it
on the way, a long press holds it. A request another line quotes (the
heartbeat's last line of the run) asks for nothing.

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

HERE = os.path.dirname(os.path.abspath(__file__))
BUTTONS = os.path.join(HERE, "qemu-buttons.py")


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


def main():
    failures = []
    with tempfile.TemporaryDirectory() as tmp:
        log, mon, qsock, got, at, sent = os.path.join(tmp, "console.log"), os.path.join(tmp, "monitor"), os.path.join(tmp, "qmp"), [], [], []
        with open(log, "w") as f:
            f.write("[  80.1] phone-selftest.sh[400]: PHONE-SELFTEST-PRESS: power 1\n")
            f.write("[  81.0] phone-selftest.sh[400]: PHONE-SELFTEST-PRESS: home 2\n")
            f.write("[  81.5] phone-selftest.sh[400]: PHONE-SELFTEST-TOUCH: 1 tap 5000 9800\n")
            f.write("[  81.6] phone-heartbeat: up 81.60 s, MemAvailable: 1646608 kB, last: PHONE-SELFTEST-TOUCH: 1 tap 5000 9800\n")
        srv, qsrv = monitor(mon, got, at), qmp(qsock, sent)
        qemu = subprocess.Popen(["sleep", "5"])
        proc = subprocess.Popen([sys.executable, BUTTONS, log, mon, str(qemu.pid), qsock], stdout=subprocess.PIPE, text=True)
        time.sleep(1.5)
        with open(log, "a") as f:
            f.write("[  90.2] phone-selftest.sh[400]: PHONE-SELFTEST-PRESS: power 3\n")
            f.write("[  90.3] phone-heartbeat: up 90.30 s, MemAvailable: 1646608 kB, last: PHONE-SELFTEST-PRESS: power 3\n")
            f.write("[  91.0] phone-selftest.sh[400]: PHONE-SELFTEST-PRESS: power 4 x5 150ms\n")
            f.write("[  91.2] phone-selftest.sh[400]: PHONE-SELFTEST-TOUCH: 2 swipe 100 5000 4000 5000 300\n")
            f.write("[  91.3] phone-selftest.sh[400]: PHONE-SELFTEST-TOUCH: 3 long 10000 0 700\n")
        qemu.wait()
        try:
            out, _ = proc.communicate(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            out, _ = proc.communicate()
            failures.append("it did not end with the QEMU process")
        srv.close()
        qsrv.close()
        if got != ["system_powerdown"] * 7:
            failures.append(f"the monitor got {got}, want seven system_powerdown")
        gaps = [b - a for a, b in zip(at[2:], at[3:])]
        if len(gaps) != 4 or any(not 0.13 <= g <= 0.3 for g in gaps):
            failures.append(f"the burst's presses came {[round(g, 3) for g in gaps]} s apart, want 0.15")
        want = ["PHONE-SELFTEST-BUTTON: pressed power 1 (system_powerdown)",
                "PHONE-SELFTEST-BUTTON: the emulator has no home button",
                "PHONE-SELFTEST-TOUCH-DONE: touched tap 1 (2 reports through QMP)",
                "PHONE-SELFTEST-BUTTON: pressed power 3 (system_powerdown)",
                "PHONE-SELFTEST-BUTTON: pressed power 4 5 times 150 ms apart (system_powerdown)",
                "PHONE-SELFTEST-TOUCH-DONE: touched swipe 2 (12 reports through QMP)",
                "PHONE-SELFTEST-TOUCH-DONE: touched long 3 (2 reports through QMP)"]
        if out.splitlines() != want:
            failures.append(f"it printed {out.splitlines()}, want {want}")
        cmds = [m for _, m in sent]
        if not cmds or cmds[0] != {"execute": "qmp_capabilities"}:
            failures.append(f"QMP began with {cmds[:1]}, not qmp_capabilities")
        events = [m["arguments"]["events"] for m in cmds[1:] if m.get("execute") == "input-send-event"]
        data = lambda e: (e["data"]["type"], e["data"]["slot"], e["data"]["tracking-id"]) + ((e["data"]["axis"], e["data"]["value"]) if e["data"]["type"] == "data" else ())
        tap = [[data(e) for e in r] for r in events[:2]]
        if tap != [[("begin", 0, 1), ("data", 0, 1, "x", 16384), ("data", 0, 1, "y", 32112)], [("end", 0, -1)]]:
            failures.append(f"the tap was {tap}")
        swipe = [[data(e) for e in r] for r in events[2:14]]
        xs = [r[0][4] for r in swipe[1:11] if r and len(r[0]) == 5]
        if len(swipe) != 12 or swipe[0][0] != ("begin", 0, 2) or swipe[11] != [("end", 0, -1)] or xs != sorted(xs) or xs[-1:] != [13107]:
            failures.append(f"the swipe was {swipe}")
        held = [t for t, m in sent if m.get("execute") == "input-send-event"][14:16]
        if len(held) != 2 or not 0.65 <= held[1] - held[0] <= 1.0:
            failures.append(f"the long press was held {[round(b - a, 3) for a, b in zip(held, held[1:])]} s, want 0.7")
    for f in failures:
        print("FAIL:", f)
    print("qemu-buttons-test:", "FAIL" if failures else "PASS")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
