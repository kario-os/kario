#!/usr/bin/env python3
"""Drive a phone.gui=1 QEMU boot through the human monitor: wait until the shell
reports PHONE-GUI-READY on the console, press keys on the emulated USB
keyboard (they reach the renderer through evdev), and screendump the
virtio-gpu scanout after each step; then keys that set things moving (the
focus gliding in grids, along candidates and down lists, pages pushed and
popped, a menu rising and sinking, the scan line running by itself);
then plug a USB mouse and keyboard in and out (PHONE-GUI-INPUT on the
console) and check the mouse's click and wheel change the screen. At the
end, what phone-paint's motion cost on the emulator's display, from its
PHONE_PAINT_TRACE lines (PHONE-GUI-MOTION; emulated, TCG).

Usage: qemu-gui-drive.py <console-log> <monitor-socket> <out-dir>
"""
import json
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

# The main menu is phone, messages, contacts, chat, wallet, gallery,
# camera, music, browser, the Tools folder (calculator, clock, calendar),
# Apps, the store, cards, settings, three a row; on the 9-key form factor
# a digit opens tiles 1-9, * the tenth. Each starts from standby (the end
# key twice: the call 07 dialled ends first).
HOME = ["f4", "f4"]
MOTION = [
    ("11-menu-glide", HOME + ["ret", "right", "right", "down", "left", "down", "down", "down", "up", "up", "up", "up"]),
    ("12-calendar-glide", ["asterisk", "3", "right", "right", "down", "down", "left", "up"]),
    ("13-calc-glide", HOME + ["ret", "asterisk", "1", "right", "down", "right", "up", "left"]),
    ("14-settings-scroll", HOME + ["ret", "down", "down", "down", "down", "right", "ret"] + ["down"] * 10 + ["up", "up"]),
    ("15-options-rise", ["tab", "down", "esc", "esc"]),
    ("16-chat-candidates", HOME + ["ret", "4", "ret"] + ["down"] * 12 + ["9", "4", "6", "6", "4", "right", "right", "left", "down", "right", "right", "left"]),
    ("17-scan-line", HOME + ["ret", "5", "f2", "ret"]),
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


def console():
    try:
        with open(log, "rb") as f:
            return f.read().decode(errors="replace")
    except FileNotFoundError:
        return ""


def motion_sums():
    """The last sums phone-paint printed (PHONE_PAINT_TRACE), or None."""
    sums = [l.split("so far: ", 1)[1].strip() for l in console().splitlines() if "phone-paint:" in l and "so far: " in l]
    return sums[-1] if sums else None


def q(v, p):
    v = sorted(v)
    return v[round((len(v) - 1) * p)] if v else 0.0


def motion_kinds():
    """phone-paint's step times on the console (PHONE_PAINT_STEPS), a line
    a burst, summed by kind: a page or panel moving (transition steps),
    something easing (a glide, a scroll, a banner: more than one draw),
    and frames that set nothing moving."""
    kinds = {"transitions": [], "easing": [], "still frames": []}
    for l in console().splitlines():
        at = l.find('{"draw"')
        if at < 0:
            continue
        try:
            b = json.loads(l[at:])
        except ValueError:
            continue
        kinds["transitions" if b.get("transition") else "easing" if len(b.get("draw", [])) > 1 else "still frames"].append(b)
    out = []
    for name, bursts in kinds.items():
        if not bursts:
            continue
        cat = lambda k: [x for b in bursts for x in b.get(k) or []]
        d, t, p, g = cat("draw"), cat("transition"), cat("present"), cat("gap")
        late = sum(1 for x in g if x > 25)
        out.append(f"{name}: {len(bursts)} bursts, {len(d)} draws (p50 {q(d, .5):.1f} ms, p95 {q(d, .95):.1f} ms), "
                   f"{len(t)} transition steps (p50 {q(t, .5):.1f} ms, p95 {q(t, .95):.1f} ms), "
                   f"handed over p50 {q(p, .5):.1f} ms, p95 {q(p, .95):.1f} ms, a frame every p50 {q(g, .5):.1f} ms, p95 {q(g, .95):.1f} ms, {late} gaps over 25 ms")
    return out


if not wait_for("PHONE-GUI-READY", 900):
    sys.exit("PHONE-GUI-READY not seen on the console")
m = Monitor(mon_path)
time.sleep(8)
for name, keys in STEPS + MOTION:
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
# The scan line ran by itself meanwhile; leave the viewfinder.
m.cmd("sendkey esc")
time.sleep(3)
# A page pushed in, screendumped as fast as the monitor answers: each
# picture a whole frame of the slide (a torn one would show two).
m.cmd("sendkey f4")
time.sleep(4)
m.cmd("sendkey ret")
t0 = time.time()
for i in range(30):
    m.cmd(f"screendump {os.path.join(out, f'18-push-{i:02d}.ppm')}")
    time.sleep(0.04)
took = time.time() - t0
time.sleep(4)
pictures = set()
for i in range(30):
    try:
        with open(os.path.join(out, f"18-push-{i:02d}.ppm"), "rb") as f:
            pictures.add(hash(f.read()))
    except FileNotFoundError:
        pass
print(f"push: 30 screendumps in {took:.1f} s, {len(pictures)} different pictures", flush=True)


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
sums = motion_sums()
print(f"PHONE-GUI-MOTION: {sums or 'nothing timed (PHONE_PAINT_TRACE not set?)'}; the emulator's display (TCG), not a phone's", flush=True)
for k in motion_kinds():
    print(f"PHONE-GUI-MOTION: {k}", flush=True)
m.cmd("quit")
sys.exit(0 if ok else 1)
