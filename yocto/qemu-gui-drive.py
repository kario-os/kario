#!/usr/bin/env python3
"""Drive a phone.gui=1 QEMU boot through the human monitor: wait until the shell
reports PHONE-GUI-READY on the console, press keys on the emulated USB
keyboard (they reach the renderer through evdev), and screendump the
virtio-gpu scanout after each step (its report goes to <out-dir>/drive.txt
too); then keys that set things moving (the
focus gliding in grids, along candidates and down lists, pages pushed and
popped, a menu rising and sinking, the scan line running by itself);
then plug a USB mouse and keyboard in and out (PHONE-GUI-INPUT on the
console) and check the mouse's click and wheel change the screen. At the
end, what phone-paint's motion cost on the emulator's display, from its
PHONE_PAINT_TRACE lines (PHONE-GUI-MOTION; emulated, TCG), and how many
page flips it asked for.

With a QMP socket, a finger on the emulator's touchscreen too (QEMU's
virtio multitouch device, qemu_touch.py), read by the renderer as on a
phone: taps on the soft keys and a tile and a swipe from the left edge
change the screen (PHONE-GUI-TOUCH).

What the shell and the renderer say comes as one console file,
gui-<boot>.log, from gui-console (phone.gui-console on the kernel
command line, qemu-gui.sh): a kernel line landing inside one of its lines
is taken out (qemu-console.py), and one that cannot be mended, or a line
missing, is named at the end (PHONE-GUI-CONSOLE) and fails the run. An
image without gui-console printed them, and phone-paint its
PHONE_PAINT_STEPS lines, as plain lines, which are read as they are.

With PHONE_PAINT_GPU in PHONE_GUI_ENV (qemu-gui.sh) the renderer must
compose on the GPU (its feature gpu, PHONE-GUI-GPU), flip pages and
compose a transition's steps; with PHONE_PAINT_GPU_RENDER too, it must
render on that node and show the buffers on the display's card through
PRIME (42-gpu-rendering.md §4); with PHONE_PAINT_GPU_FAIL too, it must give
the GPU up mid-run and draw on the CPU from then on. PHONE_GUI_WAIT above
1: the renderer takes many seconds a frame (a software rasterizer under
TCG), so each screen waits until it has stopped changing for a few of its
frames (settle), at most that many times the usual time and ten minutes
more.

Usage: qemu-gui-drive.py <console-log> <monitor-socket> <out-dir> [<qmp-socket>]
"""
import atexit
import importlib.util
import json
import os
import re
import socket
import sys
import time

import qemu_touch

_spec = importlib.util.spec_from_file_location("qemu_console", os.path.join(os.path.dirname(os.path.abspath(__file__)), "qemu-console.py"))
console = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(console)

log, mon_path, out = sys.argv[1:4]
qmp = qemu_touch.QMP(sys.argv[4]) if len(sys.argv) > 4 else None
ENV = dict(kv.split("=", 1) for kv in os.environ.get("PHONE_GUI_ENV", "").split() if "=" in kv)
GPU = ENV.get("PHONE_PAINT_GPU") in ("1", "soft")
FAIL = GPU and bool(ENV.get("PHONE_PAINT_GPU_FAIL"))
SPLIT = ENV.get("PHONE_PAINT_GPU_RENDER", "") if GPU else ""
WAIT = float(os.environ.get("PHONE_GUI_WAIT") or 1)

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
# The status bar's rows (its clock), left out when telling whether the
# screen still changes.
STATUS_ROWS = 32
# What changes by itself on a screendump's screen or follows the time of the
# run, also left out: a box (x0, y0, x1, y1) around a clock (standby's time
# and date, a call's time), the text on the camera's made-up picture or a
# demo message's time (the chat's, its start less twenty minutes); None,
# all of it (the scan line). gui-compare.py takes them from the
# PHONE-GUI-VOLATILE lines.
STANDBY = (100, 110, 380, 250)
MOVING = {
    "01-idle": STANDBY,
    "05-calc": (16, 80, 336, 216),
    "07-dial-from-idle": (60, 150, 200, 192),
    "08-mouse-before": STANDBY,
    "15-options-rise": STANDBY,
    "16-chat-candidates": (140, 122, 262, 146),
    "17-scan-line": None,
}


def say(line):
    """A line of the report, on stdout and in <out-dir>/drive.txt."""
    print(line, flush=True)
    with open(os.path.join(out, "drive.txt"), "a") as f:
        f.write(line + "\n")


# gui-console's file: the boot's id in its name.
RELAY = re.compile(r"gui-[0-9a-f]{8}\.log\Z")
# The journal's prefix on a plain line (an image without gui-console).
PREFIX = re.compile(rb"\[ *[0-9]+\.[0-9]{6}\] [^\s\[\]:]+\[[0-9]+\]: ")
# The rest of a line cut by a kernel line follows that line at once; the
# emulator lags behind on a busy machine.
SETTLE = 10
_heard = (None, None)


def hear():
    """The console so far, to its last whole line: (gui-console's lines,
    the other lines, what is wrong with gui-console's file, what was
    mended in it, its names)."""
    global _heard
    try:
        with open(log, "rb") as f:
            raw = f.read()
    except FileNotFoundError:
        raw = b""
    raw = raw[:raw.rfind(b"\n") + 1]
    if _heard[0] == raw:
        return _heard[1]
    lines = console.console_lines(raw)
    relayed, plain, wrong, mended, names = [], [], [], [], set()
    c = console.Console(log, wrong.append, False)
    for ev in console.read(lines):
        if ev[0] == "other":
            plain.append(ev[2])
        elif ev[0] == "damaged":
            if ev[2][0] is None or RELAY.match(ev[2][1]):
                c.damaged(ev[2], ev[1], lines[ev[1]])
            else:
                plain.append(lines[ev[1]])
        else:
            p, at, end, taken = ev
            if p[0] == "line" and RELAY.match(p[1]):
                c.line(p[1], p[2], p[4], at)
                relayed.append(p[4])
                names.add(p[1])
                if taken:
                    mended.append(f"line {at + 1}: {p[1]} line {p[2]} mended, {len(taken)} kernel line"
                                  f"{'s' if len(taken) > 1 else ''} taken out of it: {console.show(taken[0])}")
            else:
                plain.append(lines[at])
            plain.extend(taken)
    c.close()
    _heard = (raw, (relayed, plain, wrong, mended, sorted(names)))
    return _heard[1]


def said():
    """What the shell and the renderer said on the console so far, a line
    each: gui-console's lines, or, from an image without it, the plain
    lines, the journal's prefix taken off."""
    relayed, plain = hear()[:2]
    return [l.decode(errors="replace") for l in relayed] if relayed else \
        [PREFIX.split(l, 1)[-1].decode(errors="replace") for l in plain]


def console_report():
    """PHONE-GUI-CONSOLE: how the shell's and the renderer's lines came;
    False when one of gui-console's is damaged or missing."""
    if hear()[2]:
        time.sleep(SETTLE)
    relayed, _, wrong, mended, names = hear()
    if not relayed:
        say("PHONE-GUI-CONSOLE: no gui-console file (an image without it): the plain lines read as they are")
        return not wrong
    say(f"PHONE-GUI-CONSOLE: {' '.join(names)}: {len(relayed)} lines, {len(mended)} mended, {len(wrong)} wrong")
    for m in mended + wrong:
        say(f"PHONE-GUI-CONSOLE: {m}")
    return not wrong


def wait_for(text, timeout):
    end = time.time() + timeout
    while time.time() < end:
        if any(text in l for l in said()):
            return True
        time.sleep(1)
    return False


def count(text):
    return sum(l.count(text) for l in said())


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


def motion_sums():
    """The last sums phone-paint printed (PHONE_PAINT_TRACE), or None."""
    sums = [l.split("so far: ", 1)[1].strip() for l in said() if "phone-paint:" in l and "so far: " in l]
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
    for b in bursts():
        kinds["transitions" if b.get("transition") else "easing" if len(b.get("draw", [])) > 1 else "still frames"].append(b)
    out = []
    for name, these in kinds.items():
        if not these:
            continue
        cat = lambda k: [x for b in these for x in b.get(k) or []]
        d, t, p, g = cat("draw"), cat("transition"), cat("present"), cat("gap")
        late = sum(1 for x in g if x > 25)
        out.append(f"{name}: {len(these)} bursts, {len(d)} draws (p50 {q(d, .5):.1f} ms, p95 {q(d, .95):.1f} ms), "
                   f"{len(t)} transition steps (p50 {q(t, .5):.1f} ms, p95 {q(t, .95):.1f} ms), "
                   f"handed over p50 {q(p, .5):.1f} ms, p95 {q(p, .95):.1f} ms, a frame every p50 {q(g, .5):.1f} ms, p95 {q(g, .95):.1f} ms, {late} gaps over 25 ms")
    return out


def renderer_lines(what):
    """phone-paint's lines on the console that say what."""
    return [l.split("phone-paint: ", 1)[1].strip() for l in said() if "phone-paint: " in l and what in l]


def gpu_started():
    """Whether the renderer composes on the GPU (its ready's features, as
    the shell's PHONE-GUI-READY line lists them), and what it said."""
    feats = None
    for _ in range(30):
        ready = [l for l in said() if "PHONE-GUI-READY" in l]
        feats = re.search(r"PHONE-GUI-READY [^\[]*\[([^\]]*)\]", ready[-1]) if ready else None
        if feats:
            break
        time.sleep(1)
    said = renderer_lines("gpu:")
    return bool(feats) and "gpu" in feats.group(1).split(), said[0] if said else "nothing"


def bursts():
    """phone-paint's PHONE_PAINT_STEPS lines on the console so far."""
    out = []
    for l in said():
        at = l.find('{"draw"')
        if at >= 0:
            try:
                out.append(json.loads(l[at:]))
            except ValueError:
                pass
    return out


def dump(path):
    """The display screendumped to path: the file's bytes, b"" if none came."""
    m.cmd(f"screendump {path}")
    for _ in range(20):
        if os.path.exists(path) and os.path.getsize(path) > 0:
            break
        time.sleep(0.5)
    try:
        with open(path, "rb") as f:
            return f.read()
    except FileNotFoundError:
        return b""


def below_status(path, box):
    """A digest of the screen below the status bar without box, or None."""
    if os.path.exists(path):
        os.remove(path)
    data = dump(path)
    head = data.split(maxsplit=3)
    if len(head) < 4:
        return None
    w, h = int(head[1]), int(head[2])
    px = bytearray(data[-h * w * 3:])
    if box:
        x0, y0, x1, y1 = box
        for y in range(y0, y1):
            px[(y * w + x0) * 3:(y * w + x1) * 3] = bytes((x1 - x0) * 3)
    return hash(bytes(px[STATUS_ROWS * w * 3:]))


def settle(seconds, moving=()):
    """Gives the screen time to settle: `seconds`. With PHONE_GUI_WAIT above
    1 (a renderer that takes many seconds a frame and draws only the last
    of the frames that came meanwhile) until the screen
    below the status bar, but for the box that changes by itself (moving,
    MOVING's), has not changed, or only blinked between two pictures (a
    text cursor), for four of its frames; a screen that changes all over
    (moving None), ten of its frames. At most PHONE_GUI_WAIT times
    `seconds` or twenty of its frames, and ten minutes more."""
    if WAIT <= 1:
        time.sleep(seconds)
        return
    end = time.time() + seconds * WAIT + 600
    while not bursts() and time.time() < end:
        time.sleep(2)
    frame = frame_time(bursts())
    end = max(end, time.time() + 20 * frame + 600)
    if moving is None:
        time.sleep(max(0, min(10 * frame + 10, end - time.time())))
        return
    quiet, start, seen = max(seconds, 4 * frame + 10), time.time(), []
    tmp = os.path.join(out, "settle.tmp")
    while time.time() < end:
        now = time.time()
        seen.append((now, below_status(tmp, moving)))
        first = max((i for i, (t, _) in enumerate(seen) if t < now - quiet), default=0)
        window = [d for _, d in seen[first:]]
        runs = [d for i, d in enumerate(window) if i == 0 or d != window[i - 1]]
        if now - start >= quiet and (len(set(runs)) == 1 or (len(set(runs)) == 2 and len(runs) >= 3)):
            break
        time.sleep(5)
    if os.path.exists(tmp):
        os.remove(tmp)


def frame_time(bs):
    """The longest frame of the last few bursts (drawn, a transition step,
    handed to the panel), in seconds."""
    took = [max(b.get("draw") or [0]) + max(b.get("transition") or [0]) + max(b.get("present") or [0]) for b in bs[-5:]]
    return max(took, default=0) / 1000


def flips():
    """The panel's page flips so far, asked for and completed, from the
    last burst's line, or None."""
    got = [b["flips"] for b in bursts() if len(b.get("flips") or []) == 2]
    return tuple(got[-1]) if got else None


# The Shell logs each press as it arrives and again once handled; a press
# is counted by the second.
RECEIVED = "phone-shell: power key: from the renderer"
POWER_LINE = re.compile(r"phone-(shell|paint): (power key|screen (on|off)|panel)")


def power_lines():
    """The Shell's and the renderer's lines about the power key, the
    screen and the panel on the console so far (docs/39 §14.10)."""
    return [l[l.find("phone-"):].strip() for l in said() if POWER_LINE.search(l)]


def power_quiet(before, timeout=30):
    """The power lines after the first `before`, once no new one has come
    for three seconds (each press, screen and panel change says so)."""
    end, last, seen = time.time() + timeout * WAIT, time.time(), len(power_lines())
    while time.time() < end and time.time() - last < 3:
        time.sleep(0.5)
        if len(power_lines()) != seen:
            seen, last = len(power_lines()), time.time()
    return power_lines()[before:]


def screen_state(lines):
    """The Shell's last word on the screen and on the panel in lines: the
    renderer's answers, or what a press found the panel at (presses that
    never got to turn it leave no answer)."""
    screen = panel = None
    for l in lines:
        if m := re.match(r"phone-shell: screen (on|off)\b", l):
            screen = m.group(1)
        elif m := re.match(r"phone-shell: panel: (on|off)\b", l) or re.search(r"phone-shell: power key: .*\(the panel (on|off)\b", l):
            panel = m.group(1)
    return screen, panel


def power_key():
    """The power button held, then pressed again and again as the owner
    pressed the 6T's (docs/39 §14.10), through the monitor: system_powerdown
    holds the virt machine's gpio-keys line down for 100 ms, and again from
    each new command. Held 0.9 s: one long press, the power menu. Ten
    presses 150 ms apart, then five, then one: each a short press (the
    Shell saw every one, once), the screen and the panel on after the ten,
    off after the five, on after the last, no power menu; then the Shell
    answers a key."""
    good = True
    m.cmd("sendkey f4")
    settle(3, STANDBY)
    before = len(power_lines())
    t0 = time.monotonic()
    while time.monotonic() - t0 < 0.9:
        m.cmd("system_powerdown")
        time.sleep(0.05)
    lines = power_quiet(before)
    for l in lines:
        say(f"power held: {l}")
    presses = [l for l in lines if l.startswith("phone-shell: power key:") and not l.startswith(RECEIVED)]
    held = len(presses) == 1 and "long press" in presses[0] and "power menu" in presses[0]
    say(f"PHONE-GUI-POWER: held 0.9 s: {presses[0] if presses else 'no press reached the Shell'}")
    good = good and held
    m.cmd("sendkey esc")
    settle(3, STANDBY)
    for n, want in ((10, "on"), (5, "off"), (1, "on")):
        before = len(power_lines())
        t0 = time.monotonic()
        for i in range(n):
            time.sleep(max(0, t0 + i * 0.15 - time.monotonic()))
            m.cmd("system_powerdown")
        lines = power_quiet(before)
        for l in lines:
            say(f"power x{n}: {l}")
        presses = [l for l in lines if l.startswith("phone-shell: power key:") and not l.startswith(RECEIVED)]
        screen, panel = screen_state(power_lines())
        path = os.path.join(out, f"19-power-x{n}.ppm")
        data = dump(path)
        size = tuple(int(v) for v in data.split(maxsplit=3)[1:3]) if data else None
        right = len(presses) == n and not any("long press" in l for l in presses) and screen == want and panel == want
        say(f"PHONE-GUI-POWER: {n} presses 150 ms apart: {len(presses)} reached the Shell, "
            f"{sum('long press' in l for l in presses)} long; screen {screen}, panel {panel} (want {want}); "
            f"display {'x'.join(map(str, size)) if size else 'no screendump'}: {'ok' if right else 'WRONG'}")
        good = good and right
    m.cmd("sendkey f4")
    settle(3, STANDBY)
    before_key = shot("19-power-after")
    m.cmd("sendkey ret")
    settle(4)
    answered = shot("19-power-key") != before_key
    m.cmd("sendkey f4")
    settle(3, STANDBY)
    say(f"PHONE-GUI-POWER: a key after them {'answered' if answered else 'NOT answered'}")
    return good and answered


def touch_screen():
    """A finger on the touchscreen through QMP, as a person's on a phone
    (points in ten-thousandths of the 480x640 screen): standby's middle
    soft key (OK) opens the main menu, a drag up there moves the focus a
    row down, a tap on its title goes back to standby; the menu again, a
    tap on its first tile opens the phone app, a swipe from the left edge
    goes back (to standby). Each must change the screen."""
    if qmp is None:
        say("PHONE-GUI-TOUCH: no QMP socket, not touched")
        return False
    if not wait_for("PHONE-GUI-INPUT touchscreen added", 30):
        say("PHONE-GUI-TOUCH: the renderer did not find the touchscreen")
        return False
    # A key first: a touch on a dark screen does nothing.
    m.cmd("sendkey f4")
    settle(3, STANDBY)
    before, good = shot("20-touch-before"), True
    for n, (name, kind, nums) in enumerate([
        ("21-touch-soft-ok", "tap", [5000, 9810]),
        ("22-touch-drag", "swipe", [5000, 6000, 5000, 5000, 400]),
        ("23-touch-title-back", "tap", [5000, 800]),
        ("24-touch-soft-ok", "tap", [5000, 9810]),
        ("25-touch-tile", "tap", [1830, 2200]),
        ("26-touch-edge-back", "swipe", [60, 5000, 4000, 5100, 400]),
    ]):
        done = qemu_touch.play(qmp, qemu_touch.gesture(kind, nums, n + 1))
        settle(4, STANDBY if name.endswith("back") else ())
        after = shot(name)
        changed = done and after != before
        say(f"PHONE-GUI-TOUCH: {name}: {kind} {' '.join(map(str, nums))}: {'changed the screen' if changed else 'did NOT change it' if done else 'QMP refused it'}")
        good, before = good and changed, after
    return good


if not wait_for("PHONE-GUI-READY", 900):
    console_report()
    sys.exit("PHONE-GUI-READY not seen on the console")
ok = True
for name, box in MOVING.items():
    say(f"PHONE-GUI-VOLATILE: {name}" + (":%d,%d-%d,%d" % box if box else ""))
if GPU:
    on, said = gpu_started()
    say(f"PHONE-GUI-GPU: {'composing on the GPU' if on else 'NOT on the GPU'} ({said})")
    ok = on
    if SPLIT:
        split = f" on {SPLIT} (" in said and "through PRIME" in said
        say(f"PHONE-GUI-GPU: {'rendering on ' + SPLIT + ', shown on the display through PRIME' if split else 'NOT rendering on ' + SPLIT}")
        ok = ok and split
m = Monitor(mon_path)
# qemu-gui.sh waits for QEMU: it must quit however this script ends.
atexit.register(m.cmd, "quit")
settle(8, STANDBY)
for name, keys in STEPS + MOTION:
    for k in keys:
        m.cmd(f"sendkey {k}")
        time.sleep(1.5)
    settle(6, MOVING.get(name, ()))
    path = os.path.join(out, name + ".ppm")
    say(f"{name}: {'ok' if dump(path) else 'missing'} ({' '.join(keys) or 'boot'})")
# The scan line ran by itself meanwhile; leave the viewfinder.
m.cmd("sendkey esc")
settle(3)
# A page pushed in, screendumped as fast as the monitor answers: each
# picture a whole frame of the slide (a torn one would show two). A slow
# renderer's slide has a step or two, each many seconds on the screen:
# the screendumps spread over three of its frames.
m.cmd("sendkey f4")
settle(4, STANDBY)
pause = 0.04 if WAIT <= 1 else (3 * frame_time(bursts()) + 10) / 30
m.cmd("sendkey ret")
t0 = time.time()
for i in range(30):
    m.cmd(f"screendump {os.path.join(out, f'18-push-{i:02d}.ppm')}")
    time.sleep(pause)
took = time.time() - t0
settle(4)
pictures = set()
for i in range(30):
    try:
        with open(os.path.join(out, f"18-push-{i:02d}.ppm"), "rb") as f:
            pictures.add(hash(f.read()))
    except FileNotFoundError:
        pass
say(f"push: 30 screendumps in {took:.1f} s, {len(pictures)} different pictures")


def shot(name):
    """The screen below the status bar, screendumped as name."""
    data = dump(os.path.join(out, name + ".ppm"))
    w, h = (int(v) for v in data.split(maxsplit=3)[1:3])
    return data[-(h - STATUS_ROWS) * w * 3:]


# A USB mouse and keyboard plugged in after start (the renderer's input
# helper opens them): the wheel moves the focus and the left button is OK,
# with no pointer; unplugged, both go away again.
m.cmd("sendkey f4")
settle(3, STANDBY)
n = count("PHONE-GUI-INPUT mouse added")
m.cmd("device_add usb-mouse,id=hotmouse")
if not more("PHONE-GUI-INPUT mouse added", n, 30):
    say("hotplug: the renderer did not pick up the mouse")
    ok = False
else:
    mice = m.cmd("info mice").decode(errors="replace")
    idx = [l.split("Mouse #")[1].split(":")[0] for l in mice.splitlines() if "Mouse #" in l and "QEMU USB Mouse" in l]
    if idx:
        m.cmd(f"mouse_set {idx[0]}")
    # A key first: the screen may have timed out while the mouse came up,
    # and a click on a dark screen only lights it.
    m.cmd("sendkey f4")
    settle(3, MOVING["08-mouse-before"])
    before = shot("08-mouse-before")
    m.cmd("mouse_button 1")
    time.sleep(0.3)
    m.cmd("mouse_button 0")
    settle(4)
    clicked = shot("09-mouse-click")
    # The wheel turned towards the user: down, off the menu's first item.
    m.cmd("mouse_move 0 0 -1")
    settle(4)
    wheeled = shot("10-mouse-wheel")
    say(f"mouse: click {'changed' if clicked != before else 'did nothing'}, wheel {'moved' if wheeled != clicked else 'did nothing'}")
    ok = ok and clicked != before and wheeled != clicked
    n = count("PHONE-GUI-INPUT mouse removed")
    m.cmd("device_del hotmouse")
    if not more("PHONE-GUI-INPUT mouse removed", n, 30):
        say("hotplug: the renderer did not see the mouse go")
        ok = False
n, r = count("PHONE-GUI-INPUT keyboard added"), count("PHONE-GUI-INPUT keyboard removed")
m.cmd("device_add usb-kbd,id=hotkbd")
kbd = more("PHONE-GUI-INPUT keyboard added", n, 30)
m.cmd("device_del hotkbd")
kbd = kbd and more("PHONE-GUI-INPUT keyboard removed", r, 30)
say(f"keyboard: {'plugged in and out' if kbd else 'not seen'}")
ok = ok and kbd
ok = touch_screen() and ok
ok = power_key() and ok
sums = motion_sums()
say(f"PHONE-GUI-MOTION: {sums or 'nothing timed (PHONE_PAINT_TRACE not set?)'}; the emulator's display (TCG), not a phone's")
for k in motion_kinds():
    say(f"PHONE-GUI-MOTION: {k}")
flipped = flips()
if flipped is None:
    said = re.search(r"(\d+) page flips, (\d+) completed", sums or "")
    flipped = (int(said.group(1)), int(said.group(2))) if said else None
say(f"PHONE-GUI-FLIPS: {'%d page flips, %d completed' % flipped if flipped else 'not reported'}")
if GPU and not FAIL:
    ok = ok and bool(flipped) and flipped[0] > 0
    slid = sum(len(b.get("transition") or []) for b in bursts())
    say(f"PHONE-GUI-GPU: {slid} transition steps composed")
    ok = ok and slid > 0
    lost = renderer_lines("drawing on the CPU from now on")
    if lost:
        say(f"PHONE-GUI-GPU: gave up: {lost[0]}")
        ok = False
if FAIL:
    lost = renderer_lines("drawing on the CPU from now on")
    say(f"PHONE-GUI-GPU: {'fell back to the CPU: ' + lost[0] if lost else 'did NOT fall back to the CPU'}")
    ok = ok and bool(lost)
ok = console_report() and ok
sys.exit(0 if ok else 1)
