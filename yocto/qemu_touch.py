"""A finger on the emulator's touchscreen: QEMU's virtio-multitouch-pci
(the SKU's display names it, sku/displays/qemu-480x640.toml), touched
through QMP's input-send-event, one finger in slot 0, its axes spanning
0..32767 over the display. The guest reads it as the multitouch
protocol's slots, tracking ids and positions (paint/src/touch.rs,
hal/input/touch.go). For yocto/qemu-buttons.py (the selftest's
touchscreen step) and yocto/qemu-gui-drive.py (the GUI run).
"""
import json
import socket
import time

# QEMU's absolute axes (INPUT_EVENT_ABS_MAX).
ABS_MAX = 0x7FFF


class QMP:
    """QEMU's machine protocol on a socket: a JSON object a line each way,
    the greeting and qmp_capabilities first; events come between answers."""

    def __init__(self, path):
        self.path, self.s, self.buf = path, None, b""

    def line(self):
        while b"\n" not in self.buf:
            chunk = self.s.recv(4096)
            if not chunk:
                raise OSError("QMP closed")
            self.buf += chunk
        line, self.buf = self.buf.split(b"\n", 1)
        return json.loads(line)

    def answer(self):
        while True:
            m = self.line()
            if "return" in m or "error" in m:
                return m

    def cmd(self, execute, arguments=None):
        """Runs a command, connecting first (the socket appears once QEMU has
        started); whether QEMU did it."""
        for _ in range(60):
            if self.s is None:
                try:
                    s = socket.socket(socket.AF_UNIX)
                    s.settimeout(5)
                    s.connect(self.path)
                    self.s, self.buf = s, b""
                    self.line()
                    s.sendall(b'{"execute": "qmp_capabilities"}\n')
                    self.answer()
                except (OSError, ValueError):
                    self.s = None
                    time.sleep(1)
                    continue
            try:
                msg = {"execute": execute}
                if arguments is not None:
                    msg["arguments"] = arguments
                self.s.sendall(json.dumps(msg).encode() + b"\n")
                return "return" in self.answer()
            except (OSError, ValueError):
                self.s = None
        return False


def mtt(kind, tracking, axis="x", value=0):
    return {"type": "mtt", "data": {"type": kind, "slot": 0, "tracking-id": tracking, "axis": axis, "value": value}}


def at(x, y, tracking):
    """The finger at (x, y), ten-thousandths of the screen, as multitouch
    data events in QEMU's units."""
    return [mtt("data", tracking, "x", round(x * ABS_MAX / 10000)), mtt("data", tracking, "y", round(y * ABS_MAX / 10000))]


def gesture(kind, nums, tracking):
    """The reports a gesture takes: (seconds after the one before, events);
    QEMU ends each with a sync. tap x y, long x y ms, swipe x0 y0 x1 y1 ms:
    the finger comes down (a tracking id in slot 0), moves, and lifts
    (tracking id -1). None for a gesture it is not."""
    if len(nums) < 2:
        return None
    x, y = nums[0], nums[1]
    down = (0, [mtt("begin", tracking)] + at(x, y, tracking))
    up = [mtt("end", -1)]
    if kind == "tap" and len(nums) == 2:
        return [down, (0.08, up)]
    if kind == "long" and len(nums) == 3:
        return [down, (nums[2] / 1000, up)]
    if kind == "swipe" and len(nums) == 5:
        (x1, y1), steps = nums[2:4], 10
        moves = [(nums[4] / 1000 / steps, at(x + (x1 - x) * i / steps, y + (y1 - y) * i / steps, tracking)) for i in range(1, steps + 1)]
        return [down] + moves + [(0.03, up)]
    return None


def play(qmp, reports):
    """Sends a gesture's reports, each after its delay; whether QEMU took
    them all."""
    for delay, events in reports:
        time.sleep(delay)
        if not qmp.cmd("input-send-event", {"events": events}):
            return False
    return True
