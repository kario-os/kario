#!/usr/bin/env python3
"""Drive a phone.gui=1 QEMU boot through the human monitor: wait until the shell
reports PHONE-GUI-READY on the console, press keys on the emulated USB
keyboard (they reach the renderer through evdev), and screendump the
virtio-gpu scanout after each step; then plug a USB mouse and keyboard in
and out (PHONE-GUI-INPUT on the console) and check the mouse's click and
wheel change the screen.

Usage: qemu-gui-drive.py <console-log> <monitor-socket> <out-dir>
"""
import os
import socket
import sys
import time

log, mon_path, out = sys.argv[1:4]

STEPS = [
    ("01-idle", []),
    ("02-menu", ["ret"]),
    ("03-dialer", ["ret"]),
    ("04-options", ["tab"]),
    ("05-calc", ["esc", "f4", "ret", "7", "1", "2", "asterisk", "3", "0", "ret"]),
    ("06-sms", ["f4", "ret", "2"]),
    ("07-dial-from-idle", ["f4", "1", "0", "0", "8", "6", "ret"]),
]


def wait_for(text, timeout):
    end = time.time() + timeout
    while time.time() < end:
        try:
            with open(log, "rb") as f:
                if text.encode() in f.read():
                    return True
        except FileNotFoundError:
            pass
        time.sleep(1)
    return False


def count(text):
    try:
        with open(log, "rb") as f:
            return f.read().count(text.encode())
    except FileNotFoundError:
        return 0


def more(text, n, timeout):
    """Wait until text appears more than n times (devices there at boot
    report the same lines: the tablet is a mouse too)."""
    end = time.time() + timeout
    while time.time() < end:
        if count(text) > n:
            return True
        time.sleep(1)
    return False


class Monitor:
    def __init__(self, path):
        for _ in range(120):
            try:
                self.s = socket.socket(socket.AF_UNIX)
                self.s.connect(path)
                break
            except OSError:
                time.sleep(1)
        else:
            sys.exit("monitor socket did not appear")
        self.s.settimeout(5)
        self.read()

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

    def cmd(self, line):
        self.s.sendall(line.encode() + b"\n")
        return self.read()


if not wait_for("PHONE-GUI-READY", 900):
    sys.exit("PHONE-GUI-READY not seen on the console")
m = Monitor(mon_path)
time.sleep(8)
for name, keys in STEPS:
    for k in keys:
        m.cmd(f"sendkey {k}")
        time.sleep(1.5)
    time.sleep(6)
    path = os.path.join(out, name + ".ppm")
    m.cmd(f"screendump {path}")
    for _ in range(20):
        if os.path.exists(path) and os.path.getsize(path) > 0:
            break
        time.sleep(0.5)
    print(f"{name}: {'ok' if os.path.exists(path) else 'missing'} ({' '.join(keys) or 'boot'})", flush=True)


def shot(name):
    path = os.path.join(out, name + ".ppm")
    m.cmd(f"screendump {path}")
    for _ in range(20):
        if os.path.exists(path) and os.path.getsize(path) > 0:
            break
        time.sleep(0.5)
    with open(path, "rb") as f:
        return f.read()


# A USB mouse and keyboard plugged in after start (the renderer's input
# helper opens them): the wheel moves the focus and the left button is OK,
# with no pointer; unplugged, both go away again.
ok = True
m.cmd("sendkey f4")
time.sleep(3)
n = count("PHONE-GUI-INPUT mouse added")
m.cmd("device_add usb-mouse,id=hotmouse")
if not more("PHONE-GUI-INPUT mouse added", n, 30):
    print("hotplug: the renderer did not pick up the mouse", flush=True)
    ok = False
else:
    mice = m.cmd("info mice").decode(errors="replace")
    idx = [l.split("Mouse #")[1].split(":")[0] for l in mice.splitlines() if "Mouse #" in l and "QEMU USB Mouse" in l]
    if idx:
        m.cmd(f"mouse_set {idx[0]}")
    # A key first: the screen may have timed out while the mouse came up,
    # and a click on a dark screen only lights it.
    m.cmd("sendkey f4")
    time.sleep(3)
    before = shot("08-mouse-before")
    m.cmd("mouse_button 1")
    time.sleep(0.3)
    m.cmd("mouse_button 0")
    time.sleep(4)
    clicked = shot("09-mouse-click")
    # The wheel turned towards the user: down, off the menu's first item.
    m.cmd("mouse_move 0 0 -1")
    time.sleep(4)
    wheeled = shot("10-mouse-wheel")
    print(f"mouse: click {'changed' if clicked != before else 'did nothing'}, wheel {'moved' if wheeled != clicked else 'did nothing'}", flush=True)
    ok = ok and clicked != before and wheeled != clicked
    n = count("PHONE-GUI-INPUT mouse removed")
    m.cmd("device_del hotmouse")
    if not more("PHONE-GUI-INPUT mouse removed", n, 30):
        print("hotplug: the renderer did not see the mouse go", flush=True)
        ok = False
n, r = count("PHONE-GUI-INPUT keyboard added"), count("PHONE-GUI-INPUT keyboard removed")
m.cmd("device_add usb-kbd,id=hotkbd")
kbd = more("PHONE-GUI-INPUT keyboard added", n, 30)
m.cmd("device_del hotkbd")
kbd = kbd and more("PHONE-GUI-INPUT keyboard removed", r, 30)
print(f"keyboard: {'plugged in and out' if kbd else 'not seen'}", flush=True)
ok = ok and kbd
m.cmd("quit")
sys.exit(0 if ok else 1)
